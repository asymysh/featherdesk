# Module Spec: Server

## Overview

The Server module manages **WebTransport** sessions (and, on the fallback
carrier, WebSocket sessions), client lifecycle, frame broadcasting, and serves
the embedded web client. It is the central hub that connects all other modules
to external consumers.

Transport-layer concerns (the TCP and UDP listeners, the QUIC handshake, stream
multiplexing, datagram delivery, carrier selection, auth handshake on the
control stream) live in
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md). The server module **consumes**
the `transport::Transport` + `transport::Session` traits and adds:

- Session bookkeeping (controller slot, viewer list, max-clients enforcement).
- Per-session tasks (read control stream, read input stream, accept
  clipboard + file-transfer streams, pump frames out).
- Broadcast fan-out (one assembled access unit goes to every session's
  **frame-granular** out-queue, with a separate ring for audio; the pump
  fragments at send time).
- Keyframe cache + **bootstrap-stream** fast-join, with the freshness rule that
  makes a joiner's first live frame decodable.
- Direction + role gating for clipboard / file transfer / **parameter changes
  only** — loss-recovery and capability-negotiation messages are ungated and
  rate-limited instead (see "Role gate table").

---

## Public Interface

```rust
// module: featherdesk-host::server  (NOT a separate crate — see R-SRV-07)

/// Server manages sessions on either carrier and frame distribution.
#[async_trait::async_trait]
pub trait Server: Send + Sync {
    /// Begins listening for connections. Blocks until `cancel` fires.
    async fn start(&self, cancel: CancellationToken) -> Result<(), ServerError>;

    /// Assembles ONE access unit (22-byte FrameHeader || Annex B payload) and
    /// fans it out to every session's frame-granular out-queue. The server
    /// assigns the video Sequence and, when f.keyframe is set BY THE ENCODER
    /// (the server does NOT re-scan the bitstream), caches the assembled access
    /// unit together with that sequence for bootstrap-stream fast-join (see
    /// "Keyframe Caching Strategy"). codec_type is
    /// frame_type::VIDEO_H264 / VIDEO_HEVC today; VIDEO_AV1 (16) is reserved
    /// for the future hardware-only AV1 path (VP8/VP9 were rejected; see
    /// BRANCH.md "Codec set").
    fn broadcast(&self, codec_type: u8, f: EncodedFrame);

    /// Sends one already-encoded audio payload (Opus packet or raw PCM) to all
    /// clients. codec_type is frame_type::AUDIO_OPUS (0x08) or AUDIO_PCM (0x04);
    /// capture_ts is the chunk's capture-time timestamp. The server assigns the
    /// independent audio Sequence, marshals the 22-byte FrameHeader
    /// (width=height=0; codec/rate/channels are in the config message), and
    /// pushes it into each session's SEPARATE audio ring, so a video backlog can
    /// never delay audio (single datagram for Opus, fragmented for PCM).
    fn broadcast_audio(&self, codec_type: u8, payload: bytes::Bytes, capture_ts_ns: u64);

    /// Pushes a capability change to every session. The server rebuilds a
    /// per-recipient `ConfigPayload` for each one: `carrier`, `session_token`,
    /// `session_ttl_sec` and `resumed` come from THAT session's state, never from
    /// the caller — which is why the parameter is a `ConfigBase` and not a
    /// `ConfigPayload`. `resumed` is `false` on every broadcast (it describes a
    /// handshake, not the current params); it is `true` only on the config line the
    /// connect path writes after a successful resume.
    fn send_config(&self, cfg: protocol::ConfigBase);

    /// Pushes a cursor position/visibility update to every session as a
    /// `frame_type::CURSOR_UPDATE` (11) control datagram — 8-byte DatagramHeader + the
    /// 14-byte body, never fragmented.
    ///
    /// The server assigns `DatagramHeader.FrameID` from its PER-TYPE CURSOR_UPDATE
    /// counter: one `AtomicU32` per control datagram Type (`Ping`, `CursorUpdate`,
    /// `GamepadRumble`), owned by the Server, global across sessions, starting at 0 and
    /// incremented once per message of that Type at serialization time — including an
    /// idle re-send, so a re-send is "newer" to the client's reorder check and is
    /// applied. It is NOT per session: one broadcast serializes the 8-byte header once
    /// for every recipient (MODULE_SERVER.md:329). The counter is never gap-checked, so
    /// the gaps a shared counter produces are harmless. The caller never supplies a
    /// sequence: `protocol::CursorUpdate` has no field for one, and `CursorPublisher`
    /// holds no `seq`.
    ///
    /// The update replaces the cached join-resync position; lifecycle step 15 seeds a
    /// joining or resuming session from it.
    ///
    /// On the WebSocket fallback carrier the same datagram travels as a `0x20` message
    /// and is coalesced by the send-queue policy (MODULE_TRANSPORT) — position is
    /// latest-wins, so the newest supersedes every unsent one.
    fn send_cursor(&self, u: protocol::CursorUpdate);

    /// Publishes a cursor SHAPE on each session's cursor stream (`stream_type::CURSOR`,
    /// 0x11) as `[u32 Len][Kind 0x01 record]`, reliably.
    ///
    /// The SERVER applies the gating, not the caller:
    ///   - a session is skipped only when its held set already contains this shape AS
    ///     WRITTEN — the whole `HeldShape` key `(ShapeID, DrawW, DrawH, HotspotX,
    ///     HotspotY)`, never the ShapeID alone. A record with a known ShapeID but
    ///     different draw geometry IS written and supersedes the earlier one on the
    ///     client, which is what makes the post-resize re-emission arrive without any
    ///     cache-invalidation call (per-session LRU of 64 entries — MODULE_CAPTURE
    ///     "Shape identity"),
    ///   - a session that has been written 4 MiB of cumulative shape bytes is skipped
    ///     for the rest of its life and bumps
    ///     `featherdesk_cursor_shape_budget_exhausted_total`,
    ///   - the record replaces the cached join-resync shape.
    ///
    /// Non-blocking and infallible from the caller's side: a stalled cursor stream drops
    /// that push for that session and bumps a metric rather than stalling the frame loop.
    fn send_cursor_shape(&self, s: protocol::CursorShapeRecord);

    /// Pushes one server-originated control-stream JSON message. This is the SOLE
    /// producer for every S→C control message other than `auth_ok`/`auth_failed`
    /// (written inline on the auth path) and `config` (which rewrites per-recipient
    /// fields — see `send_config`).
    ///
    /// Non-blocking and infallible from the caller's side: a slow or closed control
    /// stream drops the message for that session and bumps
    /// `featherdesk_control_send_drops_total`, rather than stalling the caller.
    fn send_control(&self, to: Recipients, msg: ControlMessage);

    /// Pushes a HOST clipboard change out to clients as `[u32 Len][JSON]` on
    /// each recipient's clipboard stream (tag 0x02). This is the H→C direction —
    /// the mirror of `set_clipboard_callback`, which handles C→H.
    ///
    /// The pipeline's clipboard task drains `clipboard::ClipboardHandle::changes()`
    /// and calls this; the SERVER applies every gate, in the order given by
    /// CENTRAL_SPEC "Contract 9" (enabled → direction → role → stream-open →
    /// format filter → sanitize → size cap → write). The role gate is set
    /// membership on `Role::Control`, never a test against `view` — viewers and
    /// co-op players never receive a host clipboard push, whatever `direction`
    /// says, and cannot open the clipboard stream at all ("Role gate table"
    /// rows 5-7).
    ///
    /// A session with no clipboard stream open is skipped, not queued. That is an
    /// error path, not the steady state: the controller opens the `0x02` stream
    /// immediately after `auth_ok` whenever `ConfigBase.clipboard != "disabled"`
    /// (MODULE_TRANSPORT "Stream Identification"), so the only sessions without one
    /// are those where clipboard is disabled — for which the direction gate has
    /// already dropped the push.
    ///
    /// Non-blocking and infallible from the caller's side: any gate that rejects
    /// drops that push for that session and bumps
    /// `featherdesk_clipboard_drops_total{direction="h2c",reason=…}` rather than
    /// stalling the monitor task.
    fn send_clipboard(&self, c: clipboard::Content);

    /// Returns the current number of connected clients, across both carriers. The
    /// named reader is the metrics exporter, which calls
    /// `stats.set_client_count(server.client_count())` immediately before each
    /// scrape (MODULE_PIPELINE "Exported metrics catalog") — that is what feeds
    /// `featherdesk_clients`. This counts CONNECTED sessions regardless of auth
    /// state; the authenticated-only count the idle gate needs is delivered by
    /// `set_session_count_callback`, not by this method.
    fn client_count(&self) -> u32;

    /// Supplies the current session-independent config to send to each new client
    /// on connect (BEFORE any video frame). The closure is zero-argument and
    /// therefore cannot carry per-session state: `carrier`, `session_token`,
    /// `session_ttl_sec` and `resumed` are added by the server, per recipient.
    fn set_config_provider(&self, f: Box<dyn Fn() -> protocol::ConfigBase + Send + Sync>);

    /// Fires when a joining or resuming session needs an IDR the cache cannot
    /// supply — the cache is empty, or the cached access unit is STALE (see
    /// "Keyframe Caching Strategy"). The request passes through the same global
    /// coalescer as a client `{"type":"keyframe"}`, tagged `reason = "join"`, so a
    /// burst of joins still costs at most one IDR per
    /// `[server] keyframe_min_interval_ms`. The callback sets a flag the frame loop
    /// reads; it does not touch the encoder (MODULE_PIPELINE "Main Frame Loop",
    /// steps 0c and 3).
    fn set_new_client_callback(&self, f: Box<dyn Fn() + Send + Sync>);

    /// Fires with the current count of AUTHENTICATED sessions every time that
    /// count changes, in both directions: incremented at lifecycle step 12 (auth
    /// complete) and decremented at step 21 (session close), including the
    /// idle-timeout close on either carrier. It is deliberately NOT
    /// `client_count()`, which counts connected sessions whether or not they have
    /// authed: the named consumer is the frame loop's idle gate (MODULE_PIPELINE
    /// "Idle suspension"), and gating capture on *accepted* sessions would make an
    /// unauthenticated connection a capture-start primitive.
    ///
    /// The callback is invoked OUTSIDE any session lock and must not block: the
    /// pipeline's implementation is one atomic store plus, on a 0→N transition,
    /// one `Notify::notify_one()`.
    fn set_session_count_callback(&self, f: Box<dyn Fn(u32) + Send + Sync>);

    /// Fires for each BINARY input frame from the controller (or a co-op player's
    /// gamepad records). The callback (input::Dispatcher::dispatch) decodes +
    /// injects the event and returns its seq; the server hands that seq to the
    /// session's ack-writer task, which emits an InputAck(seq) so the client can
    /// measure round-trip latency. The read loop never writes. Input is binary,
    /// not JSON — see MODULE_INPUT.md.
    fn set_input_callback(&self, f: Box<dyn Fn(&[u8]) -> Result<u32, input::InputError> + Send + Sync>);

    /// Fires on EVERY controller-slot transition — acquire, release, takeover
    /// (lifecycle step 11), session close and session timeout (step 21) — and it
    /// runs BEFORE the new controller's first input record is dispatched. The
    /// pipeline wires it to `input::Dispatcher::release_all` over the same
    /// `Arc<Mutex<Box<dyn Dispatcher>>>` as the input callback, so nothing a
    /// departing controller was holding stays held down on the host (MODULE_INPUT
    /// `release_all`, TD-35).
    fn set_controller_change_callback(&self, f: Box<dyn Fn() + Send + Sync>);

    /// Fires for a clipboard message (C→H) read off the clipboard stream as
    /// [u32 Len][JSON]. The callback holds a `clipboard::ClipboardHandle`, not the
    /// monitor; it never blocks and never awaits.
    fn set_clipboard_callback(&self, f: Box<dyn Fn(clipboard::Content) -> Result<(), clipboard::ClipboardError> + Send + Sync>);

    /// Hands the file-transfer service to the server so newly-accepted
    /// bidirectional streams whose first message identifies them as
    /// file-transfer streams get routed into it. `None` disables file transfer.
    /// An `Arc` because the server serves N transfers concurrently from N stream
    /// tasks while the pipeline keeps the last reference for shutdown ordering.
    fn set_file_transfer_service(&self, svc: Option<std::sync::Arc<dyn filetransfer::Service>>);

    /// Fires when a keyframe is actually to be forced. The server applies all
    /// three rate-limiting stages before invoking, so the callback fires at most
    /// once per `[server] keyframe_min_interval_ms` across every source (client
    /// request, stale join, queue drop) — see "Keyframe-Request Rate Limiting".
    /// The callback sets a flag the frame loop reads; it does not touch the
    /// encoder.
    fn set_keyframe_request_callback(&self, f: Box<dyn Fn() + Send + Sync>);

    /// Fires for a client-driven param change (control-stream `resize` /
    /// `set_bitrate` / `set_fps` / `set_hdr`) AND for the server's own
    /// bandwidth-adaptation telemetry loop (every `[stream.adaptive]
    /// interval_ms` — see "RTT + adaptive bitrate" below).
    ///
    /// The server passes a **field delta**, never a whole `stream::Params`: it has
    /// no authoritative copy of the current parameters to overlay onto, and
    /// building one from a cache is how a client resize used to be reverted by
    /// the next adaptive tick. The pipeline wires this to `stream::Manager::apply`
    /// (MODULE_PIPELINE "Wire callbacks" — the pipeline owns constructing the
    /// Manager), which clamps, stamps an epoch and enqueues for the frame loop.
    ///
    /// The return value is the CLAMPED REQUEST, not the applied result: what the
    /// encoder did is known one frame later and arrives on the watch below.
    fn set_stream_params_callback(&self, f: Box<dyn Fn(stream::ParamDelta) -> Result<stream::Params, StreamError> + Send + Sync>);

    /// Hands the server the pipeline's authoritative applied parameters,
    /// published by the frame loop after every `apply_params`. This is the
    /// server's ONLY source for "what is the stream actually doing", and it
    /// replaces the cache the server was previously told to keep but never given.
    /// It supplies:
    ///   - the `ConfigBase` sent to each joining client (via the config provider),
    ///   - `SessionState.last_params` written at disconnect,
    ///   - the current bitrate the adaptive loop steps from,
    ///   - the `ok: false` edge, which is the ONLY report of a parameter change the
    ///     frame loop could not apply — the server fans it out as
    ///     `ResizeSuppressed` (lifecycle step 19).
    /// `borrow()` is lock-free and never blocks the frame loop.
    fn set_params_watch(&self, rx: tokio::sync::watch::Receiver<stream::Applied>);

    /// Replaces the live Authenticator AND the one `[auth]` policy gate the SERVER
    /// itself evaluates — the whole `[auth]` hot-reload applier in one call
    /// (MODULE_CONFIG "Hot reload behavior"). Both values sit behind one `ArcSwap`, so
    /// the swap is lock-free and every session task picks them up on its next
    /// authentication without coordination. Both are read PER SESSION, never mid-session.
    ///
    /// `allow_takeover` is passed explicitly because the server reads it at lifecycle
    /// step 11, outside the Authenticator. `require_auth_for_view` is NOT a parameter:
    /// it is evaluated INSIDE the Authenticator (MODULE_AUTH "Identity ceilings per
    /// mode" — an unauthenticated client becomes `Identity { max_role: View,
    /// authenticated: false }` rather than an error), so the replacement `a` already
    /// carries the new value. Passing it here would create a second evaluation site for
    /// one gate.
    ///
    /// Sessions already authenticated are NOT re-validated and are NOT closed — exactly
    /// what MODULE_AUTH "Token rotation and device revocation" promises. Only new
    /// control-stream auth messages and new `POST /auth` requests see the replacement.
    ///
    /// The server ALSO clears the `SessionCache` as part of this call, so a resume token
    /// minted under a leaked credential cannot outlive the rotation. That clear happens
    /// INSIDE this method: `SessionCache::clear` is a Server-private method on
    /// `Config.session_cache`, which has no accessor, so there is no `SessionCache::clear()`
    /// call site anywhere outside the Server and no applier may be written to call it.
    /// Live connections survive; reconnecting requires the new credential.
    ///
    /// This is the whole `[auth]` applier, and its one caller is the pipeline's
    /// `fd-config` task (MODULE_PIPELINE `config_applier`, step 12b).
    fn set_auth_policy(&self, a: Box<dyn auth::Authenticator>, allow_takeover: bool);

    /// Replaces the per-session knobs a SIGHUP may change (an `ArcSwap`), sampled
    /// ONCE when the next session is accepted. A reload never mutates a running
    /// session's ring capacity, deadlines or gating, and never disconnects anyone.
    ///
    /// `allow_origin` travels in this struct; it is loaded from the `ArcSwap` on each
    /// upgrade, before a session exists, so the origin check is armed from the first
    /// connection.
    fn set_session_defaults(&self, d: SessionDefaults);

    /// Re-reads the `[server.tls]` certificate and key FILES at the paths fixed at
    /// startup and swaps them into the live listener by calling
    /// `Transport::reload_tls` on the transport this Server owns. The paths
    /// themselves are restart-required, so there is no argument.
    ///
    /// This is the `[server.tls]` file-contents hot-reload applier (MODULE_CONFIG
    /// "Hot reload behavior"), called by the pipeline's `fd-config` task. The Server
    /// is the SOLE owner of the Transport — `Config.transport` is moved in at
    /// MODULE_PIPELINE step 7 and "has exactly one owner from there on" — so it is
    /// the only object in the process that can forward the call. On error the
    /// previous key stays in place and the error is returned for the caller to log;
    /// a bad certificate file never takes a listener down.
    fn reload_tls(&self) -> Result<(), transport::TransportError>;

    /// Frames + sends a GAMEPAD_RUMBLE (type 15) to the client that OWNS gamepad
    /// slot `index` (the controller for slot 0; a player client for slots 1…N in
    /// co-op — see Controller Model). Called by the pipeline when the active
    /// gamepad add-on gets a vibration request from the host game
    /// (MODULE_GAMEPAD.md). No-op if [gamepad] allow_rumble = false or no client
    /// owns that slot.
    fn send_gamepad_rumble(&self, index: u8, weak: u16, strong: u16, duration_ms: u32);
}

/// The one error type `Server::start` returns. Declared HERE and nowhere else;
/// MODULE_PIPELINE re-wraps it transparently as `PipelineError::Server`.
///
/// A per-SESSION failure is NEVER a `ServerError`. Anything that goes wrong with one
/// client is a `close::*` code on that client's session (see "WebTransport Session
/// Lifecycle" and the close-code registry in MODULE_TRANSPORT "Close codes");
/// `start` returns only when the whole server stops.
#[derive(Debug, thiserror::Error)]
pub enum ServerError {
    /// The transport failed to bind or serve, or was closed under us.
    #[error(transparent)] Transport(#[from] transport::TransportError),
    /// Construction-time fault: a route table that could not be built, or `start`
    /// entered twice on one Server. Never a per-session condition.
    #[error("server: {0}")] Config(String),
    /// I/O outside the transport (the embedded client filesystem, the metrics listener).
    #[error("io: {0}")] Io(#[from] std::io::Error),
}

/// Opaque per-session ordinal, assigned from a process-wide monotonic counter at
/// session accept. It is the value the client is told as `auth_ok.session_id` and
/// the value of the `client` metric label — stable for the session's life, never
/// reused within a process, carrying no identity (MODULE_PIPELINE "The `client`
/// label").
pub type SessionId = u64;

/// Which sessions a control message goes to.
pub enum Recipients {
    Every,              // every authenticated session
    Controller,         // the session holding the controller slot, if any
    One(SessionId),     // one session — a reply to that client's own request
}

// `ControlMessage` and `HdrUnavailableReason` are owned by featherdesk-protocol
// (MODULE_PROTOCOL "Control-stream JSON messages") and re-exported here, so a call
// site reads `server::ControlMessage::HdrUnavailable { .. }` while the wire
// definition stays in one crate.
pub use protocol::{ControlMessage, HdrUnavailableReason};

/// The per-session knobs a SIGHUP may change. Held in an `ArcSwap` and sampled
/// ONCE per session, when it is accepted — a running session's ring capacity,
/// deadlines and gating are fixed for its lifetime.
pub struct SessionDefaults {
    pub allow_origin: String,               // [server] allow_origin — `""` = same-origin
                                            // only (the SECURE DEFAULT). Filled at
                                            // MODULE_PIPELINE step 7 from the parsed config
                                            // and replaced wholesale by `set_session_defaults`
                                            // on SIGHUP. Loaded from the `ArcSwap` on each
                                            // WebTransport or WebSocket upgrade — i.e. before
                                            // a session exists — so the check is armed from
                                            // the first connection, not from the first reload.
    pub max_clients: u32,                   // [server]
    pub max_message_bytes: u32,             // [server]
    pub input_rate_limit: u32,              // [server]
    pub keyframe_min_interval_ms: u32,      // [server]
    pub keyframe_request_burst: u32,        // [server]
    pub keyframe_request_refill_ms: u32,    // [server]
    pub shutdown_grace_ms: u32,             // [server]
    pub datagram_send_queue_frames: u32,    // [transport]
    pub audio_send_queue_chunks: u32,       // [transport]
    pub reassembly_max_bytes: u64,          // [transport]
    pub auth_deadline: std::time::Duration, // [transport]
    pub ping_interval: std::time::Duration, // [transport]
    pub join_idr_timeout: std::time::Duration, // [transport]
    pub ws_max_message_bytes: u64,          // [transport]
    pub ws_send_queue_bytes: u64,           // [transport]
    pub clipboard: clipboard::Config,       // [clipboard] direction / formats / max_bytes,
                                            // for the server-side gating in "Role gate table"
                                            // rows 5-7 (the driver gets its own copy via
                                            // ClipboardHandle::set_config)
}

/// Config holds server configuration.
pub struct Config {
    pub transport: Box<dyn transport::Transport>, // built by MODULE_TRANSPORT; bound to the TCP + UDP port
    pub client_fs: ClientFs,                      // Embedded web client filesystem (served from /; e.g. rust-embed)
    pub authenticator: Box<dyn auth::Authenticator>, // MODULE_AUTH.md — validates control-stream auth;
                                                  // the initial value, replaced at runtime by set_auth_policy
    pub session_cache: Box<dyn SessionCache>,     // for resume (see lifecycle step 8 and
                                                  // "Session Cache (Reconnect)")
    pub allow_takeover: bool,                     // [auth] allow_takeover — controller takeover policy;
                                                  // the initial value, refreshed by the [auth]
                                                  // applier as set_auth_policy's second argument
    // NOTE: there is deliberately NO `stream_mgr` field here. Client-driven
    // parameter changes reach the Manager through `set_stream_params_callback`,
    // and the server learns the RESULT through `set_params_watch` — one route
    // out, one route back, neither of them a construction-time dependency.
    // `Manager::apply` takes `&mut self`, which does not fit a field on a
    // `Send + Sync` server shared across every session task without a lock (the
    // pipeline supplies that lock, inside the callback closure); and the pipeline
    // cannot build the Manager until its `param_tx` exists, which is after the
    // server is constructed (MODULE_PIPELINE step 7 vs step 12).
    pub session_defaults: SessionDefaults,        // initial value; replaced by set_session_defaults
                                                  // on SIGHUP. Includes max_clients: the hard cap on
                                                  // live sessions, rejected via close::SERVER_FULL
                                                  // (4429) at lifecycle step 9.
    // Logging is via the `tracing` crate (replaces the old *slog.Logger field).
}
```

**Sequence ownership:** the Server is the SOLE owner of the video and audio sequence counters (independent `AtomicU32` per type). They are assigned inside `broadcast`/`broadcast_audio`. The pipeline never sets sequence numbers. The Server likewise owns one `AtomicU32` per control datagram Type (`Ping`, `CursorUpdate`, `GamepadRumble`) for their `DatagramHeader.FrameID` — see MODULE_PROTOCOL "`FrameID` semantics differ by class".

---

## Internal Architecture

### HTTP Endpoints

Every endpoint is served on **both** listeners the transport binds to
`[server] bind` — TCP/TLS (`h2`, `http/1.1`) and UDP/QUIC (`h3`) — through one
shared `transport::HttpRouter`, so no route can drift between carriers. Only the
two upgrade paths are carrier-specific: `/wt` exists on HTTP/3 only and `/ws` on
`http/1.1` only. See [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md)
"Server-Side Implementation".

`Server::start` builds the route table below into an
`Arc<dyn transport::HttpRouter>`, installs it with `Transport::set_http_router`,
and only then awaits `Transport::start(cancel)`. Both listeners therefore dispatch
into the one router the server owns, and no route can exist on one carrier and be
missing on the other. Entering `Transport::start` with no router installed is
`transport::TransportError::NoRouter`, which reaches the pipeline as
`ServerError::Transport` — a construction bug caught at startup rather than as a
404 storm.

Every path below is relative to **`[server] base_path`** (default `"/"`). A
deployment behind a reverse proxy or tunnel that terminates on a subpath sets it
once and every route moves together — `/desk/wt`, `/desk/ws`, `/desk/auth` and so
on.

**This module does not implement the prefix, and deliberately never sees it.**
The transport strips `base_path` from the request path on both listeners before
routing and before its own `/wt` / `/ws` upgrade match
([`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) "Base path"), so the route table
below is written — and matched — at bare paths under every prefix. That is the
only placement that works: `/wt` and `/ws` are matched by the transport rather
than by this router, so a prefix applied here would move every ordinary route
and leave the two upgrade endpoints behind at the bare path. There is
correspondingly **no `base_path` field on `server::Config`**; a second copy here
would be a second thing to keep in step with the first.

The SPA derives its own prefix from `location.pathname` rather than hardcoding
`/`, so the same embedded bundle serves any prefix. The metrics listener is a
separate bind and is **not** prefixed.

| Path | Method | Handler | Description |
|------|--------|---------|-------------|
| `/` | GET | `rust-embed` service | Serves embedded web client (index.html + JS bundle); injects the current cert-hash `<meta>` in self-signed mode |
| `/cert-hashes` | GET | `cert_hashes()` | `200 {"hashes":[<base64 SHA-256(cert DER)>,…],"spki_sha256":"<base64 SHA-256(SubjectPublicKeyInfo)>"}` — current + previous self-signed cert hashes for the SPA's `serverCertificateHashes`, plus the rotation-stable SPKI fingerprint the client pins. Returns `{"hashes":[]}` with no `spki_sha256` in CA-trusted mode. See "Browser certificate trust". |
| `/healthz` | GET | `health()` | `200 {"status":"ok"}` for liveness probes |
| `/wt` | GET (upgrade) | `webtransport_upgrade()` | WebTransport session upgrade, HTTP/3 only. All media + control + input + file transfer multiplexes here. |
| `/ws` | GET (upgrade) | `websocket_upgrade()` | WebSocket **fallback** carrier, `http/1.1` on the TCP listener, subprotocol `featherdesk.v1`. Same auth, same tags, same framing, TCP head-of-line blocking. See [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) "Carrier selection". `501 Not Implemented` when `[transport] websocket_fallback = false`. |
| `/auth` | POST | `auth::Authenticator::handle_auth()` | Credential → credential token, all modes except `none`. Schema in [`MODULE_AUTH.md`](./MODULE_AUTH.md) "The `/auth` endpoint". |
| `/pair` | GET | `auth::Authenticator::handle_pair()` | Pairing form; issues the CSRF token (cookie `SameSite=Strict; Secure; HttpOnly`) that `POST /pair` requires. `404` outside `mode = "pin"` or outside the pairing window. |
| `/pair` | POST | `auth::Authenticator::handle_pair()` | PIN-based pairing (Sunshine-style first-launch flow); requires the CSRF token from `GET /pair` — see [`MODULE_AUTH.md`](./MODULE_AUTH.md) |
| `/logout` | POST | `logout()` | `{"session_token":"…"}` → revoke that token and close the session holding it with `close::AUTH_FAILED`. `401` with no information about whether the token existed. |

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

There are **two supported trust modes**, selected by whether a cert is
configured. Both are first-class — self-signed is **not** "dev only" (see
"Browser certificate trust" below for why the browser path differs from a normal
HTTPS warning).

1. **CA-trusted** — `server.tls.cert` + `server.tls.key` both set in the TOML
   config → load the PEM chain and key from those paths. The **paths** are
   `(restart required)` (changing them re-decides the trust mode), but the
   **files at those paths are re-read on SIGHUP** by `Server::reload_tls`, which
   forwards to `Transport::reload_tls` on the transport it owns — the same in-place
   swap the self-signed rotation uses below — so an ACME renewal takes effect
   without a restart and without dropping a session. Used when the deployment has a real domain
   (directly, or behind an ACME reverse proxy). The browser trusts it normally;
   the SPA passes **no** `serverCertificateHashes` (see below).
2. **Self-signed (the self-hosted / LAN default)** — both empty → the server
   generates and manages a self-signed ECDSA **P-256** certificate. Because the
   browser reaches WebTransport via `serverCertificateHashes` (below), this cert
   has constraints the CA path does not:
   - Common Name `featherdesk`; SANs `localhost`, `127.0.0.1`, hostname, and any
     `server.tls.extra_sans`.
   - **Validity ≤ 14 days.** The WebTransport `serverCertificateHashes` API
     **rejects any certificate whose total validity exceeds 14 days** (Chromium
     enforces `notAfter − notBefore ≤ 14 days`). FeatherDesk issues a **13-day**
     cert to leave rotation headroom. (The old "valid 1 year" self-signed cert
     would be **refused by the browser** on the WebTransport path — that was the
     bug this section fixes.)
   - **Auto-rotation.** A background task regenerates the **certificate** before
     expiry (default: when < 3 days remain) over the **retained** key pair, via
     `Transport::reload_tls`. The previous certificate is retained until it
     expires so that **already-loaded pages and in-flight sessions keep working
     across a rotation** — the server publishes **both** the current and the
     previous hash (the API accepts a list). On SIGHUP / restart the current cert
     is reused if still > 3 days valid.
   - Cached on disk under the OS-conventional state directory (current +
     previous certificate, and the **one long-lived private key**, which is
     retained across every rotation so the SPKI fingerprint clients pin stays
     stable; deleting the key file breaks every existing pin). **Private key file
     permissions: mode 0600 (Unix) / restrictive ACL (Windows). Verified on
     startup — if permissions are too open, the server refuses to start with a
     clear error.**

The cert is loaded into the rustls `ServerConfig` that backs both listeners
(quinn + wtransport + the TCP acceptor share the one rustls config). One cert,
one private key, one trust decision. Two callers, one mechanism: the rotation task
calls `Transport::reload_tls` directly, and a SIGHUP re-read of a CA-trusted
`cert`/`key` file reaches the same method through `Server::reload_tls`, the applier
the pipeline's `fd-config` task invokes. It swaps the `ServerConfig`'s certified key
in place: no socket rebind, no dropped sessions, and sessions already established
keep the certificate they handshook with.

There is no Let's Encrypt integration in-process — for the CA-trusted mode, front
the server with a reverse proxy (Caddy, nginx, traefik) for ACME if needed.
NOTE: many ACME-issuing proxies do not yet support proxying HTTP/3 + WebTransport
upstream; verify your proxy's QUIC support before deploying.

### Browser certificate trust (`serverCertificateHashes`)

This is mandatory reading for the self-signed mode: **a browser will not open a
`WebTransport` session to a self-signed certificate just because the user clicked
through a TLS warning.** WebTransport performs its **own** certificate check that
ignores the page's click-through trust; for a non-CA cert it connects **only** if
the page passes the cert's hash explicitly:

```js
new WebTransport(url, {
  serverCertificateHashes: [
    { algorithm: "sha-256", value: <ArrayBuffer of SHA-256(cert DER)> },
    // … previous cert's hash too, during a rotation overlap
  ]
})
```

API constraints (all satisfied by mode 2 above): the cert must use an **ECDSA**
key, total **validity ≤ 14 days**, and the hash is **SHA-256 over the DER of the
whole certificate** (not the SPKI).

**Hash delivery (resolves the chicken-and-egg).** The SPA is served by the same
self-signed origin, so:

1. The user navigates to `https://host:port/` and clicks through the **one-time**
   browser interstitial. This navigation is served by the **TCP** listener (an
   HTTP/3-only origin could not be navigated to at all); the interstitial trusts
   the *origin* for normal fetch/HTML, but **not** WebTransport.
2. The host serves the SPA with the **current + previous cert hashes** available
   to it — exposed via `GET /cert-hashes` (JSON, served on the already-trusted
   origin) **and** inlined into `index.html` as a `<meta name="featherdesk-cert-hashes">`
   so the first connection needs no extra round-trip. In CA-trusted mode this
   list is **empty/absent**, signalling the SPA to omit `serverCertificateHashes`
   entirely.
3. The SPA passes the hashes to `new WebTransport(...)` and the session opens
   because the presented cert's DER hash matches one in the list.

If a rotation happens **while a page is open**, the next reconnect re-fetches
`/cert-hashes` (cheap, on the trusted origin) before constructing the transport,
so a stale inlined hash self-heals.

**Browser support caveat:** `serverCertificateHashes` is supported on
Chrome/Edge 107+ and Firefox (recent). Safari's support is incomplete; on Safari
the self-signed mode may fail and a CA-trusted cert (mode 1) is required. This is
noted in the browser-compat matrix in `MODULE_WEB_CLIENT.md` and `PLATFORM_COMPAT.md`.

**Key retention makes the pin possible.** The self-signed key pair is generated **once**
and cached alongside the certificate. Rotation reissues a certificate over the **same**
P-256 key, so `SHA-256(SubjectPublicKeyInfo)` — printed as `spki_sha256` — is stable for
the life of the installation, while `SHA-256(cert DER)` changes every 13 days. Only the
former can be pinned. The key is regenerated only when the operator deletes the cached key
file, which is the documented way to deliberately break every existing pin.

`GET /cert-hashes` therefore returns:

```json
{"hashes": ["<base64 SHA-256(current cert DER)>", "<base64 SHA-256(previous cert DER)>"],
 "spki_sha256": "<base64 SHA-256(SubjectPublicKeyInfo)>"}
```

`hashes` feeds `serverCertificateHashes` (which requires the whole-certificate DER hash and
cannot accept an SPKI hash). `spki_sha256` is the pin. In CA-trusted mode both are absent
or empty and the SPA omits `serverCertificateHashes` and performs no pinning — the CA is
the trust anchor there.

**Startup banner.** Every start in self-signed mode prints the fingerprint so an operator
can compare it out of band (over SSH, in person, from a provisioning log):

```
FeatherDesk self-signed TLS
  SPKI  SHA-256: 3a:7f:…:c1   (stable — this is the value to compare)
  Cert  SHA-256: 9d:04:…:2e   (rotates every 13 days)
  Verify this against the fingerprint shown in the client before you trust a new host.
```

**Pin-on-first-use, client side.** On a successful connection the client stores
`{origin → spki_sha256, first_seen}` in `localStorage` under the key
`featherdesk.pin.<origin>`. On every subsequent load it compares the freshly fetched
`spki_sha256` to the stored one. A mismatch is a **hard stop**: no WebTransport session is
opened, and the client shows both fingerprints, the date the pin was recorded, and the two
ways forward — verify the new fingerprint out of band and clear the pin, or stop. There is
no click-through that silently replaces a pin. The current fingerprint is also shown in
the HUD (R-CLI-13) so a user can compare it at any time.

**Residual risk, stated plainly.** Pin-on-first-use does not authenticate the **first**
contact. An attacker who controls the network path at the moment a client first loads the
page serves their own SPA, their own `/cert-hashes` and their own certificate, and the
client pins the attacker. Nothing in the self-signed mode detects this, because there is no
out-of-band trust anchor to detect it with — the startup banner exists precisely so an
operator can supply one manually. What pinning does buy is that the attack must be present
at first contact and must then be sustained forever: any later interposition, on any
network, is detected and refused. Deployments that cannot accept a trust-on-first-use
window must use CA-trusted mode (`server.tls.cert` + `key`), where the trust anchor is the
public PKI and no pinning is performed.

### WebTransport Session Lifecycle

The same lifecycle runs on both carriers; the step numbers below are
carrier-independent.

```
1. Client opens WebTransport: new WebTransport("https://host:port/wt"), or the
   WebSocket fallback at /ws when UDP is unusable (MODULE_TRANSPORT "Carrier
   selection"). No credentials, role, or takeover flag in the URL or HTTP headers
   (browsers can't set them) — they all travel in the control-stream auth message
   (step 7).
2. Transport handler upgrades the carrier (TLS 1.3, Origin checked per R-SRV-06).
3. transport::Transport surfaces the new Session on its sessions() receiver
   (capacity 16; when that channel is full the incoming session is closed
   immediately with close::SERVER_FULL (4429), reason "server_busy", rather than
   queued — a capacity condition, never an auth verdict).
4. Server-side: spawn a per-session task and assign the session its SessionId (the
   opaque ordinal used as the `client` metric label). The session has NO
   privileges yet.
5. Server starts the [transport] auth_deadline timer (5 s) ON SESSION ACCEPT
   (covers a client that never opens any stream — it is closed at +5s).
6. streamAcceptor reads the StreamType tag (first byte) of each accepted bidi
   stream. The CONTROL stream is the one tagged 0x00 (StreamControl); a second
   0x00 is a protocol error.
7. Server reads the first JSON line on the control stream (bounded at
   [server] max_message_bytes; a longer line → close::PROTOCOL_ERROR):
       {"type":"auth","token":"<bearer>","role":"control|view|player",
        "takeover":false,"resume":false,
        "decode":{"h264":true,"h264_422":false,"h264_444":false,
                  "hevc":false,"hevc10":false}}
   Absent "role" parses as Role::Control; a present-but-unrecognised role, a
   missing "type", or a non-string "token" is malformed → close::PROTOCOL_ERROR.
   "decode" is optional (absent ⇒ DecodeCaps::CONSERVATIVE) and is NOT a security
   input — it is recorded as SessionState.decode and gates only what this client
   is sent (see "Decode capability" gates at step 14 and step 19).
8. Establish the IDENTITY. Exactly one of two paths runs; both produce
   (identity_ceiling: Role, authenticated: bool, resumed: bool, carried: Option<SessionState>).
     - Resume path (msg.resume == true): key = hex(SHA-256(msg.token));
       st = SessionCache.take(key) — TAKE, not get: the entry is REMOVED atomically, so
       two connections presenting the same token cannot both proceed; the loser sees
       None and falls through to fresh auth (where, holding no mode credential, it is
       closed 4401). Reject and fall through to fresh auth when: st is None; OR
       now >= st.absolute_expiry; OR now >= st.idle_expiry; OR ([reconnect]
       require_same_auth AND st.device_id is set AND that device is no longer in
       paired_devices_file). On accept: identity_ceiling = st.max_role,
       authenticated = st.authenticated, resumed = true, carried = Some(st).
       msg.role is IGNORED — the client never names its own role on resume.
     - Fresh auth (msg.resume == false, or resume rejected above): the server builds
       auth::AuthRequest + auth::PeerInfo and calls cfg.authenticator.authenticate.
       Err → write {"type":"auth_failed","reason":…} then
       close_with_error(close::AUTH_FAILED=4401). Ok(id) →
       identity_ceiling = id.max_role, authenticated = id.authenticated,
       resumed = false, carried = None.
     - auth_deadline expired at any point above:
       close_with_error(close::AUTH_TIMEOUT=4408).
9. Admission control (BOTH paths). If live session count >= max_clients:
   close_with_error(close::SERVER_FULL=4429, "max_clients") and, on the resume path,
   put the taken SessionState back under the SAME key with its expiries unchanged.
   Resume is NOT an exemption — reconnection must not be an unbounded-connection
   primitive.
10. Effective role (BOTH paths), computed by the SERVER only:
        requested = if resumed { carried.role } else { msg.role }
        effective = min(requested, identity_ceiling)
        if effective == Role::Player && ![gamepad] allow_coop { effective = Role::View }
    A client-supplied role is a request, never a grant.
11. Controller slot + takeover (BOTH paths). Runs the SAME compare-and-swap on the one
    controller slot (ArcSwapOption<Client>) regardless of how the session authenticated:
      - effective != Control → no slot interaction.
      - effective == Control and the slot is empty → CAS it; on CAS failure (another
        session won the race) re-read and continue below.
      - effective == Control, the slot is held, and NOT (msg.takeover AND
        cfg.allow_takeover AND authenticated) → effective = Role::View. This is the
        line the resume path used to skip; it is why two live sessions could both
        hold Control.
      - effective == Control, the slot is held, and takeover is permitted → CAS the
        slot to this session; the displaced session's cached role is rewritten to
        Role::View with SessionCache::update_if_present(key, Role::View, None) (so its
        own auto-reconnect resumes as a viewer; a no-op if that key has already been
        consumed), its held input is released, and it is closed with
        close::CONTROLLER_TAKEOVER (4410).
    Every slot transition — acquire, release, takeover — invokes the
    controller-change callback, which the pipeline wires to
    input::Dispatcher::release_all, BEFORE the new controller's first record is
    dispatched, so nothing stays held down on the host (MODULE_INPUT
    "Dispatcher::release_all"). The role the server assigns — which may be a
    downgrade — is reported in auth_ok at step 13, and the client gates its
    input-stream open on that value.
12. Issue the session token (BOTH paths). Generate a fresh 32-byte CSPRNG token
    (MODULE_AUTH "Session token properties") — resume ROTATES, it never reuses, so a
    captured token is single-use. Write SessionCache.put(hex(SHA-256(new_token)),
    SessionState{
        created_at:      carried.map(|s| s.created_at).unwrap_or(now),  // inherited on resume
        absolute_expiry: created_at + [auth] session_ttl_minutes,        // NEVER extended
        idle_expiry:     now + [reconnect] cache_ttl_seconds,
        role:            effective,       // the GRANTED role, not the requested one
        max_role:        identity_ceiling,
        authenticated, user_id, device_id, decode,
        last_params:     carried.map(|s| s.last_params).unwrap_or(applied.borrow().params),
    }). Because this happens AFTER step 11, the cache can never hold a role the server
    did not grant.
12b. The session is now AUTHENTICATED. Increment the authenticated-session count
    and invoke `set_session_count_callback` with the new value. On the 0→1
    transition this is what wakes the frame loop out of its idle park
    (MODULE_PIPELINE "Idle suspension"), and it happens here — after auth, before
    the joiner is seeded at step 14 — so the first frame the new session waits
    for is one the loop has actually been woken to produce. The wake is
    sub-frame, and step 14's existing stale-IDR path already forces a fresh
    keyframe, so no extra first-frame machinery is needed.
13. Write {"type":"auth_ok",…} on the control stream — the EFFECTIVE role, the
    gamepad slot, this session's SessionId, takeover_allowed, resumed, and (only on
    a downgrade) requested_role + downgrade_reason. The payload is
    protocol::AuthOkPayload; see MODULE_PROTOCOL "Shared Payload Types". Then send
    the config message as a JSON line (NOT a binary frame): the server takes the
    current ConfigBase from the config provider and adds this session's own
    carrier, session_token (minted at step 12), session_ttl_sec
    (= min(absolute_expiry, idle_expiry) - now, in whole seconds) and resumed —
    the provider never sees them:
    {"type":"config","codec":…,"width":…,…,"carrier":"webtransport",
     "session_token":…,"session_ttl_sec":…,"resumed":true|false}.
    If SessionState.decode says this client cannot decode the ACTIVE codec, the
    server follows the config line with
    {"type":"codec_unavailable","codec":"<active codec>","reason":"client_cannot_decode"}
    and skips step 14: the session stays open (input, clipboard and file transfer
    still work) and the client shows a persistent overlay rather than a black canvas.
14. Seed the joiner's decoder. Read the IDR cache; if it is STALE (some frame has
    been broadcast since it), raise a rate-limited keyframe request and wait up to
    [transport] join_idr_timeout for a fresh one. Then open a bootstrap UNI stream
    (tag 0x10), write [u32 Len][FrameHeader‖IDR] of the (now fresh) access unit,
    close it, and record bootstrap_seq for this session. The session's needs_idr
    flag clears only when the bootstrap IDR was fresh; otherwise the pump withholds
    deltas until a keyframe arrives. See "Keyframe Caching Strategy".
15. CURSOR stream: open a UNI stream (tag 0x11) and seed it from the cached cursor
    state, so a joiner sees the pointer even if it never moves again — next_cursor
    reports "changed since the last call" per add-on, not per session, so without
    this a client joining an idle host would get nothing. The cache is guaranteed
    non-empty once the frame loop has ticked: the first call to
    capture::CursorCapturer::next_cursor after the capturer is constructed MUST
    return Ok(Some(..)) with the current position, visibility and bitmap. This step
    is still total for the window BEFORE that first tick.
    The stream is opened for EVERY session regardless of the resolved cursorMode and
    stays open for the life of the session. When cursorMode is "separate": write
    [u32 Len][Kind 0x01 shape record] for the cached active shape if one is cached,
    then [u32 Len][Kind 0x02 position record] carrying the cached CursorUpdate; if NO
    CursorUpdate has been cached yet, write a Kind 0x02 record with ShapeID = 0,
    Visible = 0, X = 0, Y = 0 and StreamW/StreamH set to the current stream
    dimensions — the client keeps its overlay hidden until the first real update
    rather than reading an absent or truncated record. Record the shape's HeldShape
    in this session's held set. When cursorMode is "embedded" no records are written;
    the client ignores the cursor stream in that mode. Live shape and position
    records are written to the open stream as they occur, so a mid-session flip to
    "separate" is genuinely just more records on the stream that is already there.
    A RESUMED session is seeded identically: the cached shape and position are
    re-sent, because the client's overlay state did not survive the reconnect.
16. INPUT stream: the stream tagged 0x01 (controller, or a co-op `player` when
    [gamepad] allow_coop). Split it with tokio::io::split; spawn an input-reader
    task on the read half that reads [u16 RecLen]-prefixed records and forwards each
    to input::Dispatcher::dispatch, and an ack-writer task on the write half that
    drains a bounded 64-deep drop-oldest ack_out channel and writes InputAck back
    length-prefixed. The reader never awaits a write. For a `player`, only gamepad
    records (0x40-0x4F) are forwarded — keyboard/mouse/touch are dropped (see
    Controller Model).
17. The same streamAcceptor loop dispatches the remaining tags: 0x02 → clipboard
    handler (a [u32 Len][JSON] reader/writer, CONTROLLER ONLY), 0x03 → a new
    file-transfer stream handed to filetransfer::Service. Every tag is gated by the
    Role gate table below (rows 1, 5, 8); an unknown tag, a second 0x00, or a tag
    whose row rejects this session → cancel_read+cancel_write(close::PROTOCOL_ERROR)
    at STREAM scope. The session and its other streams are unaffected — a stale
    client degrades to what it is allowed instead of reconnect-looping.
18. Frame-out queues: two per-session bounded rings of WHOLE access units
    (video: cap datagram_send_queue_frames, default 8; audio: cap
    audio_send_queue_chunks, default 25; both drop-OLDEST). broadcast() pushes one
    assembled access unit per frame; the datagram_pump task pulls one, fragments it,
    and calls session.send_datagram per fragment. NEVER a fragment-granular channel
    (would corrupt frames mid-send — see MODULE_TRANSPORT "Datagram Fragmentation",
    fixes T-1).
19. Control-stream reader loop dispatches JSON lines. Exactly four messages are
    controller-only; every other message is accepted from every authenticated role:
        resize / set_bitrate / set_fps / set_hdr
                               → stream::Manager (silently dropped for view/player).
                                 If the returned effective Params differ from the
                                 request because of hysteresis, reply
                                 send_control(One(session), ResizeSuppressed{w,h}).
                                 A set_hdr the attached client set cannot decode is
                                 refused with HdrUnavailable{AttachedClientCannotDecode}.
        {"type":"keyframe"}    → per-session token bucket → global coalescer →
                                 keyframe-request callback (see "Keyframe-Request Rate
                                 Limiting"). Allowed for EVERY role: a viewer that
                                 cannot request an IDR cannot recover from loss.
        {"type":"pong"}        → match `nonce` against this session's outstanding-ping
                                 ring → record RTT. An unmatched or missing nonce is
                                 ignored, never a protocol error.
        {"type":"stats"}       → record client telemetry (decodeMs, dropped, fps,
                                 audioGaps) → adaptive slow path.
        {"type":"decode_unsupported"} → run the downgrade ladder for this session's
                                 report (chroma → "420", then codec_unavailable to
                                 that client — see MODULE_PROTOCOL "Decode
                                 capability");
                                 server re-sends config + forces a keyframe.
    A change the FRAME LOOP could not apply does NOT arrive on this stream and has no
    reply channel to answer — stream::ParamUpdate deliberately carries none. The frame
    loop republishes the UNCHANGED params on the Applied watch with ok: false, and the
    server, which already holds that watch through set_params_watch, turns that edge
    into send_control(Recipients::Every, ResizeSuppressed{w: applied.params.width,
    h: applied.params.height}) — the dims actually in force — so no client is left
    sizing a canvas to a resolution the stream never reached. That is the only
    difference from the hysteresis case above, which stays synchronous and replies to
    the one requesting session.
    An unknown "type", or a line over max_message_bytes, closes the session with
    close::PROTOCOL_ERROR (4400) — the control stream is the one stream whose
    framing the session depends on.
    (Clipboard is NOT here — it rides the clipboard stream from step 17.)
20. Datagram-in loop: `session.read_datagram()` blocks until the client sends one.
    In v1 there are no C→S datagrams (reserved); any datagram received is
    counted in a metric and dropped.
21. Session close (either side, or context cancel):
    - Cancel all per-session tasks via session.cancelled() (CancellationToken).
    - If this session held the controller slot, CAS it back to empty and invoke the
      controller-change callback (input::Dispatcher::release_all) so nothing stays
      held down on the host.
    - If the session had AUTHED, decrement the authenticated-session count and
      invoke `set_session_count_callback` with the new value. A session that
      never completed auth (closed by `auth_deadline`, or refused at step 9)
      never incremented it and must not decrement it. This is the path that
      returns the frame loop to idle when the last viewer leaves, and it runs
      for every close reason — including the WebSocket idle timeout
      (MODULE_TRANSPORT "Liveness on both carriers").
    - Drain in-flight file-transfer streams.
    - Update the SessionCache entry under this session's key with
      SessionCache::update_if_present(key, current_effective_role,
      Some(now + [reconnect] cache_ttl_seconds)): the session's CURRENT effective role
      (which may be View after a takeover demotion) and a refreshed idle expiry,
      leaving created_at and absolute_expiry untouched. If it returns false the key
      has ALREADY been consumed by another connection's resume (step 8) — the
      session's state is then DISCARDED and the key is never re-created, because a
      consumed session token must never come back to life (MODULE_AUTH "One live
      holder").
    - Drop this session's per-client metric series (see MODULE_PIPELINE "Exported
      metrics catalog", the `client` label).
    - Close the underlying transport session.
```

**Never** restart the capturer on connect (the original `capturer.Restart()` is removed — it disrupted all viewers).

### DoS Protection

- **Control-stream message size limit:** the control reader bounds each
  JSON message at `server.max_message_bytes` (default 4096). Any larger
  message → close the stream with `close::PROTOCOL_ERROR (4400)`. Legitimate
  JSON control messages stay well under 4 KB.
- **Clipboard-stream length cap:** the clipboard reader rejects a `[u32 Len]`
  prefix above `6 × [clipboard] max_bytes + 1024` **before allocating the payload
  buffer** — the `u32` can name 4 GiB, and the headroom covers worst-case JSON
  escaping plus the envelope. Over cap → `cancel_read` +
  `cancel_write(close::PROTOCOL_ERROR (4400))` on that stream only; the session
  survives. The content itself is then capped at `max_bytes` after parsing. See
  MODULE_CLIPBOARD "Size Limits".
- **Datagram size limit:** QUIC enforces the negotiated per-datagram limit
  natively; the receiver discards any datagram whose 8-byte DatagramHeader
  is malformed or whose FragIndex (bits 0-14, LAST = bit 15) is out of range.
  There is no FragCount field.
- **Reassembly bounds:** a frame whose declared `PayloadSize` exceeds 16 MiB is
  rejected before allocation; total in-progress reassembly bytes per session are
  bounded by `[transport] reassembly_max_bytes` (4 MiB) with oldest-first
  eviction. See MODULE_PROTOCOL "Datagram reassembly rules".
- **Accepted-session channel:** `Transport::sessions` is bounded at 16. When it
  is full the incoming session is closed immediately with
  `close::SERVER_FULL (4429)`, reason `"server_busy"`, rather than queued — the
  accept path never grows an unbounded backlog of unauthenticated sessions. This is
  the same shape as the `max_clients` overload at step 9 and carries the same code
  deliberately: overflow is a capacity condition, and a `4401` here would make the
  client discard its cached token and burn its single `/auth` retry over a transient
  16-deep queue.
- **Admission-time egress guard (`[server] max_egress_bps`, 0 = off):** checked
  at lifecycle step 9, in the same place and on the same path as `max_clients`.
  A session is refused when
  `(authenticated_sessions + 1) × current_stream_bitrate_bps > max_egress_bps`,
  with `close::SERVER_FULL (4429)` and reason `"max_egress"` — a distinct reason
  string from `"max_clients"` so the two capacity refusals are separable in a
  log, but deliberately the **same close code**, because both are "no capacity,
  retry later" and neither is a credential failure.
  `current_stream_bitrate_bps` is read from `applied.borrow().params.bitrate`,
  the bitrate actually in force, not the configured ceiling: a host that has
  adapted down to 4 Mbps can admit more viewers than one running at 25 Mbps, and
  that is the intended behaviour.
  This is a **count gate, not a shaper.** It makes silent oversubscription of the
  host uplink an explicit refusal; it does not make an already-admitted session
  use less bandwidth. For that, see `[transport] per_session_max_bps`
  (MODULE_TRANSPORT "Per-session pacing cap"). The guard is re-evaluated only at
  admission, so an adaptive bitrate *rise* can carry the room above the ceiling
  — accepted deliberately, because retroactively evicting a live session to
  satisfy a config key is worse than exceeding it.
- **Input rate limit:** per-client token bucket at `server.input_rate_limit`
  events/sec (default 1000). `mousemove` events are coalesced (only the latest
  position is kept). Excess events are silently dropped at the input-reader
  task.
- **Keyframe rate limit:** three stages — a client-side 500 ms throttle, a
  per-session token bucket (`[server] keyframe_request_burst = 3`, refill
  `keyframe_request_refill_ms = 2000`), and a global coalescer of one forced
  keyframe per `[server] keyframe_min_interval_ms = 500` across all clients and all
  reasons. See "Keyframe-Request Rate Limiting".
- **Session limit:** `[server] max_clients` (default 25) is owned and enforced **here**,
  in the server, and nowhere else — it is checked at lifecycle step 9, after
  authentication, on **both** the fresh-auth and the resume path, so reconnection is not
  an unbounded-connection primitive. An excess session is closed with
  `close::SERVER_FULL (4429)` and the reason string `"max_clients"` — deliberately **not**
  `AUTH_FAILED`, which a client cannot distinguish from a bad credential and which drives
  the reconnect storm this code exists to prevent. The check is after auth on purpose: an
  unauthenticated flood must not be able to evict authenticated users from the count.

### Session Cache (Reconnect)

**Two token kinds, two stores, and they never mix.**

1. A **credential token** is minted by `POST /auth` (and, as a permanent device
   token, by `POST /pair`). It lives in the Authenticator's own credential store,
   keyed `hex(SHA-256(token))`, expiring at `[auth] session_ttl_minutes`. It is what
   `authenticate()` validates. It is presented exactly once, on the control stream,
   with `"resume": false`. It is NEVER written to `server::SessionCache`.
2. A **session token** is minted by the SERVER at lifecycle step 12, after
   arbitration. It lives in `server::SessionCache` (`featherdesk-host::server`), is
   delivered only in `config.session_token`, and is the ONLY token accepted with
   `"resume": true`. It is single-use: resume consumes the entry and mints a
   replacement.

A client therefore presents its `POST /auth` token with `resume:false` and its most
recent `config.session_token` with `resume:true`, and never the other way round.
`POST /logout` revokes a session token (kind 2).

```rust
/// SessionCache stores ephemeral per-session state across short disconnects.
/// Implementation + trait live in the host's server module
/// (featherdesk-host::server), not a separate crate (R-SRV-07).
///
/// Keys are hex(SHA-256(session_token)) — the raw token is never stored, so neither a
/// heap dump nor a log of the key set yields a usable credential.
pub trait SessionCache: Send + Sync {
    /// Stores session state under the given key. Overwrites any existing entry.
    fn put(&self, key: String, st: SessionState);
    /// ATOMICALLY removes and returns the entry, or None on miss. This is the ONLY
    /// read path used by resume: consuming the entry is what makes a session token
    /// single-use and stops two connections from resuming the same session
    /// concurrently. There is deliberately no non-consuming `get` — a peek would
    /// reintroduce the duplicate-resume race.
    fn take(&self, key: &str) -> Option<SessionState>;
    /// Revokes a key without reading it (e.g. /logout, device revocation).
    fn delete(&self, key: &str);
    /// Revokes every entry whose device_id matches (PIN-mode device revocation).
    fn delete_by_device(&self, device_id: &str) -> usize;
    /// Updates an entry that is STILL PRESENT, and NEVER creates one. Writes `role`
    /// and, when `idle_expiry` is `Some`, the idle expiry; `created_at`,
    /// `absolute_expiry`, `max_role` and every other field are left untouched.
    /// Returns `false` on a miss.
    ///
    /// A miss means another connection already `take`-consumed this key at lifecycle
    /// step 8. A consumed key is DEAD: it is never re-created, and the calling
    /// session's state is discarded instead. That is what makes a session token
    /// single-use (MODULE_AUTH "One live holder"); a `put` here would resurrect a
    /// credential the resume path had already retired.
    ///
    /// Exactly two callers: lifecycle step 11 (takeover demotion — `Role::View`,
    /// `idle_expiry = None`) and lifecycle step 21 (session close — the session's
    /// CURRENT effective role, `idle_expiry = Some(now + [reconnect]
    /// cache_ttl_seconds)`). `put` is used ONLY at step 12, to create a brand-new key.
    fn update_if_present(&self, key: &str, role: protocol::Role,
      idle_expiry: Option<std::time::Instant>) -> bool;
    /// Drops every entry. Server-private: it is reached only from inside
    /// `set_auth_policy` on an `[auth]` reload, so a resume token minted under a
    /// rotated-out credential cannot outlive it. `Config.session_cache` has no
    /// accessor, so there is no `SessionCache::clear()` call site outside the Server.
    fn clear(&self);
}

pub struct SessionState {
    pub user_id: String,                     // identifier from auth (empty for token mode)
    pub device_id: String,                   // populated in PIN mode
    pub role: protocol::Role,                // the GRANTED role (written after arbitration)
    pub max_role: protocol::Role,            // identity ceiling, re-applied on every resume
    pub authenticated: bool,                 // false only on the anonymous-viewer path
    pub decode: protocol::DecodeCaps,        // what this client can decode (from the auth
                                             // message; absent ⇒ CONSERVATIVE). Never an
                                             // authorization input.
    pub created_at: std::time::Instant,      // set at FULL auth; inherited across resumes
    pub absolute_expiry: std::time::Instant, // created_at + [auth] session_ttl_minutes; never extended
    pub idle_expiry: std::time::Instant,     // disconnect + [reconnect] cache_ttl_seconds
    pub last_params: stream::Params,         // resolution/bitrate/HDR at disconnect, written at
                                             // session close from the `Applied` watch's current value
}
```

> There is no `LastVideoSeq` field — resume always reseeds the decoder via a
> fresh bootstrap-stream IDR, so a stored last-sequence offers no optimization
> (see MODULE_PROTOCOL "Resume Path"). `last_params` is kept so a resumed client
> restarts at the same resolution/bitrate/HDR instead of renegotiating.

Two independent bounds, both checked on every resume and neither able to extend the
other. `[auth] session_ttl_minutes` (default 60) fixes `absolute_expiry` at the moment of
full authentication and is **never** refreshed — resume inherits `created_at`, so a token
that keeps reconnecting still dies exactly `session_ttl_minutes` after the credential was
last actually presented. `[reconnect] cache_ttl_seconds` (default 300) is the idle bound
on a *disconnected* session's cached state. The cache is in-memory only — restarting the
binary invalidates all sessions. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) and
[`MODULE_CONFIG.md`](./MODULE_CONFIG.md).

### Session State

```rust
/// FrameOut is a per-session out-queue: a bounded ring of WHOLE access units
/// with producer-side DROP-OLDEST.
///
/// It is NOT a `tokio::sync::mpsc`. That type's `Sender` has no operation that
/// removes an element — the only way to free a slot is `Receiver::recv`, and the
/// receiver lives in the pump task — so from the producer side it can express
/// only drop-NEWEST: under load the client would receive a queue of stale frames
/// and discard the freshest one, which is the latency accumulation this design
/// exists to prevent. `ArrayQueue::force_push` evicts the head, which is the
/// stated policy, and is lock-free, so the frame-loop thread never blocks behind
/// 25 session pumps.
pub struct FrameOut {
    q: crossbeam_queue::ArrayQueue<bytes::Bytes>,
    wake: tokio::sync::Notify,
    dropped: std::sync::atomic::AtomicU64,
    closed: std::sync::atomic::AtomicBool,
}

impl FrameOut {
    /// Capacity is fixed at session accept from `[transport]
    /// datagram_send_queue_frames` (video) / `audio_send_queue_chunks` (audio).
    pub fn with_capacity(n: usize) -> Self { /* … */ }

    /// Called from the frame-loop thread (video) or the audio thread (audio).
    /// Never blocks, never awaits, never allocates. On a full ring the OLDEST
    /// queued access unit is evicted and dropped, so a congested client skips
    /// stale frames cleanly and always receives the freshest one.
    pub fn push(&self, au: bytes::Bytes) {
        if self.closed.load(std::sync::atomic::Ordering::Acquire) { return; }
        if self.q.force_push(au).is_some() {
            self.dropped.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        }
        self.wake.notify_one();
    }

    /// Called from the session's `datagram_pump` task. Returns `None` once the
    /// session is closed AND the ring is drained.
    pub async fn pop(&self) -> Option<bytes::Bytes> {
        loop {
            if let Some(au) = self.q.pop() { return Some(au); }
            if self.closed.load(std::sync::atomic::Ordering::Acquire) { return None; }
            // `Notify` holds one permit, so a `notify_one` between the pop above
            // and this await is not lost — no missed wakeup, no timeout needed.
            self.wake.notified().await;
        }
    }

    /// Session teardown: the pump drains what is left, then exits.
    pub fn close(&self) {
        self.closed.store(true, std::sync::atomic::Ordering::Release);
        self.wake.notify_one();
    }

    /// Feeds `featherdesk_datagram_send_drops_total{kind=…}`.
    pub fn dropped(&self) -> u64 { self.dropped.load(std::sync::atomic::Ordering::Relaxed) }
}

/// The per-session InputAck ring. Same shape as `FrameOut` — a fixed-capacity
/// lock-free ring plus a `Notify` — but over `(Seq, recv_ts_ns)` pairs rather
/// than encoded bytes — the input `Seq` (`u32`) and the receive timestamp in
/// nanoseconds — because an ack is two integers the ack-writer formats
/// into a 13-byte record, not a pre-serialized buffer. Capacity is fixed at 64.
///
/// **Drop-OLDEST, and the eviction is counted here by `inputReader`,** which is
/// the only pusher: `push` runs on the input-reader task right after the
/// dispatcher returns a `Seq`, and `ackWriter` is the only popper. InputAck is
/// best-effort latency telemetry — losing the oldest ack under burst costs one
/// RTT sample, while blocking the input reader would delay injection itself.
pub struct AckRing {
    q: crossbeam_queue::ArrayQueue<(u32, u64)>,
    wake: tokio::sync::Notify,
    dropped: std::sync::atomic::AtomicU64,
}

impl AckRing {
    /// Capacity is fixed at 64 at session accept; not configurable.
    pub fn with_capacity(n: usize) -> Self { /* … */ }

    /// Called from `inputReader` only. Never blocks. On a full ring the OLDEST
    /// pair is evicted and `featherdesk_input_acks_dropped_total` increments.
    pub fn push(&self, ack: (u32, u64)) {
        if self.q.force_push(ack).is_some() {
            self.dropped.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        }
        self.wake.notify_one();
    }

    /// Called from `ackWriter` only.
    pub async fn pop(&self) -> (u32, u64) { /* … */ }

    pub fn dropped(&self) -> u64 { self.dropped.load(std::sync::atomic::Ordering::Relaxed) }
}

/// One cursor shape AS WRITTEN on a session's cursor stream. The dedupe key for
/// `send_cursor_shape` is this whole value, not `shape_id` alone: two records with
/// the same ShapeID but different draw geometry are different entries, which is what
/// lets `CursorPublisher::on_stream_dims_changed` re-emit an active shape after a
/// resolution change and have it actually reach an already-connected session.
#[derive(Copy, Clone, PartialEq, Eq)]
pub struct HeldShape {
    pub shape_id: u32,
    pub draw_w: u16,
    pub draw_h: u16,
    pub hotspot_x: u16,
    pub hotspot_y: u16,
}

/// Per-session record of the cursor shapes already written on that session's cursor
/// stream, plus that session's shape-byte budget. Bounded at 64 entries,
/// least-recently-written evicted first; an evicted shape may be re-sent later.
pub struct ShapeLru {
    held: std::collections::VecDeque<HeldShape>, // most-recently-written at the back;
                                                 //   capacity 64, the front is evicted
    bytes_written: u64,                          // cumulative Kind 0x01 record bytes written
                                                 //   to this session; at >= 4 MiB no further
                                                 //   shape is written for its lifetime
}

pub struct Session {
    id: SessionId,                              // opaque ordinal; the `client` metric label
    wt: Box<dyn transport::Session>,            // underlying WebTransport (or fallback) session
    control: Box<dyn transport::Stream>,        // bidi control stream (tag 0x00)
    input: Option<Box<dyn transport::Stream>>,  // bidi input stream (tag 0x01; controller or
                                                // co-op player; None for viewers. Split: read
                                                // half in `inputReader`, write half in `ackWriter`)
    clip: Option<Box<dyn transport::Stream>>,   // bidi clipboard stream (tag 0x02; None until opened)
    cursor: Box<dyn transport::UniStream>,      // uni cursor stream (tag 0x11), opened at
                                                //   lifecycle step 15 for EVERY session
                                                //   regardless of cursorMode, and kept open for
                                                //   the life of the session. `open_uni_stream`
                                                //   returns `Box<dyn UniStream>`; `Stream` is the
                                                //   bidi trait and there is no coercion between
                                                //   them — `UniStream` has no read half.
    frame_out: std::sync::Arc<FrameOut>,        // WHOLE video access units
                                                // (cap [transport] datagram_send_queue_frames = 8; drop-OLDEST)
    audio_out: std::sync::Arc<FrameOut>,        // audio payloads, a SEPARATE ring so a video
                                                // backlog can never delay audio and vice versa
                                                // (cap [transport] audio_send_queue_chunks = 25;
                                                //  drop-OLDEST)
    ack_out: std::sync::Arc<AckRing>,           // (Seq, recv_ts_ns) pairs, cap 64, drop-OLDEST.
                                                // Same ArrayQueue + Notify shape as FrameOut,
                                                // over a different element type: InputAck is a
                                                // latency sample, so the newest seq is the useful
                                                // one and the oldest is what gets evicted.
    needs_idr: std::sync::atomic::AtomicBool,   // pump withholds deltas while set
    bootstrap_seq: u32,                         // sequence of the IDR this session was seeded with
    held_shapes: ShapeLru,                      // 64-entry LRU of the `HeldShape` records written
                                                // on the cursor stream — geometry included, so a
                                                // known ShapeID at new draw dimensions is written
                                                // again — plus this session's 4 MiB shape budget
                                                // (MODULE_CAPTURE "Shape identity")
    role: protocol::Role,
    identity: auth::Identity,
    // Per-session logging is a `tracing` span (replaces the old *slog.Logger field).
}
```

**Per-session tasks:**
- `datagram_pump()`: selects over `audio_out` and `frame_out`, sending an audio
  payload ahead of a video frame when both are ready (audio is the master clock;
  a 20 ms Opus packet is one datagram and costs nothing to prioritize). It pulls
  one whole access unit, fragments it into datagrams of at most
  `wt.max_datagram_size()` bytes (re-read per access unit), and calls
  `wt.send_datagram()` per fragment. Loss is whole-frame, never mid-frame. While
  `needs_idr` is set it drops and counts every access unit with
  `keyframe == false`. On the WebSocket carrier the same pump applies the
  coalescing and drop policies in MODULE_TRANSPORT "Send-side queue policy on the
  fallback carrier" and emits one `0x20` message per access unit. QUIC has its own
  keepalive (configured via `[transport] keepalive_period`).
- `controlReader()`: reads newline-JSON messages on the control stream →
  dispatches keyframe/pong/stats/decode_unsupported/resize/set_* per lifecycle
  step 19 and the Role gate table (NOT clipboard — see below).
- `inputReader()` (controller, or co-op `player` for gamepad records only): splits the
  input stream with `tokio::io::split` and owns the **read** half. It reads
  `[u16 RecLen]`-prefixed binary records → `input::Dispatcher::dispatch`, and pushes
  each returned `Seq` — paired with the `recv_ts_ns` it sampled when the record came
  off the wire, so the ring element is `(u32, u64)` and `ackWriter` needs no clock of
  its own — into the bounded `ack_out` ring (cap 64, **drop-oldest**). It
  never writes to the stream itself, so a client that stops draining its ack side can
  never wedge its own input path.
- `ackWriter()`: owns the **write** half of the input stream, drains `ack_out`, and
  writes each `InputAck` length-prefixed. **InputAck is best-effort:** it is a latency
  sample, not a delivery guarantee, and a dropped one costs the client one missing
  measurement and nothing else. The drop is counted by the PUSHER, not here:
  `AckRing::push` runs on the input-reader task and is the only place that can
  observe a `force_push` eviction, so it is what increments
  `featherdesk_input_acks_dropped_total`; `ackWriter` never sees the evicted pair.
- `clipboardReader()` (started when a `0x02` stream is accepted from the controller):
  reads `[u32 Len][JSON]` clipboard messages, rejecting a `Len` prefix above
  `6 × [clipboard] max_bytes + 1024` **before allocating** as a stream-scope
  protocol error, then
  applying the C→H gate list from CENTRAL_SPEC Contract 9 (direction, format filter,
  sanitize, content cap) before invoking `set_clipboard_callback`; writes host→client
  the same way.
- `stream_acceptor()`: `session.accept_stream()` loop; reads each stream's StreamType tag
  and dispatches (0x01 input, 0x02 clipboard, 0x03 → `filetransfer::Service::serve_stream`),
  applying rows 1, 5 and 8 of the Role gate table at the accept point.

### Keepalive, liveness & timeouts

Three independent timers, each owning a distinct concern — do not conflate them:

| Timer | Layer | Default | What it does |
|-------|-------|---------|--------------|
| `auth_deadline` | app | 5 s | Closes a session that hasn't authed the control stream (`close::AUTH_TIMEOUT 4408`). Armed on session accept. |
| `keepalive_period` / `max_idle_timeout` | transport (**both carriers**) | 15 s / 30 s | **The real liveness mechanism**, and it must exist on both carriers — these two keys are carrier-generic (MODULE_TRANSPORT "Liveness on both carriers"). **WebTransport:** QUIC sends keepalive PINGs and closes after `max_idle_timeout` of no received packets; because the server streams datagrams that elicit ACKs, a vanished client is dropped automatically, with no app logic. **WebSocket:** QUIC's guarantees are absent, so the session task sends a WebSocket **Ping control frame** at `keepalive_period` and closes the session when no Pong and no inbound message has arrived for `max_idle_timeout`. TCP's own keepalives are measured in hours and a half-open connection through a tunnel outlives them, so without that app-side timer a vanished WebSocket client holds a `max_clients` slot — and the **controller slot** — until the OS gives up. An idle-timeout close runs the full step-21 path, including releasing the controller slot and firing `set_controller_change_callback` → `release_all`. |
| `ping_interval` | app | 2 s | Server sends a `Ping` **datagram** (type 2, 4-byte `u32` LE nonce); client replies `{"type":"pong","nonce":…}` on the control stream. This is purely an **RTT sample** for the adaptive loop (MODULE_STREAM_PARAMS) — it is **NOT** a liveness check (lost ping/pong datagrams are normal and ignored). `ping_interval = 0` disables it and the adaptive loop uses QUIC `smoothed_rtt` alone. |

- **No idle-input disconnect in v1.** A viewer (or an idle controller) that sends
  no input is legitimate and is **never** dropped for inactivity — liveness is
  QUIC's job above. Session *lifetime* is bounded only by the session-token TTL
  (MODULE_AUTH), which is checked on reconnect/resume, not enforced as a live
  kick.
- **Graceful shutdown** sends `{"type":"server_shutdown"}` to every session, waits
  `[server] shutdown_grace_ms` (250) for the lines to flush, then closes all
  sessions with `close::SERVER_SHUTDOWN 4503`. See R-SRV-05.

### Broadcasting (frame-granular fan-out; pump fragments at send)

```rust
fn broadcast(&self, codec_type: u8, f: stream::EncodedFrame) {
    // 1. Marshal 22-byte FrameHeader: version=1, kind=codec_type,
    //    sequence=self.video_seq.fetch_add(1, Ordering::Relaxed) (atomic),
    //    timestamp_ns=f.timestamp_ns, width=f.width, height=f.height,
    //    payload_size=f.data.len().
    // 2. Build header || f.data into one contiguous Bytes = the access unit.
    //    Set last_broadcast_seq to the sequence just assigned.
    // 3. If f.keyframe (set by the ENCODER — the server does NOT re-scan NALs),
    //    store this assembled access unit AND its sequence under idr_mu as the
    //    bootstrap seed (see "Keyframe Caching Strategy").
    // 4. Iterate the sessions list: push the WHOLE access unit into each
    //    session's `frame_out` ring with `FrameOut::push`. On a full ring the
    //    OLDEST queued frame is evicted and this one enqueued (a slow client thus
    //    skips stale frames cleanly), the per-session drop counter is bumped
    //    — `featherdesk_datagram_send_drops_total{kind="video"}`, the fast-path
    //    congestion signal — that session's needs_idr flag is set, and a keyframe
    //    request with reason = "queue_drop" is raised for it. `push` is lock-free
    //    and never blocks the frame-loop thread, whatever any client is doing.
    //    Fragmentation happens later, in datagram_pump — NOT here. The ring is
    //    frame-granular so a slow client never receives a half-sent frame.
}
```

`broadcast_audio` pushes into `audio_out` with the same call and `kind="audio"`.

`datagram_pump` turns one access unit into N datagrams
(N = ⌈(22 + frame_bytes) / (max_datagram_size() − 8)⌉) with the 8-byte
DatagramHeader; fragment 0 starts with the 22-byte FrameHeader, later fragments
carry only raw payload bytes (see MODULE_PROTOCOL "Datagram Fragmentation").
NALs are never split across access units.

### Keyframe Caching Strategy

- A keyframe is a complete access unit containing `SPS, PPS, IDR` (H.264) or
  `VPS, SPS, PPS, IDR` (HEVC). The cache stores **the assembled access unit**
  (`FrameHeader || Annex B`) together with its `sequence`, NOT pre-fragmented
  datagrams — it is delivered over the reliable **bootstrap stream**.
- On `broadcast`, `last_broadcast_seq` is set to the sequence just assigned; if
  `f.keyframe` (encoder-set), the access unit and that sequence are stored under
  `idr_mu`.
- **The cached IDR is FRESH for a joiner iff `idr.sequence == last_broadcast_seq`**
  — nothing has been broadcast since it, so it is still the head of the reference
  chain and the joiner's next live frame will be `idr.sequence + 1`. Any other state
  is STALE: the frames between the cached IDR and now went to other clients and this
  one will never see them.
- **On join or resume:**
  1. FRESH, or the cache is empty — go to 3 (empty means no frame has ever been
     broadcast, so nothing can be stale).
  2. STALE — request a keyframe through the **same** rate-limited path clients use
     ("Keyframe-Request Rate Limiting", `reason = "join"`), and wait up to
     `[transport] join_idr_timeout` (default `1s`) for the cache to be refreshed
     with a keyframe newer than `last_broadcast_seq` was at entry. The joining
     session's pump does not run while waiting.
  3. Write the cached access unit to the bootstrap stream and record its `sequence`
     as this session's `bootstrap_seq`.
  4. On timeout at step 2, write the stale IDR anyway and leave the session's
     `needs_idr` flag set (below) so the pump withholds deltas; re-issue the
     keyframe request every `join_idr_timeout` until one arrives. The client sees a
     held frame, never garbage.
- On new client connect/resume the server sends, in order: **auth_ok + config
  (JSON lines on the control stream) → the fresh IDR (on the bootstrap stream) →
  the cursor seed (on the cursor stream) → live datagrams**.
- **The forced keyframe is not a storm.** Every request — join, client, and
  queue-drop — funnels through the one 500 ms coalescer, so a burst of joins costs
  at most 2 IDR/s in total. That limiter is exactly what TD-26 was about; the
  over-correction was refusing to force at all.
- **Per-session needs-IDR gate.** Each `Session` carries `needs_idr: AtomicBool`,
  set `true` at accept. The `datagram_pump` **drops and counts** every access unit
  with `keyframe == false` while it is set, and clears it on the first access unit
  with `keyframe == true` that it forwards — or at step 3 above when the bootstrap
  IDR was FRESH. It is set again whenever `frame_out` drop-oldest discards a frame
  for this session (that session's reference chain is now broken; sending it further
  deltas wastes bandwidth), and that drop also raises a keyframe request with
  `reason = "queue_drop"`. This makes the invariant hold by construction, and is the
  server-side mirror of the client gate in [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md)
  "Video Decode".

### Keyframe-Request Rate Limiting

Keyframe requests arrive from three sources: a client's `{"type":"keyframe"}` (gap,
reassembly deadline, decoder error, return from background), a join or resume whose
cached IDR is stale ("Keyframe Caching Strategy"), and a `frame_out` drop-oldest
that broke a session's reference chain. All three enter the same three-stage
limiter, tagged `reason = client / join / queue_drop`.

1. **Client-side throttle (advisory).** The browser sends at most one
   `{"type":"keyframe"}` per 500 ms (MODULE_WEB_CLIENT "Video Decode"). A courtesy,
   not a defence — the server never relies on it.
2. **Per-session token bucket (defence).** Each session holds a bucket of
   `[server] keyframe_request_burst` tokens (default 3), refilled one token every
   `[server] keyframe_request_refill_ms` (default 2000). A `{"type":"keyframe"}` with
   no token is dropped and counted in `featherdesk_keyframe_requests_dropped_total`;
   it is **not** a protocol error and never closes the session. This bounds one
   hostile or broken client to 0.5 requests/s sustained. `join` and `queue_drop`
   requests are server-generated and bypass the bucket.
3. **Global coalescer.** At most one forced keyframe per
   `[server] keyframe_min_interval_ms` (default 500) across all sessions and all
   reasons. A request arriving inside an open window sets a pending flag and the
   keyframe is forced **at the end of the window** — coalesced, never dropped. This
   is what makes "N clients detect the same loss" cost exactly one IDR, and what
   makes forcing an IDR on a stale join (see "Keyframe Caching Strategy") safe: a
   burst of joins costs at most 2 IDR/s in total.

`featherdesk_keyframes_forced_total` counts stage-3 outputs;
`featherdesk_keyframe_requests_total{reason}` counts stage-2 inputs. A healthy
session shows the two diverging under multi-client load and converging with one
client — which is what "storm detection" means operationally.

### Controller Model (+ gamepad co-op)

- **Single keyboard/mouse controller.** One controller slot (`ArcSwapOption<Client>`);
  the first client whose auth message carries `"role":"control"` claims it via CAS.
  It receives all keyboard/mouse/wheel input and gamepad **slot 0**. When it
  disconnects the slot reopens for the next `"role":"control"` client. Every
  transition of the slot — acquire, release, takeover — fires the
  controller-change callback, so `input::Dispatcher::release_all` runs before the
  next controller's first record is injected and no key, button or touch is left
  held down on the host.
- **Player slots (co-op, `[gamepad] allow_coop`).** Additional `"role":"player"`
  clients each claim one **gamepad slot** (1…`max_controllers-1`). The server keeps
  a `player_slots: HashMap<SessionId, u32>` (client → global pad index), reads each player's
  **input stream for gamepad records only** (keyboard/mouse/touch from players are
  dropped), and routes them to `input::Dispatcher` for that slot. Rumble for slot N
  is sent back to the owning client. On disconnect the slot is freed and the virtual
  pad `Disconnect`ed.
- `view` clients send no input. KB/mouse co-op (multiple cursors) is out of scope.

### Role gate table (normative, allowlist)

Every privileged operation is listed. A gate is written as membership in an explicit
set — never as "not `view`" — so a role added later inherits nothing by default. The
role tested is always the **effective** role the server arbitrated at step 10/11; a
client-supplied role is never consulted after that point. Where a row names a config
key, both the role test and the key test must pass.

| # | Operation | Carrier | Allowed roles (allowlist) | Additional condition | Enforcement point | On reject |
|---|-----------|---------|---------------------------|----------------------|-------------------|-----------|
| 1 | Open input stream | bidi tag `0x01` | `{Control, Player}` | `[input] enabled` | `stream_acceptor` | `cancel_read`+`cancel_write(close::PROTOCOL_ERROR)`, **stream scope only** |
| 2 | Keyboard / mouse / wheel / touch records (`0x00`–`0x3F`) | input stream | `{Control}` | — | `inputReader` | drop the record, `featherdesk_role_rejects_total{op="input"}`++ |
| 3 | Gamepad records for slot 0 (`0x40`–`0x4F`) | input stream | `{Control}` | `[gamepad] enabled` | `inputReader` | drop the record, metric++ |
| 4 | Gamepad records for slots 1…`max_controllers-1` | input stream | `{Control, Player}` | `[gamepad] enabled` **and** the session OWNS that slot: a `Player` owns exactly the one slot arbitration assigned it (which requires `allow_coop`); a `Control` owns every slot below `max_controllers` that no player currently owns — the single-client multi-pad path, which does not require `allow_coop` | `inputReader` | drop the record, metric++ |
| 5 | Open clipboard stream | bidi tag `0x02` | `{Control}` | `[clipboard] enabled` **and** `direction != disabled` | `stream_acceptor` | `cancel_read`+`cancel_write(close::PROTOCOL_ERROR)`, stream scope |
| 6 | Clipboard message C→H | clipboard stream | `{Control}` | `direction ∈ {bidirectional, client_to_host}` | `clipboardReader` | drop the message silently, `featherdesk_clipboard_drops_total{direction="c2h",reason="direction"}`++ |
| 7 | Clipboard delivery H→C | clipboard stream | recipient `{Control}` | `direction ∈ {bidirectional, host_to_client}` **and** that session has an open `0x02` stream | `send_clipboard`, per recipient | skip that recipient, `featherdesk_clipboard_drops_total{direction="h2c",reason=…}`++ |
| 8 | Open file-transfer stream | bidi tag `0x03` | `{Control}` | `[filetransfer] enabled` (i.e. `set_file_transfer_service(Some(_))`) | `stream_acceptor` | `cancel_read`+`cancel_write(close::PROTOCOL_ERROR)`, stream scope |
| 9 | `INIT` (upload), `LIST_REQUEST`, `GET_REQUEST` (download) | file stream | `{Control}` | — | already gated at row 8; `serve_stream` re-asserts | `ERROR {code:"forbidden"}` then reset the stream |
| 10 | `{"type":"resize"}` | control stream | `{Control}` | — | `controlReader` | silent drop, `featherdesk_role_rejects_total{op="resize"}`++, `tracing::debug` |
| 11 | `{"type":"set_bitrate"}` / `{"type":"set_fps"}` / `{"type":"set_hdr"}` | control stream | `{Control}` | — | `controlReader` | silent drop, `featherdesk_role_rejects_total{op="set_params"}`++ |
| 12 | `{"type":"keyframe"}` | control stream | `{View, Player, Control}` | per-session token bucket, `keyframe_request_burst` / `keyframe_request_refill_ms` | `controlReader` | drop beyond the bucket; the global `keyframe_min_interval_ms` coalescing still applies |
| 13 | `{"type":"pong"}` | control stream | `{View, Player, Control}` | — | `controlReader` | — |
| 14 | `{"type":"stats"}` | control stream | `{View, Player, Control}` | — | `controlReader` | — |
| 15 | `{"type":"decode_unsupported"}` | control stream | `{View, Player, Control}` | each rung recorded **per session**, so one client is never offered the same rung twice; the rung ITSELF is unconditional and fires on the first report from ANY role — rung 1 downgrades chroma to `"420"` session-wide, re-sends `config` and forces a keyframe (the ladder is MODULE_PROTOCOL "Decode capability"; this row does not restate it) | `controlReader` | — |
| 16 | `"takeover": true` in the auth message | control stream | requested role must be `Control` | `[auth] allow_takeover` **and** `identity.authenticated` | step 11 | ignored — the client is arbitrated to `View` as usual, and told so in `auth_ok` |
| 17 | `GAMEPAD_RUMBLE` delivery | datagram / reliable fallback | the session that owns that slot index | `[gamepad] allow_rumble` | `send_gamepad_rumble` | no-op |
| 18 | Prometheus scrape | separate `[metrics]` port | — (unauthenticated by design) | bind must be loopback or the server warns at startup | metrics listener | — |
| 19 | `POST /logout` | HTTPS | — | body must carry the exact session token being revoked | `logout()` | `401`, no information about whether the token existed |
| 20 | `POST /auth` | HTTPS | — (unauthenticated by design: it is the credential entry point) | body ≤ 4096 bytes; the per-identity and process-global limiters of MODULE_AUTH "Rate limiting" apply; `503` when `[auth] mode = "none"` | `Authenticator::handle_auth` | `401 {"error":"bad_credentials"}`, identical body and timing for every failure |
| 21 | `GET` / `POST /pair` | HTTPS | — (unauthenticated by design) | `mode = "pin"` **and** the pairing window is open, else `404`; `POST` additionally requires the CSRF token issued on `GET` and consumes one `[auth] max_pin_attempts` slot | `Authenticator::handle_pair` | `404` outside the window; `403` on a missing/ wrong CSRF token; `401` on a wrong PIN |

**Stream-scope rule.** Rows 1, 5 and 8 reset the offending **stream** and never the
session. A stale client that opens a stream its arbitrated role does not permit must
degrade to what it is allowed, not reconnect-loop.

**No implicit inheritance.** `Role` is an ordered enum, but ordering is used in exactly
one place — the ceiling clamp at step 10. Gates are set membership. `Role::Player` is
NOT "a controller minus keyboard"; rows 2, 5, 8, 10 and 11 exclude it explicitly. What
the ordering does guarantee is that no row ever admits a LOWER role to an operation it
refuses a higher one: where row 4 or row 17 turns on which gamepad slot a session owns,
that is instance state, not a privilege `Player` holds and `Control` does not.

---

## Refactoring Directives

### R-SRV-01: Authentication (now base feature — implemented via MODULE_AUTH)
Authentication is mandatory for all non-`none` modes. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) for modes (token / password / pin / oauth-deferred), the Authenticator interface, session token issuance, and the `/auth` + `/pair` + `/logout` HTTP handlers. The server parses and size-checks the first control-stream message, hands the parsed `auth::AuthRequest` plus an `auth::PeerInfo` to `cfg.authenticator.authenticate`, and is itself authoritative for the effective role, the session token and the controller slot (steps 8-12).

### R-SRV-02: Fix Codec Type Constant (folded into interface)
`broadcast(&self, codec_type: u8, f: EncodedFrame)` now carries the codec type; the server emits `frame_type::VIDEO_H264` (and future codec frame types as added). The codec is also advertised in the Config handshake so the client configures the matching decoder.

### R-SRV-03: Remove Custom itoa()
Replace any hand-rolled integer-to-string helper with standard formatting
(`u32::to_string()` / `format!` / the `itoa` crate for hot paths).

### R-SRV-04: Add Client Metrics Per-Connection
Track per-client: frames sent, frames dropped, bytes sent, connection duration, and RTT. These are **per-session** counters owned by the server (not `StatsSnapshot`, which is process-aggregate); they are exported with the opaque `client` ordinal label and removed at session close — see MODULE_PIPELINE "The `client` label". Lifecycle step 21 drops the series.

### R-SRV-05: Graceful Client Notification on Shutdown
On graceful shutdown the server notifies clients before tearing the transport down,
so the SPA can show "server shut down" instead of a bare disconnect and can suppress
its reconnect timer. The sequence is ordered and bounded:

```
1. server.send_control(Recipients::Every, ControlMessage::ServerShutdown)
2. wait [server] shutdown_grace_ms (default 250) so the control lines flush
3. for each session: close_with_error(close::SERVER_SHUTDOWN, "server shutdown")
4. cancel the transport CancellationToken; drop the Transport (RAII closes both
   listeners)
```

The grace is a flush window, not a drain: an unresponsive client is closed at step 3
regardless. Step 1 is what makes step 3's code (4503) actionable — a QUIC close code
alone does not reach application JavaScript on every browser.

### R-SRV-06: Origin Validation
Replace any insecure skip-verify default with configurable origin checking. Default to same-host only; allow override via `server.allow_origin` in the TOML config (see MODULE_CONFIG.md).

### R-SRV-07: Server lives in the `featherdesk-host` crate
The `Server` type and `Config` live in `featherdesk-host`'s `server` module; it consumes the `featherdesk-transport` crate. No separate public `server` crate is needed (the v2 native client is a *client* — it never imports the server).

### R-SRV-08: Bandwidth Estimation (now base feature — flows into stream::Manager)
The server derives RTT and the congestion window from `Session::path_stats()` — `smoothed_rtt` and `congestion_window`, sampled per session at call time — augmented by the app-level `Ping` **datagram** (frame type 2, 4-byte `u32` LE nonce) whose `{"type":"pong","nonce":…}` reply arrives on the control stream. There is no `{"type":"ping"}` control message. On the WebSocket carrier the same Ping travels as a `0x20`-tagged message with the identical layout, and the RTT it measures includes TCP retransmission — which is exactly the signal the adaptive loop should see on that carrier.

The fast-path congestion signal is the **server's own datagram-drop rate** (frames dropped from `frame_out` on overflow), NOT the result of `send_datagram` (which is fire-and-forget and never reports loss), plus the client's `{"type":"stats"}` `dropped` delta.

The server supplies these signals **per session**, not pre-aggregated: it feeds `stream::Manager` a `stream::ParamDelta::Telemetry` every `[stream.adaptive] interval_ms` (100 ms) and reads `Applied.params` for "current". The Manager reduces N sessions to one encoder setting with the reference-client rule in MODULE_STREAM_PARAMS "Aggregating N clients into one encoder setting" — the controller is the reference, a median override covers a network-wide problem, and one slow viewer served by its own drop-oldest ring never moves the encoder. The Manager hands the result to the **pipeline's parameter funnel**; the pipeline's frame loop is the sole path that calls `update_stream_params` on the encoder/capturer (M-6: encode and param-update never run concurrently) — the Manager does **not** mutate the encoder or capturer directly. A session in constant-QP mode has no bitrate to step from until the first congestion signal seeds one; see MODULE_STREAM_PARAMS "Cold start". This is congestion **reaction**, not bandwidth estimation — no active probe exists and none should be inferred. See [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md).

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
| Integration | Egress guard: with `max_egress_bps` set to just under `3 × current bitrate`, the third session is refused with `close::SERVER_FULL (4429)` reason `"max_egress"` while the first two stream normally; with the key at `0` all three are admitted | No |
| Unit | The egress guard reads the bitrate actually in force (`applied.borrow().params.bitrate`), not `[stream.adaptive] max_bitrate_bps`: after the adaptive loop halves the bitrate, a session previously refused is admitted | No |
| Integration | Idle suspension: with zero authenticated sessions `featherdesk_capture_idle` is 1 and no frames are broadcast; a session completing auth (step 12b) wakes the loop and the first frame arrives within `idle_keyframe_ms + join_idr_timeout`. An accepted-but-unauthed connection does **not** wake it | No |
| Unit | The session-count callback fires on auth (step 12b) and on close (step 21), and never for a session that closed before authing — an `auth_deadline` timeout leaves the count unchanged | No |
| Integration | `base_path = "/desk/"` moves **every** route on **both** listeners together: `/desk/`, `/desk/cert-hashes`, `/desk/healthz`, `/desk/wt`, `/desk/ws`, `/desk/auth`, `/desk/pair`, `/desk/logout` all answer, and each bare-path equivalent returns 404. Proves no route keeps the old prefix | No |
| Integration | With `base_path = "/desk/"` the SPA served at `/desk/` opens its carrier against `/desk/wt` (or `/desk/ws`) and fetches `/desk/cert-hashes`, all derived from `location.pathname` — no hardcoded `/` anywhere in the bundle | No |
| Unit | The route table this module installs matches **bare** paths regardless of `base_path`: with `base_path = "/desk/"` the `HttpRouter` receives `/auth`, not `/desk/auth`. Proves the strip happens in the transport and is not duplicated here | No |
| Unit | `base_path` validation: a value not beginning and ending with `/`, or containing `..`, a query or a scheme, is a startup error | No |
| Integration | New client receives bootstrap-stream IDR before live datagrams | No |
| Integration | Multi-client broadcast fan-out | No |
| Integration | Stale-IDR join: broadcast an IDR, broadcast 60 delta frames, then connect a second client — the bootstrap stream carries a **newly forced** IDR whose sequence equals `last_broadcast_seq`, and the client's first live frame is `bootstrapSeq + 1` | No |
| Integration | Idle-screen join: with the capturer producing nothing, a join on a stale cache still receives a fresh IDR within `idle_keyframe_ms + join_idr_timeout` | No |
| Integration | IDR storm: 25 clients each send `{"type":"keyframe"}` within 50 ms — exactly one keyframe is forced, and a 26th request 600 ms later forces a second | No |
| Integration | A client joining a host whose pointer has not moved since before it connected receives a shape record and a Kind 0x02 position record on the cursor stream, and renders a cursor without any pointer movement; a resumed session is seeded identically | No |
| Unit | Per-session keyframe token bucket: a client sending 10 requests in 1 s consumes 3 and has 7 counted in `featherdesk_keyframe_requests_dropped_total`, with no session close | No |
| Unit | `send_config(ConfigBase)` fans out to three sessions and each receives its OWN `session_token`, `session_ttl_sec`, `carrier` and `resumed = false` | No |
| Unit | Role gate table: for each of `Role::View`/`Player`/`Control` × each of rows 1-16, the gate's outcome matches the table — in particular a `player` is refused `0x02`, `0x03`, `resize` and `set_*` | No |
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
| Memory per client | <640KB (8 video frames × ~32KB via `datagram_send_queue_frames`, plus 25 audio chunks via `audio_send_queue_chunks` and the 64-entry ack ring) |
