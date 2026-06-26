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
    // codecType is FrameTypeVideoH264 (additional FrameType* values may be
    // added as new codecs are introduced — VP8 was rejected and its slot
    // (5) is reserved).
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
}

// Config holds server configuration.
type Config struct {
    Port           int    // HTTPS port (default: 30084)
    Bind           string // Bind address (default: "0.0.0.0")
    Log            *slog.Logger
    ClientFS       fs.FS              // Embedded web client filesystem
    Authenticator  auth.Authenticator // see MODULE_AUTH.md — gates WebSocket upgrade + /pair
    SessionCache   SessionCache       // for reconnect (see "Resume Path" below)
    AllowTakeover  bool               // controller takeover policy
    StreamMgr      stream.Manager     // applies client-driven parameter changes
}
```

**Sequence ownership:** the Server is the SOLE owner of the video and audio sequence counters (independent `atomic.Uint32` per type). They are assigned inside `Broadcast`/`BroadcastAudio`. The pipeline never sets sequence numbers.

---

## Internal Architecture

### HTTP Endpoints

| Path | Method | Handler | Description |
|------|--------|---------|-------------|
| `/` | GET | `http.FileServer` | Serves embedded web client (index.html + compositor.js) |
| `/healthz` | GET | `handleHealth` | `200 {"status":"ok"}` for load balancer probes |
| `/ws` | GET | `handleWS` | WebSocket upgrade endpoint (gated by `auth.Authenticator`) |
| `/auth` | POST | `handleAuth` | Login endpoint for password/token modes (returns session token) |
| `/pair` | POST | `handlePair` | PIN-based pairing (Sunshine-style first-launch flow) — see [`MODULE_AUTH.md`](./MODULE_AUTH.md) |
| `/logout` | POST | `handleLogout` | Revoke a session token immediately |

> Operational metrics live on a **separate Prometheus endpoint** (port 9090
> by default, plain HTTP, no auth) per the `[metrics]` section of
> [`./MODULE_CONFIG.md`](./MODULE_CONFIG.md). The legacy `/status` JSON
> endpoint on the main port is being replaced by `/healthz` (binary check)
> plus the Prometheus endpoint (detailed metrics) — keeps the main TLS
> server free of unauthenticated observability surface.

### TLS Configuration

TLS is **mandatory** -- WebCodecs in browsers requires a secure context.

**Minimum version:** TLS 1.2 (configurable via `server.tls.min_version`).
**Cipher suites:** AEAD only -- GCM and ChaCha20-Poly1305. No CBC, RC4, 3DES.
Go implementation: set `tls.Config.MinVersion = tls.VersionTLS12` and
`tls.Config.CipherSuites` to the AEAD subset.

Sources, in order:

1. **`server.tls.cert` + `server.tls.key` both set** in the TOML config ->
   load the PEM chain and key from those paths. Reload on SIGHUP.
2. **Both empty** (development) -> server generates a self-signed ECDSA P-256
   certificate at startup:
   - Common Name: `viewport-rds`
   - SANs: `localhost`, `127.0.0.1`, hostname
   - Valid: 1 year
   - Cached on disk under the OS-conventional state directory so restarts
     reuse the same cert. **Private key file permissions: mode 0600 (Unix)
     / restrictive ACL (Windows). Verified on startup -- if permissions
     are too open, server refuses to start with a clear error.**

There is no Let's Encrypt integration -- front the server with a reverse
proxy (Caddy, nginx, traefik) for ACME if needed.

### WebSocket Connection Lifecycle

```
1. Client connects to /ws?role=control|view[&resume=<token>&last_video_seq=N]
2. Resume path: if resume=<token> present:
     a. SessionCache.Get(token) — verify token + check TTL
     b. On success: skip auth, mark client as "resumed", jump to step 7
     c. On failure: close with 4401 (client must re-auth)
3. Auth path: Authenticator.Authenticate(r) — see MODULE_AUTH.md
     - Mode=none: accept (returns empty Identity)
     - Mode=token: check ?token=<t> against config
     - Mode=password: require Authorization: Bearer <session_token> (issued by POST /auth)
     - Mode=pin: require Authorization: Bearer <session_token> (issued by POST /pair)
     On failure: reject with HTTP 401 (before WebSocket upgrade)
4. Check cfg.Server.MaxClients (default 25) → reject with 503 if full
5. websocket.Accept() (Origin checked per R-SRV-06)
6. Issue new session token (32-byte random, base64url) → SessionCache.Put(token, sessionState)
7. role=control? → atomic CAS on controller slot
     - First wins; others become viewers
     - If AllowTakeover && current controller is the same authenticated user → CAS replaces
8. Store *Client in sync.Map, increment atomic counter
9. SEND Config frame FIRST: codec, dims, fps, hdr, cursorMode,
   session_token (from step 6), session_ttl_sec, resumed=true/false
10. Send cached keyframe message if available (Config → keyframe → live)
11. If NO keyframe cached: invoke onNewClient → pipeline forces keyframe on active encoder
    If keyframe cached: do NOT force (avoid storm)
12. Block in ReadLoop():
    - JSON text → dispatch:
        input events       → onInput callback (controller only)
        resize/set_*       → stream.Manager (controller only; viewers ignored)
        {"type":"keyframe"} → rate-limited keyframe-request callback
        {"type":"pong"}    → record RTT
        {"type":"stats"}   → record client telemetry
    - Binary messages from client → protocol violation, ignore/log
13. On disconnect:
    - Remove from sync.Map; decrement counter
    - Release controller slot (if was controller)
    - Update SessionCache entry: keep state cached for reconnect.cache_ttl_seconds
    - conn.CloseNow()
```

**Never** restart the capturer on connect (the original `capturer.Restart()` is removed -- it disrupted all viewers).

### DoS Protection

- **Message size limit:** `conn.SetReadLimit(server.max_message_bytes)` (default 4096).
  Any text frame exceeding this is immediately closed with status 1009 (Message Too Big).
  No legitimate input event exceeds 4KB.
- **Input rate limit:** Per-client token bucket at `server.input_rate_limit` events/sec
  (default 1000). `mousemove` events are coalesced (only the latest position is kept).
  Events exceeding the bucket are silently dropped.
- **Keyframe rate limit:** Max 1 forced keyframe per 500ms (coalesced across all clients).
- **Connection limit:** `server.max_clients` (default 25). Excess connections rejected with 503.

### Session Cache (Reconnect)

```go
// SessionCache stores ephemeral per-session state across short disconnects.
// Implementation lives in internal/server; interface in pkg/server.
type SessionCache interface {
    // Put stores session state under the given token with the configured TTL.
    Put(token string, st SessionState)
    // Get fetches state and refreshes TTL on hit; returns ok=false on miss/expiry.
    Get(token string) (st SessionState, ok bool)
    // Delete revokes a token (e.g., /logout).
    Delete(token string)
}

type SessionState struct {
    UserID      string         // identifier from auth (empty for token mode)
    Role        string         // "control" | "view"
    CreatedAt   time.Time
    ExpiresAt   time.Time      // refreshed on each WebSocket connect
    LastVideoSeq uint32        // for client-driven catch-up (currently informational)
    LastParams  stream.Params  // snapshot of resolution/bitrate/HDR at disconnect
}
```

TTL comes from `[reconnect] cache_ttl_seconds` (default 300s). The cache is in-memory only — restarting the binary invalidates all sessions. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) and [`MODULE_CONFIG.md`](./MODULE_CONFIG.md).

### Client State

```go
type Client struct {
    conn   *websocket.Conn
    send   chan []byte       // Buffered write channel (cap 16)
    log    *slog.Logger
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

- A keyframe is a complete access unit that already contains `SPS, PPS, IDR` (H.264) — because all of a frame's NALs are in ONE message, the cache is automatically self-contained and decodable.
- On `Broadcast`, if `f.Keyframe` (and verified by inspecting NALs for IDR type 5), store the whole `msg` under `idrMu`.
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

### R-SRV-01: Authentication (now base feature — implemented via MODULE_AUTH)
Authentication is mandatory for all non-`none` modes. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) for modes (token / password / pin / oauth-deferred), the Authenticator interface, session token issuance, and the `/auth` + `/pair` + `/logout` HTTP handlers. The server's only job here is calling `cfg.Authenticator.Authenticate(r)` at the WebSocket upgrade and routing to the appropriate session-cache lookup on `?resume=`.

### R-SRV-02: Fix Codec Type Constant (folded into interface)
`Broadcast(codecType uint8, f EncodedFrame)` now carries the codec type; the server emits `FrameTypeVideoH264` (and future codec frame types as added). The codec is also advertised in the Config handshake so the client configures the matching decoder.

### R-SRV-03: Remove Custom itoa()
Replace the hand-rolled `itoa()` function (lines 286-306) with `strconv.Itoa()`.

### R-SRV-04: Add Client Metrics Per-Connection
Track per-client: frames sent, frames dropped, bytes sent, connection duration, latency (via ping/pong RTT).

### R-SRV-05: Graceful Client Notification on Shutdown
Before shutting down, send a control frame to all clients indicating "server shutting down" so the client can show appropriate UI.

### R-SRV-06: Origin Validation
Replace `InsecureSkipVerify` with configurable origin checking. Default to same-host only; allow override via `server.allow_origin` in the TOML config (see MODULE_CONFIG.md).

### R-SRV-07: Extract Interface to `pkg/server`
Move the `Server` interface and `Config` to a public package. Keep WebSocket implementation in `internal/server/`.

### R-SRV-08: Bandwidth Estimation (now base feature — flows into stream.Manager)
The server measures RTT (via WS ping/pong, every 10s) and packet-loss proxy (via the `{"type":"stats"}` JSON message: `dropped` counter delta). It feeds these signals to `stream.Manager` every 500ms; the Manager applies the adaptive policy from `[stream.adaptive]` and pushes updated `stream.Params` to the encoder + capturer via the `Configurable*` interfaces. See [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md).

### R-SRV-09: Health Check Endpoint
`/healthz` endpoint returns 200 `{"status":"ok"}` for load balancer probes. Detailed counters (uptime, clients, frames, bytes/sec, drop rates) live on the Prometheus endpoint (separate port — see MODULE_CONFIG `[metrics]`).

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
