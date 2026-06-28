# Module Spec: Transport

## Overview

FeatherDesk runs over **HTTP/3 + WebTransport (QUIC)**. There is **no WebSocket
fallback**. The effective browser floor is **Chrome 107+, Edge 98+, Firefox
130+, Safari 18.2+** — WebTransport ships earlier (Chrome 97 / Firefox 114), but
the client also needs WebCodecs, which raises the floor to Chrome 107 / Firefox
130 (see [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) → "Supported browsers"). The
substantial performance + protocol benefits of QUIC justify dropping legacy
fallbacks.

This module defines:

- The **transport stack** the rest of the system runs on (QUIC → WebTransport).
- The **channel model**: which kinds of messages travel as unreliable datagrams,
  and which travel on reliable bidirectional streams.
- The **connection lifecycle**: how clients connect, authenticate, and shut down.
- The **fragmentation rules** for messages that exceed the datagram MTU.

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

**What we give up:** ubiquity (some corporate firewalls block UDP), older
browsers (Safari 16/17, Firefox <130). Both deemed acceptable; the
modern-browsers floor is documented up front. Networks that block UDP/443 are
documented as unsupported environments — the user's network is their problem,
not FeatherDesk's.

---

## Public Interface

```rust
// crate: featherdesk-transport

/// Transport is the QUIC/WebTransport server abstraction. The pipeline owns
/// one Transport instance; it accepts connections, multiplexes streams +
/// datagrams, and surfaces them to the server module.
#[async_trait::async_trait]
pub trait Transport: Send + Sync {
    /// Binds the QUIC listener and serves WebTransport sessions until `cancel`
    /// fires. Blocks (awaits). TLS is mandatory; cert source is per
    /// server.tls config (MODULE_SERVER).
    async fn start(&self, cancel: CancellationToken) -> Result<(), TransportError>;

    /// Yields newly-accepted WebTransport sessions. The server module reads from
    /// the receiver and drives per-session auth + dispatch.
    fn sessions(&mut self) -> tokio::sync::mpsc::Receiver<Box<dyn Session>>;

    // No close(): the listener is torn down by dropping the Transport (RAII) or
    // by cancelling `cancel`. Cleanup is deterministic on Drop.
}

/// Session is one accepted WebTransport session = one client connection.
/// Wraps the underlying `wtransport` session with FeatherDesk semantics.
#[async_trait::async_trait]
pub trait Session: Send + Sync {
    /// A CancellationToken that fires when the session closes.
    fn cancelled(&self) -> CancellationToken;

    /// The client's network address (for logging only).
    fn remote_addr(&self) -> std::net::SocketAddr;

    /// Blocks until the client opens a new bidirectional stream. The FIRST byte
    /// of every such stream is a StreamType tag (stream_type::CONTROL / INPUT /
    /// CLIPBOARD / FILE); the server reads it to dispatch the stream. Streams
    /// are identified by tag, NOT by accept order. See "Stream Identification".
    async fn accept_stream(&self, cancel: CancellationToken) -> Result<Box<dyn Stream>, TransportError>;

    /// Opens a server-initiated bidirectional stream (unused in v1).
    async fn open_stream(&self, cancel: CancellationToken) -> Result<Box<dyn Stream>, TransportError>;

    /// Opens a server-initiated unidirectional stream. Used for the bootstrap
    /// stream: the server writes stream_type::BOOTSTRAP (0x10) then the seed
    /// IDR. See "Connection Lifecycle" and MODULE_PROTOCOL "Fast-Join".
    async fn open_uni_stream(&self, cancel: CancellationToken) -> Result<Box<dyn UniStream>, TransportError>;

    /// Blocks for the next inbound datagram. Returns at most one datagram per
    /// call. Sized up to ~1200 bytes (the QUIC datagram MTU).
    async fn read_datagram(&self, cancel: CancellationToken) -> Result<bytes::Bytes, TransportError>;

    /// Queues ONE datagram for delivery. Fire-and-forget per datagram; delivery
    /// is best-effort. Datagrams larger than the negotiated MTU (typically 1200
    /// bytes) MUST be fragmented by the caller. The caller's send path must be
    /// frame-granular (enqueue whole access units, fragment at send) — never a
    /// small fixed channel of individual fragments. See "Datagram Fragmentation".
    fn send_datagram(&self, payload: bytes::Bytes) -> Result<(), TransportError>;

    /// Gracefully closes the session with an application code. Standard codes
    /// are defined in MODULE_PROTOCOL.
    fn close_with_error(&self, code: u32, reason: &str) -> Result<(), TransportError>;
}

/// Bidirectional QUIC stream. AsyncRead+AsyncWrite; closing is RAII (drop, or
/// AsyncWriteExt::shutdown for a graceful FIN) — there is no explicit Close().
pub trait Stream: tokio::io::AsyncRead + tokio::io::AsyncWrite + Send + Unpin {
    fn stream_id(&self) -> u64;
    /// Abort the read direction with a reset code.
    fn cancel_read(&self, code: u32);
    /// Abort the write direction with a reset code.
    fn cancel_write(&self, code: u32);
}

/// Unidirectional (write-only) QUIC stream. Closing is RAII (drop / shutdown).
pub trait UniStream: tokio::io::AsyncWrite + Send + Unpin {
    fn stream_id(&self) -> u64;
    fn cancel_write(&self, code: u32);
}

/// Config sources the [server] + [server.tls] + [transport] sections.
pub struct Config {
    pub bind: String,      // "0.0.0.0:30084" — UDP listen address
    pub tls: TlsConfig,    // certificate sources (same as MODULE_SERVER)
    pub max_clients: u32,  // max concurrent sessions (default 25)
    // Logging is via the `tracing` crate (replaces the old *slog.Logger field).
}

/// One thiserror-derived error enum for the crate.
#[derive(Debug, thiserror::Error)]
pub enum TransportError {
    #[error("transport closed")] Closed,
    #[error("tls: {0}")] Tls(String),
    #[error("io: {0}")] Io(#[from] std::io::Error),
}

/// Returns a Transport backed by quinn + wtransport.
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
| **Video frames** (H.264 / HEVC) | Datagrams (fragmented) | S → C | Late frame is useless; skip to fresh frame. Loss → client requests keyframe. |
| **Audio chunks** | Datagrams | S → C | Same as video. (Deferred — audio module paused.) |
| **Cursor updates** | Datagrams | S → C | Latest-wins; an old position is uninteresting. |
| **Server Ping** | Datagrams | S → C | Best-effort; missed Pings are harmless. |
| **Gamepad rumble** | Datagrams | S → C | Best-effort; rumble for a button press that's already past is useless. |
| **Control stream** (tag `0x00`) | Reliable bidirectional stream | both | Auth, Config, JSON control messages, keyframe requests. **Newline-delimited JSON**, capped at `max_message_bytes`. No bulk payloads. |
| **Input stream** (tag `0x01`) | Reliable bidirectional stream | both | Binary input records + InputAck, **`[u16 RecLen]`-prefixed** both directions. Reliability is non-negotiable. |
| **Bootstrap stream** (tag `0x10`) | Reliable **unidirectional** stream | S → C | One `[u32 Len][FrameHeader‖IDR]` keyframe to seed a joining/resuming decoder, then close. Guarantees a decodable first frame even though live video is lossy datagrams. |
| **Clipboard stream** (tag `0x02`) | Reliable bidirectional stream | both | `[u32 Len][JSON]` clipboard offers/data, opened on demand. Off the 4 KiB control stream because payloads reach 1 MiB. |
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
  │     ├─ JSON control C → S (keyframe, resize, set_*, pong, stats)
  │     └─ (Stays open for the life of the session)
  ├─ Input stream       (client-opened bidi, tag 0x01; controller role only)
  │     ├─ [u16 RecLen] binary input records C → S
  │     └─ [u16 RecLen] InputAck S → C
  ├─ Bootstrap stream   (server-opened UNI, tag 0x10; once per join/resume)
  │     └─ [u32 Len][FrameHeader‖IDR] then close
  ├─ Clipboard stream   (client-opened bidi, tag 0x02; on demand)
  │     └─ [u32 Len][JSON] both directions
  └─ File-transfer streams (client-opened bidi, tag 0x03; one per transfer)
        18-byte framed file-transfer protocol from MODULE_FILETRANSFER.

Datagrams (separate from streams):
  S → C: video / audio / cursor / ping / gamepad rumble
  C → S: (none in v1 — reserved for future client-side media)
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
// First byte of every stream. Client-opened on bidi streams; server-opened
// on the bootstrap uni stream. Defined in featherdesk-protocol, shared with the client.
pub mod stream_type {
    pub const CONTROL: u8 = 0x00;   // bidi, client-opened, exactly one per session
    pub const INPUT: u8 = 0x01;     // bidi, client-opened, controller role only
    pub const CLIPBOARD: u8 = 0x02; // bidi, client-opened, on demand
    pub const FILE: u8 = 0x03;      // bidi, client-opened, one per transfer
    pub const BOOTSTRAP: u8 = 0x10; // uni,  server-opened, one per join/resume
}
```

The server's accept loop reads the first byte of each accepted bidi stream and
dispatches: `0x00` → control handler, `0x01` → input handler, `0x02` → clipboard
handler, `0x03` → a new file-transfer handler. An unknown tag, a **second**
`0x00`, or an `0x01`/`0x02` from a `view`-role client is a protocol error
(`close::PROTOCOL_ERROR`). The browser client reads the first byte of each incoming
**uni** stream identically and routes `0x10` to its bootstrap reader.

---

## Connection Lifecycle

```
1. Client: GET /                        → embedded HTML/JS client
2. Client: POST /auth (HTTPS, JSON body) → server validates credentials,
                                           returns {"session_token":"…","ttl_sec":…}
3. Client: new WebTransport("https://host:port/wt")
4. WebTransport handshake (TLS 1.3, 1 RTT)
5. Server: AcceptSession → Transport surfaces a new Session to the server module.
   The server arms a 5 s auth deadline on the session HERE (step 5), not when a
   stream arrives — a client that never opens any stream is closed at +5 s.
6. Client: opens a bidirectional stream, writes the StreamType tag byte 0x00
   (control), then the auth line:
       {"type":"auth","token":"<session_token>","role":"control|view|player"}
7. Server: accept loop reads the 0x00 tag → routes to the control handler →
   reads the auth line.
8. Server: validates token + role
     - Valid       → reply {"type":"auth_ok","session":{…}}, then send the
                     {"type":"config",…} line, then open the bootstrap uni
                     stream (tag 0x10) and write the seed IDR.
     - Invalid     → close_with_error(close::AUTH_FAILED, "auth failed")
     - Auth deadline fired (no 0x00 control stream + valid auth within 5 s)
                   → close_with_error(close::AUTH_TIMEOUT, "auth timeout")
9. Server: accepts additional client streams (input 0x01, clipboard 0x02, file
   0x03) as they arrive, dispatching by tag.
10. Steady state: media on datagrams, control + input on streams, all multiplexed
11. Either side closes → close_with_error or graceful close → Transport surfaces
    the close event to the server module → session resources released
```

**Auth carrying (no `Sec-WebSocket-Protocol`, no URL query token).** Browsers
cannot set `Authorization` headers on the WebTransport constructor. The
previous WebSocket-specific `Sec-WebSocket-Protocol: bearer.<token>` trick
doesn't apply. Instead, the **first-frame on the control stream** carries the
auth token. This works identically for browser and native clients.

The HTTPS `POST /auth` endpoint (for credentials → session token exchange) is
unchanged from the previous design — it's plain HTTPS, browser-friendly,
arbitrary headers / bodies allowed. Only the WebTransport carry mechanism
changes.

### Resume

Same idea — the session token IS the resume credential. To resume, the client
opens a new WebTransport session, opens the control stream (tag `0x00`), and
sends:

```json
{"type":"auth","token":"<session_token>","role":"control","resume":true}
```

Server checks the SessionCache; if present + within TTL, it replies
`{"type":"auth_ok",…}`, sends `{"type":"config","resumed":true,…}`, opens a
fresh **bootstrap stream** with the current cached IDR, and continues the live
datagram stream. If not, replies `auth_failed` with code 4401 and the client
falls back to fresh `POST /auth`.

> There is **no `last_video_seq`** in the resume message. The bootstrap stream
> always seeds a decodable keyframe, so a client-supplied last-sequence hint
> buys nothing (see [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) → "Resume Path").

---

## Datagram Fragmentation

QUIC datagrams are capped at the negotiated path MTU — typically **1200 bytes**
of payload after QUIC overhead. Video frames are 5-50 KB. They must be
fragmented at the application layer.

The 8-byte `DatagramHeader` lives in `MODULE_PROTOCOL.md`, but the rules are:

- Each frame gets a monotonic **FrameID** (u32) == the access unit's
  `FrameHeader.Sequence`.
- Each datagram carries `[Version][Type][FrameID u32][FragIndex u16]`. There is
  **no FragCount field** — bit 15 of `FragIndex` is the LAST flag, and the last
  fragment's index N means the frame has N+1 fragments (indices 0..N). Fragment 0
  additionally carries the 22-byte `FrameHeader` at the start of its payload.
- The receiver buffers fragments by `(Type, FrameID)` until complete or a
  reassembly deadline fires (default one frame interval, `fragment_reassembly_ms`).
- On deadline the partial frame is **dropped**, a metric increments, and the
  client requests a keyframe on the control stream.
- The receiver only ever holds the **latest** in-progress frame per Type. A new
  FrameID arriving while an older one is incomplete immediately discards the
  older one.

This gives "late frame = useless = dropped" semantics for free at the transport
boundary, with no retransmit cost.

> **Send-side queue is frame-granular, not fragment-granular (fixes T-1).** A
> 1080p IDR is ~40-50 KB ≈ 40+ datagrams. The server must NOT push individual
> fragments into a small fixed-size channel — a channel cap of, say, 32 would
> drop fragments *mid-frame*, corrupting **every** frame under load instead of
> cleanly dropping whole stale frames. Instead the per-session out-queue holds
> **assembled access units** (cap ~8 frames, drop-oldest); a single pump
> task pulls one frame, fragments it, and calls `send_datagram` per fragment
> back-to-back. Loss is then naturally whole-frame (recovered by the reassembly
> deadline), never mid-frame. This queue is owned by the server module
> ([`MODULE_SERVER.md`](./MODULE_SERVER.md)); `Transport::send_datagram` itself is
> fire-and-forget per datagram.

---

## Server-Side Implementation

Built on **`quinn`** + **`wtransport`**. Both are production-grade for QUIC
(`quinn` is the de-facto Rust QUIC implementation; `wtransport` is the WebTransport
server on top of it — pre-1.0, see the maturity note in the Overview).

Single UDP socket, single TLS certificate (same source as the previous WSS
config), single HTTP/3 server. The `/auth` HTTP endpoint runs on the same
HTTP/3 server. The `/wt` path is the WebTransport upgrade target. The embedded
client (HTML + JS) is served from `/`.

```rust
// crate: featherdesk-transport (src/wt.rs)
use wtransport::{Endpoint, ServerConfig};

// A single endpoint (HTTP/3 over quinn) handles /, /auth, /wt on the same UDP
// port; an h3 layer serves the / and /auth routes, wtransport runs the /wt
// WebTransport upgrade.
let endpoint = Endpoint::server(
    ServerConfig::builder()
        .with_bind_address(cfg.bind.parse()?)
        .with_certificate(cfg.tls.into_rustls()?)   // same PEM source as MODULE_SERVER
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

**No separate TCP listener.** HTTP/3 replaces the previous HTTPS/TCP listener
entirely; everything the client needs lives on the QUIC/UDP port.

---

## Client-Side (Browser)

```js
// Auth
const r = await fetch('/auth', { method: 'POST', body: JSON.stringify({…}) });
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

// Transport
const wt = new WebTransport(`https://${location.host}/wt`, opts);
await wt.ready;

// Control stream first (always). First byte = StreamType tag 0x00.
const control = await wt.createBidirectionalStream();
const ctlWriter = control.writable.getWriter();
const ctlReader = control.readable.getReader();
await ctlWriter.write(new Uint8Array([0x00]));        // StreamControl tag
await ctlWriter.write(jsonEncode({
    type: 'auth', token: session_token, role: 'control'
}));

const authResp = await readJSON(ctlReader);  // expects {"type":"auth_ok",...}
// then a {"type":"config",...} line follows on the same stream.

// Input stream (controller role only). First byte = StreamType tag 0x01.
const input = await wt.createBidirectionalStream();
const inWriter = input.writable.getWriter();
await inWriter.write(new Uint8Array([0x01]));          // StreamInput tag
// … [u16 RecLen]-prefixed binary input records flow on input.writable

// Incoming UNI streams: the server-opened bootstrap stream (tag 0x10) seeds
// the decoder with a reliable keyframe before any datagrams are decoded.
for await (const uni of wt.incomingUnidirectionalStreams) {
    routeUniStream(uni); // reads first byte: 0x10 → bootstrap IDR reader
}

// Datagrams (video, audio, cursor, ping)
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
| `close::AUTH_FAILED` | 4401 | Bad credentials, expired token, **or expired resume token** |
| `close::AUTH_TIMEOUT` | 4408 | Control stream not opened + authed within 5 s |
| `close::CONTROLLER_TAKEOVER` | 4410 | Controller slot seized |
| `close::SERVER_SHUTDOWN` | 4503 | Server going away |

There is no separate "resume expired" code — an expired resume token is just
`close::AUTH_FAILED (4401)`, and the client falls back to a fresh `POST /auth`.

Stream resets reuse the same code space; a stream `cancel_read(code)` with one
of these means "this stream is unusable for the given reason."

---

## Configuration

```toml
[server]
bind         = "0.0.0.0:30084"     # UDP listen address (HTTP/3 + WebTransport)
allow_origin = ""                  # same-origin only (default); set to "*" only for LAN
max_clients  = 25
max_message_bytes = 4096           # max control-stream message
input_rate_limit  = 1000           # per-client input events/sec cap

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
max_streams_bidi         = 16      # cap on concurrent bidi streams per session
max_streams_uni          = 16      # cap on concurrent uni streams (rarely used)
enable_datagrams         = true    # MUST be true; required for video
fragment_reassembly_ms   = 17      # drop deadline at 60 fps; use 34 at 30 fps
datagram_send_queue_frames = 8     # per-session out-queue depth in WHOLE frames
                                   # (drop-oldest). Frame-granular, never fragment-
                                   # granular — see "Datagram Fragmentation".
auth_deadline            = "5s"    # close unauthed sessions (close::AUTH_TIMEOUT 4408)
```

> `max_streams_bidi` must comfortably exceed the steady-state stream count
> (control + input + clipboard + N file transfers). Default 16 allows ~13
> concurrent file transfers alongside the three persistent streams.

---

## Security

- **TLS 1.3 is mandatory** in QUIC; weaker negotiation is not possible.
- **Cert handling** — two modes (see MODULE_SERVER "TLS Configuration" +
  "Browser certificate trust"): config-supplied PEM (CA-trusted) **or** a
  short-lived (≤14-day) auto-rotated self-signed ECDSA P-256 cert reached from
  the browser via `serverCertificateHashes`. Unlike the old WSS path, a
  self-signed cert is **not** trusted by a click-through — the browser requires
  the cert hash, served via `/cert-hashes`. Self-signed private key file is mode
  `0600` (Unix) / ACL-restricted (Windows), verified at startup.
- **Origin header** validated on the HTTP/3 upgrade (`Origin` is sent on
  WebTransport just as on WebSocket). Default `allow_origin = ""` is
  same-origin only; matches the audit fix from pass 2.
- **Auth must complete on the control stream within 5 s** of session
  acceptance. Sessions without successful auth are closed with code 4408. This
  prevents idle unauthenticated sessions from sitting on the connection table.
- **Per-client rate limits** from the audit (4 KiB max control message, 1000
  input events/sec) carry over and apply to the control + input streams.
- **Cross-origin** WebTransport requires the same explicit opt-in CORS-style
  check as WebSocket; nothing changes here.

---

## Why no WebSocket fallback

Considered and rejected:

| Reason | Argument |
|---|---|
| "Some networks block UDP" | True (corporate, some hotel WiFi). Mitigation: those users are documented as unsupported, the same way some networks block ports for other tools. Not worth doubling the implementation surface. |
| "Older browsers" | WebTransport landed in Chrome 97 (Jan 2022) / Edge 98 / Firefox 114 (Jun 2023) / Safari 18.2 (Dec 2024), but the **effective floor is Chrome 107 / Firefox 130** because the client also needs WebCodecs. Anything older is past the modern-browser support floor of the rest of the spec (WebCodecs, MediaStreamTrackProcessor) — already not supported. |
| "Implementation simplicity" | The opposite — running both means two complete server transports, two client transports, two channel models, two auth flows. Single transport is cleaner. |
| "TCP works everywhere" | True for connectivity, not true for latency under loss. The whole point of moving was packet loss; falling back to TCP gives back exactly the property we wanted to eliminate. |

The trade-off is explicit: FeatherDesk targets **modern browsers on networks
that allow UDP/443**, in exchange for substantially better latency under
realistic network conditions.

---

## Status

📋 **Specced — not yet built.** This module replaces the previous WebSocket
transport entirely. Implementation order: probe `quinn` cert flow against
self-signed local dev → wire `/wt` upgrade → control-stream auth handshake →
datagram fragmentation + reassembly on a mock video stream → integrate with
the existing pipeline.
