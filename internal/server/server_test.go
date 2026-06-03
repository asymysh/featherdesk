package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aseem/viewport-rds/internal/logger"
	"github.com/aseem/viewport-rds/internal/protocol"
	"github.com/coder/websocket"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	log := logger.New(io.Discard, logger.ERROR)
	clientFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
	srv := New(Config{Port: 0, Log: log, ClientFS: clientFS})

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(srv.cfg.ClientFS)))
	mux.HandleFunc("/status", srv.handleStatus)
	mux.HandleFunc("/ws", srv.handleWS)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestStatusEndpoint(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status code: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q, want application/json", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["clients"]; !ok {
		t.Fatal("response missing 'clients' field")
	}
}

func TestWebSocketUpgrade(t *testing.T) {
	_, ts := testServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[4:] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWebSocketReceivesBinaryFrame(t *testing.T) {
	srv, ts := testServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[4:] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	// Give the client time to register
	time.Sleep(50 * time.Millisecond)

	// Broadcast a fake NAL with IDR start code
	fakeNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0xAA, 0xBB, 0xCC}
	srv.Broadcast([][]byte{fakeNAL}, 320, 240, 12345)

	typ, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("message type: got %v, want binary", typ)
	}

	if len(msg) < protocol.HeaderSize {
		t.Fatalf("message too short: %d bytes", len(msg))
	}

	hdr, err := protocol.UnmarshalHeader(msg[:protocol.HeaderSize])
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.Type != protocol.FrameTypeVideoH264 {
		t.Fatalf("frame type: got %d, want %d", hdr.Type, protocol.FrameTypeVideoH264)
	}
	if hdr.Width != 320 || hdr.Height != 240 {
		t.Fatalf("dimensions: got %dx%d, want 320x240", hdr.Width, hdr.Height)
	}
	if hdr.Timestamp != 12345 {
		t.Fatalf("timestamp: got %d, want 12345", hdr.Timestamp)
	}
	if hdr.PayloadSize != uint32(len(fakeNAL)) {
		t.Fatalf("payload size: got %d, want %d", hdr.PayloadSize, len(fakeNAL))
	}

	payload := msg[protocol.HeaderSize:]
	if len(payload) != len(fakeNAL) {
		t.Fatalf("payload length: got %d, want %d", len(payload), len(fakeNAL))
	}
}

func TestClientDisconnectNoPanic(t *testing.T) {
	srv, ts := testServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[4:] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if srv.ClientCount() != 1 {
		t.Fatalf("client count: got %d, want 1", srv.ClientCount())
	}

	conn.Close(websocket.StatusNormalClosure, "bye")
	time.Sleep(100 * time.Millisecond)

	if srv.ClientCount() != 0 {
		t.Fatalf("client count after disconnect: got %d, want 0", srv.ClientCount())
	}

	// Broadcast after disconnect should not panic
	fakeNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x01}
	srv.Broadcast([][]byte{fakeNAL}, 640, 480, 99999)
}

func TestNewClientGetsIDR(t *testing.T) {
	srv, ts := testServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Pre-broadcast an IDR frame before any client connects
	idrNAL := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0xDE, 0xAD}
	srv.Broadcast([][]byte{idrNAL}, 1920, 1080, 42)

	// Now connect; should receive the cached IDR immediately
	wsURL := "ws" + ts.URL[4:] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.CloseNow()

	_, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}

	hdr, _ := protocol.UnmarshalHeader(msg[:protocol.HeaderSize])
	if hdr.Type != protocol.FrameTypeVideoH264 {
		t.Fatalf("expected video frame, got type %d", hdr.Type)
	}
	if hdr.Width != 1920 || hdr.Height != 1080 {
		t.Fatalf("IDR dimensions: got %dx%d, want 1920x1080", hdr.Width, hdr.Height)
	}
}
