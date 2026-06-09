package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/asymysh/featherdesk/internal/logger"
	"github.com/coder/websocket"
)

func testServerWithToken(t *testing.T, token string) (*Server, *httptest.Server) {
	t.Helper()
	log := logger.New(io.Discard, logger.ERROR)
	clientFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	}
	srv := New(Config{Port: 0, Log: log, ClientFS: clientFS, Token: token})

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", srv.handleWS)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestWebSocketRejectsMissingToken(t *testing.T) {
	_, ts := testServerWithToken(t, "secret123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[4:] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("dial succeeded without token, want rejection")
	}
}

func TestWebSocketRejectsWrongToken(t *testing.T) {
	_, ts := testServerWithToken(t, "secret123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[4:] + "/ws?token=wrong"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		conn.CloseNow()
		t.Fatal("dial succeeded with wrong token, want rejection")
	}
}

func TestWebSocketAcceptsCorrectToken(t *testing.T) {
	srv, ts := testServerWithToken(t, "secret123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[4:] + "/ws?token=secret123"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial with correct token: %v", err)
	}
	defer conn.CloseNow()

	time.Sleep(50 * time.Millisecond)
	if srv.ClientCount() != 1 {
		t.Fatalf("client count: got %d, want 1", srv.ClientCount())
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(10, 3)

	for i := 0; i < 3; i++ {
		if !rl.allow() {
			t.Fatalf("call %d: got denied, want allowed (burst=3)", i)
		}
	}
	if rl.allow() {
		t.Fatal("4th immediate call allowed, want denied")
	}

	time.Sleep(150 * time.Millisecond) // refills ~1.5 tokens at 10/sec
	if !rl.allow() {
		t.Fatal("call after refill denied, want allowed")
	}
}
