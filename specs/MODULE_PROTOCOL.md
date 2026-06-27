# Module Spec: Protocol

## Overview

The Protocol module defines the binary wire format for all data exchanged
between server and client over the **WebTransport / QUIC** transport defined in
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md). It handles serialization and
deserialization of frame headers with zero per-frame allocation, plus the
**datagram fragmentation header** required to send video frames (5-50 KB) over
the ~1200-byte QUIC datagram MTU.

The transport module owns "how messages are delivered" (datagrams vs reliable
streams, when to drop late frames, auth handshake). This module owns "what the
bytes look like" — the framing, the type codes, the field layout. The two are
read together.

---

## Public Interface

```go
package protocol

const HeaderSize = 22

// Frame type constants. Each Type appears on exactly one transport channel
// (datagram vs control stream vs input stream); see "Channel Model" below.
const (
    FrameTypeVideoH264    uint8 = 1   // datagram (S→C); fragmented
    FrameTypePing         uint8 = 2   // datagram (S→C); 8-byte nonce
    // 3 reserved (client Pong routed on the control stream as JSON)
    FrameTypeAudioPCM     uint8 = 4   // datagram (S→C); fragmented (deferred)
    // 5 reserved (formerly VideoVP8 — VP8 rejected; never reuse without protocol version bump)
    FrameTypeConfig       uint8 = 6   // control stream (S→C, JSON payload)
    FrameTypeVideoHEVC    uint8 = 7   // datagram (S→C); fragmented
    FrameTypeCursorUpdate uint8 = 11  // datagram (S→C); latest-wins
    FrameTypeClipboard    uint8 = 12  // control stream (S→C, JSON payload; MODULE_CLIPBOARD)
    FrameTypeInputAck     uint8 = 14  // input stream (S→C)
    FrameTypeGamepadRumble uint8 = 15 // datagram (S→C); MODULE_GAMEPAD
    // 0x50 reserved for future webcam redirection (deferred from v1).
    // Do NOT reuse without a protocol version bump.
)

// Application-layer close codes (carried by transport.Session.CloseWithError
// and stream cancellation). See MODULE_TRANSPORT.
const (
    CloseNormal             uint32 = 0
    CloseProtocolError      uint32 = 4400 // malformed message
    CloseAuthFailed         uint32 = 4401 // bad / expired session token
    CloseAuthTimeout        uint32 = 4408 // client didn't auth within 5 s
    CloseControllerTakeover uint32 = 4410 // controller slot seized
    CloseServerShutdown     uint32 = 4503
)
// NOTE: There is no binary KeyframeReq or Resize type.
//   - Keyframe requests arrive as JSON on the control stream: {"type":"keyframe"}
//   - Resolution changes are pushed as a fresh Config frame.
//   - Input events are BINARY (not JSON) — see MODULE_INPUT.md.

// FrameHeader is the fixed-size header prepended to every reliable-stream message and every reassembled datagram frame.
type FrameHeader struct {
    Version     uint8  // Protocol version (currently 1)
    Type        uint8  // Frame type identifier
    Sequence    uint32 // Monotonic frame counter (per-type: video and audio independent)
    Timestamp   uint64 // CLOCK_MONOTONIC nanoseconds
    Width       uint16 // Frame width (or sample rate for audio)
    Height      uint16 // Frame height (or channel count for audio)
    PayloadSize uint32 // Size of payload following this header
}

// MarshalHeader writes h into buf (must be >= HeaderSize bytes).
// Zero allocation. Returns error if buf is too short.
func MarshalHeader(h FrameHeader, buf []byte) error

// UnmarshalHeader reads a FrameHeader from buf.
// Returns errShortBuffer if len(buf) < HeaderSize.
// Returns errUnsupportedVersion if Version != 1.
func UnmarshalHeader(buf []byte) (FrameHeader, error)
```

---

## Wire Format — 22-byte FrameHeader (control + input streams)

Used by every server-to-client frame on the **control stream** and the **input
stream** (Config, Clipboard, InputAck — anything reliable + ordered). Also used
inside datagram payloads (after fragmentation reassembly) so the upstack
decoder sees the same shape regardless of transport channel.

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       1     uint8    Version       Raw byte (currently 1)
1       1     uint8    Type          Raw byte
2       4     uint32   Sequence      Little-endian (monotonic per-type)
6       8     uint64   Timestamp     Little-endian (CLOCK_MONOTONIC ns)
14      2     uint16   Width         Little-endian
16      2     uint16   Height        Little-endian
18      4     uint32   PayloadSize   Little-endian
------- Total: 22 bytes -------

[Header: 22 bytes][Payload: PayloadSize bytes]
```

## Wire Format — 8-byte DatagramHeader (video / audio datagrams)

QUIC datagrams cap at ~1200 bytes after QUIC overhead. Video frames are
5-50 KB so they must be fragmented at the application layer. Each datagram
carries a small fragmentation header so the receiver can reassemble.

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       1     uint8    Version       =1
1       1     uint8    Type          Frame type (FrameTypeVideoH264 / VideoHEVC / AudioPCM / CursorUpdate / Ping / GamepadRumble)
2       4     uint32   FrameID       Little-endian; monotonic per Type
6       2     uint16   FragIndex     Little-endian; 0-based
                                     bit 15 = LAST fragment flag
                                     bits 0-14 = fragment index (max 32767)
------- Total: 8 bytes -------

[DatagramHeader: 8 bytes][Fragment payload: up to ~1192 bytes]
```

**The FIRST fragment** (FragIndex == 0) of a frame carries the **22-byte
FrameHeader at the start of its payload**, then the leading bytes of the
frame's bitstream. Subsequent fragments carry only raw payload bytes.

**The LAST fragment** sets the `LAST` flag (bit 15 of `FragIndex`). Receiver
reassembles when it sees both fragment 0 (with FrameHeader) and the last
fragment, AND has received every fragment in between.

Small frame types (Ping = 8 bytes, CursorUpdate, GamepadRumble) typically fit
in a single datagram — fragment 0 + LAST flag, all in one datagram.

### Datagram reassembly rules

- Receiver maintains one reassembly buffer per `(Type, FrameID)`.
- Reassembly **deadline**: one frame interval (16.6 ms at 60 fps; configured
  via `[transport] fragment_reassembly_ms`). On deadline expiry the partial
  frame is **dropped**, a metric increments, and the client sends a JSON
  `{"type":"keyframe"}` on the control stream.
- A **new FrameID arriving while an older one is in progress** for the same
  Type discards the older buffer immediately (latest-wins).
- Out-of-order fragments are tolerated (datagrams have no delivery guarantee).
- Duplicate fragments are silently ignored.
- A fragment whose total reassembled size would exceed a safety cap
  (`16 MB` by default) is rejected and the frame dropped.

This gives "late frame = useless = dropped" semantics for free, without
retransmit overhead.

### Frame Types

Each Type appears on **exactly one transport channel**. The channel — datagram,
control stream, or input stream — is fixed by the Type and is the only thing
the receiver needs to know to route the message.

| Type | Value | Dir | Channel | Payload | Width/Height |
|------|-------|-----|---------|---------|--------------|
| VideoH264 | 1 | S→C | **datagram** (fragmented) | One access unit, Annex B (keyframe = SPS+PPS+IDR) | Frame dims |
| Ping | 2 | S→C | datagram | 8-byte nonce; client replies with JSON `{"type":"pong","nonce":…}` on the control stream | Unused (0) |
| _(reserved)_ | 3 | — | — | Reserved | — |
| AudioPCM | 4 | S→C | datagram (fragmented) | Raw S16LE PCM (deferred — audio paused) | SampleRate, Channels |
| _(reserved)_ | 5 | — | — | Formerly VideoVP8 — rejected. Do not reuse without version bump. | — |
| Config | 6 | S→C | **control stream** | JSON handshake (codec, dims, fps, hdr, cursorMode, session_token, resumed) | Unused (0) |
| VideoHEVC | 7 | S→C | **datagram** (fragmented) | HEVC access unit, Annex B (keyframe = VPS+SPS+PPS+IDR) | Frame dims |
| CursorUpdate | 11 | S→C | datagram | Cursor position + optional image (latest-wins) | Unused (0) |
| Clipboard | 12 | S→C | control stream | JSON clipboard push (see [`MODULE_CLIPBOARD.md`](./MODULE_CLIPBOARD.md)) | Unused (0) |
| InputAck | 14 | S→C | **input stream** | Client input `seq` (u32 LE) + server-recv timestamp (u64 LE) | Unused (0) |
| GamepadRumble | 15 | S→C | datagram | 9 bytes `[Index u8][WeakMag u16][StrongMag u16][DurationMs u32]` (see [`MODULE_GAMEPAD.md`](./MODULE_GAMEPAD.md)) | Unused (0) |
| _(reserved)_ | 0x50 | — | — | Reserved for future webcam redirection. | — |
| **Input events** | **0x01-0x4F** | **C→S** | **input stream** (6-byte header) | Binary input records (keyboard 0x10-0x1F, mouse 0x20-0x2F, touch 0x30-0x3F, gamepad 0x40-0x4F; see [`MODULE_INPUT.md`](./MODULE_INPUT.md) and [`MODULE_GAMEPAD.md`](./MODULE_GAMEPAD.md)) | n/a |

> **Input record total size = 6-byte shared header + per-type payload.** The
> dispatcher Type→length table in [`MODULE_INPUT.md`](./MODULE_INPUT.md) lists
> exact totals per record (e.g. `KeyEvent` total 9 bytes = 6 header + 3 payload).

> `Resize` is **not** a separate type — a resolution change is a fresh `Config`
> frame. `KeyframeReq` is **not** a binary type — requested as JSON on the
> control stream.

### Channel Model (over WebTransport)

```
SESSION (one per client)
  ├─ DATAGRAMS (unreliable, fire-and-forget)
  │     S → C: VideoH264 / VideoHEVC / AudioPCM (each fragmented)
  │            CursorUpdate / Ping / GamepadRumble
  │     C → S: (none in v1 — reserved for future client-side media)
  │
  ├─ CONTROL STREAM (reliable bidirectional; opened first by client)
  │     S → C frames (22-byte FrameHeader): Config (6), Clipboard (12)
  │     C → S JSON messages: auth, keyframe, pong, stats, resize, set_*,
  │                          clipboard, gamepad_connect/disconnect
  │
  └─ INPUT STREAM (reliable bidirectional; opened by client after auth)
        S → C frames (22-byte FrameHeader): InputAck (14)
        C → S binary records (6-byte header): input events 0x01-0x4F
```

File-transfer streams are additional bidirectional streams (one per transfer)
opened on demand — see [`MODULE_FILETRANSFER.md`](./MODULE_FILETRANSFER.md).

**Why this split:** datagrams have "late = useless = drop" semantics for free;
streams have "must arrive in order" semantics for free. We pick the right
channel per message type. See [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md)
"Channel Model" for the longer rationale.

### Control-stream JSON messages (C → S)

JSON line-delimited (one message per line, `\n` terminator), reliable, ordered.
Cost is negligible at these rates and human-readability aids debugging:

```json
{"type": "keyframe"}                                 // request an IDR after a gap
{"type": "pong", "nonce": 12345}                     // reply to a server Ping
{"type": "stats", "decodeMs": 3.2, "dropped": 0}     // optional client telemetry
{"type": "resize", "width": 1280, "height": 720}     // dynamic resolution change
{"type": "set_bitrate", "kbps": 8000}                // dynamic bitrate (control role only)
{"type": "set_fps", "fps": 30}                       // dynamic frame rate (control role only)
{"type": "set_hdr", "hdr": true}                     // toggle HDR pipeline
{"type": "clipboard", "format": "text/plain", "text": "..."}   // clipboard C→H (MODULE_CLIPBOARD)
```

- **Input events are NOT here** — they are binary (see above).
- `keyframe`/`pong`/`stats`/`resize`/`set_*`/`clipboard` carry no input `seq`.
- `resize`, `set_*`, `clipboard` (C→H) are gated by authorization role
  (see [`MODULE_AUTH.md`](./MODULE_AUTH.md)); the server silently drops them
  from `view` role clients. `clipboard` (C→H) is additionally gated by
  `[clipboard] direction`: dropped when `direction = "host_to_client"` or
  `"disabled"` (see [`MODULE_CLIPBOARD.md`](./MODULE_CLIPBOARD.md)).
- Parameter-change messages flow through the `stream.Params` contract (see
  [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)); the server may emit a
  new `FrameTypeConfig` if the codec or color space changed.

### Resume Path

The session token from a previous connection IS the resume credential. On a
new WebTransport session the client opens the control stream and sends the auth
message with `resume: true`:

```json
{
  "type":"auth",
  "token":"<session_token>",
  "role":"control",
  "resume":true,
  "last_video_seq":12345
}
```

If the server still has the session cached AND the token verifies:

- Server skips the full auth/Config exchange and sends `Config{resumed:true}`.
- Server replays the most recent cached IDR (datagram-fragmented).
- Server resumes live stream from the next encoder frame.

If the token is unknown / expired / fails verification the server closes the
WebTransport session with `CloseAuthFailed (4401)` and the client falls back to
a fresh `POST /auth` + new WebTransport session. See
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) + [`MODULE_AUTH.md`](./MODULE_AUTH.md).

### Sequence Number Semantics (server → client)

- The **server** owns and increments the sequence counters. Video and Audio have **independent** counters (both start at 0).
- Sequence increments by 1 for each message the server *sends* of that type (a frame dropped at the server for a slow client still consumed a sequence number for the stream as a whole — see note).
- **Per-client drop note:** The server may drop a frame for an individual slow client (full send buffer). Because the sequence is stream-global, that client will see a gap and may request a keyframe. This is intentional and bounded by the keyframe-request rate limit (see MODULE_SERVER).
- Client gap rule: `if (seq > lastSeq + 1)` → frames were missed → request a keyframe via the text channel. **Exception:** the first live frame after a cached-IDR fast-join is NOT treated as a gap (see "Fast-Join Reconciliation").
- Sequence wraps at `2^32` (≈ 828 days at 60 fps — acceptable; client handles wrap with modular comparison).

### Fast-Join Reconciliation (IDR cache vs gap detection)

When a client connects, the server sends `Config`, then the **cached IDR** message (which may carry an old sequence number), then the live stream. To avoid a false gap:

- The client sets `lastSeq = <seq of the first frame it receives>` and does NOT run gap detection on the transition from the cached IDR to the first live frame.
- Gap detection begins only from the **second** live frame onward.

### A/V Synchronization & Canonical Clock

- **Canonical clock:** `CLOCK_MONOTONIC`, in **nanoseconds**, sampled once per process at startup as the epoch. EVERY timestamp on the wire (video and audio) MUST come from this clock.
- **Stamp at source:** Video frames are stamped at capture time; audio chunks are stamped at capture time (NOT at the moment the pipeline reads them from the channel — the channel can buffer up to ~640 ms).
- Client sync: `skew = videoTimestamp − audioTimestamp`.
  - `skew > +40ms` (audio ahead): client holds audio in its buffer.
  - `skew < −40ms` (audio behind): client drops the stale audio chunk.
  - 40 ms is the human-perceivable A/V boundary.

### Video Payload Framing (Annex B)

The video payload is the **complete access unit** for one frame, with all NAL units concatenated and Annex B start codes (`00 00 00 01`) retained:
```
[00 00 00 01][NAL 1 ...][00 00 00 01][NAL 2 ...] ...
```
- Keyframe messages contain `SPS, PPS, IDR` in order (self-contained, decodable cold).
- The browser `VideoDecoder` consumes the whole payload as one `EncodedVideoChunk` — it never needs to split NALs, so no length-prefixing is used.
- The server detects keyframes by scanning for an IDR NAL (type 5) to set the `EncodedVideoChunk` key/delta hint via the per-frame message (a dedicated keyframe flag is not on the wire; the client derives it from the bitstream, same as the original design).

---

## Design Properties

- **Zero-allocation:** Marshal writes into caller-owned buffer; no heap escapes
- **Fixed size:** Header is always exactly 22 bytes (enables pre-allocation)
- **Little-endian:** Matches x86/ARM native byte order (no conversion on most hardware)
- **Datagram boundaries are intrinsic; reliable-stream messages carry a length-prefix.** See MODULE_TRANSPORT for the channel framing rules.
- **Versioned:** Byte 0 enables future protocol evolution without out-of-band negotiation
- **Sequenced:** Per-type counters enable server-side drop detection without ack overhead
- **Clock-aligned:** Both video and audio share CLOCK_MONOTONIC for A/V sync

---

## Refactoring Directives

### R-PRO-01: Connection Handshake
Right after auth_ok on the control stream, the server MUST send a `FrameTypeConfig` (type 6, JSON payload) on the control stream, BEFORE any video/audio/IDR datagrams flow:
```json
{
    "version": 1,
    "codec": "avc1.42E01F",
    "width": 1920,
    "height": 1080,
    "fps": 60,
    "hdr": false,
    "color_space": "bt709",
    "audio": true,
    "audioSampleRate": 48000,
    "audioChannels": 2,
    "cursorMode": "separate",
    "session_token": "Yhgz...43chars...AbCd",
    "session_ttl_sec": 3600,
    "resumed": false
}
```
- `codec` is the **full WebCodecs codec string** (e.g., `avc1.42E01F` for H.264 Constrained Baseline L3.1, or `hvc1.2.4.L93.B0` for HEVC Main10 HDR), not a short label — the client passes it straight to `VideoDecoder.configure({codec})`.
- `hdr` and `color_space` advertise the HDR mode (see [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)).
- `cursorMode` is `"separate"` (client renders cursor from `CursorUpdate` messages) or `"embedded"` (cursor is burned into the video frame).
- `session_token` is issued by the server after successful auth (see [`MODULE_AUTH.md`](./MODULE_AUTH.md)); client stores it (in-memory only) for reconnection.
- `session_ttl_sec` is the auth session lifetime (from `[auth] session_ttl_minutes`). The reconnect state cache has a separate, shorter TTL (`[reconnect] cache_ttl_seconds`, default 300s).
- `resumed = true` on Config frames sent in response to a successful resume — client skips full decoder re-init and just waits for the replayed IDR.
- If capabilities change (resolution, codec, cursor mode, HDR), the server sends a **new** Config frame; the client reconfigures its decoder and input scaling.
- Send order on connect: **Config → cached IDR (if any) → live frames.**

### Shared Payload Types (Go)

```go
// ConfigPayload is the JSON body of a FrameTypeConfig (type 6) message.
type ConfigPayload struct {
    Version          int    `json:"version"`
    Codec            string `json:"codec"`            // full WebCodecs string
    Width            int    `json:"width"`
    Height           int    `json:"height"`
    FPS              int    `json:"fps"`
    HDR              bool   `json:"hdr"`
    ColorSpace       string `json:"color_space"`      // "bt709" | "bt2020"
    Audio            bool   `json:"audio"`
    AudioSampleRate  int    `json:"audioSampleRate"`
    AudioChannels    int    `json:"audioChannels"`
    CursorMode       string `json:"cursorMode"`       // "separate" | "embedded"
    SessionToken     string `json:"session_token"`    // for reconnection
    SessionTTLSec    int    `json:"session_ttl_sec"`
    Resumed          bool   `json:"resumed"`          // true on successful resume
}

// CursorUpdate is the payload of a FrameTypeCursorUpdate (type 11) message.
// Binary little-endian: [x:u16][y:u16][visible:u8][imageChanged:u8][w:u16][h:u16][rgba...]
type CursorUpdate struct {
    X, Y          uint16
    Visible       bool
    ImageChanged  bool
    W, H          uint16
    RGBA          []byte // present only when ImageChanged
}
```

These live in `pkg/protocol` alongside `FrameHeader`, shared by server and (conceptually) any native client.

### R-PRO-02: Add Bounds Check to MarshalHeader
`MarshalHeader` should return an error if `buf` is shorter than `HeaderSize`, rather than panicking. This is a low-cost check (single comparison) that prevents crashes from propagating.

### R-PRO-03: Separate Audio Header Semantics
Width/Height overloading for audio is now documented as a known interpretation rule. The `FrameTypeConfig` handshake communicates audio parameters upfront. No structural change needed — the overloading is acceptable given the handshake makes semantics explicit.

### R-PRO-04: Annex B Per-Frame Concatenation (REVISED from round 1)
Serialize each video frame as ONE message containing the **complete access unit** in Annex B (start codes retained), all NALs concatenated. Do NOT length-prefix and do NOT split NALs across messages. Rationale: the browser `VideoDecoder` consumes a whole access unit as a single `EncodedVideoChunk` and never iterates NALs, so length-prefixing adds client-side AVCC `avcC` complexity for zero benefit. This also makes keyframe caching trivially correct (the IDR message already contains SPS+PPS+IDR).

> This reverses the round-1 length-prefix decision. If a non-browser native client is ever added that needs O(1) NAL access, length-prefixing can be offered as an opt-in via a Config capability flag — but the default and only browser-facing format is Annex B.

### R-PRO-05: Canonical Monotonic Clock for All Media Timestamps
Define a single process-wide `CLOCK_MONOTONIC` epoch. Every video frame (from every capture backend) and every audio chunk MUST be stamped from this clock, in nanoseconds, **at capture time**. The pipeline must NOT stamp audio at channel-read time. This is a hard requirement for A/V sync — mixing wall-clock (`UnixMilli`) and monotonic, or ms and ns, silently breaks sync.

### R-PRO-06: Server Owns Sequence Counters
The server is the sole owner of the video and audio sequence counters (independent, per type). They are assigned in `Broadcast`/`BroadcastAudio` at send time. The pipeline must NOT maintain its own frame sequence counter.

### R-PRO-07: Magic Bytes in Handshake (Optional)
The `FrameTypeConfig` message acts as the implicit handshake. The first 2 bytes of the first message are always `0x01 0x06` (Version=1, Type=Config), usable for protocol sniffing.

### R-PRO-08: Extract to `pkg/protocol`
This module is already pure (no dependencies). Move it directly to `pkg/protocol/` as the canonical wire format shared between server and any future native client implementations.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Round-trip marshal/unmarshal for all frame types | No |
| Unit | Short buffer and unsupported version error handling | No |
| Unit | Sequence number overflow/wrap behavior | No |
| Unit | Deterministic encoding (same input → same bytes) | No |
| Unit | Buffer reuse safety | No |
| Unit | Annex B access-unit assembly (SPS+PPS+IDR ordering, start codes) | No |
| Unit | Client→server JSON control parsing (keyframe, pong, stats, resize, set_*, clipboard) | No |
| Unit | Binary input record decode (every Type, truncated/oversized/unknown Type, InputBatch caps) | No |
| Benchmark | Marshal throughput (target: <1ns/op) | No |
| Benchmark | Unmarshal throughput (target: <0.5ns/op) | No |
| Fuzz | Random bytes → UnmarshalHeader (no panics) | No |

---

## Performance Characteristics

| Metric | Current | Target |
|--------|---------|--------|
| Marshal | ~0.55 ns/op | <1 ns/op |
| Unmarshal | ~0.28 ns/op | <0.5 ns/op |
| Allocations | 0 per frame | 0 per frame |
| Header overhead | 22 bytes/frame | Acceptable (0.014% of 1080p frame) |
