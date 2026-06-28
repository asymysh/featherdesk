# Module Spec: Server

## Overview

The Server module manages **HTTP/3 + WebTransport** sessions, client lifecycle,
frame broadcasting, and serves the embedded web client. It is the central hub
that connects all other modules to external consumers.

Transport-layer concerns (UDP listener, QUIC handshake, stream multiplexing,
datagram delivery, auth handshake on the control stream) live in
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md). The server module **consumes**
the `transport::Transport` + `transport::Session` traits and adds:

- Session bookkeeping (controller slot, viewer list, max-clients enforcement).
- Per-session tasks (read control stream, read input stream, accept
  clipboard + file-transfer streams, pump frames out).
- Broadcast fan-out (one assembled access unit goes to every session's
  **frame-granular** out-queue; the pump fragments at send time).
- Keyframe cache + **bootstrap-stream** fast-join.
- Direction + role gating for clipboard / file transfer / parameter changes.

---

## Public Interface

```rust
// crate: featherdesk-server

/// Server manages WebTransport sessions and frame distribution.
#[async_trait::async_trait]
pub trait Server: Send + Sync {
    /// Begins listening for connections. Blocks until `cancel` fires.
    async fn start(&self, cancel: CancellationToken) -> Result<(), ServerError>;

    /// Assembles ONE access unit (22-byte FrameHeader || Annex B payload) and
    /// fans it out to every session's frame-granular out-queue. The server
    /// assigns the video Sequence and, when f.keyframe is set BY THE ENCODER
    /// (the server does NOT re-scan the bitstream), caches the assembled access
    /// unit for bootstrap-stream fast-join. codec_type is
    /// frame_type::VIDEO_H264 / VIDEO_HEVC (VP8 was rejected; slot 5 reserved).
    fn broadcast(&self, codec_type: u8, f: EncodedFrame);

    /// Sends one already-encoded audio payload (Opus packet or raw PCM) to all
    /// clients. codec_type is frame_type::AUDIO_OPUS (0x08) or AUDIO_PCM (0x04);
    /// capture_ts is the chunk's capture-time timestamp. The server assigns the
    /// independent audio Sequence, marshals the 22-byte FrameHeader
    /// (width=height=0; codec/rate/channels are in the config message), and
    /// enqueues it frame-granular (single datagram for Opus, fragmented for PCM).
    fn broadcast_audio(&self, codec_type: u8, payload: bytes::Bytes, capture_ts_ns: u64);

    /// Pushes a Config (handshake / capability change) to all clients, or to a
    /// single client on connect.
    fn send_config(&self, cfg: ConfigPayload);

    /// Pushes a CursorUpdate (client-side cursor mode).
    fn send_cursor(&self, c: CursorUpdate);

    /// Returns the current number of connected clients.
    fn client_count(&self) -> u32;

    /// Supplies the current Config to send to each new client on connect
    /// (BEFORE any video frame).
    fn set_config_provider(&self, f: Box<dyn Fn() -> ConfigPayload + Send + Sync>);

    /// Fires when a new client connects. The pipeline uses it to force a
    /// keyframe ONLY when no cached keyframe is available.
    fn set_new_client_callback(&self, f: Box<dyn Fn() + Send + Sync>);

    /// Fires for each BINARY input frame from the controller. The callback
    /// (input::Dispatcher::dispatch) decodes + injects the event and returns its
    /// seq; the server then emits an InputAck(seq) so the client can measure
    /// round-trip latency. Input is binary, not JSON — see MODULE_INPUT.md.
    fn set_input_callback(&self, f: Box<dyn Fn(&[u8]) -> Result<u32, input::InputError> + Send + Sync>);

    /// Fires for a clipboard message (C→H) read off the clipboard stream as
    /// [u32 Len][JSON].
    fn set_clipboard_callback(&self, f: Box<dyn Fn(clipboard::Content) -> Result<(), clipboard::ClipboardError> + Send + Sync>);

    /// Hands the file-transfer service to the server so newly-accepted
    /// bidirectional streams whose first message identifies them as
    /// file-transfer streams get routed into it. `None` disables file transfer.
    fn set_file_transfer_service(&self, svc: Option<filetransfer::Service>);

    /// Fires when a client requests a keyframe (JSON {"type":"keyframe"}). The
    /// server rate-limits before invoking.
    fn set_keyframe_request_callback(&self, f: Box<dyn Fn() + Send + Sync>);

    /// Frames + sends a GAMEPAD_RUMBLE (type 15) to the client that OWNS gamepad
    /// slot `index` (the controller for slot 0; a player client for slots 1…N in
    /// co-op — see Controller Model). Called by the pipeline when the active
    /// gamepad add-on gets a vibration request from the host game
    /// (MODULE_GAMEPAD.md). No-op if [gamepad] allow_rumble = false or no client
    /// owns that slot.
    fn send_gamepad_rumble(&self, index: u8, weak: u16, strong: u16, duration_ms: u32);
}

/// Config holds server configuration.
pub struct Config {
    pub transport: Box<dyn transport::Transport>, // built by MODULE_TRANSPORT; bound to UDP port
    pub client_fs: ClientFs,                      // Embedded web client filesystem (served from /; e.g. rust-embed)
    pub authenticator: Box<dyn auth::Authenticator>, // MODULE_AUTH.md — validates control-stream auth
    pub session_cache: Box<dyn SessionCache>,     // for resume (see "Resume Path")
    pub allow_takeover: bool,                     // controller takeover policy
    pub stream_mgr: Box<dyn stream::Manager>,     // applies client-driven parameter changes
    pub max_clients: u32,                         // hard cap; rejects via close::AUTH_FAILED on overflow
    // Logging is via the `tracing` crate (replaces the old *slog.Logger field).
}
```

**Sequence ownership:** the Server is the SOLE owner of the video and audio sequence counters (independent `AtomicU32` per type). They are assigned inside `broadcast`/`broadcast_audio`. The pipeline never sets sequence numbers.

---

## Internal Architecture

### HTTP/3 Endpoints

All endpoints run on the **same HTTP/3 server** (the QUIC/UDP port). There is
**no separate TCP listener**.

| Path | Method | Handler | Description |
|------|--------|---------|-------------|
| `/` | GET | `rust-embed` service | Serves embedded web client (index.html + JS bundle) |
| `/healthz` | GET | `health()` | `200 {"status":"ok"}` for liveness probes |
| `/wt` | GET (upgrade) | `webtransport_upgrade()` | WebTransport session upgrade. All media + control + input + file transfer multiplexes here. |
| `/auth` | POST | `auth()` | Credential exchange → session token (password/PIN modes) |
| `/pair` | POST | `pair()` | PIN-based pairing (Sunshine-style first-launch flow) — see [`MODULE_AUTH.md`](./MODULE_AUTH.md) |
| `/logout` | POST | `logout()` | Revoke a session token immediately |

> **Why no separate `/files`:** previously file transfer ran on its own
> WebSocket to avoid HOL blocking video. Under QUIC each file transfer is its
> own bidirectional stream within the same WebTransport session — automatically
> isolated from media, same auth, same TLS context. One fewer endpoint, no
> isolation loss. The file-transfer protocol from
> [`MODULE_FILETRANSFER.md`](../interaction/MODULE_FILETRANSFER.md) is unchanged; only its
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

The cert is loaded into the rustls `ServerConfig` that backs the QUIC endpoint
(quinn + wtransport share the one rustls config). One cert, one private key,
one HTTP/3 server.

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
3. transport::Transport surfaces the new Session on its sessions() receiver.
4. Server-side: spawn a per-session task. The session has NO privileges yet.
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
       then close_with_error(close::AUTH_FAILED=4401). Session ends.
     - 5-second timer expired without auth: close_with_error(close::AUTH_TIMEOUT=4408).
9. Check cfg.max_clients → close_with_error(close::AUTH_FAILED) if full (overloaded
   variant: distinguishable via reason string "max_clients").
10. Issue new session_token (32-byte random, base64url) → SessionCache.Put.
11. role check + controller slot:
     - First control connection wins; subsequent control whose auth message
       omits `"takeover":true` become viewers.
     - Explicit takeover (auth message `"takeover":true`) honored only when
       AllowTakeover is true. Displaced controller is closed with
       close::CONTROLLER_TAKEOVER (4410).
12. Send the config message on the control stream as a JSON line (NOT a binary
    frame): {"type":"config","codec":…,"width":…,…,"session_token":…,
    "session_ttl_sec":…,"resumed":true|false}.
13. Seed the joiner's decoder: open a bootstrap UNI stream (tag 0x10) and write
    [u32 Len][FrameHeader‖IDR] of the cached access unit, then close it. If NO
    keyframe is cached yet (very first client), invoke onNewClient → pipeline
    forces a keyframe; the first encoded IDR is then sent on the bootstrap stream.
14. INPUT stream: the stream tagged 0x01 (controller role only). Spawn an
    input-reader task that reads [u16 RecLen]-prefixed records and forwards
    each to input::Dispatcher::dispatch; it writes InputAck back length-prefixed.
15. The same streamAcceptor loop dispatches the remaining tags: 0x02 → clipboard
    handler (a [u32 Len][JSON] reader/writer; direction+role gated), 0x03 → a new
    file-transfer stream handed to filetransfer::Service. An unknown tag, or an
    0x01/0x02 from a view-role client, → cancel_read+cancel_write(close::PROTOCOL_ERROR).
16. Frame-out queue: per-session bounded channel of WHOLE access units
    (cap = datagram_send_queue_frames, default 8; drop-OLDEST on overflow).
    broadcast() pushes one assembled access unit per frame; the datagram_pump
    task pulls one frame, fragments it, and calls session.send_datagram per
    fragment. NEVER a fragment-granular channel (would corrupt frames mid-send —
    see MODULE_TRANSPORT "Datagram Fragmentation", fixes T-1).
17. Control-stream reader loop dispatches JSON lines (controller only for
    role-gated ones):
        resize/set_*           → stream::Manager (drop for viewers)
        {"type":"keyframe"}    → rate-limited keyframe-request callback
        {"type":"pong"}        → record RTT
        {"type":"stats"}       → record client telemetry
        {"type":"chroma_unsupported"} → stream::Manager downgrades chroma to "420";
                                 server re-sends config + forces a keyframe
    (Clipboard is NOT here — it rides the clipboard stream from step 15.)
18. Datagram-in loop: ReadDatagram blocks until the client sends one.
    In v1 there are no C→S datagrams (reserved); any datagram received is
    counted in a metric and dropped.
19. Session close (either side, or context cancel):
    - Cancel all per-session tasks via session.cancelled() (CancellationToken).
    - Release controller slot (if was controller).
    - Drain in-flight file-transfer streams.
    - Update SessionCache entry: keep state cached for reconnect.cache_ttl_seconds.
    - Close the underlying transport session.
```

**Never** restart the capturer on connect (the original `capturer.Restart()` is removed — it disrupted all viewers).

### DoS Protection

- **Control-stream message size limit:** the control reader bounds each
  JSON message at `server.max_message_bytes` (default 4096). Any larger
  message → close the stream with `close::PROTOCOL_ERROR (4400)`. Legitimate
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
  task.
- **Keyframe rate limit:** max 1 forced keyframe per 500 ms (coalesced across
  all clients).
- **Session limit:** `server.max_clients` (default 25). Excess sessions are
  closed at the post-auth check with `close::AUTH_FAILED (4401)` and a clear
  log line.

### Session Cache (Reconnect)

```rust
/// SessionCache stores ephemeral per-session state across short disconnects.
/// Implementation lives in the server crate's internal module; the trait is in
/// featherdesk-server.
pub trait SessionCache: Send + Sync {
    /// Stores session state under the given token with the configured TTL.
    fn put(&self, token: String, st: SessionState);
    /// Fetches state and refreshes TTL on hit; returns None on miss/expiry.
    fn get(&self, token: &str) -> Option<SessionState>;
    /// Revokes a token (e.g., /logout).
    fn delete(&self, token: &str);
}

pub struct SessionState {
    pub user_id: String,                // identifier from auth (empty for token mode)
    pub role: String,                   // "control" | "view"
    pub created_at: std::time::Instant,
    pub expires_at: std::time::Instant, // refreshed on each WebTransport connect
    pub last_params: stream::Params,    // snapshot of resolution/bitrate/HDR at disconnect
}
```

> There is no `LastVideoSeq` field — resume always reseeds the decoder via a
> fresh bootstrap-stream IDR, so a stored last-sequence offers no optimization
> (see MODULE_PROTOCOL "Resume Path"). `LastParams` is kept so a resumed client
> restarts at the same resolution/bitrate/HDR instead of renegotiating.

TTL comes from `[reconnect] cache_ttl_seconds` (default 300s). The cache is in-memory only — restarting the binary invalidates all sessions. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) and [`MODULE_CONFIG.md`](./MODULE_CONFIG.md).

### Session State

```rust
pub struct Session {
    wt: Box<dyn transport::Session>,            // underlying WebTransport session
    control: Box<dyn transport::Stream>,        // bidi control stream (tag 0x00)
    input: Option<Box<dyn transport::Stream>>,  // bidi input stream (tag 0x01; None for viewers)
    clip: Option<Box<dyn transport::Stream>>,   // bidi clipboard stream (tag 0x02; None until opened)
    frame_out: tokio::sync::mpsc::Sender<bytes::Bytes>, // bounded queue of WHOLE access units
                                                // (cap datagram_send_queue_frames=8; drop-OLDEST)
    role: String,                               // "control" | "view"
    identity: auth::Identity,
    // Per-session logging is a `tracing` span (replaces the old *slog.Logger field).
}
```

**Per-session tasks:**
- `datagram_pump()`: pulls one whole access unit from `frame_out`, fragments it
  into ≤~1192-byte datagrams, and calls `wt.send_datagram()` per fragment. Loss
  is whole-frame, never mid-frame. QUIC has its own keepalive (configured via
  `[transport] keepalive_period`).
- `controlReader()`: reads newline-JSON messages on the control stream →
  dispatches keyframe/pong/stats/resize/set_* (NOT clipboard — see below).
- `inputReader()` (controller only): reads `[u16 RecLen]`-prefixed binary
  records on the input stream → `input::Dispatcher::dispatch`; writes `InputAck`
  back length-prefixed on the same stream.
- `clipboardReader()` (started when a 0x02 stream is accepted): reads
  `[u32 Len][JSON]` clipboard messages → `clipboard.Monitor.Set`
  (direction+role gated); writes host→client clipboard the same way.
- `streamAcceptor()`: `wt.AcceptStream` loop; reads each stream's StreamType tag
  and dispatches (0x01 input, 0x02 clipboard, 0x03 → `filetransfer::Service::serve_stream`).

### Broadcasting (frame-granular fan-out; pump fragments at send)

```rust
fn broadcast(&self, codec_type: u8, f: stream::EncodedFrame) {
    // 1. Marshal 22-byte FrameHeader: version=1, kind=codec_type,
    //    sequence=self.video_seq.fetch_add(1, Ordering::Relaxed) (atomic),
    //    timestamp_ns=f.timestamp_ns, width=f.width, height=f.height,
    //    payload_size=f.data.len().
    // 2. Build header || f.data into one contiguous Bytes = the access unit.
    // 3. If f.keyframe (set by the ENCODER — the server does NOT re-scan NALs),
    //    store this assembled access unit under idr_mu as the bootstrap seed.
    // 4. Iterate the sessions list: push the WHOLE access unit into each
    //    session's frame_out channel. If the channel is full, drop the OLDEST
    //    queued frame and enqueue this one (a slow client thus skips stale
    //    frames cleanly), and increment a per-session drop metric.
    //    Fragmentation happens later, in datagram_pump — NOT here. The queue is
    //    frame-granular so a slow client never receives a half-sent frame.
}
```

`datagram_pump` turns one access unit into N datagrams (N ≈ frame_bytes / 1192)
with the 8-byte DatagramHeader; fragment 0 starts with the 22-byte FrameHeader,
later fragments carry only raw payload bytes (see MODULE_PROTOCOL "Datagram
Fragmentation"). NALs are never split across access units.

### Keyframe Caching Strategy

- A keyframe is a complete access unit that contains `SPS, PPS, IDR` (H.264) or
  `VPS, SPS, PPS, IDR` (HEVC). The cache stores **the assembled access unit**
  (`FrameHeader || Annex B`), NOT pre-fragmented datagrams — because it is
  delivered over the reliable **bootstrap stream**, not as datagrams.
- On `broadcast`, if `f.keyframe` (encoder-set), the access unit is stored under `idr_mu`.
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

### Controller Model (+ gamepad co-op)

- **Single keyboard/mouse controller.** One controller slot (`ArcSwapOption<Client>`);
  the first client whose auth message carries `"role":"control"` claims it via CAS.
  It receives all keyboard/mouse/wheel input and gamepad **slot 0**. When it
  disconnects the slot reopens for the next `"role":"control"` client.
- **Player slots (co-op, `[gamepad] allow_coop`).** Additional `"role":"player"`
  clients each claim one **gamepad slot** (1…`max_controllers-1`). The server keeps
  a `player_slots: HashMap<SessionId, u32>` (client → global pad index), reads each player's
  **input stream for gamepad records only** (keyboard/mouse/touch from players are
  dropped), and routes them to `input::Dispatcher` for that slot. Rumble for slot N
  is sent back to the owning client. On disconnect the slot is freed and the virtual
  pad `Disconnect`ed.
- `view` clients send no input. KB/mouse co-op (multiple cursors) is out of scope.

---

## Refactoring Directives

### R-SRV-01: Authentication (now base feature — implemented via MODULE_AUTH)
Authentication is mandatory for all non-`none` modes. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) for modes (token / password / pin / oauth-deferred), the Authenticator interface, session token issuance, and the `/auth` + `/pair` + `/logout` HTTP handlers. The server's only job here is calling `cfg.Authenticator.Authenticate` on the first control-stream message and routing to the appropriate session-cache lookup based on the bearer token contained in that message.

### R-SRV-02: Fix Codec Type Constant (folded into interface)
`broadcast(&self, codec_type: u8, f: EncodedFrame)` now carries the codec type; the server emits `frame_type::VIDEO_H264` (and future codec frame types as added). The codec is also advertised in the Config handshake so the client configures the matching decoder.

### R-SRV-03: Remove Custom itoa()
Replace any hand-rolled integer-to-string helper with standard formatting
(`u32::to_string()` / `format!` / the `itoa` crate for hot paths).

### R-SRV-04: Add Client Metrics Per-Connection
Track per-client: frames sent, frames dropped, bytes sent, connection duration, latency (via ping/pong RTT).

### R-SRV-05: Graceful Client Notification on Shutdown
Before shutting down, send a control frame to all clients indicating "server shutting down" so the client can show appropriate UI.

### R-SRV-06: Origin Validation
Replace any insecure skip-verify default with configurable origin checking. Default to same-host only; allow override via `server.allow_origin` in the TOML config (see MODULE_CONFIG.md).

### R-SRV-07: Server lives in the `featherdesk-host` crate
The `Server` type and `Config` live in `featherdesk-host`'s `server` module; it consumes the `featherdesk-transport` crate. No separate public `server` crate is needed (the v2 native client is a *client* — it never imports the server).

### R-SRV-08: Bandwidth Estimation (now base feature — flows into stream::Manager)
The server derives RTT from the QUIC connection's **smoothed RTT** (quinn exposes it via the connection's path stats), augmented by an app-level `{"type":"ping"}`/`{"type":"pong"}` on the control stream for an end-to-end sample. The fast-path congestion signal is the **server's own datagram-drop rate** (frames dropped from `frame_out` on overflow), NOT the result of `send_datagram` (which is fire-and-forget and never reports loss), plus the client's `{"type":"stats"}` `dropped` delta. It feeds these signals to `stream::Manager` every 100ms (per `[stream.adaptive] interval_ms`); the Manager applies the adaptive policy from `[stream.adaptive]` and hands the effective `stream::Params` to the **pipeline's `param_ch`**. The pipeline's frame loop is the sole path that calls `update_stream_params` on the encoder/capturer (M-6: encode and param-update never run concurrently) — the Manager does **not** mutate the encoder or capturer directly. See [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md).

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
| Unit | frame_out drop-oldest under overflow (no mid-frame fragment drop) | No |
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
