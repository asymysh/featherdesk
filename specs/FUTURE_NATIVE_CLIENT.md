# Future Direction: Native Client

> **Status: future scope.** Not implemented, not actively planned, not in any
> release milestone. This document captures the architecture analysis so that
> today's server work doesn't paint the project into a browser-only corner.

---

## Why this document exists

The current client is a browser running vanilla JS + WebCodecs. That's a great
starting point — zero install, universal, fast iteration. But a real
remote-desktop / streaming product eventually wants a **native client** for
performance, security, and feature reasons (HDR, raw HID input, low-latency
audio, multi-monitor, opaque protocol, etc.).

This file exists to ensure:

1. **The wire protocol stays transport-agnostic.** A future native client must
   be able to speak it over QUIC or raw UDP, not just WebSocket.
2. **The server architecture remains layered.** A future QUIC transport must
   plug in alongside the existing WebSocket transport without rewriting the
   pipeline.
3. **No browser-specific assumptions leak into the protocol or input encoding.**
   A native client must be able to produce byte-identical wire output to a
   browser client for the same input.

---

## Transport analysis

| Transport | Browser? | Latency | NAT | Multiplex | Notes |
|-----------|---------|---------|-----|-----------|-------|
| WebSocket (today) | ✅ | TCP, HOL blocks | trivial | single channel | Code reuse with server |
| Raw TCP | ❌ | TCP, HOL blocks | trivial | single | Marginally lower overhead than WS |
| **QUIC** | ❌ | UDP, no HOL block | trivial (UDP/443) | many streams + datagrams | **Recommended native target** |
| WebTransport | partial | UDP, no HOL block | trivial | many streams + datagrams | QUIC for browsers; uneven support |
| WebRTC DataChannel | ✅ | UDP via DTLS | NAT punching, P2P | data channels | Heavyweight SDP/ICE signaling |
| Pure UDP | ❌ | minimal | nightmare | manual | What Moonlight does — own reliability layer |

### Multiple parallel WebSockets — would NOT help

A common intuition is to put input on its own "high-priority" WebSocket and
video on another. This **does not work** the way you'd hope:

1. **TCP doesn't honor app-level priority.** Each WebSocket is an independent
   TCP connection with its own congestion window, retransmit queue, and RTT.
   The kernel doesn't know your input socket is "more important" — under
   congestion it backs off both. The only OS-level QoS is DSCP bits which the
   public internet ignores.

2. **HOL blocking still exists per-socket.** A dropped TCP segment on the video
   socket blocks only the video socket (good — input doesn't drag with it), but
   you still get retransmit-induced jitter on whichever socket experiences
   loss. Better than single-socket, worse than QUIC streams.

Plus you pay sync overhead (two timelines to reconcile), two reconnect state
machines, two TLS handshakes, and 2× file descriptors per client.

---

## Recommended native transport: QUIC with streams + datagrams

A single QUIC connection carries:

| Channel | Type | Reliability | Use |
|---------|------|------------|-----|
| Stream 0 | unidirectional | reliable, ordered | Video frames (large bursts) |
| Stream 1 | unidirectional | reliable, ordered | Audio chunks (low rate) |
| Stream 2 | bidirectional | reliable, ordered | Control (Config handshake, mode changes, keepalive) |
| Datagrams | unreliable | latest-wins | Input events, cursor position |

Why this wins:

- **One TLS 1.3 handshake** for all channels, with optional 0-RTT resumption
  on reconnect.
- **True parallel streams** — flow-controlled independently, no HOL across
  streams.
- **Datagrams skip reliability entirely** for input. Stale mouse deltas should
  never be retransmitted; the next sample is always more useful than the
  previous one.
- **Connection migration** — QUIC tolerates a client switching from WiFi to
  cellular without dropping the connection.
- **NAT-friendly UDP** — same NAT traversal story as plain UDP, no signaling
  required.

### Server library
`github.com/quic-go/quic-go` — BSD-3, mature, ALPN-aware, supports HTTP/3
datagrams. Add as a sibling transport to the existing WebSocket listener; both
front the same `protocol` encoder.

### Client library
Native client side picks one based on language:
- Rust: `quinn` (Apache 2.0)
- Go: `quic-go` (same as server)
- C/C++: `lsquic`, `msquic` (Microsoft Apache 2.0)

---

## Native client pros vs cons

### Performance wins

| Aspect | Browser today | Native potential | Delta |
|--------|--------------|------------------|-------|
| Decode latency | 5–15ms (WebCodecs → canvas paint) | 1–3ms (direct surface attach) | **~10ms** |
| Input latency | 8–20ms (DOM event → WebSocket) | <1ms (raw HID → UDP datagram) | **~15ms** |
| Frame presentation | rAF jitter (~16ms variance) | tear-on-input + adaptive vsync | smoother |
| Audio latency | ~100ms (Web Audio queue) | <20ms (CoreAudio/WASAPI/ALSA direct) | **~80ms** |
| Multi-stream sync | hard (manual timestamp glue) | trivial (QUIC streams + native sync) | architecture |
| Gamepad / HID | broken (limited Gamepad API) | full HID access via OS native APIs | enables gaming |
| HDR | partial browser support | direct platform HDR surfaces | quality |
| Multi-monitor | hacks | native | UX |
| HEVC HW decode | works in Chrome ≥107 with hardware | direct platform decoder always | universal |
| Protocol opacity | full source visible in DevTools | opaque compiled binary | security |
| Codec flexibility | bound to WebCodecs supported set | direct platform encoder/decoder access | more codecs |

### Costs

| Cost | Detail |
|------|--------|
| Distribution | per-OS builds, code signing (Apple Developer ID + notarization, Windows SmartScreen + EV cert, Linux AppImage + signature) |
| Auto-update | Sparkle (macOS), Squirrel (Windows), custom for Linux AppImage |
| 3× platform maintenance | OS-specific bugs in capture surface, input devices, audio backends |
| User trust | install vs "open URL" |
| UX divergence | each OS has different shortcuts, window chrome, menu conventions |
| Initial engineering cost | GUI framework integration, build pipelines, signing infra |

---

## Architectural implications for current (browser-only) work

These are the rules we follow now so that adding a native client later is a
**transport addition**, not a rewrite:

### 1. Wire protocol stays transport-agnostic

`MODULE_PROTOCOL.md` defines bytes-in / bytes-out only. No WebSocket-specific
framing, no HTTP headers in protocol semantics. The protocol must serialize
identically whether it's wrapped in a WebSocket binary message, a QUIC stream,
a raw TCP socket, or even a Unix socket for local testing.

**Concrete rules:**
- No assumption of message boundaries from the transport — the protocol's own
  header carries the length.
- No reliance on TCP's in-order delivery for the datagram channel (input
  events). Input events must be self-contained and idempotent / latest-wins.
- No HTTP headers, no cookies, no Origin checks at the protocol layer
  (transport handles those).

### 2. Server is layered: `pipeline → protocol encoder → transport`

```
[ pipeline ] ─→ EncodedFrame
                    │
                    ▼
            [ protocol encoder ] ─→ bytes
                    │
                    ▼
           [ transport wrapper ]    ←── this is the swappable layer
            ├── WebSocket           ←── exists today
            ├── QUIC stream         ←── future
            └── raw TCP / Unix      ←── testing
```

Today only WebSocket is implemented. Future: add a QUIC transport alongside.
The protocol encoder doesn't know which transport is calling it.

### 3. Input encoding stays platform-neutral

The protocol's input messages encode neutral keycodes, mouse coordinates in
stream pixel space, and button bitmaps. No browser DOM event names, no
JavaScript key codes, no platform-specific keymaps in the wire format.

A native client encoding events from raw HID must produce byte-identical
output to a browser client for the same logical input (e.g. "Ctrl+C" on a US
QWERTY layout produces the same bytes regardless of source).

### 4. Server advertises capabilities in Config handshake

The Config message tells the client what codecs the server can encode, what
audio formats are available, what input ranges the server supports. A native
client and a browser client should be able to make informed decisions from the
same Config payload.

### 5. No browser-specific assumptions in MODULE_SERVER

`MODULE_SERVER.md` should describe the transport surface in transport-neutral
language. Anywhere it says "WebSocket" today, it should be readable as "the
current transport implementation, which is WebSocket".

---

## Out of scope for current planning

- Choice of native GUI framework (Tauri, Qt, native AppKit/Win32, SDL3, etc.)
- Auto-update infrastructure
- Code signing pipeline
- Distribution channels (App Store, direct download, package managers)
- Native client codec decoder selection (FFmpeg vs platform native vs
  Vulkan Video decode)
- Native client input device discovery (HID, gamepads, controllers)
- Multi-display protocol extensions

These are all future-project concerns and will be specced when the native
client project is opened.

---

## Trigger conditions to revisit

The native client project should be opened when **any** of these become true:

- A real customer asks for gaming-grade input latency (sub-10ms RTT)
- A real customer asks for HDR remote desktop
- A real customer asks for multi-display capture+display sync
- We need protocol opacity for licensing or anti-abuse reasons
- The browser WebCodecs ceiling becomes the binding constraint on overall
  latency (currently the wire + capture + encode are larger contributors)

Until then, the browser client is the right client.
