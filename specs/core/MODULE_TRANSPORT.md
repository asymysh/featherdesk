# Module Spec: Transport

## Overview

FeatherDesk runs over **HTTP/3 + WebTransport (QUIC)**, with a **WebSocket
fallback carrier** for networks that block UDP and for browsers below the
WebTransport floor. The floor for the product as a whole is **Chrome 107+,
Edge 98+, Firefox 130+, Safari 16.4+** — set by WebCodecs, which both carriers
need; the WebTransport carrier additionally needs Safari 26.4+ (see
[`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) → "Supported browsers").
QUIC's substantial performance + protocol benefits make it the preferred carrier;
the fallback is explicitly degraded and exists so that a blocked network yields a
slower session rather than no session.

This module defines:

- The **transport stack** the rest of the system runs on (TCP/HTTPS bootstrap,
  QUIC → WebTransport, and the WebSocket fallback carrier).
- The **channel model**: which kinds of messages travel as unreliable datagrams,
  and which travel on reliable bidirectional streams.
- The **connection lifecycle**: how clients connect, authenticate, and shut down.
- The **fragmentation rules** for messages that exceed the datagram limit the
  transport reports at runtime.

The wire format of the messages themselves (frame headers, input records,
clipboard payloads) lives in [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md). The
transport is the envelope; the protocol is the contents.

> **Implementation note.** WebTransport is provided by the **`wtransport`** crate
> (built on **`quinn`**); it is **pre-1.0 and tracks a still-draft spec** (maturity
> risk), and that risk is **isolated behind the `featherdesk-transport` crate** —
> the single insulation point (see CENTRAL "Risks").

---

## Why WebTransport (QUIC), not WebSocket

**WebSocket over TCP** has a single fatal property for real-time video: TCP's
in-order reliable delivery. A single lost packet stalls **all subsequent
delivery** until retransmit completes — video, audio, input, control, all of
it. At 1% packet loss the user experience degrades visibly; at 2% it's unusable.

**WebTransport over QUIC** provides:

| Property | Benefit for FeatherDesk |
|---|---|
| Independent stream multiplexing | Loss on the video path never blocks input delivery |
| Unreliable datagrams (RFC 9221) | Late video frames are dropped at the transport layer — never blocks delivery of fresh ones |
| Reliable streams when needed | Input, control, clipboard, file transfer keep their must-arrive guarantees |
| 1-RTT TLS 1.3 handshake (0-RTT on resume) | Faster connection setup vs TCP + TLS's 2-3 RTTs |
| Connection migration | Survives WiFi → cellular handoff without a reconnect blackout |
| BBR congestion control | Adapts to network change in ~100-300 ms vs TCP cubic's seconds |
| Single connection carries everything | Video + audio + input + control + file transfer share one auth, one TLS context, one congestion controller |

**What we give up:** ubiquity (some corporate firewalls block UDP) and browsers
below the WebTransport floor (Safari < 26.4, Firefox < 114). Neither costs the
user the product: both land on the **WebSocket fallback carrier** below, which
delivers the same protocol over TCP at the cost of head-of-line blocking under
loss. What is genuinely given up there is the latency property this table exists
for — which is why WebTransport is the *preferred* carrier and the fallback is
documented as degraded rather than as an equal option.

---

## Public Interface

```rust
// crate: featherdesk-transport

/// Transport is the listener abstraction: the TCP/HTTPS bootstrap listener, the
/// QUIC/WebTransport listener, and the WebSocket fallback carrier. The pipeline
/// owns one Transport instance; it accepts connections, multiplexes streams +
/// datagrams, and surfaces them to the server module.
#[async_trait::async_trait]
pub trait Transport: Send + Sync {
    /// Installs the route table BOTH listeners dispatch into. It is a setter, not a
    /// `Config` field, because the two ends cannot be built in either order otherwise:
    /// `server::Config.transport` takes the Transport BY VALUE, so the server does not
    /// exist when `transport::new` runs; and `/cert-hashes` has to read THIS Transport's
    /// live certificate state, so the router cannot be built first either.
    ///
    /// `Server::start` therefore builds its own route table (MODULE_SERVER "HTTP
    /// Endpoints"), calls this, and only then awaits `Transport::start`. Entering
    /// `start` with no router installed is `TransportError::NoRouter` — a construction
    /// bug, caught at startup rather than as a 404 storm. Internally an `ArcSwap`;
    /// calling it twice replaces the router and is not needed in v1.
    fn set_http_router(&self, r: std::sync::Arc<dyn HttpRouter>);

    /// Binds BOTH listeners on `cfg.bind` — TCP and UDP — and serves them until
    /// `cancel` fires. Blocks (awaits). TLS is mandatory on both; cert source is
    /// per server.tls config (MODULE_SERVER). The route table must already be
    /// installed with `set_http_router`; entering `start` without one is
    /// `TransportError::NoRouter`.
    async fn start(&self, cancel: CancellationToken) -> Result<(), TransportError>;

    /// Yields newly-accepted sessions from BOTH carriers. The server module reads
    /// from the receiver and drives per-session auth + dispatch. Capacity 16; when
    /// the channel is full the incoming session is closed immediately with
    /// `close::SERVER_FULL (4429)` reason `"server_busy"` rather than queued, which
    /// is the same shape as the `max_clients` overload the server applies after
    /// auth. It is never `AUTH_FAILED`: accept-queue overflow is a capacity
    /// condition, and a client that treats it as a credential failure discards its
    /// cached token and burns its single `/auth` retry.
    fn sessions(&mut self) -> tokio::sync::mpsc::Receiver<Box<dyn Session>>;

    /// Swaps the certificate chain and private key backing the live listeners'
    /// rustls `ServerConfig`, in place: no socket rebind, no dropped sessions.
    /// Sessions already established keep the certificate they handshook with; only
    /// future handshakes see the new chain.
    ///
    /// Two callers, one mechanism: the self-signed auto-rotation task (MODULE_SERVER
    /// "Browser certificate trust"), and `Server::reload_tls` — the applier for the
    /// `[server.tls]` **file contents** row (MODULE_CONFIG "Hot reload behavior"),
    /// which the pipeline's `fd-config` task calls. The Server is the sole owner of
    /// the Transport, so it is the only object that can forward the call; there is
    /// no signal task. On error the previous key stays in place and the reload is
    /// logged at `error` — a bad certificate file never takes a listener down.
    fn reload_tls(
        &self,
        chain: Vec<rustls::pki_types::CertificateDer<'static>>,
        key: rustls::pki_types::PrivateKeyDer<'static>,
    ) -> Result<(), TransportError>;

    // No close(): the listeners are torn down by dropping the Transport (RAII) or
    // by cancelling `cancel`. Cleanup is deterministic on Drop.
}

/// Session is one accepted client connection, on either carrier: a WebTransport
/// session over QUIC, or a `/ws` WebSocket over TCP. Both implementations expose
/// this identical surface, so everything above the transport crate is
/// carrier-blind — see "Carrier selection".
#[async_trait::async_trait]
pub trait Session: Send + Sync {
    /// A CancellationToken that fires when the session closes.
    fn cancelled(&self) -> CancellationToken;

    /// The client's network address (for logging only).
    fn remote_addr(&self) -> std::net::SocketAddr;

    /// Which carrier this session runs on. Everything above the transport crate is
    /// carrier-blind; this exists for the `config` message's `carrier` field, the
    /// `featherdesk_clients{carrier}` label, and per-session logging.
    fn carrier(&self) -> Carrier;

    /// Blocks until the client opens a new bidirectional stream. The FIRST byte
    /// of every such stream is a StreamType tag (stream_type::CONTROL / INPUT /
    /// CLIPBOARD / FILE); the server reads it to dispatch the stream. Streams
    /// are identified by tag, NOT by accept order. See "Stream Identification".
    async fn accept_stream(&self, cancel: CancellationToken) -> Result<Box<dyn Stream>, TransportError>;

    /// Opens a server-initiated bidirectional stream (unused in v1).
    async fn open_stream(&self, cancel: CancellationToken) -> Result<Box<dyn Stream>, TransportError>;

    /// Opens a server-initiated unidirectional stream. Used for the bootstrap
    /// stream (the server writes stream_type::BOOTSTRAP (0x10) then the seed IDR)
    /// and the cursor stream (stream_type::CURSOR (0x11), which stays open for the
    /// life of the session). See "Connection Lifecycle", MODULE_PROTOCOL
    /// "Fast-Join" and MODULE_CAPTURE "Cursor delivery".
    async fn open_uni_stream(&self, cancel: CancellationToken) -> Result<Box<dyn UniStream>, TransportError>;

    /// Blocks for the next inbound datagram. Returns at most one datagram per
    /// call, sized at most `max_datagram_size()`.
    async fn read_datagram(&self, cancel: CancellationToken) -> Result<bytes::Bytes, TransportError>;

    /// The largest application payload that fits in ONE datagram on this session,
    /// RIGHT NOW.
    ///
    /// `Some(n)` on the WebTransport carrier: quinn's current
    /// `max_datagram_size()`, already net of the QUIC short header, packet number,
    /// AEAD tag, DATAGRAM frame type and the HTTP/3 quarter-stream-id. DPLPMTUD
    /// raises and lowers it mid-connection, so callers MUST re-read it rather than
    /// cache it. A session that negotiated no datagram support is closed at accept,
    /// so this never returns `Some(0)`.
    ///
    /// `None` on the WebSocket fallback carrier: there is no datagram limit, and
    /// the pump emits one message per access unit without fragmenting.
    fn max_datagram_size(&self) -> Option<usize>;

    /// Current path statistics for this session, sampled at call time. The adaptive
    /// loop reads this every `[stream.adaptive] interval_ms`; it is the fast,
    /// per-ack RTT signal that the 1 Hz client `stats` line cannot provide.
    ///
    /// On the WebTransport carrier these come from quinn's connection path stats.
    /// On the WebSocket carrier `smoothed_rtt` is the OS TCP RTT where the platform
    /// exposes it and the app-level Ping/pong sample otherwise, and `lost_packets`
    /// is always 0 — TCP retransmits below the application, so loss is invisible
    /// here and shows up as latency instead. See "Carrier selection".
    fn path_stats(&self) -> PathStats;

    /// Queues ONE datagram for delivery. Fire-and-forget; delivery is best-effort.
    /// `payload` MUST be at most `max_datagram_size()` bytes; a larger payload
    /// returns `TransportError::DatagramTooLarge` and is not sent. Callers
    /// fragment; see "Datagram Fragmentation". The caller's send path must be
    /// frame-granular (enqueue whole access units, fragment at send) — never a
    /// small fixed channel of individual fragments.
    fn send_datagram(&self, payload: bytes::Bytes) -> Result<(), TransportError>;

    /// Gracefully closes the session with an application code. Standard codes
    /// are defined in MODULE_PROTOCOL.
    fn close_with_error(&self, code: u32, reason: &str) -> Result<(), TransportError>;
}

/// One bidirectional lane: a QUIC stream on the WebTransport carrier, one tagged
/// message lane of the single connection on the fallback carrier (where
/// `stream_id` is a synthetic per-lane id). AsyncRead+AsyncWrite; closing is RAII
/// (drop, or AsyncWriteExt::shutdown for a graceful FIN) — there is no explicit
/// Close().
pub trait Stream: tokio::io::AsyncRead + tokio::io::AsyncWrite + Send + Unpin {
    fn stream_id(&self) -> u64;
    /// Abort the read direction with a reset code.
    fn cancel_read(&self, code: u32);
    /// Abort the write direction with a reset code.
    fn cancel_write(&self, code: u32);
}

/// One unidirectional (write-only) lane — same carrier mapping as `Stream`.
/// Closing is RAII (drop / shutdown).
pub trait UniStream: tokio::io::AsyncWrite + Send + Unpin {
    fn stream_id(&self) -> u64;
    fn cancel_write(&self, code: u32);
}

/// One HTTP request handler, shared by BOTH listeners. The transport crate owns
/// the sockets and the TLS context; the server module owns the routes. Because
/// the same router serves TCP/TLS and HTTP/3, a route can never be present on
/// one carrier and absent on the other.
#[async_trait::async_trait]
pub trait HttpRouter: Send + Sync {
    async fn handle(&self, req: http::Request<Body>) -> http::Response<Body>;
}

/// Owned snapshot — no borrow of transport internals, so it is safe to sample
/// from any task. Cheap: a struct copy, no lock.
#[derive(Clone, Copy, Debug)]
pub struct PathStats {
    pub smoothed_rtt: std::time::Duration,
    pub cwnd: u64,               // congestion window in bytes; 0 when the carrier
                                 //   does not expose one. "How big may a datagram
                                 //   be" is `max_datagram_size()`, not this.
    pub lost_packets: u64,       // cumulative; always 0 on the WebSocket carrier
    pub sent_packets: u64,       // cumulative
    pub congestion_events: u64,  // cumulative
}

/// Config sources the [server] + [server.tls] + [transport] sections. There is
/// deliberately no `max_clients` here (session admission is the server's — see
/// MODULE_SERVER "DoS Protection").
pub struct Config {
    pub bind: SocketAddr,  // "0.0.0.0:30084" — bound BOTH as TCP and as UDP
    pub tls: TlsConfig,    // certificate sources (same as MODULE_SERVER); one
                           // rustls ServerConfig backs both listeners
    // There is deliberately no `http` field: the route table arrives after
    // construction, through `Transport::set_http_router`. The server owns the routes
    // and takes the Transport by value, so it cannot exist yet here; and
    // `/cert-hashes` reads this Transport's own certificate state, so the router
    // cannot be built first either.
    // Logging is via the `tracing` crate (replaces the old *slog.Logger field).
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Carrier {
    /// HTTP/3 + WebTransport over QUIC. Preferred: independent lanes, unreliable
    /// datagrams for media.
    WebTransport,
    /// WebSocket over TCP. Degraded fallback: one ordered byte stream for every
    /// lane, so loss stalls all of them together. See "Carrier selection".
    WebSocket,
}

impl Carrier {
    /// The wire value of the `config` message's `carrier` field.
    pub fn as_str(self) -> &'static str {
        match self { Carrier::WebTransport => "webtransport",
                     Carrier::WebSocket    => "websocket" }
    }
}

/// One thiserror-derived error enum for the crate.
#[derive(Debug, thiserror::Error)]
pub enum TransportError {
    #[error("transport closed")] Closed,
    #[error("tls: {0}")] Tls(String),
    #[error("io: {0}")] Io(#[from] std::io::Error),
    #[error("datagram too large: {got} B > {max} B")]
    DatagramTooLarge { got: usize, max: usize },
    #[error("no HTTP router installed: Transport::set_http_router was never called")]
    NoRouter,
}

/// Returns a Transport backed by quinn + wtransport (QUIC) and hyper +
/// tokio-rustls (the TCP listener and the WebSocket fallback carrier).
pub fn new(cfg: Config) -> Result<impl Transport, TransportError> { /* … */ }
```

---

## Channel Model — what travels how

The single most important transport decision is which message types go on
**unreliable datagrams** and which go on **reliable streams**. The rule:

> **Datagrams** for anything where a late message is useless and a dropped
> message is preferable to a stall.
> **Reliable streams** for anything where delivery + ordering must be guaranteed
> (input events MUST arrive; a missed keydown is unrecoverable).

| Channel | Mechanism | Direction | Rationale |
|---|---|---|---|
| **Video frames** (H.264 / HEVC / AV1) | Datagrams (fragmented) | S → C | Late frame is useless; skip to fresh frame. Loss → client requests keyframe. |
| **Audio chunks** | Datagrams | S → C | Same as video: a late 20 ms packet is useless. A 20 ms Opus packet is one datagram; PCM fragments. Loss is concealed from the audio `Sequence`, never retransmitted. |
| **Cursor position** | Datagrams | S → C | Latest-wins; an old position is uninteresting. Fixed 14-byte payload, never fragmented. |
| **Cursor stream** (tag `0x11`) | Reliable **unidirectional** stream | S → C | `[u32 Len][record]` cursor shapes (and the one join-time position record). A bitmap is useless if partially delivered and is far past the datagram MTU, so it needs reliability, not latest-wins. Opened at join, stays open. |
| **Server Ping** | Datagrams | S → C | Best-effort; missed Pings are harmless. |
| **Gamepad rumble** | Datagrams | S → C | Best-effort; rumble for a button press that's already past is useless. |
| **Control stream** (tag `0x00`) | Reliable bidirectional stream | both | Auth, Config, JSON control messages, keyframe requests (any role). **Newline-delimited JSON**, capped at `max_message_bytes`. No bulk payloads. |
| **Input stream** (tag `0x01`) | Reliable bidirectional stream | both | Binary input records + InputAck, **`[u16 RecLen]`-prefixed** both directions. Reliability is non-negotiable. |
| **Bootstrap stream** (tag `0x10`) | Reliable **unidirectional** stream | S → C | One `[u32 Len][FrameHeader‖IDR]` keyframe to seed a joining/resuming decoder, then close. Guarantees a decodable first frame even though live video is lossy datagrams. |
| **Clipboard stream** (tag `0x02`) | Reliable bidirectional stream | both | `[u32 Len][JSON]` clipboard offers/data. Opened by the controller right after `auth_ok` when `config.clipboard != "disabled"`; viewers and players never open it. Off the 4 KiB control stream because payloads reach 1 MiB. |
| **Gamepad input** | Reliable stream (same as input) | C → S | Snapshot diff is reliable; rumble (S → C) goes datagram. |
| **File-transfer streams** (tag `0x03`) | Reliable bidirectional streams (one per transfer) | both | Each transfer is independent; one stalling transfer doesn't block others or the media path. |

**Stream layout (per session).** Every stream's first byte is a `StreamType`
tag, so streams are dispatched by **identity, not open order** (see "Stream
Identification"):

```
Session opens
  ├─ Control stream     (client-opened bidi, tag 0x00)
  │     ├─ Auth handshake (first JSON line after the tag)
  │     ├─ Config (S → C, JSON line)
  │     ├─ JSON control C → S (keyframe, resize, set_*, pong, stats,
  │     │                       decode_unsupported)
  │     └─ (Stays open for the life of the session)
  ├─ Input stream       (client-opened bidi, tag 0x01; controller or co-op player)
  │     ├─ [u16 RecLen] binary input records C → S
  │     └─ [u16 RecLen] InputAck S → C
  ├─ Bootstrap stream   (server-opened UNI, tag 0x10; once per join/resume)
  │     └─ [u32 Len][FrameHeader‖IDR] then close
  ├─ Cursor stream      (server-opened UNI, tag 0x11; once per join, stays open)
  │     └─ [u32 Len][cursor record] shapes + the join position record
  ├─ Clipboard stream   (client-opened bidi, tag 0x02; CONTROLLER ONLY, opened
  │                      immediately after auth_ok whenever the config message
  │                      advertises clipboard != "disabled" — eagerly, NOT on
  │                      first use, so a host→client push has somewhere to land
  │                      before the client has copied anything)
  │     └─ [u32 Len][JSON] both directions
  └─ File-transfer streams (client-opened bidi, tag 0x03; one per transfer)
        18-byte framed file-transfer protocol from MODULE_FILETRANSFER.

Datagrams (separate from streams):
  S → C: video / audio / cursor position / ping / gamepad rumble
  C → S: (none in v1 — reserved for future client-side media)

On the WebSocket fallback carrier the same lanes travel as tagged messages over
one TCP connection, datagrams included (tag 0x20) — see "Carrier selection".
```

> **No `/files` endpoint anymore.** File transfer was previously a separate
> WebSocket connection precisely to avoid HOL blocking video. Under QUIC, each
> transfer is its own stream within the same session — automatic isolation,
> same auth, same TLS context. Cleaner and one fewer endpoint to operate.

### Stream Identification

QUIC does **not** guarantee that streams are accepted in the order the client
opened them, and the client opens several stream kinds (input, clipboard, file
transfer) at unpredictable times. Dispatching by accept-order is therefore a
bug. Instead, **every stream is self-identifying**: the opener writes a 1-byte
`StreamType` tag as the very first byte, before any framed payload.

```rust
// First byte of every stream (QUIC carrier) and of every message (WebSocket
// carrier). One tag space, both carriers. Defined in featherdesk-protocol,
// shared with the client.
pub mod stream_type {
    pub const CONTROL: u8 = 0x00;   // bidi, client-opened, exactly one per session
    pub const INPUT: u8 = 0x01;     // bidi, client-opened, controller or co-op player
    pub const CLIPBOARD: u8 = 0x02; // bidi, client-opened, CONTROLLER only; opened
                                    // immediately after auth_ok when the config
                                    // message advertises clipboard != "disabled"
    pub const FILE: u8 = 0x03;      // bidi, client-opened, one per transfer
    pub const BOOTSTRAP: u8 = 0x10; // uni,  server-opened, one per join/resume
    pub const CURSOR: u8 = 0x11;    // uni,  server-opened, one per session, stays open.
                                    // Carries cursor SHAPE records (Kind 0x01) and the
                                    // single join-time position record (Kind 0x02) — on
                                    // BOTH carriers. It never carries a stream of
                                    // positions; those are CURSOR_UPDATE datagrams.
    pub const DATAGRAM: u8 = 0x20;  // WebSocket carrier ONLY — the datagram lane, and the
                                    // sole carrier of PING/CURSOR_UPDATE/GAMEPAD_RUMBLE
                                    // there. A QUIC stream opened with this tag is a
                                    // protocol error.
}
```

The server's accept loop reads the first byte of each accepted bidi stream and
dispatches: `0x00` → control handler, `0x01` → input handler, `0x02` → clipboard
handler, `0x03` → a new file-transfer handler. The browser client reads the first
byte of each incoming **uni** stream identically and routes `0x10` to its
bootstrap reader and `0x11` to its cursor reader. The transport layer does not
know about roles: role gating for `0x01`/`0x02`/`0x03` is the server's, per
[`MODULE_SERVER.md`](./MODULE_SERVER.md) "Role gate table".

**A bad tag is a stream-scope error, never a session-scope one.** An unknown tag,
a `0x20` on a QUIC stream, a **second** `0x00`, or a stream whose role gate
rejects this session → `cancel_read(close::PROTOCOL_ERROR)` +
`cancel_write(close::PROTOCOL_ERROR)` on **that stream only**; the session and
every other stream keep running. This matters because a client's own idea of its
role can be stale — it may have been silently downgraded (see `auth_ok.role`) —
and a stale assumption must degrade, not disconnect. The **only** stream whose
failure closes the session is the control stream, because the session's framing
contract depends on it: a malformed control line, a line over
`max_message_bytes`, or a closed control stream →
`close_with_error(close::PROTOCOL_ERROR, …)` on the session.

---

## Connection Lifecycle

```
1. Client: GET /  (HTTPS over TCP)      → embedded HTML/JS client, plus
                                           Alt-Svc: h3=":<port>"; ma=86400
2. Client: POST /auth (JSON body, JSON response — schema in MODULE_AUTH
   "The /auth endpoint", served by `Authenticator::handle_auth`)
                                        → {"session_token":"…","ttl_sec":…,
                                           "role_ceiling":"…"}
3. Client: new WebTransport("https://host:port/wt")
   (or, when UDP is blocked, the /ws upgrade — see "Carrier selection")
4. WebTransport handshake (TLS 1.3, 1 RTT)
5. Server: AcceptSession → Transport surfaces a new Session to the server module.
   The server arms a 5 s auth deadline on the session HERE (step 5), not when a
   stream arrives — a client that never opens any stream is closed at +5 s.
6. Client: opens a bidirectional stream, writes the StreamType tag byte 0x00
   (control), then the auth line:
       {"type":"auth","token":"<session_token>","role":"control|view|player",
        "resume":false,"takeover":false,"decode":{…}}
7. Server: accept loop reads the 0x00 tag → routes to the control handler →
   reads the auth line.
8. Server: validates the token, clamps the requested role to the credential's
   ceiling, and arbitrates the controller slot
     - Valid       → reply {"type":"auth_ok",…} carrying the EFFECTIVE role,
                     the gamepad slot and the session id (which may differ from
                     what the client asked for — see MODULE_PROTOCOL "Shared
                     Payload Types"), then send the {"type":"config",…} line,
                     then open the bootstrap uni stream (tag 0x10) and write the
                     seed IDR, then open the cursor uni stream (tag 0x11) and
                     seed it with the cached cursor shape + position. The cursor
                     stream is opened for EVERY session regardless of the
                     resolved cursorMode and stays open for the life of the
                     session — a mid-session flip to "separate" is then just
                     more records on the stream that is already there.
     - Invalid     → close_with_error(close::AUTH_FAILED, "auth failed")
     - Server full → close_with_error(close::SERVER_FULL, "max_clients")
     - Auth deadline fired (no 0x00 control stream + valid auth within 5 s)
                   → close_with_error(close::AUTH_TIMEOUT, "auth timeout")
9. Server: accepts additional client streams (input 0x01, clipboard 0x02, file
   0x03) as they arrive, dispatching by tag.
10. Steady state: media on datagrams, control + input on streams, all multiplexed
11. Either side closes → close_with_error or graceful close → Transport surfaces
    the close event to the server module → session resources released
```

**The lifecycle is carrier-independent.** On the fallback carrier steps 3-4 are
the `/ws` upgrade instead of the WebTransport constructor, and "opens a stream"
means "sends a message tagged with that lane's `StreamType`"; every other step,
including the 5 s auth deadline, is byte-for-byte the same.

**Auth carrying (no `Sec-WebSocket-Protocol`, no URL query token).** Browsers
cannot set `Authorization` headers on the WebTransport constructor. Instead, the
**first message on the control lane** carries the auth token, on both carriers.
The fallback's `featherdesk.v1` subprotocol is a version marker and never carries
a credential — a token in a subprotocol header or a URL ends up in proxy and
server logs. This works identically for browser and native clients.

The HTTPS `POST /auth` endpoint exchanges a mode credential for a bearer — a
**credential token**, returned in the response's `session_token` field. It lives
in the Authenticator's own credential store, is presented exactly once on the
control stream with `"resume": false`, and is never written to
`server::SessionCache`. Its request, response and error schema are defined once
in [`MODULE_AUTH.md`](./MODULE_AUTH.md) "The `/auth` endpoint"
(`Authenticator::handle_auth`). Only the WebTransport carry mechanism is
transport's concern.

### Resume

**Two token kinds, and they never mix.** The token a client presents with
`resume:true` is NOT the one `POST /auth` returned: it is the **session token**
the SERVER minted at [`MODULE_SERVER.md`](./MODULE_SERVER.md) lifecycle step 12,
after arbitration, and delivered only in `config.session_token`. That one lives
in `server::SessionCache`, is single-use, and is the ONLY token accepted with
`"resume": true`; the `POST /auth` credential token is presented exactly once
with `"resume": false` (MODULE_AUTH "Two token kinds"). To resume, the client
opens a new WebTransport session, opens the control stream (tag `0x00`), and
sends:

```json
{"type":"auth","token":"<config.session_token>","resume":true}
```

Server checks the SessionCache. The lookup **consumes** the cached entry,
admission control and controller-slot arbitration run exactly as on a fresh
connection, the role is taken from the cache (the `role` field in a `resume:true`
message is ignored), and the reply carries a **freshly minted** session token that
the client must adopt — see [`MODULE_SERVER.md`](./MODULE_SERVER.md) steps 8-13.
If the entry is present + within TTL, the server replies `{"type":"auth_ok",…}`,
sends `{"type":"config","resumed":true,…}`, opens a fresh **bootstrap stream**
with a *fresh* IDR (forced through the rate-limited keyframe path if the cached
one is stale — see [`MODULE_SERVER.md`](./MODULE_SERVER.md) "Keyframe Caching
Strategy"), re-seeds the cursor stream, and continues the live datagram stream.
If not, replies `auth_failed` with code 4401 and the client falls back to fresh
`POST /auth`.

> There is **no `last_video_seq`** in the resume message. The bootstrap stream
> always seeds a decodable keyframe, so a client-supplied last-sequence hint
> buys nothing (see [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) → "Resume Path").

---

## Datagram Fragmentation

QUIC datagrams are capped at whatever the current path MTU leaves after QUIC's
own overhead. **There is no constant for this and the specs must not state one.**
The sender reads `Session::max_datagram_size()` once per access unit and derives
its payload budget:

```rust
const DATAGRAM_HEADER_SIZE: usize = 8;   // featherdesk-protocol

// Once per access unit, in datagram_pump — NOT once per process.
let frag_payload = match sess.max_datagram_size() {
    None    => usize::MAX,   // fallback carrier: one message, no fragmentation
    Some(n) => n.saturating_sub(DATAGRAM_HEADER_SIZE),
};
```

Worked example, WebTransport carrier at a conservative 1200-byte path MTU with an
8-byte destination connection id: 1200 − 1 (short-header type byte) − 8 (DCID)
− 4 (packet number, worst case) − 16 (AEAD tag) − 1 (DATAGRAM frame type)
− 2 (HTTP/3 quarter-stream-id varint) ≈ **1168** bytes reported by
`max_datagram_size()`, giving `frag_payload = 1160`. A 40 KB IDR (40 960 payload
bytes plus the 22-byte `FrameHeader`) is then ⌈40 982 / 1160⌉ = **36 fragments**.
Once DPLPMTUD raises the path MTU the same frame takes fewer. Neither number is a
constant anywhere in the code or the specs.

A `send_datagram` that returns `TransportError::DatagramTooLarge` means the path
MTU shrank between the read and the send. The pump logs it at `warn`, increments
`featherdesk_datagram_oversize_total`, **abandons the remaining fragments of that
access unit** (a partial frame is useless — see "Datagram reassembly rules" in
[`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md)) and re-reads `max_datagram_size()`
for the next one.

The 8-byte `DatagramHeader` lives in `MODULE_PROTOCOL.md`, but the rules are:

- Each frame gets a monotonic **FrameID** (u32) == the access unit's
  `FrameHeader.Sequence`. For the control datagram types, which have no
  `FrameHeader`, it is a per-Type server counter (see MODULE_PROTOCOL
  "`FrameID` semantics differ by class").
- Each datagram carries `[Version][Type][FrameID u32][FragIndex u16]`. There is
  **no FragCount field** — bit 15 of `FragIndex` is the LAST flag, and the last
  fragment's index N means the frame has N+1 fragments (indices 0..N). Fragment 0
  additionally carries the 22-byte `FrameHeader` at the start of its payload.
- The receiver's buffering, deadline, eviction, ordering, wraparound and size-cap
  rules are defined once, in [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) →
  "Datagram reassembly rules". They are not restated here.

This gives "late frame = useless = dropped" semantics for free at the transport
boundary, with no retransmit cost.

> **Send-side queue is frame-granular, not fragment-granular (fixes T-1).** A
> 1080p IDR is ~40-50 KB ≈ 36+ datagrams. The server must NOT push individual
> fragments into a small fixed-size channel — a channel cap of, say, 32 would
> drop fragments *mid-frame*, corrupting **every** frame under load instead of
> cleanly dropping whole stale frames. Instead the per-session out-queue is a
> `FrameOut` ring of **assembled access units** (cap
> `[transport] datagram_send_queue_frames`, producer-side drop-oldest); a single
> pump task pulls one frame, fragments it, and calls `send_datagram` per fragment
> back-to-back. Loss is then naturally whole-frame (recovered by the reassembly
> deadline), never mid-frame. `FrameOut` is owned by the server module
> ([`MODULE_SERVER.md`](./MODULE_SERVER.md)); `Transport::send_datagram` itself is
> fire-and-forget per datagram.

---

## Server-Side Implementation

Built on **`quinn`** + **`wtransport`**. Both are production-grade for QUIC
(`quinn` is the de-facto Rust QUIC implementation; `wtransport` is the WebTransport
server on top of it — pre-1.0, see the maturity note in the Overview).

**Two listeners, one port, one certificate, one router.** `[server] bind`
supplies a single `host:port`; the transport binds a **TCP** listener and a
**UDP** socket on it. Both terminate TLS from the same rustls `ServerConfig`
(so a self-signed rotation swaps one key for both), and both dispatch into the
**same** `HttpRouter` the server module supplies. A route therefore cannot
exist on one carrier and be missing on the other.

| Listener | ALPN | Serves |
|---|---|---|
| TCP + TLS 1.3 | `h2`, `http/1.1` | `/`, `/cert-hashes`, `/healthz`, `/auth`, `/pair`, `/logout`, and the `/ws` WebSocket upgrade (`http/1.1` only) |
| UDP + QUIC (HTTP/3) | `h3` | the same routes, plus the `/wt` WebTransport upgrade. `/ws` is not served over h3. |

**The TCP listener is what makes the origin loadable.** A browser's first
navigation to an origin is always TCP; it will not attempt HTTP/3 to a host it
has never spoken to. Serving `GET /` only over HTTP/3 means the SPA can never be
fetched, and therefore the WebTransport session it would open can never be
constructed. The TCP listener is not optional and has no config key.

**`Alt-Svc`.** Every response from the TCP listener carries
`Alt-Svc: h3=":<port>"; ma=86400`, where `<port>` is the port from
`[server] bind`. This moves the browser's subsequent same-origin fetches
(`/cert-hashes`, `/auth`, `/healthz`) onto HTTP/3 and keeps one QUIC connection
warm. Note that `new WebTransport()` connects to the URL's authority over
HTTP/3 **directly** and does not itself consult `Alt-Svc` — the header is an
optimisation for the ordinary fetches; the listener is the load-bearing part.

This does **not** reintroduce a second media transport. The TCP listener serves
static assets and short JSON endpoints; no video, audio, input or cursor byte
ever crosses it. The separate degraded media carrier is the WebSocket fallback
in "Carrier selection" below, which is a deliberate, documented product decision
with its own section.

```rust
// crate: featherdesk-transport (src/wt.rs)
use wtransport::{Endpoint, ServerConfig};

let tls = cfg.tls.into_rustls()?;          // ONE rustls ServerConfig, both listeners

// 1. TCP + TLS: static assets, JSON endpoints, /ws upgrade, Alt-Svc.
//    hyper + tokio-rustls; ALPN ["h2", "http/1.1"].
let tcp = tokio::net::TcpListener::bind(cfg.bind).await?;
spawn_tcp_https(tcp, tls.clone(), cfg.http.clone(), alt_svc_for(cfg.bind), cancel.clone());

// 2. UDP + QUIC: the same routes over HTTP/3, plus the /wt upgrade.
let endpoint = Endpoint::server(
    ServerConfig::builder()
        .with_bind_address(cfg.bind)
        .with_certificate(tls)
        .build(),
)?;

// Accept loop: each incoming session is surfaced to the server module.
while let Some(incoming) = endpoint.accept().await {
    let session = incoming.await?;          // WebTransport upgrade (validates :path = /wt)
    if sessions_tx.send(new_session(session)).await.is_err() {
        break; // server module dropped the receiver
    }
}
```

---

## Client-Side (Browser)

```js
// Auth
const r = await fetch('/auth', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mode, password }),
});
const { session_token } = await r.json();

// Certificate trust: in self-signed mode the browser opens WebTransport ONLY if
// we pass the cert's SHA-256(DER) hash(es). CA-trusted mode returns an empty
// list and we omit the option. See MODULE_SERVER "Browser certificate trust".
const hashes = await (await fetch('/cert-hashes')).json();   // {hashes:[base64,…]}
const opts = hashes.hashes.length ? {
    serverCertificateHashes: hashes.hashes.map(b64 => ({
        algorithm: 'sha-256', value: base64ToArrayBuffer(b64),
    })),
} : {};

// Carrier: WebTransport if it opens within the deadline, otherwise the /ws
// fallback. Everything below is identical on both — same tags, same framing.
// See "Carrier selection".
const wt = await openCarrier(opts);

// Control stream first (always). First byte = StreamType tag 0x00.
const control = await wt.createBidirectionalStream();
const ctlWriter = control.writable.getWriter();
const ctlReader = control.readable.getReader();
await ctlWriter.write(new Uint8Array([0x00]));        // StreamControl tag
// Two token kinds, and they never mix (MODULE_AUTH "Two token kinds"): the /auth
// bearer above is presented with resume:false; the token cached from a previous
// session's `config.session_token` is the ONLY one accepted with resume:true.
const resuming = cachedSessionToken != null;
await ctlWriter.write(jsonEncode({
    type: 'auth', token: resuming ? cachedSessionToken : session_token,
    role, resume: resuming,
    takeover: takeoverRequested, decode: await probeDecode(),
}));

const authResp = await readJSON(ctlReader);  // expects {"type":"auth_ok",...}
// authResp.role is the EFFECTIVE role — the server may have downgraded us, and
// opening a stream that role is not entitled to costs us that stream.
// Then a {"type":"config",...} line follows on the same stream.

// Input stream — only for an effective role that may send input.
// First byte = StreamType tag 0x01.
if (authResp.role === 'control' || authResp.role === 'player') {
    const input = await wt.createBidirectionalStream();
    const inWriter = input.writable.getWriter();
    await inWriter.write(new Uint8Array([0x01]));      // StreamInput tag
    // … [u16 RecLen]-prefixed binary input records flow on input.writable
}

// Incoming UNI streams: the server-opened bootstrap stream (tag 0x10) seeds
// the decoder with a reliable keyframe before any datagrams are decoded; the
// cursor stream (tag 0x11) carries shapes and the join position record and
// stays open for the life of the session.
for await (const uni of wt.incomingUnidirectionalStreams) {
    routeUniStream(uni); // reads first byte: 0x10 → bootstrap IDR reader,
                         //                   0x11 → cursor reader
}

// Datagrams (video, audio, cursor position, ping, rumble)
for await (const chunk of wt.datagrams.readable) {
    routeDatagram(chunk); // dispatch by Type field in the 8-byte DatagramHeader
}
```

The client module ([`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md)) covers the full
client-side wiring including fragmentation reassembly, codec setup, and
adaptive bitrate.

---

## Native Client (v2)

Reserved for v2. A native client uses `quinn` directly (no HTTP/3 / no
WebTransport — they're browser conveniences) for a leaner stack and sub-ms
input latency. The wire protocol (frame format, channel model) is **identical**
to the browser path; only the transport library differs. See the v2 entry in
[`README.md`](../../README.md) → "Future plan" and (future)
`MODULE_NATIVE_CLIENT.md` for the embedded Tailscale `tsnet` + auth-key paste
flow that ships with the native client.

---

## Application-Layer Error Codes

The close codes are defined **once**, in [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md)
(`featherdesk-protocol`). The transport crate references `protocol::close::*` — it
does **not** redefine them, so the two can never drift:

```rust
use featherdesk_protocol::close;

// Used directly, e.g.:
sess.close_with_error(close::AUTH_TIMEOUT, "auth timeout");
```

| Code | Value | Meaning |
|---|---|---|
| `close::NORMAL` | 0 | Graceful close |
| `close::PROTOCOL_ERROR` | 4400 | Malformed message / bad StreamType tag / duplicate control stream |
| `close::AUTH_FAILED` | 4401 | Bad credentials, expired token, **or expired resume token**. Credential failures ONLY — a capacity, queue-depth or load condition is never reported with this code (see `SERVER_FULL`) |
| `close::AUTH_TIMEOUT` | 4408 | Control stream not opened + authed within 5 s |
| `close::CONTROLLER_TAKEOVER` | 4410 | Controller slot seized |
| `close::SERVER_FULL` | 4429 | Server has no capacity for this session: `max_clients` reached (after auth, lifecycle step 9) **or** the 16-deep accept queue is full (before auth, step 3). The client should back off and retry, resuming — never re-authenticate |
| `close::SERVER_SHUTDOWN` | 4503 | Server going away |

There is no separate "resume expired" code — an expired resume token is just
`close::AUTH_FAILED (4401)`, and the client falls back to a fresh `POST /auth`.

Stream resets reuse the same code space; a stream `cancel_read(code)` with one
of these means "this stream is unusable for the given reason".

The client's per-code reconnect policy is
[`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) "Reconnect policy"; a
code that is not in that table is treated as the generic case. On the WebSocket
fallback carrier these values are the WebSocket close code verbatim, except
`NORMAL (0)`, which is sent as `1000`.

---

## Configuration

```toml
[server]
bind         = "0.0.0.0:30084"     # TCP *and* UDP listen address: HTTPS bootstrap +
                                   # HTTP/3 + WebTransport, one port (restart required)
allow_origin = ""                  # same-origin only (default); set to "*" only for LAN
max_clients  = 25                  # hard cap on live sessions; enforced by the SERVER
                                   # after auth, rejecting with close::SERVER_FULL (4429)
max_message_bytes = 4096           # max control-stream message
input_rate_limit  = 1000           # per-client input events/sec cap
keyframe_min_interval_ms   = 500   # global coalescer: at most one forced IDR per this
                                   # window, across all clients and all reasons
keyframe_request_burst     = 3     # per-session token bucket for {"type":"keyframe"}
keyframe_request_refill_ms = 2000  # one token per this interval
shutdown_grace_ms          = 250   # flush window for {"type":"server_shutdown"} before
                                   # closing sessions with close::SERVER_SHUTDOWN 4503

[server.tls]
cert         = ""                  # CA-trusted mode: path to PEM cert chain
key          = ""                  # CA-trusted mode: path to PEM private key
                                   # BOTH empty = self-signed mode (the self-hosted/LAN
                                   # default): short-lived (≤14d) auto-rotated ECDSA P-256
                                   # cert; browser connects via serverCertificateHashes.
                                   # See MODULE_SERVER "Browser certificate trust".
extra_sans   = []                  # self-signed mode: extra SANs (e.g. ["host.lan","10.0.0.5"])
rotate_before = "3d"               # self-signed mode: regenerate when < this remains
# (No min_version knob — TLS 1.3 is mandatory under QUIC; config uses
#  deny_unknown_fields, so a min_version key would be rejected. See MODULE_SERVER.)

[transport]
# Tunables for the QUIC transport. Defaults are good; expose for ops debugging.
keepalive_period         = "15s"   # QUIC keepalive PINGs (transport-level liveness)
max_idle_timeout         = "30s"   # QUIC closes after this much silence (must be > keepalive_period)
ping_interval            = "2s"    # app-level Ping datagram cadence (RTT sampling, NOT liveness;
                                   # 0 disables). See MODULE_SERVER "Keepalive, liveness & timeouts".
initial_max_data         = "10MiB" # initial connection-level flow control window
initial_max_stream_data  = "1MiB"  # per-stream flow control
max_streams_bidi         = 16      # cap on concurrent bidi streams per session.
                                   # VALIDATED: must be >= 3 + filetransfer.max_concurrent
                                   # + filetransfer.queue_depth (3 + 4 + 8 = 15 at defaults)
max_streams_uni          = 16      # cap on concurrent uni streams; must be >= 2
                                   # (bootstrap + cursor)
enable_datagrams         = true    # MUST be true on the WebTransport carrier; required for video
fragment_reassembly_ms   = 17      # drop deadline at 60 fps; use 34 at 30 fps
reassembly_max_bytes     = "4MiB"  # per-session in-progress reassembly bytes; oldest-
                                   # FrameID-first eviction above it
join_idr_timeout         = "1s"    # how long a join waits for a fresh IDR before serving
                                   # the stale one with the pump gated
datagram_send_queue_frames = 8     # per-session video out-queue depth in WHOLE frames
                                   # (`FrameOut`, drop-oldest). Frame-granular, never
                                   # fragment-granular — see "Datagram Fragmentation".
audio_send_queue_chunks  = 25      # per-session audio out-queue depth in WHOLE chunks (a
                                   # SEPARATE FrameOut ring, ~500 ms at frame_ms = 20, drop-oldest)
websocket_fallback       = true    # serve the degraded WebSocket carrier on /ws.
                                   # false → /ws returns 501 and UDP-blocked clients
                                   # cannot connect at all. See "Carrier selection".
ws_max_message_bytes     = "16MiB" # inbound cap on the fallback carrier; must be >= the
                                   # 16 MiB per-frame reassembly cap
ws_send_queue_bytes      = "8MiB"  # fallback carrier out-queue byte ceiling; exceeding it
                                   # after coalescing closes the session (4400)
auth_deadline            = "5s"    # close unauthed sessions (close::AUTH_TIMEOUT 4408)
```

> `max_streams_bidi` is **validated, not tuned**: the config loader requires
> `max_streams_bidi >= 3 + filetransfer.max_concurrent + filetransfer.queue_depth`
> — three persistent streams (control, input, clipboard) plus one stream per active
> or queued file transfer — and refuses to start otherwise, naming the arithmetic.
> At the defaults that is `3 + 4 + 8 = 15 <= 16`. The same sum is advertised to the
> client as `config.fileStreamBudget` (12), so a well-behaved client never opens a
> stream the transport cannot grant.
>
> **Behaviour at the limit.** QUIC does not reject an over-budget stream open — it
> flow-controls it, so `createBidirectionalStream()` returns a promise that simply
> never settles. The client therefore races every file-stream open against a **10 s**
> deadline; on expiry it abandons that open, surfaces `too_many_streams` against that
> file in the Files panel, and retries it from its own local queue when a slot frees.
> No transfer ever hangs without UI state, and the session and the in-flight
> transfers are unaffected.

---

## Security

- **TLS 1.3 is mandatory** on both listeners — under QUIC weaker negotiation is
  not possible, and the TCP listener is configured for TLS 1.3 only.
- **Cert handling** — two modes (see MODULE_SERVER "TLS Configuration" +
  "Browser certificate trust"): config-supplied PEM (CA-trusted) **or** a
  short-lived (≤14-day) auto-rotated self-signed ECDSA P-256 cert reached from
  the browser via `serverCertificateHashes`. Unlike the old WSS path, a
  self-signed cert is **not** trusted by a click-through — the WebTransport
  handshake requires the cert hash, served via `/cert-hashes`. Self-signed private
  key file is mode `0600` (Unix) / ACL-restricted (Windows), verified at startup.
  One rustls `ServerConfig` backs both listeners, and `Transport::reload_tls` swaps
  the certified key in place for both. See MODULE_SERVER "Browser certificate
  trust" for the pin-on-first-use record and the residual first-contact risk.
- **Origin header** validated on the HTTP/3 upgrade and on the `/ws` upgrade
  (`Origin` is sent on WebTransport just as on WebSocket). Default
  `allow_origin = ""` is same-origin only; matches the audit fix from pass 2. The
  value has exactly one initial-value path and one applier: it is a field of
  `server::SessionDefaults`, filled at MODULE_PIPELINE step 7 from the parsed
  config and replaced wholesale by `Server::set_session_defaults` on the
  `fd-config` task. It is loaded from the `ArcSwap` on each upgrade — i.e. before
  a session exists — so the check is armed from the first connection, not from
  the first reload. There is no separate `Server` setter for it.
- **Auth must complete on the control stream within 5 s** of session
  acceptance, on either carrier. Sessions without successful auth are closed with
  code 4408. This prevents idle unauthenticated sessions from sitting on the
  connection table.
- **Session admission is the server's**, after auth: `[server] max_clients` is
  counted across both carriers and an excess session is closed with
  `close::SERVER_FULL (4429)` — never `AUTH_FAILED`, which a client cannot
  distinguish from a bad credential (see MODULE_SERVER "DoS Protection").
- **Per-client rate limits** from the audit (4 KiB max control message, 1000
  input events/sec) carry over and apply to the control + input lanes on both
  carriers.
- **Cross-origin** WebTransport requires the same explicit opt-in CORS-style
  check as WebSocket; nothing changes here.

---

## Carrier selection

Two carriers deliver the identical protocol. **The stream tags, the per-lane
framing, the control vocabulary, the close codes and the auth handshake are the
same on both**; only the envelope differs. The fallback is a second
`transport::Session` implementation inside `featherdesk-transport` — nothing above
the transport crate knows which carrier it is running on, except the one `carrier`
field in the `config` message and the `carrier` metric label.

| | WebTransport (preferred) | WebSocket (fallback) |
|---|---|---|
| Endpoint | `/wt` on the UDP/QUIC listener | `/ws` on the TCP listener, `http/1.1`, subprotocol `featherdesk.v1` |
| Lanes | one QUIC stream per tag + a datagram lane | one TCP connection; every message is tagged (below) |
| Media loss behaviour | late frame dropped at the transport, other lanes unaffected | retransmitted; **all lanes stall together** |
| Browser floor | Chrome 107 / Edge 98 / Firefox 130 / Safari 26.4 | Chrome 107 / Edge 98 / Firefox 130 / Safari 16.4 |
| Works on UDP-blocked networks | no | yes |

**This mode is degraded, on purpose.** A WebSocket runs over one TCP connection,
so every lane shares one ordered byte stream: a single lost packet stalls video,
audio, input, control and clipboard together until the retransmit lands. That is
precisely the property WebTransport was chosen to eliminate (see "Why WebTransport
(QUIC), not WebSocket" above, which remains the reason it is the *preferred*
carrier). At 1% loss the fallback is visibly worse; at 2% it is unpleasant. It is
not a co-equal transport. The client shows the carrier in the HUD, the server logs
it per session and labels `featherdesk_clients{carrier="websocket"}`, so a degraded
deployment is visible rather than mysterious.

### How the client selects

```javascript
// connection.js — runs after the capability gate (MODULE_WEB_CLIENT step 0).
const STICKY_KEY     = "featherdesk.carrier";
const STICKY_MS      = 600_000;   // 10 min: re-probe QUIC after this
const WT_DEADLINE_MS = 3_000;
const WS_DEADLINE_MS = 5_000;

async function openCarrier(opts) {
    let sticky = null;
    try { sticky = JSON.parse(sessionStorage.getItem(STICKY_KEY) || "null"); } catch {}
    const skipWt = sticky && sticky.carrier === "websocket"
                         && Date.now() - sticky.at < STICKY_MS;

    if (!skipWt && "WebTransport" in window) {
        const wt = new WebTransport(`https://${location.host}/wt`, opts);
        try {
            await withDeadline(wt.ready, WT_DEADLINE_MS);
            try { sessionStorage.removeItem(STICKY_KEY); } catch {}
            return wrapWebTransport(wt);          // carrier = "webtransport"
        } catch (e) {
            try { wt.close(); } catch {}
            // fall through — UDP blocked, QUIC handshake failed, or too slow
        }
    }

    const ws = new WebSocket(`wss://${location.host}/ws`, "featherdesk.v1");
    ws.binaryType = "arraybuffer";
    await withDeadline(onceOpen(ws), WS_DEADLINE_MS);
    try { sessionStorage.setItem(STICKY_KEY,
              JSON.stringify({ carrier: "websocket", at: Date.now() })); } catch {}
    return wrapWebSocket(ws);                     // carrier = "websocket"
}
```

- The 3 s WebTransport deadline is what makes a UDP-blackholing firewall (no ICMP
  reject, packets simply vanish) fall through instead of hanging: the QUIC
  handshake has no failure signal there, only silence.
- The sticky preference lives in `sessionStorage`, so it costs one probe per tab
  per 10 minutes rather than one per reconnect. It is a latency optimisation,
  never a correctness input.
- `serverCertificateHashes` applies to the WebTransport attempt only. The
  WebSocket carrier is ordinary TLS over TCP, so the one-time interstitial the
  user already clicked through for `GET /` covers it — which is *why* the fallback
  works on Safari, whose `serverCertificateHashes` support is incomplete.
- If both attempts fail the client renders a connection-failed notice naming both
  errors. It never silently retries a third way.

### Wire format on the fallback carrier

Every message is a WebSocket **binary** message. A text message, or a message
shorter than 1 byte, is a protocol error: close with `close::PROTOCOL_ERROR (4400)`.

```
Offset  Size  Type     Field       Encoding
------  ----  ------   -----       --------
0       1     uint8    StreamTag   a stream_type::* value (see "Stream Identification")
1       …     bytes    Body        EXACTLY the bytes that lane carries on QUIC
------- Total: 1 + len(Body) -------
```

The tag byte is the same tag the QUIC carrier writes as a stream's first byte;
here it is repeated per message because there is one connection instead of one
stream per lane. **WebSocket message boundaries are not framing** — each lane's own
framing (below) is authoritative, exactly as on QUIC, so the same reader code
serves both carriers.

| Tag | Lane | Body framing (identical to the QUIC carrier) |
|---|---|---|
| `0x00` | Control | newline-delimited JSON; a message may carry one or more whole lines; the reader frames on `\n` |
| `0x01` | Input | `[u16 RecLen LE][record]`, both directions |
| `0x02` | Clipboard | `[u32 Len LE][JSON]`, both directions |
| `0x03` | File transfer | the 18-byte file-transfer header + payload. Concurrent transfers are distinguished by the header's own `TransferID`, which the INITIATOR assigns and carries on every message including `INIT` (odd = client-initiated, even ≥ 2 = host-initiated) — on QUIC the per-transfer stream does it instead. See [`MODULE_FILETRANSFER.md`](../interaction/MODULE_FILETRANSFER.md) "Transfer ID ownership" |
| `0x10` | Bootstrap | `[u32 Len LE][22-byte FrameHeader‖IDR]`, S→C, one message per join. Self-delimiting, so no stream-close marker is needed |
| `0x11` | Cursor | `[u32 Len LE][cursor record]`, S→C. Kind 0x01 shape records plus the single join-time Kind 0x02 position record — the same content as on QUIC. Live cursor positions are CURSOR_UPDATE on the `0x20` lane, not here |
| `0x20` | Datagram | `[8-byte DatagramHeader][payload]` — the datagram lane. S→C only in v1 |

**`0x20` is fallback-only.** A QUIC stream whose first byte is `0x20` is a protocol
error; the datagram lane has no stream on the QUIC carrier. It lives in the same
`stream_type` module so there is exactly one tag space.

**Datagram-only types become reliable messages.** `PING (2)`, `CURSOR_UPDATE (11)`
and `GAMEPAD_RUMBLE (15)` keep their exact 8-byte `DatagramHeader` + payload layout
and travel as `0x20` messages. They are reliable and ordered here instead of
best-effort; that changes nothing in the encoders or decoders, only the loss
profile.

**The sender never fragments on this carrier.** `Session::max_datagram_size()`
returns `None` (see "Datagram Fragmentation"), so the pump emits **one `0x20`
message per access unit** with `FragIndex = 0 | LAST` — the same single-fragment
shape a 20 ms Opus packet already uses today. The receiver's reassembly path is
therefore unchanged, and `fragment_reassembly_ms` never fires because a partial
frame cannot occur. `[transport] ws_max_message_bytes = "16MiB"` bounds an inbound
message; a larger one is `close::PROTOCOL_ERROR (4400)`.

**Close codes.** `close::*` values map 1:1 onto WebSocket close codes: `4400`,
`4401`, `4408`, `4410`, `4429` and `4503` are already in WebSocket's
application-private `4000-4999` range and are sent verbatim; `close::NORMAL (0)`
is sent as WebSocket `1000` (Normal Closure). The mapping is total in both
directions.

**Everything else is unchanged.** Same `Origin` check, same `allow_origin` default,
same 5 s `auth_deadline` armed at connection accept, same `max_clients` (counted
across both carriers and enforced by the server), same `max_message_bytes` cap on
control lines, same `input_rate_limit`.

### Send-side queue policy on the fallback carrier

The QUIC carrier has one ring per media kind (`frame_out` / `audio_out`, whole
access units, drop-oldest) and sends control datagrams straight through
`send_datagram`, because the network drops what is stale. A reliable carrier drops
nothing, so the fallback carrier's per-session out-queue must do it:

| Datagram Type | Policy |
|---|---|
| `VIDEO_H264 (1)` / `VIDEO_HEVC (7)` / `VIDEO_AV1 (16)` | drop-oldest whole access units, cap `[transport] datagram_send_queue_frames` (8) — identical to the QUIC pump |
| `AUDIO_OPUS (8)` / `AUDIO_PCM (4)` | drop-oldest, cap `[transport] audio_send_queue_chunks` (25 ≈ 500 ms). A dropped chunk is a `Sequence` gap the client conceals (see MODULE_WEB_CLIENT "Audio Playback Pipeline") |
| `PING (2)` | **coalesce** — an unsent PING is replaced by the newer one; at most one is ever queued |
| `CURSOR_UPDATE (11)` | **coalesce** — the 14-byte body is position-only, so the newest supersedes every unsent one. Cursor SHAPES are not here: they ride the reliable `0x11` lane and are never coalesced away |
| `GAMEPAD_RUMBLE (15)` | **coalesce per gamepad index** — at most one unsent rumble per index |
| Control / input / clipboard / file-transfer / bootstrap / cursor lanes | never coalesced, never dropped. These lanes are reliable by contract; if they cannot drain, the session is closed rather than silently losing a keystroke |

The queue is bounded in bytes as well as in entries: once
`[transport] ws_send_queue_bytes` (default 8 MiB) is exceeded *after* applying the
policies above, the session is closed with `close::PROTOCOL_ERROR (4400)` and a log
line naming the lane that would not drain. A client that cannot keep up with a
reliable lane is a broken client, not a slow one.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `StreamType` tag dispatch (0x00/0x01/0x02/0x03/0x10/0x11, unknown tag, a `0x20` on a QUIC stream, a duplicate 0x00) each produce the correct handler or a **stream-scope** `close::PROTOCOL_ERROR (4400)` | No |
| Unit | Datagram fragmentation: `[Version][Type][FrameID][FragIndex]` encode/decode round-trip, including the bit-15 LAST-flag convention (no FragCount field) | No |
| Unit | `max_datagram_size()` drives fragmentation: a mocked session reporting 1168 fragments a 40 KB access unit into 36 datagrams of ≤1160 payload bytes; reporting `None` produces exactly one message; a mid-frame shrink surfaces `DatagramTooLarge` and abandons the remaining fragments | No |
| Unit | Reassembly holds at most 2 in-progress buffers per Type — a third `FrameID` evicts the oldest, and a partial frame is never delivered | No |
| Unit | Reassembly deadline (`fragment_reassembly_ms`) drops a stale partial frame and increments the drop metric, without blocking delivery of the next frame | No |
| Unit | Per-session out-queue is a frame-granular `FrameOut` with drop-oldest (cap `datagram_send_queue_frames`) — confirms a full queue never drops a fragment mid-frame | No |
| Unit | Carrier parity: the same control line, input record, clipboard message and access unit produce byte-identical lane bodies on both carriers — only the envelope differs | No |
| Unit | Fallback out-queue policy: `Ping` and `CursorUpdate` coalesce to one queued entry each; `GamepadRumble` coalesces per index; video is drop-oldest at `datagram_send_queue_frames`; the reliable lanes (including the `0x11` cursor lane) are never coalesced or dropped | No |
| Unit | `close::*` ↔ WebSocket close-code mapping is total in both directions, including `NORMAL (0)` ↔ `1000` | No |
| Integration | TCP bootstrap: `GET /` over TCP/TLS returns the SPA and an `Alt-Svc: h3=":<port>"; ma=86400` header; `/cert-hashes` and `/auth` answer identically on both listeners | No |
| Integration | Full WebTransport handshake (TLS 1.3 + HTTP/3 upgrade to `/wt`) against both TLS modes: CA-trusted PEM and self-signed with `serverCertificateHashes` | No |
| Integration | Carrier selection: with UDP blackholed, `wt.ready` does not settle, the 3 s deadline fires, and the client completes auth over `/ws` and receives a bootstrap IDR | No |
| Integration | `reload_tls` swaps the certified key with an established session still streaming: the session is unaffected and the next handshake presents the new chain | No |
| Integration | `auth_deadline` (default 5s): a session that never completes the control-stream auth handshake is closed with `close::AUTH_TIMEOUT (4408)` | No |
| Integration | `keepalive_period < max_idle_timeout` is enforced as a config invariant; a session survives an idle gap shorter than `max_idle_timeout` via QUIC keepalive PINGs | No |
| Integration | `allow_origin = ""` (default) rejects a cross-origin WebTransport upgrade and a cross-origin `/ws` upgrade; `"*"` accepts both | No |
| Integration | A mis-tagged stream (unknown tag, second `0x00`, `0x01` from a viewer) is cancelled at stream scope; the session and its other streams keep running | No |
| Load | `fileStreamBudget` ceiling: a client that opens more file-transfer streams than advertised has the extras flow-controlled by QUIC (the open promise never settles); its 10 s open deadline fires and surfaces `too_many_streams` per file. The session, the control/input/clipboard streams and the in-flight transfers are unaffected — no panic, no stall | No |

---

## Status

📋 **Specced — not yet built.** This module replaces the previous WebSocket
transport entirely. Implementation order: bind the TCP listener and serve `/`,
`/cert-hashes` and `/auth` with `Alt-Svc` → probe `quinn` cert flow against
self-signed local dev → wire `/wt` upgrade → control-stream auth handshake →
datagram fragmentation + reassembly on a mock video stream → integrate with
the existing pipeline → then the WebSocket fallback carrier.
