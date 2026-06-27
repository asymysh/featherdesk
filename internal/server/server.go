package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aseem/viewport-rds/internal/logger"
	"github.com/aseem/viewport-rds/internal/protocol"
	"github.com/coder/websocket"
)

// Config holds server parameters.
type Config struct {
	Port     int
	Bind     string
	Log      *logger.Logger
	ClientFS fs.FS
}

const maxClients = 25

// Server handles HTTP/WebSocket connections and video frame broadcast.
type Server struct {
	cfg         Config
	httpSrv     *http.Server
	clients     sync.Map // map[*Client]struct{}
	count       atomic.Int32
	lastIDR     []byte
	idrMu       sync.RWMutex
	onNewClient func()
	onInput     func([]byte)
	controller  atomic.Pointer[Client]

	framesBroadcast atomic.Uint64
	bytesBroadcast  atomic.Uint64
	encoderType     string
	audioEnabled    bool
}

// New creates a Server ready to listen.
func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

// SetNewClientCallback registers a function called on each new client connection.
func (s *Server) SetNewClientCallback(fn func()) {
	s.onNewClient = fn
}

func (s *Server) SetEncoderType(t string) {
	s.encoderType = t
}

func (s *Server) SetAudioEnabled(v bool) {
	s.audioEnabled = v
}

func (s *Server) SetInputCallback(fn func([]byte)) {
	s.onInput = fn
}

// Start begins listening with auto-generated TLS for WebCodecs compatibility.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(s.cfg.ClientFS)))
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/ws", s.handleWS)

	tlsCert, err := generateSelfSignedCert()
	if err != nil {
		return err
	}

	s.httpSrv = &http.Server{
		Addr:    net.JoinHostPort(s.cfg.Bind, itoa(s.cfg.Port)),
		Handler: mux,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		},
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	ln, err := tls.Listen("tcp", s.httpSrv.Addr, s.httpSrv.TLSConfig)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutCtx)
	}()

	s.cfg.Log.Info("server", "listening on https://"+ln.Addr().String())
	if err := s.httpSrv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "FeatherDesk"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("0.0.0.0"), net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}, nil
}

// Broadcast sends an encoded frame to all connected clients.
// The frame is prepended with a protocol header.
func (s *Server) Broadcast(nals [][]byte, width, height uint16, timestamp uint64) {
	for _, nal := range nals {
		hdr := protocol.FrameHeader{
			Type:        protocol.FrameTypeVideoH264,
			Timestamp:   timestamp,
			Width:       width,
			Height:      height,
			PayloadSize: uint32(len(nal)),
		}
		var hdrBytes [protocol.HeaderSize]byte
		protocol.MarshalHeader(hdr, hdrBytes[:])
		msg := make([]byte, protocol.HeaderSize+len(nal))
		copy(msg, hdrBytes[:])
		copy(msg[protocol.HeaderSize:], nal)

		// Check if this NAL is an IDR (store for new clients)
		if isIDR(nal) {
			s.idrMu.Lock()
			s.lastIDR = msg
			s.idrMu.Unlock()
		}

		s.clients.Range(func(key, _ any) bool {
			c := key.(*Client)
			c.Send(msg)
			return true
		})

		s.framesBroadcast.Add(1)
		s.bytesBroadcast.Add(uint64(len(msg)))
	}
}

// BroadcastAudio sends a PCM audio chunk to all connected clients.
func (s *Server) BroadcastAudio(pcm []byte, timestamp uint64) {
	hdr := protocol.FrameHeader{
		Type:        protocol.FrameTypeAudioPCM,
		Timestamp:   timestamp,
		Width:       48000, // sample rate in width field
		Height:      2,     // channels in height field
		PayloadSize: uint32(len(pcm)),
	}
	var hdrBytes [protocol.HeaderSize]byte
	protocol.MarshalHeader(hdr, hdrBytes[:])
	msg := make([]byte, protocol.HeaderSize+len(pcm))
	copy(msg, hdrBytes[:])
	copy(msg[protocol.HeaderSize:], pcm)

	s.clients.Range(func(key, _ any) bool {
		c := key.(*Client)
		c.Send(msg)
		return true
	})
}

func (s *Server) ClientCount() int {
	return int(s.count.Load())
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	hasController := s.controller.Load() != nil
	json.NewEncoder(w).Encode(map[string]any{
		"clients":          s.ClientCount(),
		"max_clients":      maxClients,
		"encoder":          s.encoderType,
		"audio":            s.audioEnabled,
		"has_controller":   hasController,
		"frames_broadcast": s.framesBroadcast.Load(),
		"bytes_broadcast":  s.bytesBroadcast.Load(),
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if int(s.count.Load()) >= maxClients {
		http.Error(w, "max clients reached", http.StatusServiceUnavailable)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.cfg.Log.Error("server", "ws accept: "+err.Error())
		return
	}

	role := r.URL.Query().Get("role")
	isController := role == "control"

	client := newClient(conn, s.cfg.Log)

	if isController && s.controller.CompareAndSwap(nil, client) {
		client.onText = s.onInput
		s.cfg.Log.Info("server", "controller connected")
	} else if isController {
		client.onText = nil
		s.cfg.Log.Info("server", "controller rejected (slot taken), connected as viewer")
	}

	s.clients.Store(client, struct{}{})
	s.count.Add(1)
	s.cfg.Log.Info("server", "client connected ("+itoa(s.ClientCount())+" total)")

	s.idrMu.RLock()
	idr := s.lastIDR
	s.idrMu.RUnlock()
	if idr != nil {
		client.Send(idr)
	}

	if s.onNewClient != nil {
		s.onNewClient()
	}

	client.ReadLoop(r.Context())

	s.clients.Delete(client)
	s.count.Add(-1)
	if s.controller.CompareAndSwap(client, nil) {
		s.cfg.Log.Info("server", "controller disconnected")
	}
	s.cfg.Log.Info("server", "client disconnected ("+itoa(s.ClientCount())+" total)")
}

func isIDR(nal []byte) bool {
	if len(nal) < 5 {
		return false
	}
	var nalType byte
	if nal[0] == 0 && nal[1] == 0 && nal[2] == 0 && nal[3] == 1 {
		nalType = nal[4] & 0x1F
	} else if nal[0] == 0 && nal[1] == 0 && nal[2] == 1 {
		nalType = nal[3] & 0x1F
	}
	return nalType == 5
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
