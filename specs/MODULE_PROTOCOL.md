# Module Spec: Protocol

## Overview

The Protocol module defines the binary wire format for all data exchanged between server and client over WebSocket. It handles serialization and deserialization of frame headers with zero per-frame allocation.

---

## Public Interface

```go
package protocol

const HeaderSize = 22

// Frame type constants.
// Server → client uses these binary frame types with the 22-byte FrameHeader.
// Client → server uses BINARY frames for input (compact 6-byte record header,
// see MODULE_INPUT.md), and TEXT frames for rare JSON control. See "Channel
// Model" below.
const (
    FrameTypeVideoH264    uint8 = 1
    FrameTypePing         uint8 = 2
    // 3 reserved (client Pong routed via JSON text channel)
    FrameTypeAudioPCM     uint8 = 4
    // 5 reserved (formerly VideoVP8 -- VP8 codec rejected; never reuse without protocol version bump)
    FrameTypeConfig       uint8 = 6  // S→C JSON handshake; resent on capability change
    FrameTypeVideoHEVC    uint8 = 7  // S→C HEVC access unit (VPS+SPS+PPS+IDR; NAL types 19-20)
    FrameTypeCursorUpdate uint8 = 11 // S→C cursor position + optional image
    FrameTypeClipboard    uint8 = 12 // S→C clipboard push (JSON payload; see MODULE_CLIPBOARD.md)
    FrameTypeInputAck     uint8 = 14 // S→C echoes client input seq + recv timestamp
    FrameTypeGamepadRumble uint8 = 15 // S→C gamepad rumble (controller index + magnitudes + duration; MODULE_GAMEPAD.md)
    // 0x50 reserved for future webcam redirection (deferred from v1).
    // Do NOT reuse without a protocol version bump — when webcam returns it
    // MUST reclaim 0x50 to keep wire compatibility consistent.
)

// Custom WebSocket close codes (RFC 6455 allows 4000-4999 for private use)
const (
    CloseResumeExpired     = 4401 // session token unknown or expired; client must re-auth
    CloseControllerTakeover = 4410 // controller slot seized by another authenticated user
)
// NOTE: There is no binary KeyframeReq or Resize type.
//   - Keyframe requests arrive as JSON text: {"type":"keyframe"}
//   - Resolution changes are pushed as a fresh Config frame.
//   - Input events are BINARY (not JSON) — see MODULE_INPUT.md.

// FrameHeader is the fixed-size header prepended to every WebSocket binary message.
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

## Wire Format (Binary Layout)

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       1     uint8    Version       Raw byte (currently 1)
1       1     uint8    Type          Raw byte
2       4     uint32   Sequence      Little-endian (monotonic frame counter per-type)
6       8     uint64   Timestamp     Little-endian (CLOCK_MONOTONIC nanoseconds)
14      2     uint16   Width         Little-endian
16      2     uint16   Height        Little-endian
18      4     uint32   PayloadSize   Little-endian
------- Total: 22 bytes -------

[Header: 22 bytes][Payload: PayloadSize bytes]
```

### Frame Types

**Server → client** frames use the 22-byte `FrameHeader` below.
**Client → server** uses two channels distinguished by the WebSocket opcode:
**binary** frames (input) and **text** frames (JSON control). See "Channel
Model" below.

| Type | Value | Dir | Header | Payload Content | Width/Height |
|------|-------|-----|--------|-----------------|--------------|
| VideoH264 | 1 | S→C | 22-byte | One access unit, all NALs concatenated Annex B (keyframe = SPS+PPS+IDR) | Frame dims |
| Ping | 2 | S→C | 22-byte | 8-byte nonce (client pongs over text channel) | Unused (0) |
| _(reserved)_ | 3 | — | — | Reserved — client Pong over text channel | — |
| AudioPCM | 4 | S→C | 22-byte | Raw S16LE interleaved PCM (deferred — audio paused) | SampleRate, Channels |
| _(reserved)_ | 5 | — | — | Formerly VideoVP8 — rejected. Do not reuse without version bump. | — |
| Config | 6 | S→C | 22-byte | JSON handshake (codec, dims, fps, hdr, cursorMode, session_token) | Unused (0) |
| VideoHEVC | 7 | S→C | 22-byte | HEVC access unit, Annex B (keyframe = VPS+SPS+PPS+IDR) | Frame dims |
| CursorUpdate | 11 | S→C | 22-byte | Cursor position + optional image | Unused (0) |
| Clipboard | 12 | S→C | 22-byte | JSON clipboard push (see [`MODULE_CLIPBOARD.md`](./MODULE_CLIPBOARD.md)) | Unused (0) |
| InputAck | 14 | S→C | 22-byte | Client input `seq` (u32 LE) + server-recv timestamp (u64 LE) | Unused (0) |
| GamepadRumble | 15 | S→C | 22-byte | 9-byte payload `[Index u8][WeakMag u16][StrongMag u16][DurationMs u32]` (see [`MODULE_GAMEPAD.md`](./MODULE_GAMEPAD.md)) | Unused (0) |
| _(reserved)_ | 0x50 | — | — | Reserved for future webcam redirection. Do not reuse without protocol version bump. | — |
| **Input events** | **0x01-0x4F** | **C→S** | 6-byte | Binary input records (keyboard 0x10-0x1F, mouse 0x20-0x2F, touch 0x30-0x3F, gamepad 0x40-0x4F; see [`MODULE_INPUT.md`](./MODULE_INPUT.md) and [`MODULE_GAMEPAD.md`](./MODULE_GAMEPAD.md)) | n/a |

> **Input record total size = 6-byte shared header + per-type payload.** The
> dispatcher Type→length table in [`MODULE_INPUT.md`](./MODULE_INPUT.md) lists
> exact totals per record (e.g. `KeyEvent` total 9 bytes = 6 header + 3 payload).

> `Resize` is **not** a separate type — a resolution change is a fresh `Config`
> frame. `KeyframeReq` is **not** a binary type — requested via JSON text.

### Channel Model

```
SERVER → CLIENT : binary frames only (22-byte FrameHeader + payload)

CLIENT → SERVER : discriminated by WebSocket opcode
  ├─ BINARY opcode:
  │     byte[1] in 0x01..0x4F   → input record (6-byte header, MODULE_INPUT)
  │     (0x50 reserved for future webcam — current binaries reject)
  └─ TEXT opcode (JSON):           rare, human-triggered control
```

**Why input is binary, not JSON:** measured ~121× faster decode, zero-alloc,
~70-79% smaller, and a far smaller attack surface. Industry-unanimous
(VNC/RDP/Moonlight/Parsec/Steam). Full rationale + record formats in
[`MODULE_INPUT.md`](./MODULE_INPUT.md). This replaces the previous
"client→server is JSON-only" rule.

### Client → Server Control Channel (JSON text)

Only **rare, human-triggered** messages use JSON text (their cost is negligible
and human-readability aids debugging):

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

### Resume Path (client → server, before WebSocket upgrade)

The client carries the session token in the `Sec-WebSocket-Protocol` subprotocol header as `bearer.<session_token>` and optionally appends `?last_video_seq=<N>` to the URL (a non-credential hint) to attempt resumption. If the server still has the session cached AND the token verifies:

- Server skips the auth handshake.
- Server sends `Config{resumed: true}` immediately.
- Server replays the most recent cached IDR (binary frame).
- Server resumes live stream from the next encoder frame.

If the token is unknown / expired / fails verification the server closes the WebSocket with a 4401 close code and the client falls back to a fresh authenticated handshake. See [`MODULE_AUTH.md`](./MODULE_AUTH.md) + [`MODULE_SERVER.md`](./MODULE_SERVER.md).

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
- **No framing needed:** WebSocket messages already have length-delimited boundaries
- **Versioned:** Byte 0 enables future protocol evolution without out-of-band negotiation
- **Sequenced:** Per-type counters enable server-side drop detection without ack overhead
- **Clock-aligned:** Both video and audio share CLOCK_MONOTONIC for A/V sync

---

## Refactoring Directives

### R-PRO-01: Connection Handshake
On WebSocket connect, the server MUST send a `FrameTypeConfig` (binary frame type 6, JSON payload) as the first message, BEFORE any video/audio/IDR frame:
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
