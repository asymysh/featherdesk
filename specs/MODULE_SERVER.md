# Module Spec: Server

## Overview

The Server module manages HTTPS/WebSocket connections, client lifecycle, frame broadcasting, and serves the embedded web client. It is the central hub that connects all other modules to external consumers.

---

## Public Interface

```go
package server

// Server manages WebSocket connections and frame distribution.
type Server interface {
    // Start begins listening for connections. Blocks until ctx is cancelled.
    Start(ctx context.Context) error

    // Broadcast assembles ONE per-frame message (header + concatenated NALs)
    // and fans it out to all clients. The server assigns the video Sequence,
    // and caches the whole message if it is a keyframe (for fast-join).
    // codecType is FrameTypeVideoH264 or FrameTypeVideoVP8.
    Broadcast(codecType uint8, f EncodedFrame)

    // BroadcastAudio sends one PCM chunk to all clients. Server assigns the
    // audio Sequence; the chunk carries its capture-time Timestamp.
    BroadcastAudio(chunk AudioChunk)

    // SendConfig pushes a Config (handshake / capability change) to all clients,
    // or to a single client on connect.
    SendConfig(cfg ConfigPayload)

    // SendCursor pushes a CursorUpdate (client-side cursor mode).
    SendCursor(c CursorUpdate)

    // ClientCount returns the current number of connected clients.
    ClientCount() int32

    // SetConfigProvider supplies the current Config to send to each new client
    // on connect (BEFORE any video frame).
    SetConfigProvider(fn func() ConfigPayload)

    // SetNewClientCallback fires when a new client connects. The pipeline uses it
    // to force a keyframe ONLY when no cached keyframe is available.
    SetNewClientCallback(fn func())

    // SetInputCallback fires for each input JSON text message from the controller.
    // The callback injects the event and returns its seq (0 if none); the server
    // then emits an InputAck(seq) so the client can measure round-trip latency.
    SetInputCallback(fn func([]byte) (seq uint32))

    // SetKeyframeRequestCallback fires when a client requests a keyframe
    // (JSON {"type":"keyframe"}). The server rate-limits before invoking.
    SetKeyframeRequestCallback(fn func())

    SetEncoderType(t string)
    SetAudioEnabled(v bool)
}

// Config holds server configuration.
type Config struct {
    Port     int    // HTTPS port (default: 30084)
    Bind     string // Bind address (default: "0.0.0.0")
    Token    string // Session auth token (empty = generate random at startup)
    Log      Logger
    ClientFS fs.FS  // Embedded web client filesystem
}
```

**Sequence ownership:** the Server is the SOLE owner of the video and audio sequence counters (independent `atomic.Uint32` per type). They are assigned inside `Broadcast`/`BroadcastAudio`. The pipeline never sets sequence numbers.

---

## Internal Architecture

### HTTP Endpoints

| Path | Method | Handler | Description |
|------|--------|---------|-------------|
| `/` | GET | `http.FileServer` | Serves embedded web client (index.html + ES modules) |
| `/status` | GET | `handleStatus` | JSON status: `{"clients": N, "encoder": "...", "audio": bool}` |
| `/ws` | GET | `handleWS` | WebSocket upgrade endpoint |

### TLS Configuration

Self-signed ECDSA P-256 certificate generated at startup:
- Common Name: "viewport-rds"
- SANs: localhost, 127.0.0.1, 0.0.0.0
- Valid: 1 year
- Purpose: Enables WebCodecs API (requires secure context in browsers)

### WebSocket Connection Lifecycle

```
1. Client connects to /ws?role=control|view&token=<t>
2. Validate token (R-SRV-01) → reject 401 if invalid
3. Check maxClients (25) → reject with 503 if full
4. websocket.Accept() (Origin checked per R-SRV-06)
5. role=control? → atomic CAS on controller slot (first wins; others become viewers)
6. Store *Client in sync.Map, increment atomic counter
7. SEND Config frame FIRST (codec, dims, fps, audio, cursorMode) ← decoder setup
8. Send cached keyframe message if available (Config → keyframe → live)
9. If NO keyframe cached: invoke onNewClient → pipeline forces keyframe on active encoder
   If keyframe cached: do NOT force (avoid storm)
10. Block in ReadLoop():
    - JSON text → dispatch:
        input events → onInput callback (controller only)
        {"type":"keyframe"} → rate-limited keyframe-request callback
        {"type":"pong"} → record RTT
        {"type":"stats"} → record client telemetry
    - Binary messages from client → protocol violation, ignore/log
11. On disconnect:
    - Remove from sync.Map; decrement counter
    - Release controller slot (if was controller)
    - conn.CloseNow()
```

**Never** restart the capturer on connect (the original `capturer.Restart()` is removed — it disrupted all viewers).

### Client State

```go
type Client struct {
    conn   *websocket.Conn
    send   chan []byte       // Buffered write channel (cap 16)
    log    *logger.Logger
    onText func([]byte)     // Text message handler
}
```

**Per-Client Goroutines:**
- `writePump()`: Drains `send` channel → `conn.Write()`. Sends WebSocket ping every 10s.
- `ReadLoop()`: Blocking read → dispatch text messages to callback.

### Broadcasting (one message per frame)

```go
func (s *Server) Broadcast(codecType uint8, f EncodedFrame) {
    // 1. Concatenate all f.NALs verbatim → payload (Annex B, start codes retained)
    // 2. Marshal 22-byte header:
    //      Version=1, Type=codecType, Sequence=s.videoSeq++ (atomic),
    //      Timestamp=f.Timestamp, Width=f.Width, Height=f.Height, PayloadSize=len(payload)
    // 3. msg = header || payload   (single allocation, single WebSocket message)
    // 4. If f.Keyframe: cache msg under idrMu (the COMPLETE access unit = SPS+PPS+IDR)
    // 5. Range over sync.Map: client.Send(msg) → non-blocking enqueue; drop if buffer full
}
```

Exactly one WebSocket binary message per frame. NALs are NEVER split across messages (this fixes the original per-NAL broadcast).

### Keyframe Caching Strategy (fixed)

- A keyframe is a complete access unit that already contains `SPS, PPS, IDR` (H.264) or a VP8 keyframe — because all of a frame's NALs are in ONE message, the cache is automatically self-contained and decodable.
- On `Broadcast`, if `f.Keyframe` (and verified by inspecting NALs for IDR type 5 / VP8 keyframe bit), store the whole `msg` under `idrMu`.
- On new client connect, the server sends, in order: **Config → cached keyframe msg (if any) → live frames**.
- If NO keyframe is cached yet (very first client), the server invokes the new-client callback so the pipeline forces a keyframe on the active encoder.
- If a keyframe IS cached, the server does NOT force a new one — the joiner decodes from the cache. This prevents a keyframe storm when many clients join.

**Sequence note for fast-join:** the cached keyframe carries its original (possibly old) sequence number. The client initializes `lastSeq` from the first frame it receives and suppresses gap detection on the first live transition (see protocol "Fast-Join Reconciliation").

### Keyframe-Request Rate Limiting

- Clients request keyframes via JSON text `{"type":"keyframe"}` (gap recovery).
- The server coalesces requests: at most **one forced keyframe per 500 ms**, regardless of how many clients ask. Requests within the window are dropped (the imminent keyframe satisfies them all).

### Controller Model

- Single controller slot (`atomic.Pointer[Client]`)
- First client with `?role=control` claims the slot via CAS
- Controller receives all input events (keyboard/mouse/wheel)
- Other clients are passive viewers (no input)
- When controller disconnects, slot becomes available for next `?role=control` client

---

## Refactoring Directives

### R-SRV-01: Add Authentication
Implement token-based access control:
- Server generates a random session token at startup (printed to stdout)
- Clients must provide token as `?token=...` query parameter on WebSocket upgrade
- Reject unauthorized connections with 401
- Optional: separate tokens for controller vs viewer roles

### R-SRV-02: Fix Codec Type Constant (folded into interface)
`Broadcast(codecType uint8, f EncodedFrame)` now carries the codec type; the server emits `FrameTypeVideoH264` or `FrameTypeVideoVP8` accordingly. The codec is also advertised in the Config handshake so the client configures the matching decoder.

### R-SRV-03: Remove Custom itoa()
Replace the hand-rolled `itoa()` function (lines 286-306) with `strconv.Itoa()`.

### R-SRV-04: Add Client Metrics Per-Connection
Track per-client: frames sent, frames dropped, bytes sent, connection duration, latency (via ping/pong RTT).

### R-SRV-05: Graceful Client Notification on Shutdown
Before shutting down, send a control frame to all clients indicating "server shutting down" so the client can show appropriate UI.

### R-SRV-06: Origin Validation
Replace `InsecureSkipVerify` with configurable origin checking. Default to same-host only; allow `--allow-origin=*` for development.

### R-SRV-07: Extract Interface to `pkg/server`
Move the `Server` interface and `Config` to a public package. Keep WebSocket implementation in `internal/server/`.

### R-SRV-08: Add Bandwidth Estimation
Implement periodic bandwidth probes between server and client to enable adaptive bitrate in the encoder.

### R-SRV-09: Health Check Endpoint
Add `/healthz` endpoint returning 200 with `{"status": "ok", "uptime": "...", "clients": N}` for monitoring/load balancing.

### R-SRV-10: Multi-Controller Support (Future)
Allow configurable number of controllers (for pair programming). Input events would need a priority/merge strategy.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Status endpoint response format | No |
| Unit | IDR detection (NAL type parsing) | No |
| Unit | Client Send with full/empty channel | No |
| Integration | WebSocket upgrade + binary frame delivery | No |
| Integration | Client disconnect cleanup (no panic, count=0) | No |
| Integration | New client receives cached IDR | No |
| Integration | Multi-client broadcast fan-out | No |
| Load | 25 concurrent clients receiving 60fps | No (but resource-heavy) |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Broadcast fan-out (25 clients, 1080p frame) | <2ms |
| Connection setup (TLS + WS upgrade) | <100ms |
| Max clients | 25 (configurable) |
| Frame drop under load | <5% per slow client |
| Ping RTT (LAN) | <5ms |
| Memory per client | <512KB (16 frame buffers × ~32KB each) |
