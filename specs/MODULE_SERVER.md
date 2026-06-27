# Module Spec: Server

## Overview

The Server module manages **HTTP/3 + WebTransport** sessions, client lifecycle,
frame broadcasting, and serves the embedded web client. It is the central hub
that connects all other modules to external consumers.

Transport-layer concerns (UDP listener, QUIC handshake, stream multiplexing,
datagram delivery, auth handshake on the control stream) live in
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md). The server module **consumes**
the `transport.Transport` + `transport.Session` interfaces and adds:

- Session bookkeeping (controller slot, viewer list, max-clients enforcement).
- Per-session goroutines (read control stream, read input stream, accept
  clipboard + file-transfer streams, pump frames out).
- Broadcast fan-out (one assembled access unit goes to every session's
  **frame-granular** out-queue; the pump fragments at send time).
- Keyframe cache + **bootstrap-stream** fast-join.
- Direction + role gating for clipboard / file transfer / parameter changes.

---

## Public Interface

```go
package server

// Server manages WebTransport sessions and frame distribution.
type Server interface {
    // Start begins listening for connections. Blocks until ctx is cancelled.
    Start(ctx context.Context) error

    // Broadcast assembles ONE access unit (22-byte FrameHeader || Annex B
    // payload) and fans it out to every session's frame-granular out-queue.
    // The server assigns the video Sequence and, when f.Keyframe is set BY THE
    // ENCODER (the server does NOT re-scan the bitstream), caches the assembled
    // access unit for bootstrap-stream fast-join. codecType is
    // FrameTypeVideoH264 / FrameTypeVideoHEVC (VP8 was rejected; slot 5 reserved).
    Broadcast(codecType uint8, f EncodedFrame)

    // BroadcastAudio sends one already-encoded audio payload (Opus packet or raw
    // PCM) to all clients. codecType is FrameTypeAudioOpus (0x08) or
    // FrameTypeAudioPCM (0x04); captureTs is the chunk's capture-time Timestamp.
    // The server assigns the independent audio Sequence, marshals the 22-byte
    // FrameHeader (Width=Height=0; codec/rate/channels are in the config message),
    // and enqueues it frame-granular (single datagram for Opus, fragmented for PCM).
    BroadcastAudio(codecType uint8, payload []byte, captureTs uint64)

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

    // SetInputCallback fires for each BINARY input frame from the controller.
    // The callback (input.Dispatcher.Dispatch) decodes + injects the event and
    // returns its seq; the server then emits an InputAck(seq) so the client can
    // measure round-trip latency. Input is binary, not JSON — see MODULE_INPUT.md.
    SetInputCallback(fn func(frame []byte) (seq uint32, err error))

    // SetClipboardCallback fires for a clipboard message (C→H) read off the
    // clipboard stream as [u32 Len][JSON].
    SetClipboardCallback(fn func(content clipboard.Content) error)

    // SetFileTransferService hands the file-transfer service to the server so
    // newly-accepted bidirectional streams whose first message identifies them
    // as file-transfer streams get routed into it. nil disables file transfer.
    SetFileTransferService(svc filetransfer.Service)

    // SetKeyframeRequestCallback fires when a client requests a keyframe
    // (JSON {"type":"keyframe"}). The server rate-limits before invoking.
    SetKeyframeRequestCallback(fn func())

    // SendGamepadRumble frames + sends a FrameTypeGamepadRumble (type 15) to
    // the current controller client. Called by the pipeline when the active
    // gamepad add-on receives a vibration request from the host game (see
    // MODULE_GAMEPAD.md). No-op if [gamepad] allow_rumble = false or there
    // is no controller connected.
    SendGamepadRumble(index uint8, weak, strong uint16, durationMs uint32)
}

// Config holds server configuration.
type Config struct {
    Transport      transport.Transport // built by MODULE_TRANSPORT; bound to UDP port
    Log            *slog.Logger
    ClientFS       fs.FS               // Embedded web client filesystem (served from /)
    Authenticator  auth.Authenticator  // see MODULE_AUTH.md — validates control-stream auth
    SessionCache   SessionCache        // for resume (see "Resume Path")
    AllowTakeover  bool                // controller takeover policy
    StreamMgr      stream.Manager      // applies client-driven parameter changes
    MaxClients     int                 // hard cap; rejects via CloseAuthFailed on overflow
}
```

**Sequence ownership:** the Server is the SOLE owner of the video and audio sequence counters (independent `atomic.Uint32` per type). They are assigned inside `Broadcast`/`BroadcastAudio`. The pipeline never sets sequence numbers.

---

## Internal Architecture

### HTTP/3 Endpoints

All endpoints run on the **same HTTP/3 server** (the QUIC/UDP port). There is
**no separate TCP listener**.

| Path | Method | Handler | Description |
|------|--------|---------|-------------|
| `/` | GET | `http.FileServer` | Serves embedded web client (index.html + JS bundle) |
| `/healthz` | GET | `handleHealth` | `200 {"status":"ok"}` for liveness probes |
| `/wt` | GET (upgrade) | `handleWebTransport` | WebTransport session upgrade. All media + control + input + file transfer multiplexes here. |
| `/auth` | POST | `handleAuth` | Credential exchange → session token (password/PIN modes) |
| `/pair` | POST | `handlePair` | PIN-based pairing (Sunshine-style first-launch flow) — see [`MODULE_AUTH.md`](./MODULE_AUTH.md) |
| `/logout` | POST | `handleLogout` | Revoke a session token immediately |

> **Why no separate `/files`:** previously file transfer ran on its own
> WebSocket to avoid HOL blocking video. Under QUIC each file transfer is its
> own bidirectional stream within the same WebTransport session — automatically
> isolated from media, same auth, same TLS context. One fewer endpoint, no
> isolation loss. The file-transfer protocol from
> [`MODULE_FILETRANSFER.md`](./MODULE_FILETRANSFER.md) is unchanged; only its
> carrier changes.

> Operational metrics live on a **separate Prometheus endpoint** (port 9090
> by default, plain HTTP, no auth) per the `[metrics]` section of
> [`./MODULE_CONFIG.md`](./MODULE_CONFIG.md). The legacy `/status` JSON
> endpoint on the main port is being replaced by `/healthz` (binary check)
> plus the Prometheus endpoint (detailed metrics) — keeps the main TLS
> server free of unauthenticated observability surface.

### TLS Configuration

TLS is **mandatory and TLS 1.3** — QUIC requires TLS 1.3, so the
`min_version` knob from the earlier WSS-era spec is gone (informational only).

Sources, in order:

1. **`server.tls.cert` + `server.tls.key` both set** in the TOML config →
   load the PEM chain and key from those paths. Hot reload on SIGHUP.
2. **Both empty** (development) → server generates a self-signed ECDSA P-256
   certificate at startup:
   - Common Name: `featherdesk`
   - SANs: `localhost`, `127.0.0.1`, hostname
   - Valid: 1 year
   - Cached on disk under the OS-conventional state directory so restarts
     reuse the same cert. **Private key file permissions: mode 0600 (Unix)
     / restrictive ACL (Windows). Verified on startup — if permissions
     are too open, server refuses to start with a clear error.**

The cert is loaded into both the `tls.Config` and `quic.Config` (they share the
same `*tls.Certificate`). One cert, one private key, one HTTP/3 server.

There is no Let's Encrypt integration — front the server with a reverse
proxy (Caddy, nginx, traefik) for ACME if needed. NOTE: many ACME-issuing
proxies do not yet support proxying HTTP/3 + WebTransport upstream; verify
your proxy's QUIC support before deploying.

### WebTransport Session Lifecycle

```
1. Client opens WebTransport: new WebTransport("https://host:port/wt")
   No credentials, role, or takeover flag in the URL or HTTP headers (browsers
   can't set them) — they all travel in the control-stream auth message (step 7).
2. Transport handler upgrades to WebTransport (TLS 1.3, Origin checked per R-SRV-06).
3. transport.Transport surfaces the new Session on its SessionsChan.
4. Server-side: spawn a per-session goroutine. The session has NO privileges yet.
5. Server starts a 5-second auth timer ON SESSION ACCEPT (covers a client that
   never opens any stream — it is closed at +5s).
6. streamAcceptor reads the StreamType tag (first byte) of each accepted bidi
   stream. The CONTROL stream is the one tagged 0x00 (StreamControl); a second
   0x00 is a protocol error.
7. Server reads the first JSON line on the control stream:
       {"type":"auth","token":"<bearer>","role":"control|view","resume":bool}
8. Authenticator.Authenticate validates the token. See MODULE_AUTH.md.
     - Resume path: if resume=true AND SessionCache.Get(token) hits within TTL,
       skip new-session setup, mark "resumed", jump to step 12.
     - Fresh auth: validate token according to mode (token/password/pin/none).
     - Failure: server writes {"type":"auth_failed"} on the control stream,
       then CloseWithError(CloseAuthFailed=4401). Session ends.
     - 5-second timer expired without auth: CloseWithError(CloseAuthTimeout=4408).
9. Check cfg.MaxClients → CloseWithError(CloseAuthFailed) if full (overloaded
   variant: distinguishable via reason string "max_clients").
10. Issue new session_token (32-byte random, base64url) → SessionCache.Put.
11. role check + controller slot:
     - First control connection wins; subsequent control whose auth message
       omits `"takeover":true` become viewers.
     - Explicit takeover (auth message `"takeover":true`) honored only when
       AllowTakeover is true. Displaced controller is closed with
       CloseControllerTakeover (4410).
12. Send the config message on the control stream as a JSON line (NOT a binary
    frame): {"type":"config","codec":…,"width":…,…,"session_token":…,
    "session_ttl_sec":…,"resumed":true|false}.
13. Seed the joiner's decoder: open a bootstrap UNI stream (tag 0x10) and write
    [u32 Len][FrameHeader‖IDR] of the cached access unit, then close it. If NO
    keyframe is cached yet (very first client), invoke onNewClient → pipeline
    forces a keyframe; the first encoded IDR is then sent on the bootstrap stream.
14. INPUT stream: the stream tagged 0x01 (controller role only). Spawn an
    input-reader goroutine that reads [u16 RecLen]-prefixed records and forwards
    each to input.Dispatcher.Dispatch; it writes InputAck back length-prefixed.
15. The same streamAcceptor loop dispatches the remaining tags: 0x02 → clipboard
    handler (a [u32 Len][JSON] reader/writer; direction+role gated), 0x03 → a new
    file-transfer stream handed to filetransfer.Service. An unknown tag, or an
    0x01/0x02 from a view-role client, → CancelRead+CancelWrite(CloseProtocolError).
16. Frame-out queue: per-session bounded channel of WHOLE access units
    (cap = datagram_send_queue_frames, default 8; drop-OLDEST on overflow).
    Broadcast() pushes one assembled access unit per frame; the datagramPump
    goroutine pulls one frame, fragments it, and calls session.SendDatagram per
    fragment. NEVER a fragment-granular channel (would corrupt frames mid-send —
    see MODULE_TRANSPORT "Datagram Fragmentation", fixes T-1).
17. Control-stream reader loop dispatches JSON lines (controller only for
    role-gated ones):
        resize/set_*           → stream.Manager (drop for viewers)
        {"type":"keyframe"}    → rate-limited keyframe-request callback
        {"type":"pong"}        → record RTT
        {"type":"stats"}       → record client telemetry
    (Clipboard is NOT here — it rides the clipboard stream from step 15.)
18. Datagram-in loop: ReadDatagram blocks until the client sends one.
    In v1 there are no C→S datagrams (reserved); any datagram received is
    counted in a metric and dropped.
19. Session close (either side, or context cancel):
    - Cancel all per-session goroutines via session.Context().
    - Release controller slot (if was controller).
    - Drain in-flight file-transfer streams.
    - Update SessionCache entry: keep state cached for reconnect.cache_ttl_seconds.
    - Close the underlying transport session.
```

**Never** restart the capturer on connect (the original `capturer.Restart()` is removed — it disrupted all viewers).

### DoS Protection

- **Control-stream message size limit:** the control reader bounds each
  JSON message at `server.max_message_bytes` (default 4096). Any larger
  message → close the stream with `CloseProtocolError (4400)`. Legitimate
  JSON control messages stay well under 4 KB.
- **Datagram size limit:** QUIC enforces the per-datagram MTU (~1200 bytes)
  natively; the receiver discards any datagram whose 8-byte DatagramHeader
  is malformed or whose FragIndex (bits 0-14, LAST = bit 15) is out of range.
  There is no FragCount field.
- **Reassembly size cap:** a frame whose total reassembled size would exceed
  16 MB is rejected and the in-progress buffer discarded.
- **Input rate limit:** per-client token bucket at `server.input_rate_limit`
  events/sec (default 1000). `mousemove` events are coalesced (only the latest
  position is kept). Excess events are silently dropped at the input-reader
  goroutine.
- **Keyframe rate limit:** max 1 forced keyframe per 500 ms (coalesced across
  all clients).
- **Session limit:** `server.max_clients` (default 25). Excess sessions are
  closed at the post-auth check with `CloseAuthFailed (4401)` and a clear
  log line.

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
    ExpiresAt   time.Time      // refreshed on each WebTransport connect
    LastParams  stream.Params  // snapshot of resolution/bitrate/HDR at disconnect
}
```

> There is no `LastVideoSeq` field — resume always reseeds the decoder via a
> fresh bootstrap-stream IDR, so a stored last-sequence offers no optimization
> (see MODULE_PROTOCOL "Resume Path"). `LastParams` is kept so a resumed client
> restarts at the same resolution/bitrate/HDR instead of renegotiating.

TTL comes from `[reconnect] cache_ttl_seconds` (default 300s). The cache is in-memory only — restarting the binary invalidates all sessions. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) and [`MODULE_CONFIG.md`](./MODULE_CONFIG.md).

### Session State

```go
type Session struct {
    wt        transport.Session   // underlying WebTransport session
    control   transport.Stream    // bidi control stream (tag 0x00)
    input     transport.Stream    // bidi input stream (tag 0x01; nil for viewers)
    clip      transport.Stream    // bidi clipboard stream (tag 0x02; nil until opened)
    frameOut  chan []byte         // bounded queue of WHOLE access units
                                  // (cap datagram_send_queue_frames=8; drop-OLDEST)
    role      string              // "control" | "view"
    identity  auth.Identity
    log       *slog.Logger
}
```

**Per-session goroutines:**
- `datagramPump()`: pulls one whole access unit from `frameOut`, fragments it
  into ≤~1192-byte datagrams, and calls `wt.SendDatagram()` per fragment. Loss
  is whole-frame, never mid-frame. QUIC has its own keepalive (configured via
  `[transport] keepalive_period`).
- `controlReader()`: reads newline-JSON messages on the control stream →
  dispatches keyframe/pong/stats/resize/set_* (NOT clipboard — see below).
- `inputReader()` (controller only): reads `[u16 RecLen]`-prefixed binary
  records on the input stream → `input.Dispatcher.Dispatch`; writes `InputAck`
  back length-prefixed on the same stream.
- `clipboardReader()` (started when a 0x02 stream is accepted): reads
  `[u32 Len][JSON]` clipboard messages → `clipboard.Monitor.Set`
  (direction+role gated); writes host→client clipboard the same way.
- `streamAcceptor()`: `wt.AcceptStream` loop; reads each stream's StreamType tag
  and dispatches (0x01 input, 0x02 clipboard, 0x03 → `filetransfer.Service.ServeStream`).

### Broadcasting (frame-granular fan-out; pump fragments at send)

```go
func (s *Server) Broadcast(codecType uint8, f stream.EncodedFrame) {
    // 1. Marshal 22-byte FrameHeader: Version=1, Type=codecType,
    //    Sequence=s.videoSeq++ (atomic), Timestamp=f.Timestamp,
    //    Width=f.W, Height=f.H, PayloadSize=len(f.Data).
    // 2. Build header || f.Data into one contiguous []byte = the access unit.
    // 3. If f.Keyframe (set by the ENCODER — the server does NOT re-scan NALs),
    //    store this assembled access unit under idrMu as the bootstrap seed.
    // 4. Range over the sessions list: push the WHOLE access unit into each
    //    session's frameOut channel. If the channel is full, drop the OLDEST
    //    queued frame and enqueue this one (a slow client thus skips stale
    //    frames cleanly), and increment a per-session drop metric.
    //    Fragmentation happens later, in datagramPump — NOT here. The queue is
    //    frame-granular so a slow client never receives a half-sent frame.
}
```

`datagramPump` turns one access unit into N datagrams (N ≈ frame_bytes / 1192)
with the 8-byte DatagramHeader; fragment 0 starts with the 22-byte FrameHeader,
later fragments carry only raw payload bytes (see MODULE_PROTOCOL "Datagram
Fragmentation"). NALs are never split across access units.

### Keyframe Caching Strategy

- A keyframe is a complete access unit that contains `SPS, PPS, IDR` (H.264) or
  `VPS, SPS, PPS, IDR` (HEVC). The cache stores **the assembled access unit**
  (`FrameHeader || Annex B`), NOT pre-fragmented datagrams — because it is
  delivered over the reliable **bootstrap stream**, not as datagrams.
- On `Broadcast`, if `f.Keyframe` (encoder-set), the access unit is stored under `idrMu`.
- On new client connect/resume, the server sends, in order: **config (JSON line
  on the control stream) → cached IDR (on the bootstrap stream) → live datagrams**.
- If NO keyframe is cached yet (very first client), the server invokes the
  new-client callback so the pipeline forces a keyframe; that first IDR is then
  written to the bootstrap stream.
- If a keyframe IS cached, the server does NOT force a new one — the joiner
  decodes the bootstrap IDR. Prevents a keyframe storm.

**Sequence note for fast-join:** the cached keyframe carries its original
(possibly old) FrameID/Sequence. The client initializes `lastSeq` from the
bootstrap frame and suppresses gap detection on the first live datagram
transition (see protocol "Fast-Join").

### Keyframe-Request Rate Limiting

- Clients request keyframes via JSON `{"type":"keyframe"}` on the control
  stream (gap recovery, fragment-reassembly deadline hit, decoder error).
- The server coalesces requests: at most **one forced keyframe per 500 ms**,
  regardless of how many clients ask.

### Controller Model

- Single controller slot (`atomic.Pointer[Client]`)
- First client whose auth message carries `"role":"control"` claims the slot via CAS
- Controller receives all input events (keyboard/mouse/wheel)
- Other clients are passive viewers (no input)
- When controller disconnects, slot becomes available for the next `"role":"control"` client

---

## Refactoring Directives

### R-SRV-01: Authentication (now base feature — implemented via MODULE_AUTH)
Authentication is mandatory for all non-`none` modes. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) for modes (token / password / pin / oauth-deferred), the Authenticator interface, session token issuance, and the `/auth` + `/pair` + `/logout` HTTP handlers. The server's only job here is calling `cfg.Authenticator.Authenticate` on the first control-stream message and routing to the appropriate session-cache lookup based on the bearer token contained in that message.

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
Move the `Server` interface and `Config` to a public package. Keep the WebTransport server implementation in `internal/server/`.

### R-SRV-08: Bandwidth Estimation (now base feature — flows into stream.Manager)
The server derives RTT from the QUIC connection's **`SmoothedRTT`** (quic-go exposes it via `ConnectionState`), augmented by an app-level `{"type":"ping"}`/`{"type":"pong"}` on the control stream for an end-to-end sample. The fast-path congestion signal is the **server's own datagram-drop rate** (frames dropped from `frameOut` on overflow), NOT the result of `SendDatagram` (which is fire-and-forget and never reports loss), plus the client's `{"type":"stats"}` `dropped` delta. It feeds these signals to `stream.Manager` every 500ms; the Manager applies the adaptive policy from `[stream.adaptive]` and pushes updated `stream.Params` to the encoder + capturer via the `Configurable*` interfaces. See [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md).

### R-SRV-09: Health Check Endpoint
`/healthz` endpoint returns 200 `{"status":"ok"}` for load balancer probes. Detailed counters (uptime, clients, frames, bytes/sec, drop rates) live on the Prometheus endpoint (separate port — see MODULE_CONFIG `[metrics]`).

### R-SRV-10: Multi-Controller Support (Future)
Allow configurable number of controllers (for pair programming). Input events would need a priority/merge strategy.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | `/healthz` response format | No |
| Unit | StreamType tag dispatch (0x00/0x01/0x02/0x03, unknown tag, duplicate 0x00) | No |
| Unit | frameOut drop-oldest under overflow (no mid-frame fragment drop) | No |
| Integration | WebTransport handshake + datagram + stream delivery | No |
| Integration | Client disconnect cleanup (no panic, count=0) | No |
| Integration | New client receives bootstrap-stream IDR before live datagrams | No |
| Integration | Multi-client broadcast fan-out | No |
| Load | 25 concurrent clients receiving 60fps | No (but resource-heavy) |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Broadcast fan-out (25 clients, 1080p frame) | <2ms |
| Connection setup (TLS 1.3 + WebTransport upgrade) | <100ms |
| Max clients | 25 (configurable) |
| Frame drop under load | <5% per slow client |
| Ping RTT (LAN) | <5ms |
| Memory per client | <320KB (8 frame buffers × ~32KB each, `datagram_send_queue_frames`) |
