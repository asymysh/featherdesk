# Module Spec: Transport

## Overview

FeatherDesk runs over **HTTP/3 + WebTransport (QUIC)**. There is **no WebSocket
fallback**. The architecture targets modern browsers (Chrome 97+, Edge 98+,
Firefox 114+, Safari 18.2+) and the substantial performance + protocol benefits
of QUIC justify dropping legacy fallbacks.

This module defines:

- The **transport stack** the rest of the system runs on (QUIC → WebTransport).
- The **channel model**: which kinds of messages travel as unreliable datagrams,
  and which travel on reliable bidirectional streams.
- The **connection lifecycle**: how clients connect, authenticate, and shut down.
- The **fragmentation rules** for messages that exceed the datagram MTU.

The wire format of the messages themselves (frame headers, input records,
clipboard payloads) lives in [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md). The
transport is the envelope; the protocol is the contents.

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
browsers (Safari 16/17, Firefox <114). Both deemed acceptable; the
modern-browsers floor is documented up front. Networks that block UDP/443 are
documented as unsupported environments — the user's network is their problem,
not FeatherDesk's.

---

## Public Interface

```go
package transport

// Transport is the QUIC/WebTransport server abstraction. The pipeline owns
// one Transport instance; it accepts connections, multiplexes streams +
// datagrams, and surfaces them to the server module.
type Transport interface {
    // Start binds the QUIC listener and serves WebTransport sessions until
    // ctx is cancelled. Blocks. TLS is mandatory; cert source is per
    // server.tls config (MODULE_SERVER).
    Start(ctx context.Context) error

    // SessionsChan yields newly-accepted WebTransport sessions. The server
    // module reads from it and drives per-session auth + dispatch.
    SessionsChan() <-chan Session

    Close() error
}

// Session is one accepted WebTransport session = one client connection.
// Wraps the underlying webtransport-go session with FeatherDesk semantics.
type Session interface {
    // Context returns a context cancelled when the session closes.
    Context() context.Context

    // RemoteAddr returns the client's network address (for logging only).
    RemoteAddr() net.Addr

    // OpenControlStream is called by the server right after Accept; returns
    // the first bidirectional stream the client opened. Auth + control
    // messages flow on this stream. See "Connection Lifecycle" below.
    OpenControlStream() (Stream, error)

    // AcceptStream blocks until the client opens a new bidirectional stream
    // (input, clipboard, file transfer, etc.).
    AcceptStream(ctx context.Context) (Stream, error)

    // OpenStream opens a server-initiated bidirectional stream.
    OpenStream(ctx context.Context) (Stream, error)

    // OpenUniStream opens a server-initiated unidirectional stream (rarely
    // used — most reliable channels are bidirectional).
    OpenUniStream(ctx context.Context) (UniStream, error)

    // ReadDatagram blocks for the next inbound datagram. Returns at most one
    // datagram per call. Sized up to ~1200 bytes (the QUIC datagram MTU).
    ReadDatagram(ctx context.Context) ([]byte, error)

    // SendDatagram queues a datagram for delivery. Returns immediately;
    // delivery is best-effort. Datagrams larger than the negotiated MTU
    // (typically 1200 bytes) MUST be fragmented by the caller — see
    // "Datagram Fragmentation" below.
    SendDatagram(payload []byte) error

    // CloseWithError gracefully closes the session with an application code.
    // Standard codes are defined in MODULE_PROTOCOL.
    CloseWithError(code uint32, reason string) error
}

type Stream interface {
    io.ReadWriteCloser
    StreamID() uint64
    // CancelRead / CancelWrite abort one direction with a reset code.
    CancelRead(code uint32)
    CancelWrite(code uint32)
}

type UniStream interface {
    io.WriteCloser
    StreamID() uint64
    CancelWrite(code uint32)
}

// Config sources the [server] + [server.tls] + [transport] sections.
type Config struct {
    Bind       string         // "0.0.0.0:30084" — UDP listen address
    TLS        TLSConfig      // certificate sources (same as MODULE_SERVER)
    MaxClients int            // max concurrent sessions (default 25)
    Logger     *slog.Logger
}

// New returns a Transport backed by quic-go + webtransport-go.
func New(cfg Config) (Transport, error)
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
| **Control stream** (first bidi) | Reliable bidirectional stream | both | Auth, Config, clipboard, JSON control messages, keyframe requests. |
| **Input stream** | Reliable bidirectional stream | both | Binary input records + InputAck. Reliability is non-negotiable. |
| **Gamepad input** | Reliable stream (same as input) | C → S | Snapshot diff is reliable; rumble (S → C) goes datagram. |
| **File-transfer streams** | Reliable bidirectional streams (one per transfer) | both | Each transfer is independent; one stalling transfer doesn't block others or the media path. |

**Stream layout (per session):**

```
Session opens
  ├─ Control stream     (first bidirectional stream, opened by client)
  │     ├─ Auth handshake
  │     ├─ Config (S → C, JSON)
  │     ├─ JSON control C → S (keyframe, resize, set_*, clipboard, pong, stats)
  │     ├─ Clipboard pushes S → C
  │     └─ (Stays open for the life of the session)
  ├─ Input stream       (second bidirectional stream, opened by client)
  │     ├─ Binary input records C → S
  │     └─ InputAck S → C
  └─ File-transfer streams (opened on demand)
        Per active transfer: one bidirectional stream with the 18-byte framed
        file-transfer protocol from MODULE_FILETRANSFER.

Datagrams (separate from streams):
  S → C: video / audio / cursor / ping / gamepad rumble
  C → S: (none in v1 — reserved for future client-side media)
```

> **No `/files` endpoint anymore.** File transfer was previously a separate
> WebSocket connection precisely to avoid HOL blocking video. Under QUIC, each
> transfer is its own stream within the same session — automatic isolation,
> same auth, same TLS context. Cleaner and one fewer endpoint to operate.

---

## Connection Lifecycle

```
1. Client: GET /                        → embedded HTML/JS client
2. Client: POST /auth (HTTPS, JSON body) → server validates credentials,
                                           returns {"session_token":"…","ttl_sec":…}
3. Client: new WebTransport("https://host:port/wt")
4. WebTransport handshake (TLS 1.3, 1 RTT)
5. Server: AcceptSession → Transport surfaces a new Session to the server module
6. Client: opens first bidirectional stream (the CONTROL stream)
7. Client: writes first frame on control stream:
       {"type":"auth","token":"<session_token>","role":"control|view"}
8. Server: validates token + role
     - Valid       → reply {"type":"auth_ok","session":{…}} + Config frame
     - Invalid     → CloseWithError(4401, "auth failed")
     - Auth timeout → CloseWithError(4408, "auth timeout") after 5 s without auth
9. Server: opens / accepts additional streams (input, file transfer) as needed
10. Steady state: media on datagrams, control + input on streams, all multiplexed
11. Either side closes → CloseWithError or graceful close → Transport surfaces
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
opens a new WebTransport session and sends:

```json
{"type":"auth","token":"<session_token>","role":"control","resume":true,"last_video_seq":12345}
```

Server checks the SessionCache; if present + within TTL, replays cached Config
+ keyframe and continues. If not, replies `auth_failed` with code 4401 and the
client falls back to fresh `POST /auth`.

---

## Datagram Fragmentation

QUIC datagrams are capped at the negotiated path MTU — typically **1200 bytes**
of payload after QUIC overhead. Video frames are 5-50 KB. They must be
fragmented at the application layer.

The fragmentation header lives in `MODULE_PROTOCOL.md`, but the rules are:

- Each frame gets a monotonic **FrameID** (u32).
- Frame fragments are tagged `[FrameID][FragIndex][FragCount]` + a flag for the
  first/last fragment.
- The receiver buffers fragments by FrameID until all are received or a
  reassembly deadline fires (default: one frame interval).
- On deadline (e.g. 16.6 ms at 60 fps) the frame is **dropped**, a metric
  increments, and the client requests a keyframe on the control stream.
- The receiver only ever holds the **latest** in-progress frame. A new FrameID
  arriving while an older one is incomplete immediately discards the older one.

This gives "late frame = useless = dropped" semantics for free at the transport
boundary, with no retransmit cost.

---

## Server-Side Implementation

Built on `github.com/quic-go/quic-go` + `github.com/quic-go/webtransport-go`.
Both are production-ready (`quic-go` is the reference Go QUIC implementation;
`webtransport-go` is its WebTransport extension by the same team).

Single UDP socket, single TLS certificate (same source as the previous WSS
config), single HTTP/3 server. The `/auth` HTTP endpoint runs on the same
HTTP/3 server. The `/wt` path is the WebTransport upgrade target. The embedded
client (HTML + JS) is served from `/`.

```go
// internal/transport/wt/wt.go
import (
    "github.com/quic-go/quic-go/http3"
    "github.com/quic-go/webtransport-go"
)

// Single HTTP/3 server handles /, /auth, /wt on the same UDP port.
h3 := &http3.Server{
    Addr:      cfg.Bind,
    Handler:   mux,           // mux carries /, /auth, /wt routes
    TLSConfig: cfg.TLS.HTTP3Config(),
}
wt := &webtransport.Server{ H3: h3 }
mux.Handle("/wt", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    sess, err := wt.Upgrade(w, r)
    if err != nil { … return }
    t.sessions <- newSession(sess)
}))
```

**No separate TCP listener.** HTTP/3 replaces the previous HTTPS/TCP listener
entirely; everything the client needs lives on the QUIC/UDP port.

---

## Client-Side (Browser)

```js
// Auth
const r = await fetch('/auth', { method: 'POST', body: JSON.stringify({…}) });
const { session_token } = await r.json();

// Transport
const wt = new WebTransport(`https://${location.host}/wt`);
await wt.ready;

// Control stream first (always)
const control = await wt.createBidirectionalStream();
const ctlWriter = control.writable.getWriter();
const ctlReader = control.readable.getReader();
await ctlWriter.write(jsonEncode({
    type: 'auth', token: session_token, role: 'control'
}));

const authResp = await readJSON(ctlReader);  // expects {"type":"auth_ok",...}

// Input stream
const input = await wt.createBidirectionalStream();
// … binary input records flow on input.writable

// Datagrams (video, audio, cursor, ping)
for await (const chunk of wt.datagrams.readable) {
    routeDatagram(chunk); // dispatch by first byte (Type field)
}
```

The client module ([`MODULE_CLIENT.md`](./MODULE_CLIENT.md)) covers the full
client-side wiring including fragmentation reassembly, codec setup, and
adaptive bitrate.

---

## Native Client (v2)

Reserved for v2. A native client uses `quic-go` directly (no HTTP/3 / no
WebTransport — they're browser conveniences) for a leaner stack and sub-ms
input latency. The wire protocol (frame format, channel model) is **identical**
to the browser path; only the transport library differs. See the v2 entry in
[`README.md`](../README.md) → "Future plan" and (future)
`MODULE_NATIVE_CLIENT.md` for the embedded Tailscale `tsnet` + auth-key paste
flow that ships with the native client.

---

## Application-Layer Error Codes

```go
const (
    CloseNormal             uint32 = 0
    CloseAuthFailed         uint32 = 4401  // bad credentials / expired token
    CloseAuthTimeout        uint32 = 4408  // client didn't auth within 5 s
    CloseControllerTakeover uint32 = 4410  // controller slot seized
    CloseProtocolError      uint32 = 4400  // malformed message
    CloseServerShutdown     uint32 = 4503  // server going away
    CloseResumeExpired      uint32 = 4401  // alias of AuthFailed for clarity
)
```

Stream resets reuse the same code space; a stream `CancelRead(code)` with one
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
cert         = ""                  # production: path to PEM cert chain
key          = ""                  # production: path to PEM private key
                                   # empty = self-signed (dev only, browsers warn)
min_version  = "1.3"               # TLS 1.3 mandatory under QUIC; field is informational

[transport]
# Tunables for the QUIC transport. Defaults are good; expose for ops debugging.
keepalive_period         = "15s"   # idle keepalive
max_idle_timeout         = "30s"   # close after this much silence
initial_max_data         = "10MiB" # initial connection-level flow control window
initial_max_stream_data  = "1MiB"  # per-stream flow control
max_streams_bidi         = 16      # cap on concurrent bidi streams per session
max_streams_uni          = 16      # cap on concurrent uni streams (rarely used)
enable_datagrams         = true    # MUST be true; required for video
fragment_reassembly_ms   = 17      # drop deadline at 60 fps; use 34 at 30 fps
```

---

## Security

- **TLS 1.3 is mandatory** in QUIC; weaker negotiation is not possible.
- **Cert handling** identical to the previous WSS path (config-supplied PEM or
  startup-generated self-signed). Self-signed cert private key file is mode
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
| "Older browsers" | Chrome 97 (Jan 2022) onward, Edge 98 (Feb 2022), Firefox 114 (Jun 2023), Safari 18.2 (Dec 2024). Anything older is past the modern-browser support floor of the rest of the spec (WebCodecs, MediaStreamTrackProcessor) — already not supported. |
| "Implementation simplicity" | The opposite — running both means two complete server transports, two client transports, two channel models, two auth flows. Single transport is cleaner. |
| "TCP works everywhere" | True for connectivity, not true for latency under loss. The whole point of moving was packet loss; falling back to TCP gives back exactly the property we wanted to eliminate. |

The trade-off is explicit: FeatherDesk targets **modern browsers on networks
that allow UDP/443**, in exchange for substantially better latency under
realistic network conditions.

---

## Status

📋 **Specced — not yet built.** This module replaces the previous WebSocket
transport entirely. Implementation order: probe `quic-go` cert flow against
self-signed local dev → wire `/wt` upgrade → control-stream auth handshake →
datagram fragmentation + reassembly on a mock video stream → integrate with
the existing pipeline.
