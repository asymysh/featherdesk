# Module Spec: Protocol

## Overview

The Protocol module defines the binary wire format for all data exchanged
between server and client over the **WebTransport / QUIC** transport — and over
its WebSocket fallback carrier — defined in
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md). It handles serialization and
deserialization of frame headers with zero per-frame allocation, plus the
**datagram fragmentation header** required to send video frames (5-50 KB) over
the QUIC datagram limit the transport reports at runtime
(`Session::max_datagram_size()`).

The transport module owns "how messages are delivered" (datagrams vs reliable
streams, which carrier a session runs on, when to drop late frames, auth
handshake). This module owns "what the bytes look like" — the framing, the type
codes, the field layout. The two are read together.

---

## Public Interface

```rust
// crate: featherdesk-protocol

pub const HEADER_SIZE: usize = 22;

// FrameHeader Type constants. The 22-byte FrameHeader is carried only by the
// MEDIA datagram types (video + audio) and on the bootstrap stream. The control
// datagram types (Ping/CursorUpdate/GamepadRumble) and the control/input/
// clipboard/cursor streams do NOT use FrameHeader (see "Channel Model" + "media
// vs control" below).
pub mod frame_type {
    pub const VIDEO_H264: u8 = 1;     // datagram media (S→C); fragmented + bootstrap-stream join IDR
    pub const PING: u8 = 2;           // datagram control (S→C); 4-byte u32 LE nonce, no FrameHeader
    // 3 reserved (client Pong routed on the control stream as JSON)
    pub const AUDIO_PCM: u8 = 4;      // datagram media (S→C); fragmented; raw S16LE (PCM fallback codec)
    // 5 reserved (formerly VideoVP8 — VP8 rejected)
    // 6 retired (was Config; Config is now a control-stream JSON message)
    pub const VIDEO_HEVC: u8 = 7;     // datagram media (S→C); fragmented + bootstrap-stream join IDR
    pub const AUDIO_OPUS: u8 = 8;     // datagram media (S→C); single-datagram Opus packet (default audio codec)
    pub const AUDIO_MIC: u8 = 9;      // datagram media (C→S) — the ONLY client→server datagram in v1.
                                      // One mic packet in the session's negotiated audio codec (Opus,
                                      // or S16LE PCM when no codec add-on is loaded). Carries the
                                      // FrameHeader with the CLIENT's capture timestamp. Controller-only;
                                      // dropped for any other role. See MODULE_AUDIO "Microphone".
    pub const CURSOR_UPDATE: u8 = 11; // datagram control (S→C); 14-byte fixed body, latest-wins,
                                      // no FrameHeader; shapes ride the cursor stream
    // 12 retired (was Clipboard; clipboard is now a clipboard-stream message)
    pub const INPUT_ACK: u8 = 14;     // input stream (S→C), length-prefixed
    pub const GAMEPAD_RUMBLE: u8 = 15;// datagram control (S→C); MODULE_GAMEPAD, no FrameHeader
    pub const VIDEO_AV1: u8 = 16;     // datagram media (S→C); fragmented + bootstrap-stream join.
                                      // Payload is a raw low-overhead OBU temporal unit, NOT Annex B
                                      // (see MODULE_HARDWARE_ENCODE.md). Reserved for the `av1` HW
                                      // add-on tier; no add-on implements it yet (impl deferred).
    // 0x50 reserved for future webcam redirection (deferred from v1).
    //
    // The frame_type space is APPEND-ONLY: a value that has been allocated,
    // retired, or reserved is NEVER reused, and a reallocation would require a
    // protocol version bump. Retired: 6, 12. Reserved but never allocated: 3, 5,
    // 0x50. Every other value named above is LIVE — 16 (VIDEO_AV1) is an
    // allocated constant, not a retired one; only its add-on tier is deferred.
}

// Application-layer close codes (carried by transport `Session::close_with_error`
// and stream cancellation). featherdesk-protocol is the SOLE owner of these; the
// transport crate references these rather than redefining them.
pub mod close {
    pub const NORMAL: u32 = 0;
    pub const PROTOCOL_ERROR: u32 = 4400;      // malformed control-stream message (session-scope); bad
                                               // StreamType tag or malformed lane framing (stream-scope)
    pub const AUTH_FAILED: u32 = 4401;         // bad / expired session token (also: resume token expired)
    pub const AUTH_TIMEOUT: u32 = 4408;        // client didn't open + auth the control stream within 5 s
    pub const CONTROLLER_TAKEOVER: u32 = 4410; // controller slot seized
    pub const SERVER_FULL: u32 = 4429;         // no capacity: max_clients, or the accept queue is full —
                                               // retry later, NOT a credential failure
    pub const SERVER_SHUTDOWN: u32 = 4503;
}
// The close-code space is APPEND-ONLY: a retired code is never reused. On the
// WebSocket fallback carrier these values are the WebSocket close code verbatim
// (4400/4401/4408/4410/4429/4503 already sit in the application-private
// 4000-4999 range); `NORMAL (0)` is sent as WebSocket 1000 (Normal Closure).

/// Authorization role. Owned by featherdesk-protocol so auth, server, transport and
/// the client all reference ONE definition. Ordered by privilege: every privilege of
/// a lower variant is also held by a higher one, which is what makes the ceiling
/// clamp in MODULE_AUTH "Effective role" a total function.
///
/// The ordering is a statement about ROLES, not about instances. Where a gate turns
/// on which gamepad slot a session OWNS (MODULE_SERVER "Role gate table" rows 4 and
/// 17), that is instance state, not a privilege a lower role holds and a higher one
/// does not — no row may ever grant `Player` an operation `Control` is refused.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Debug, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    View = 0,    // receives video/audio/cursor; sends keyframe/pong/stats/decode_unsupported only
    Player = 1,  // View + gamepad records for the one slot it owns (co-op)
    Control = 2, // Player + keyboard/mouse/touch, clipboard, file transfer, param changes,
                 // and gamepad records for every slot no player owns
}

impl Role {
    /// Wire spelling.
    pub fn as_str(self) -> &'static str {
        match self { Role::View => "view", Role::Player => "player", Role::Control => "control" }
    }
    /// The role an auth message with NO `role` field requests — the documented
    /// "auto" behaviour. Arbitration then demotes it if the controller slot is
    /// taken. A role string that is PRESENT but unrecognised is a malformed auth
    /// message → close::PROTOCOL_ERROR.
    pub fn auto() -> Role { Role::Control }
}
// NOTE: There is no binary KeyframeReq, Resize, or Config type.
//   - Keyframe requests arrive as JSON on the control stream: {"type":"keyframe"}
//   - Resolution changes are pushed as a fresh control-stream {"type":"config"}.
//   - Input events are BINARY (length-prefixed) on the input stream — see MODULE_INPUT.md.

// FrameHeader is the 22-byte header prepended to every MEDIA access unit
// (inside datagram fragment 0, and inside each bootstrap-stream message).
#[repr(C)]
pub struct FrameHeader {
    pub version: u8,       // Protocol version (currently 1)
    pub kind: u8,          // Frame type identifier (a `frame_type::*` value)
    pub sequence: u32,     // Monotonic frame counter (per-type: video and audio independent)
    pub timestamp_ns: u64, // CLOCK_MONOTONIC nanoseconds
    pub width: u16,        // Video frame width. UNUSED for audio (=0) — audio rate/
                           // channels/codec are advertised in the `config` message.
    pub height: u16,       // Video frame height. UNUSED for audio (=0).
    pub payload_size: u32, // Size of payload following this header
}

impl FrameHeader {
    /// Serialize into `buf` (must be >= HEADER_SIZE). Zero allocation.
    /// Err if `buf` is too short.
    pub fn write_to(&self, buf: &mut [u8]) -> Result<(), ProtocolError> { /* … */ }

    /// Parse a FrameHeader from `buf`. Err(ShortBuffer) if len < HEADER_SIZE;
    /// Err(UnsupportedVersion) if version != 1.
    pub fn read_from(buf: &[u8]) -> Result<FrameHeader, ProtocolError> { /* … */ }
}
```

---

## Wire Format — 22-byte FrameHeader (media access units only)

Used **only on the media channels**: at the start of datagram fragment 0 (so the
upstack decoder sees the same shape after reassembly) and at the start of each
bootstrap-stream message. It is NOT used on the control, input, clipboard, or
cursor streams — those carry their own compact framing (JSON lines,
length-prefixed records). `Config`, `Clipboard`, and `InputAck` are **not** FrameHeader frames.

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

QUIC datagrams cap at whatever the current path MTU leaves after QUIC overhead;
the sender reads the live figure from `Session::max_datagram_size()` (see
[`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) → "Datagram Fragmentation").
Video frames are 5-50 KB so they must be fragmented at the application layer.
Each datagram carries a small fragmentation header so the receiver can
reassemble.

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       1     uint8    Version       =1
1       1     uint8    Type          Frame type (VideoH264 / VideoHEVC / VideoAV1 / AudioOpus / AudioPCM / CursorUpdate / Ping / GamepadRumble)
2       4     uint32   FrameID       Little-endian; on media types == the FrameHeader.Sequence of this
                                     access unit, on control types a per-Type server counter (below)
6       2     uint16   FragIndex     Little-endian; bit 15 = LAST flag; bits 0-14 = fragment index (0-based, max 32767)
------- Total: 8 bytes -------

[DatagramHeader: 8 bytes][Fragment payload: up to max_datagram_size() − 8 bytes]
```

**`Ping (2)` payload.**

```
Offset  Size  Type     Field   Encoding
------  ----  ------   -----   --------
0       4     uint32   Nonce   Little-endian
------- Total: 4 bytes -------

[DatagramHeader: 8 bytes][Nonce: 4 bytes]   = 12 bytes on the wire
```

**Why `u32`.** The nonce is the one field on the branch that crosses from binary
into JSON, and it crosses into JavaScript: a `Number` is an IEEE-754 double with
2^53 of exact integer range, and `JSON.stringify` throws outright on a `BigInt`.
A `u32` maxes out at 4 294 967 295, so it round-trips verbatim through
`dv.getUint32(8, true)` → `JSON.stringify` → `serde_json` with no conversion
step, no encoding rule, and nothing to get wrong. There is deliberately no
padding: the payload is exactly 4 bytes.

**Nonce generation and matching.** The server draws nonces from a per-session
`u32` counter seeded from the CSPRNG at session accept and incremented once per
ping — never a fresh random draw, so a value cannot collide with a live entry in
its own window. It keeps a ring of the **8** most recent outstanding pings as
`(nonce: u32, sent_at: Instant)`; entries older than `4 × [transport]
ping_interval` are evicted. On `{"type":"pong","nonce":N}` the server looks up
`N`: a hit records `rtt = now − sent_at` and removes the entry; a miss, or a
`pong` with no `nonce` at all, is **ignored and counted — never a protocol
error**, because a lost or reordered ping datagram is normal and must not close a
session.

## Wire Format — 14-byte CursorUpdate (control datagram, type 11)

The payload of a CURSOR_UPDATE datagram, after the 8-byte DatagramHeader. Fixed
size, so the "control datagrams are never fragmented" rule holds by construction:
there is no variable-length field. The cursor bitmap is NOT here — see "Wire
Format — cursor stream records".

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       2     uint16   X             Little-endian; hotspot X in stream pixels
2       2     uint16   Y             Little-endian; hotspot Y in stream pixels
4       2     uint16   StreamW       Little-endian; stream width X/Y were computed against
6       2     uint16   StreamH       Little-endian; stream height X/Y were computed against
8       4     uint32   ShapeID       Little-endian; 0 = no shape published yet
12      1     uint8    Visible       0 = hidden, 1 = visible
13      1     uint8    Reserved      MUST be 0 on send; receivers MUST ignore it
------- Total: 14 bytes -------

[DatagramHeader: 8 bytes][CursorUpdate: 14 bytes]   = 22 bytes on the wire
```

`DatagramHeader.FrameID` carries the **cursor sequence**: the per-Type `u32` the
server increments once for every CursorUpdate it broadcasts (see "`FrameID`
semantics differ by class"). Datagrams reorder, so
latest-wins needs an order: a receiver DISCARDS a CursorUpdate whose FrameID is
not newer than the last one it accepted (`is_newer()`, the same modular
comparison used for `FrameHeader.Sequence`). `FragIndex` is always `0 | LAST`.

`StreamW`/`StreamH` make the coordinate space self-describing, so a CursorUpdate
that was in flight across a resolution change still lands in the right place: the
client scales by `StreamW`/`StreamH` from the datagram, never by the dimensions of
the latest `config`.

## Wire Format — cursor stream records (`stream_type::CURSOR`, 0x11)

Framing: `[u32 Len LE][record]`, `Len` = the record's byte length. Two kinds; the
first two bytes of every record are `Version` and `Kind`, so the stream is
self-describing and a future kind does not renumber anything.

**Kind 0x01 — Shape.** Sent when a shape the session has not yet received becomes
active. Reliable, so a shape is never half-delivered.

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       1     uint8    Version       =1
1       1     uint8    Kind          =0x01 (Shape)
2       4     uint32   ShapeID       Little-endian; content hash (MODULE_CAPTURE "Shape identity")
6       2     uint16   PixelW        Little-endian; bitmap width  in bitmap pixels,
                                     1..=128 — a **sender** obligation, not only a receiver check
8       2     uint16   PixelH        Little-endian; bitmap height in bitmap pixels,
                                     1..=128 — a **sender** obligation, not only a receiver check
10      2     uint16   DrawW         Little-endian; width  to draw at, in stream pixels, >=1
12      2     uint16   DrawH         Little-endian; height to draw at, in stream pixels, >=1
14      2     uint16   HotspotX      Little-endian; hotspot X in stream pixels, 0 <= HotspotX < DrawW
16      2     uint16   HotspotY      Little-endian; hotspot Y in stream pixels, 0 <= HotspotY < DrawH
18      4     uint32   PixelBytes    Little-endian; MUST equal PixelW * PixelH * 4
22      N     uint8[]  Pixels        Straight (non-premultiplied) R,G,B,A; top-down; stride = PixelW*4
------- Total: 22 + PixelBytes bytes; maximum 22 + 128*128*4 = 65558 -------
```

That bound is what closes this record's maximum at `22 + 128*128*4 = 65558`
bytes, and it has exactly ONE producer-side enforcer: the host's cursor
publisher, in `CursorPublisher::tick`. No path may serialize a Kind 0x01 record
outside the range, and capture add-ons return the OS's native bitmap at its
native size and never resample. `DrawW`/`DrawH`/`HotspotX`/`HotspotY` are
stream-pixel on-screen quantities and are NOT bounded by the bitmap cap.

**Kind 0x02 — Position.** The 14-byte CursorUpdate body, byte-identical to the
datagram payload, delivered reliably. Sent exactly once per session — at join,
after the Shape record — and once more when a session resumes, because the
client's overlay state does not survive a reconnect. This is true on BOTH
carriers: on the WebSocket fallback carrier live positions still travel as
CURSOR_UPDATE, as a `0x20`-tagged message (`[0x20][8-byte DatagramHeader][14-byte
body]`, 23 bytes), exactly as PING and GAMEPAD_RUMBLE do. There is no second
cursor-position encoding.

```
Offset  Size  Type     Field         Encoding
------  ----  ------   -----         --------
0       1     uint8    Version       =1
1       1     uint8    Kind          =0x02 (Position)
2       14    uint8[]  CursorUpdate  The 14-byte body above, unchanged
------- Total: 16 bytes -------
```

**Receiver rules.**

- `Len != 22 + PixelBytes` (checked first, before any field past offset 22 is
  read), `Len > 65558`, `Version != 1`, `PixelBytes != PixelW * PixelH * 4`,
  `PixelW` or `PixelH` outside `1..=128`, `HotspotX >= DrawW`, or
  `HotspotY >= DrawH` → cancel the cursor stream with `close::PROTOCOL_ERROR`
  (4400) — `cancel_read` / `cancel_write` on the `stream_type::CURSOR` (0x11)
  stream ONLY. The session survives, with its video, audio, input, clipboard and
  file-transfer streams, and the client keeps the last shape it holds.
- On a Kind 0x02 record, `Len != 16` → cancel the cursor stream with
  `close::PROTOCOL_ERROR` (4400); the session survives.
- An unknown `Kind` is skipped using `Len` (forward compatibility), not an error.
- A Shape record **supersedes** any previously received record with the same
  ShapeID (the server re-emits the active shape with recomputed `DrawW`/`DrawH`
  after a resolution change).
- A CursorUpdate whose `ShapeID` the client does not hold keeps the last shape it
  does hold on screen; it never blanks the pointer waiting for a record.
  `ShapeID = 0` means no shape has ever been published — the overlay is hidden.
- The cursor stream is long-lived. The receiver MUST read it continuously so QUIC
  flow-control credit keeps being extended; a client that stops reading it stalls
  only its own cursor.

**`FrameID` semantics differ by class.** On **media** types (`VideoH264`,
`VideoHEVC`, `VideoAV1`, `AudioPCM`, `AudioOpus`) `DatagramHeader.FrameID` carries
the same value as the `FrameHeader.Sequence` of the access unit it belongs to,
duplicated into the compact datagram header so the receiver can group fragments
without first reassembling and parsing the 22-byte header. Video and audio
counters are independent.

On **control** types (`Ping`, `CursorUpdate`, `GamepadRumble`) there is no
`FrameHeader`, so `FrameID` is instead a **per-Type monotonic `u32` counter owned
by the server**, incremented once per message of that Type and starting at 0. It
is used only to answer "is this newer than what I have" — via the same
`is_newer()` in "Datagram reassembly rules" — for latest-wins delivery, duplicate
suppression, and the fallback carrier's send-side coalescing. It is never
gap-checked and never triggers a keyframe request.

There are **two classes** of datagram Type, split by whether a 22-byte
FrameHeader is present — **media** (needs a wire Timestamp + Sequence) vs
**control** (self-describing). Note this is independent of fragmentation: a media
frame may be fragmented (video, PCM audio) OR fit in one datagram (Opus audio),
but it ALWAYS carries the FrameHeader.

- **Media** (carry the 22-byte FrameHeader) — `VideoH264 (1)`, `VideoHEVC (7)`,
  `VideoAV1 (16)`, `AudioPCM (4)`, `AudioOpus (8)`. **Fragment 0** (FragIndex == 0) starts its
  payload with the FrameHeader, then the leading access-unit bytes; subsequent
  fragments carry only raw access-unit bytes. `FrameHeader.PayloadSize` is the
  size of the **complete access unit** (the bytes after the 22-byte header), NOT
  this fragment. Video and 20 ms PCM audio fragment into several datagrams; a
  20 ms **Opus** packet (~100–300 B) fits one datagram (`FragIndex = 0 | LAST`)
  but still carries the FrameHeader (for its capture Timestamp + audio Sequence).
- **Control** (no FrameHeader) — `Ping (2)`, `CursorUpdate (11)`, `GamepadRumble
  (15)`. The datagram is `[8-byte DatagramHeader][raw payload]`, always one
  datagram (`FragIndex = 0 | LAST`). A Ping is the 8-byte header + a 4-byte `u32`
  LE nonce (12 B total); a CursorUpdate is the header + a 14-byte fixed body
  (22 B); GamepadRumble is the header + a 9-byte payload. Their payloads are
  self-describing and fixed-size (a CursorUpdate is exactly 14 bytes and carries
  the dimensions its coordinates were computed against), so a FrameHeader would be
  redundant and fragmentation can never be required. Their `FrameID` is a per-Type
  server counter rather than a media Sequence (see "`FrameID` semantics differ by
  class").

**The LAST fragment** sets the `LAST` flag (bit 15 of `FragIndex`). Its index
value N (bits 0-14) means the frame has **N+1 fragments, indices 0..N** — no
separate fragment-count field is needed because the last fragment's index
encodes the count.

### Datagram reassembly rules

**A partially reassembled frame is never handed to the decoder.** Not on the
deadline, not on eviction, not on a `DatagramTooLarge` abort — a truncated access
unit is not a decodable access unit, and feeding one poisons the reference chain
the client is about to rebuild. Every path below either delivers a complete frame
or delivers nothing.

- **Buffer key.** One reassembly buffer per `(Type, FrameID)`.
- **Control types** (`Ping`, `CursorUpdate`, `GamepadRumble`) arrive as one
  `FragIndex = 0 | LAST` datagram and are delivered immediately — no buffer, no
  `FrameHeader`, no `+22` accounting.
- **Completion.** A media buffer is complete when the LAST fragment (index `N`)
  has arrived AND fragments `0..N` are all present AND the accumulated byte count
  equals `FrameHeader.PayloadSize + 22`. Any other total is a corrupt frame: drop
  it, count it, and mark the decoder needs-IDR. For single-datagram media (a 20 ms
  Opus packet, and every media frame on the WebSocket carrier) `N == 0` and
  completion is immediate.
- **Ordering is by modular comparison, always.** `FrameID` and
  `FrameHeader.Sequence` are `u32` and wrap at 2^32 (≈ 828 days of video at
  60 fps). Every comparison uses:
  ```rust
  /// True when `a` is newer than `b` in a wrapping u32 sequence space.
  #[inline]
  fn is_newer(a: u32, b: u32) -> bool {
      let d = a.wrapping_sub(b);
      d != 0 && d < 0x8000_0000
  }
  ```
  Plain `>` is never correct here and appears nowhere in the specs or the code.
- **Latest-wins and late fragments.** The receiver keeps `highest_frame_id` per
  Type. A fragment whose `FrameID` is not `is_newer` than `highest_frame_id` and
  does not belong to a live buffer is discarded silently — it is a duplicate or a
  late straggler and must never resurrect a frame already superseded.
- **Interleaving.** The receiver holds at most **2** in-progress buffers per Type:
  the newest `FrameID` and one predecessor. The sender pumps the tail of frame N
  and the head of frame N+1 back to back, so reordering across that boundary is
  routine and a 1-deep window would destroy a frame per reorder. A third `FrameID`
  evicts the oldest buffer immediately, and completing the newest evicts any older
  one.
- **Out-of-order and duplicates.** Fragments within a buffer may arrive in any
  order. A duplicate `FragIndex` for a buffer is ignored (first copy wins); it is
  not an error.
- **Deadline.** `[transport] fragment_reassembly_ms` (default 17 ≈ one 60 fps
  frame; use 34 at 30 fps), measured from the arrival of **the first fragment of
  that `FrameID`** — not from frame start, not from the last fragment. On expiry
  the buffer is dropped, `featherdesk_reassembly_timeouts_total` increments, the
  decoder is marked needs-IDR — see [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md)
  → "Video Decode" for the gate that consumes it — and (for video Types) the client
  sends `{"type":"keyframe"}` subject to its own request throttle. On the WebSocket
  carrier the deadline never fires: nothing is fragmented there, so a partial frame
  cannot exist.
- **Per-frame size cap.** A frame whose `FrameHeader.PayloadSize` exceeds 16 MiB is
  rejected before any allocation and the buffer is discarded.
- **Per-session buffer bound.** Total bytes held in in-progress buffers for one
  session never exceed `[transport] reassembly_max_bytes` (default `4MiB`; a 1080p
  IDR is ~50 KB and a 4K IDR ~200 KB, so this is 20× the largest legitimate frame).
  On exceeding it the receiver evicts oldest-`FrameID`-first until under the cap and
  increments `featherdesk_reassembly_evictions_total`. Buffers allocate lazily:
  before fragment 0 arrives — the only fragment carrying the `FrameHeader`, and
  therefore `PayloadSize` — a buffer holds only the fragments actually received,
  never a peer-declared size.
- **`FragIndex` cannot wrap.** Bits 0-14 give indices 0..32767, i.e. at most 32768
  fragments. At any real `max_datagram_size()` that is > 37 MB of payload — more
  than double the 16 MiB per-frame cap, which fires first. The index space is
  therefore unreachable by construction and needs no wrap rule.

This gives "late frame = useless = dropped" semantics for free, without
retransmit overhead. The reliable **bootstrap stream** (see Channel Model)
guarantees a *newly-joined* client a decodable first keyframe even though live
video is unreliable.

### Frame Types

Each Type appears on **exactly one transport channel** with one framing. The
22-byte `FrameHeader` is used **only on the media channels** (datagram fragment 0
and the bootstrap stream). Control, input, clipboard, and cursor messages use
their own compact framing (described per channel below), so no stream ever
carries two framings in the same direction.

| Type | Value | Dir | Channel | Payload |
|------|-------|-----|---------|---------|
| VideoH264 | 1 | S→C | **datagram media** (fragmented) + bootstrap stream for the join IDR | One access unit, Annex B (keyframe = SPS+PPS+IDR) |
| Ping | 2 | S→C | datagram control (no FrameHeader) | 4-byte `u32` LE nonce; client replies `{"type":"pong","nonce":<u32>}` on the control stream |
| _(reserved)_ | 3 | — | — | Reserved |
| AudioPCM | 4 | S→C | **datagram media** (fragmented) | Raw S16LE PCM (the no-codec fallback; carries the FrameHeader for capture Timestamp) |
| _(reserved)_ | 5 | — | — | Formerly VideoVP8 — rejected. Do not reuse without version bump. |
| VideoHEVC | 7 | S→C | **datagram media** (fragmented) + bootstrap stream for the join IDR | HEVC access unit, Annex B (keyframe = VPS+SPS+PPS+IDR) |
| AudioOpus | 8 | S→C | **datagram media** (single datagram) | One 20 ms Opus packet (default audio codec; FrameHeader carries capture Timestamp + audio Sequence) |
| CursorUpdate | 11 | S→C | datagram control (no FrameHeader) | 14-byte fixed record: hotspot position, visibility, ShapeID (bitmaps ride the cursor stream, tag 0x11) |
| InputAck | 14 | S→C | **input stream** (length-prefixed) | 13-byte `[Type=14 u8][Seq u32][RecvTimestampNs u64]` |
| GamepadRumble | 15 | S→C | datagram control (no FrameHeader) | 9 bytes `[Index u8][WeakMag u16][StrongMag u16][DurationMs u32]` (see [`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md)) |
| VideoAV1 | 16 | S→C | **datagram media** (fragmented) + bootstrap stream for the join keyframe | One AV1 temporal unit — raw low-overhead OBU stream, **not** Annex B (keyframe = OBU_SEQUENCE_HEADER + key OBU_FRAME). Reserved for the `av1` HW add-on tier; no add-on implements it yet (impl deferred) |
| _(reserved)_ | 0x50 | — | — | Reserved for future webcam redirection. |
| **Input events** | **0x01-0x4F** | **C→S** | **input stream** (length-prefixed) | Binary input records (keyboard 0x10-0x1F, mouse 0x20-0x2F, touch 0x30-0x3F, gamepad 0x40-0x4F; see [`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md) and [`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md)) |

> **Config (was type 6) and Clipboard (was type 12) are no longer binary
> `FrameHeader` frames.** `Config` is now a JSON line on the control stream
> (`{"type":"config",...}`); clipboard rides its own clipboard stream. Both
> moves remove mixed-framing from the control stream and remove the control-stream
> size-cap hazard for clipboard. Type values 6 and 12 are retired from the
> on-the-wire FrameHeader Type space (do not reuse without a version bump).

> `Resize` is **not** a separate type — a resolution change is a fresh `config`
> JSON message. `KeyframeReq` is **not** a binary type — requested as JSON on
> the control stream.

### Channel Model

Each stream is **single-framing and self-delimiting**:

```
SESSION (one per client)
  ├─ DATAGRAMS (unreliable, fire-and-forget)
  │     S → C: VideoH264 / VideoHEVC / VideoAV1 / AudioPCM (fragmented; 8-byte DatagramHeader)
  │            AudioOpus / CursorUpdate / Ping / GamepadRumble (single datagram each)
  │     C → S: (none in v1 — reserved for future client-side media)
  │
  ├─ CONTROL STREAM  (reliable bidi; FIRST stream the client opens)
  │     Framing: newline-delimited JSON, BOTH directions.
  │     S → C: {"type":"auth_ok"|"auth_failed"|"config"|"hdr_unavailable"|...}
  │     C → S: {"type":"auth"|"keyframe"|"pong"|"stats"|"resize"|"set_*"}
  │
  ├─ INPUT STREAM  (reliable bidi; SECOND stream the client opens; controller or co-op player)
  │     Framing: [u16 RecLen LE][record], BOTH directions.
  │     C → S: input records (6-byte header + per-type payload)
  │     S → C: InputAck (13-byte message)
  │
  ├─ BOOTSTRAP STREAM  (reliable UNIdirectional; server → client, once at join)
  │     Framing: [u32 Len LE][22-byte FrameHeader || access unit]; one IDR; then close.
  │     Seeds the joiner's decoder with a guaranteed-decodable keyframe.
  │
  ├─ CURSOR STREAM  (reliable UNIdirectional; server → client, opened at join)
  │     Framing: [u32 Len LE][cursor record]; shapes + the join position record.
  │     See MODULE_CAPTURE "Cursor delivery".
  │
  ├─ CLIPBOARD STREAM  (reliable bidi; the controller opens it right after auth_ok
  │                     whenever config.clipboard != "disabled")
  │     Framing: [u32 Len LE][JSON clipboard message], BOTH directions.
  │     See MODULE_CLIPBOARD.
  │
  └─ FILE-TRANSFER STREAMS  (reliable bidi; one per active transfer)
        Framing: 18-byte header with PayloadLen (self-delimiting). See MODULE_FILETRANSFER.
```

**Why this split:** datagrams give "late = useless = drop" for free; reliable
streams give "must arrive, in order" for free; independent QUIC streams give
HOL-isolation for free. Each message type lands on the channel whose guarantees
it needs. On the **WebSocket fallback carrier** the same lanes travel as tagged
messages over one TCP connection with the identical per-lane framing — the tree
above describes the message shapes on both carriers; only the envelope differs.
See [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) for the rationale, the
per-stream identification rules, and "Carrier selection".

### Control-stream JSON messages

Newline-delimited JSON (one message per line, `\n` terminator), reliable,
ordered, **both directions**. A reader frames a message by reading to the next
`\n`. The control reader caps any single line at `server.max_message_bytes`
(default 4 KiB) — bulk payloads (clipboard) never travel here. On the WebSocket
fallback carrier the identical lines travel as `0x00`-tagged messages.

**This table is exhaustive.** Every message on the control stream appears here
with a producer and a consumer; a `"type"` not listed is a protocol error.

| Message | Dir | Producer | Consumer | Payload | Roles |
|---|---|---|---|---|---|
| `auth` | C→S | client `connect()` — the first line after the `0x00` tag | server lifecycle step 7 → `auth::Authenticator` | `AuthPayload` — `{token, role, resume, takeover, decode}` | n/a (this is the handshake) |
| `auth_ok` | S→C | server, written inline on the auth path before the `config` line | client `connect()` → gates the input-stream open | `AuthOkPayload` — `{role, requested_role, downgrade_reason, gamepad_slot, session_id, takeover_allowed, resumed}` | n/a |
| `auth_failed` | S→C | server lifecycle step 8 (then `close::AUTH_FAILED`) | client `connect()` → surfaces the reason, falls back to `POST /auth` | `{"reason":"bad token"\|"resume expired"}` | n/a |
| `config` | S→C | server, per recipient, from a `ConfigBase` (`send_config`, and the connect path) | client `readControl` → decoder configure, cursor mode, clipboard-stream open, HUD | `ConfigPayload` | all |
| `hdr_unavailable` | S→C | `Server::send_control(Recipients::Every, ControlMessage::HdrUnavailable{reason})` — from `Pipeline::apply_params` on `StreamError::HdrUnsupported`, from `degrade_to_software`, and from the server's own `set_hdr` admission gate | client `readControl` → clears the HDR toggle, shows the reason; the stream stays SDR and the decoder is untouched | `{"reason":"no_hevc_encoder"\|"no_ten_bit_capture"\|"attached_client_cannot_decode"\|"degraded_to_software"}` | all |
| `codec_unavailable` | S→C | `Server::send_control(Recipients::One(session), ControlMessage::CodecUnavailable{..})` — at join when the session cannot decode the active codec, and as the terminal rung of the decode ladder | client `readControl` → persistent overlay, no decoder configure; input/clipboard/file transfer keep working | `{"codec":"…","reason":"client_cannot_decode"}` | requester only |
| `resize_suppressed` | S→C | `Server::send_control(Recipients::One(session), ControlMessage::ResizeSuppressed{..})` from the control dispatch when hysteresis drops a `resize` | client `readControl` → keeps its canvas size, letterboxes at the stream dimensions, transient toast | `{"width":u32,"height":u32}` — the **effective** stream dims | requester only |
| `server_shutdown` | S→C | `Server::send_control(Recipients::Every, ControlMessage::ServerShutdown)` from the shutdown sequence | client `readControl` → "server shut down" state, cancels the auto-reconnect timer | `{}` | all |
| `keyframe` | C→S | client `requestKeyframe()` — gap, reassembly deadline, decoder error, return from background | server control dispatch → three-stage limiter → keyframe-request callback | `{}` | **all authenticated roles** |
| `pong` | C→S | client datagram dispatch, on a `Ping (2)` datagram | server control dispatch → matches `nonce` against the session's ping ring → RTT sample | `{"nonce":u32}` | all |
| `stats` | C→S | client `stats.js`, 1 Hz | server control dispatch → adaptive slow path + per-client metrics | `{"decodeMs":f64,"dropped":u32,"fps":u32,"audioGaps":u32}` | all |
| `decode_unsupported` | C→S | client, when `VideoDecoder.isConfigSupported()` rejects the advertised config | server control dispatch → the decode ladder (see "Decode capability") | `{"codec":"…","chroma":"420\|422\|444","hdr":bool}` | all |
| `resize` | C→S | controller client, debounced `ResizeObserver` on the canvas (250 ms) | server control dispatch → `stream::Manager` | `{"width":u32,"height":u32}` | **control only** |
| `set_bitrate` | C→S | controller client, HUD quality control (R-CLI-13). Optional producer: a build with no quality UI never sends it | server control dispatch → `stream::Manager` | `{"kbps":u32}` — kbps; the server multiplies by 1000 into `Params.bitrate_bps` | **control only** |
| `set_fps` | C→S | controller client, HUD quality control. Optional producer | server control dispatch → `stream::Manager` | `{"fps":u32}` | **control only** |
| `set_hdr` | C→S | controller client, HUD quality control. Optional producer | server control dispatch → the HDR admission gate, then `stream::Manager`; a rejection produces `hdr_unavailable` | `{"hdr":bool}` | **control only** |

```json
// Server → client:
{"type":"auth_ok","role":"control","requested_role":"control","gamepad_slot":0,
 "session_id":41,"takeover_allowed":false,"resumed":false}
{"type":"auth_failed","reason":"bad token"}
{"type":"config","version":1,"codec":"avc1.64002A","width":1920,"height":1080,
 "fps":60,"hdr":false,"color_space":"bt709","chroma":"420","audio":true,
 "audioCodec":"opus","audioSampleRate":48000,"audioChannels":2,
 "audioLayout":"stereo","audioDescription":"","cursorMode":"separate",
 "clipboard":"bidirectional","fileStreamBudget":12,"carrier":"webtransport",
 "session_token":"…","session_ttl_sec":3600,"resumed":false}
{"type":"hdr_unavailable","reason":"no_hevc_encoder"}
{"type":"codec_unavailable","codec":"hvc1.2.4.L123.B0","reason":"client_cannot_decode"}
{"type":"resize_suppressed","width":1920,"height":1080}
{"type":"server_shutdown"}

// Client → server:
{"type":"auth","token":"…","role":"control","resume":false,"takeover":false,
 "decode":{"h264":true,"h264_422":false,"h264_444":false,"hevc":false,"hevc10":false}}
{"type":"keyframe"}
{"type":"pong","nonce":1234567}                      // u32 echoed verbatim from the Ping datagram
{"type":"stats","decodeMs":3.2,"dropped":0,"fps":60,"audioGaps":0}
{"type":"decode_unsupported","codec":"avc1.64002A","chroma":"420","hdr":false}
{"type":"resize","width":1280,"height":720}
{"type":"set_bitrate","kbps":8000}
{"type":"set_fps","fps":30}
{"type":"set_hdr","hdr":true}
```

- **There is no `{"type":"ping"}` control message.** The RTT probe is the `Ping`
  **datagram** (type 2); only its `pong` reply is a control line.
- **Input events are NOT here** — they are binary on the input stream.
- **Clipboard is NOT here** — it rides the clipboard stream (it can be up to
  1 MiB, far beyond the 4 KiB control-line cap).
- `keyframe`/`pong`/`stats`/`resize`/`set_*` carry no input `seq`.
- **Role gating is exhaustive.** Exactly four client→server messages are
  controller-only: `resize`, `set_bitrate`, `set_fps`, `set_hdr`. The server
  silently drops them from `view` and `player` clients — they are user-driven
  preferences, not correctness mechanisms. Every other client→server message —
  `auth`, `keyframe`, `pong`, `stats`, `decode_unsupported` — is accepted from
  **every authenticated role**, because each is either the handshake itself or a
  recovery/negotiation path a viewer needs as much as a controller. `keyframe` is
  bounded by rate limiting, not by role. These two lists are exhaustive and
  mutually exclusive; the enforcement point is
  [`MODULE_SERVER.md`](./MODULE_SERVER.md) "Role gate table".
- `dropped` in `stats` is a per-window delta and `audioGaps` counts concealed
  audio frames in the same window (adaptive slow path; see MODULE_WEB_CLIENT
  R-CLI-06 and "Audio Playback Pipeline").
- Parameter-change messages flow through the `stream.Params` contract (see
  [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)); the server emits a new
  `config` message only when a **client-visible** field changed (codec, width,
  height, fps, hdr, color_space, chroma, cursorMode, clipboard) — a bitrate-only
  or QP-only change sends none.

### Resume Path

The session token from a previous connection IS the resume credential. On a
new WebTransport session the client opens the control stream and sends the auth
message with `resume: true`:

```json
{ "type":"auth", "token":"<session_token>", "resume":true }
```

The `role` field is **ignored** on a resume: the role comes from the cached
entry. The cache lookup **consumes** the entry, so a replayed token cannot serve
two connections, and admission control and controller-slot arbitration run
exactly as on a fresh connection (see [`MODULE_SERVER.md`](./MODULE_SERVER.md)
steps 8-13).

If the server still has the session cached AND the token verifies:

- Server replies `{"type":"auth_ok", …}` carrying the **arbitrated** role — which
  may be a downgrade, e.g. an ex-controller whose slot was taken while it was
  away.
- Server mints a **new** session token and sends it in the `config` message; the
  presented token is consumed and is never valid again.
- Server sends `{"type":"config", "resumed":true, ...}` on the control stream.
- Server opens a **bootstrap stream** and writes a *fresh* IDR — forced through
  the rate-limited keyframe path if the cached one is stale (see "Fast-Join"
  below).
- Server opens a **cursor stream** and re-seeds the client's overlay with the
  cached shape + position (see [`MODULE_SERVER.md`](./MODULE_SERVER.md)).
- Server resumes the live datagram stream from the next encoder frame.

If the token is unknown / expired / already consumed / fails verification the
server closes the WebTransport session with `close::AUTH_FAILED (4401)` and the
client falls back to a fresh `POST /auth` + new WebTransport session.

> There is **no `last_video_seq`** field. The server always seeds a resumed
> client with a fresh bootstrap IDR; a stale client-provided sequence offers no
> useful optimization (a **fresh** cached IDR is what makes the stream decodable
> — see "Fast-Join" — and it is sent reliably regardless). See
> [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) + [`MODULE_AUTH.md`](./MODULE_AUTH.md).

### Sequence Number Semantics (server → client)

- The **server** owns and increments the sequence counters. Video and Audio have **independent** counters (both start at 0).
- Sequence increments by 1 for each message the server *sends* of that type (a frame dropped at the server for a slow client still consumed a sequence number for the stream as a whole — see note).
- **Per-client drop note:** The server may drop a frame for an individual slow client (full send buffer). Because the sequence is stream-global, that client will see a gap and may request a keyframe. This is intentional and bounded by the keyframe-request rate limit (see MODULE_SERVER).
- Client gap rule: a live frame is a gap when `is_newer(seq, lastSeq)` and `seq.wrapping_sub(lastSeq) > 1` → frames were missed → request a keyframe via a `{"type":"keyframe"}` JSON message on the control stream. On the fresh-IDR path there is nothing to suppress at the bootstrap→live transition: the bootstrap IDR is fresh, so the first live frame is `bootstrapSeq + 1` (see "Fast-Join"). The one exception is the `join_idr_timeout` path (MODULE_SERVER "Keyframe Caching Strategy" step 4), where the server wrote the stale IDR and left `needs_idr` set — the pump withholds deltas, the first live frame the client sees is a later forced IDR with a sequence greater than `bootstrapSeq + 1`, and the keyframe request this rule emits is harmless and expected.
- Sequence wraps at `2^32` (≈ 828 days at 60 fps — acceptable); every comparison is modular, via the `is_newer()` in "Datagram reassembly rules".

### Fast-Join (bootstrap stream + gap detection)

A newly-joined (or resumed) client must start from a decodable keyframe, but live
video is unreliable datagrams (a lost fragment of a datagram IDR would be
undecodable). So the join keyframe is sent over a **reliable bootstrap stream**:

1. Server sends `{"type":"config",...}` on the control stream.
2. Server ensures the cached IDR is **fresh** — that nothing has been broadcast
   since it — forcing one through the rate-limited keyframe path if not (see
   [`MODULE_SERVER.md`](./MODULE_SERVER.md) → "Keyframe Caching Strategy"), then
   opens a unidirectional **bootstrap stream**, writes the IDR as
   `[u32 Len][22-byte FrameHeader || access unit]`, and closes the stream.
3. Client reads the bootstrap stream, seeds its `VideoDecoder` with the IDR
   (`type:"key"`), sets `lastSeq` and `bootstrapSeq` from that frame's Sequence,
   and clears its needs-IDR flag.
4. **Dedup.** The forced IDR is also broadcast to every session as datagrams,
   including this one, so the joiner would otherwise receive the same access unit
   twice. The client discards every datagram whose `FrameID` is **not**
   `is_newer(FrameID, bootstrapSeq)` — one integer comparison, no state machine.
   The server performs the same elision on the send side: a session's pump skips
   the access unit whose sequence equals the `bootstrap_seq` it just wrote to that
   session's bootstrap stream, saving ~36 datagrams per join. Both rules are
   normative; the client's is the one that must hold under a race.
5. **Gap detection runs from the first live datagram frame** on every path
   except `join_idr_timeout`. Because the bootstrap IDR is fresh, the next live
   frame is exactly `bootstrapSeq + 1` and there is no discontinuity to suppress.
   If it is not, that is a genuine gap and requesting a keyframe is the correct
   response. The carve-out is the `join_idr_timeout` path (MODULE_SERVER
   "Keyframe Caching Strategy" step 4), where the server wrote the stale IDR and
   left `needs_idr` set — the pump withholds deltas, the first live frame the
   client sees is a later forced IDR with a sequence greater than
   `bootstrapSeq + 1`, and the keyframe request its gap rule emits is harmless
   and expected.

This bounds the reliability cost to exactly one stream per join. Subsequent
keyframes (periodic or on-request) travel as ordinary datagrams; if one is lost,
the client requests another via `{"type":"keyframe"}`.

### A/V Synchronization — audio is the master clock

- **Canonical clock:** `CLOCK_MONOTONIC`, in **nanoseconds**, sampled once per process at startup as the epoch. EVERY media timestamp on the wire (video and audio) MUST come from this clock.
- **Stamp at source:** video frames stamped at capture; audio frames stamped at capture (NOT when the pipeline reads them — see MODULE_PIPELINE / MODULE_AUDIO).
- **Audio is the master; video slaves to it.** When audio is present, the client plays audio **gaplessly** from a small (~40 ms) jitter buffer and treats the audio playout position as "now". At draw time it presents the decoded **video** frame whose `Timestamp` is nearest the audio playout time:
  - video ahead → hold the current frame a beat;
  - video behind by > ~1 frame interval → drop frame(s) to catch up.
- Audio is **never** held or dropped for sync (only Opus PLC fills genuine packet loss). Desync is self-correcting and bounded by the audio buffer (~40 ms). This **reverses** the earlier rule (which held/dropped audio to match video — wrong for realtime, since audio glitches are far more perceptible than a frame of video judder). Full model: [`MODULE_AUDIO.md`](../media/MODULE_AUDIO.md) "A/V Sync".
- With audio disabled / no audio add-on, video presents on its own capture clock (no master).

### Video Payload Framing (Annex B)

The video payload is the **complete access unit** for one frame, with all NAL units concatenated and Annex B start codes (`00 00 00 01`) retained:
```
[00 00 00 01][NAL 1 ...][00 00 00 01][NAL 2 ...] ...
```
- A keyframe access unit is self-contained, decodable cold: H.264 = `SPS, PPS, IDR`; HEVC = `VPS, SPS, PPS, IDR` (in order).
- The browser `VideoDecoder` consumes the whole payload as one `EncodedVideoChunk` — it never needs to split NALs, so no length-prefixing is used.
- **Keyframe signaling is NOT on the wire** (the 22-byte `FrameHeader` carries no keyframe flag). Each side derives it independently:
  - **Server:** the encoder reports it (`stream.EncodedFrame.Keyframe`), used to refresh the IDR cache and seed the bootstrap stream. The server never re-scans the bitstream.
  - **Client:** derives the `EncodedVideoChunk` key/delta hint by inspecting the first VCL NAL of the reassembled access unit — H.264 `nal_unit_type == 5` (IDR), HEVC `nal_unit_type ∈ 16..21` (IRAP: BLA/IDR/CRA). Frames arriving on the bootstrap stream are always `key`.

---

## Design Properties

- **Zero-allocation:** Marshal writes into caller-owned buffer; no heap escapes
- **Fixed size:** Header is always exactly 22 bytes (enables pre-allocation)
- **Little-endian:** Matches x86/ARM native byte order (no conversion on most hardware)
- **Framing is per-channel, not uniform:** datagram boundaries are intrinsic; the control stream is newline-delimited JSON; the input stream is `[u16 RecLen]`-prefixed binary; the bootstrap, cursor and clipboard streams are `[u32 Len]`-prefixed (a binary access unit, a binary cursor record and a JSON message respectively); file-transfer streams are self-delimiting via their own 18-byte header. A `[u32 Len]` prefix is validated **before** any allocation — on the clipboard stream against `6 × [clipboard] max_bytes + 1024` (6:1 is the exact worst case for JSON escaping — a control byte frames as `\u00XX`), on the cursor stream against the 65558-byte record maximum and against `22 + PixelBytes` for a Kind 0x01 record (`16` for Kind 0x02), on the bootstrap stream against the 16 MiB per-frame cap and against `22 + FrameHeader.PayloadSize` — and an over-long prefix is a stream-scope protocol error (`cancel_read`/`cancel_write(close::PROTOCOL_ERROR)`; the session survives). See the "Channel Model" section above and [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md).
- **Versioned:** Byte 0 enables future protocol evolution without out-of-band negotiation
- **Sequenced:** Per-type counters enable server-side drop detection without ack overhead
- **Clock-aligned:** Both video and audio share CLOCK_MONOTONIC for A/V sync

### Version pinning (why there is no negotiation handshake)

There is deliberately **no protocol version negotiation** in v1, because the v1
client is the **embedded web client served by the same binary** — server and
client are always the *same build*, so the version cannot mismatch by design.
`version` byte = `1`; a receiver rejects any other value
(`Err(UnsupportedVersion)`), it does not negotiate down.

- **Stale browser cache** is the only realistic mismatch (a user's browser
  serving an old SPA against a freshly-upgraded server). Mitigation: the SPA
  bundle is served with **content-hashed asset URLs + `Cache-Control: no-cache`
  on `index.html`**, so a server upgrade invalidates the cached bundle on the
  next load. As a backstop, `index.html` carries a `featherdesk-build` value and
  the SPA aborts with a "refresh to update" notice if the `config` message's
  `version`/build does not match what it was built against.
- **The v2 native client** is the case that *will* need real negotiation
  (independently shipped, may lag the server). That is exactly why protobuf/
  `prost` is reserved for the v2 control protocol (see CENTRAL_SPEC "Technology
  Choices") — version negotiation is a v2 concern, not retrofitted onto v1's
  fixed binary header. See [`../client/MODULE_NATIVE_CLIENT.md`](../client/MODULE_NATIVE_CLIENT.md).

---

## Refactoring Directives

### R-PRO-01: Connection Handshake
Right after `auth_ok` on the control stream, the server MUST send a `config`
JSON message on the control stream, BEFORE any video/audio/IDR media flows:
```json
{
    "type": "config",
    "version": 1,
    "codec": "avc1.64002A",
    "width": 1920,
    "height": 1080,
    "fps": 60,
    "hdr": false,
    "color_space": "bt709",
    "chroma": "420",
    "audio": true,
    "audioCodec": "opus",
    "audioSampleRate": 48000,
    "audioChannels": 2,
    "audioLayout": "stereo",
    "audioDescription": "",
    "cursorMode": "separate",
    "clipboard": "bidirectional",
    "fileStreamBudget": 12,
    "carrier": "webtransport",
    "session_token": "Yhgz...43chars...AbCd",
    "session_ttl_sec": 3600,
    "resumed": false
}
```
- `codec` is the **full WebCodecs codec string** (e.g. `avc1.64002A` for H.264 High at 1080p60, or `hvc1.2.4.L123.B0` for HEVC Main10 HDR at 1080p60; the level is computed — see MODULE_ABI "Codec-string computation"), not a short label — the client passes it straight to `VideoDecoder.configure({codec})`.
- `hdr` and `color_space` advertise the HDR mode (see [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)). `color_space` restates the bitstream's `matrix_coefficients`; the SPS VUI is authoritative and the range is always limited.
- `chroma` is the negotiated subsampling (`420`/`422`/`444`), clamped to what every
  attached session reported it can decode. The client re-probes the advertised
  config with `VideoDecoder.isConfigSupported()` before configuring; if the probe
  fails it replies `{"type":"decode_unsupported",…}` and the server walks the
  downgrade ladder (see "Decode capability").
- `audioDescription` is the base64 codec-init blob the client needs to configure its
  audio decoder — the RFC 7845 OpusHead for `audioCodec == "opus"`, empty for
  `pcm/s16le` and for mono/stereo Opus (mapping family 0 needs none). It is REQUIRED
  and non-empty whenever `audioCodec == "opus"` and `audioChannels > 2` (see
  [`MODULE_AUDIO.md`](../media/MODULE_AUDIO.md) "The Opus identification header").
  When `audio` is `false`, `audioCodec` is `""`, `audioChannels` is `0`,
  `audioSampleRate` is `0`, `audioLayout` is `""` and `audioDescription` is `""`.
- `cursorMode` is `"separate"` (client renders the cursor from `CursorUpdate`
  datagrams + the cursor stream) or `"embedded"` (cursor is burned into the video frame).
- `clipboard` is the effective direction (`"disabled"` / `"client_to_host"` /
  `"host_to_client"` / `"bidirectional"`); the controller opens the `0x02` stream iff
  it is not `"disabled"`.
- `fileStreamBudget` is how many file-transfer streams this client may hold open at
  once (`[filetransfer] max_concurrent + queue_depth`).
- `carrier` is `"webtransport"` or `"websocket"` — the client shows it in the HUD so a
  degraded session is visible (see [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) "Carrier selection").
- `session_token` is issued by the server after successful auth (see [`MODULE_AUTH.md`](./MODULE_AUTH.md)); the client stores it (in-memory only) for reconnection. Stamped by the server per recipient; never supplied by the config provider.
- `session_ttl_sec` is the remaining life of **this session's** token in whole seconds, recomputed per recipient on every `config` message, so a long-lived client watches it count down. The reconnect state cache has a separate, shorter TTL (`[reconnect] cache_ttl_seconds`, default 300s). Stamped per recipient.
- `resumed = true` on a `config` message sent in response to a successful resume — client skips full decoder re-init and waits for the bootstrap-stream IDR. Stamped per recipient; `false` on every broadcast.
- If capabilities change (resolution, codec, cursor mode, HDR, clipboard direction), the server sends a **new** `config` message; the client reconfigures its decoder and input scaling.
- Send order on connect: **`auth_ok` → `config` (control stream) → IDR (bootstrap stream) → cursor stream seed → live frames (datagrams).**

### Shared Payload Types (Rust)

```rust
/// AuthPayload is the FIRST line the client writes on the control stream, after
/// the 0x00 tag byte. Every field except `token` defaults when absent.
#[derive(serde::Serialize, serde::Deserialize)]
pub struct AuthPayload {
    #[serde(rename = "type")] pub kind: String,   // always "auth"
    pub token: String,
    /// Absent = "auto": parses as `Role::Control`, then arbitration demotes it if
    /// the controller slot is taken. IGNORED on a resume (the role comes from the
    /// cached entry). A role string that is PRESENT but unrecognised is a
    /// malformed auth message → close::PROTOCOL_ERROR.
    #[serde(default = "Role::auto")] pub role: Role,
    #[serde(default)] pub resume: bool,           // set by the client's reconnect path
    #[serde(default)] pub takeover: bool,         // set only by an explicit user action
    /// What this client can DECODE, probed with VideoDecoder.isConfigSupported()
    /// before connecting. Absent ⇒ DecodeCaps::CONSERVATIVE. Not a security input
    /// (a lying client only harms itself) — see "Decode capability".
    #[serde(default)] pub decode: DecodeCaps,
}

/// AuthOkPayload is the server's reply, written immediately after a successful
/// auth and BEFORE the `config` line. It reports the server's ARBITRATED result,
/// which is not necessarily what the client asked for: a second `control` client
/// is assigned `view`, and a `player` is assigned `view` when
/// `[gamepad] allow_coop = false`. The client MUST drive its behaviour from these
/// fields and not from its own URL hash — opening an input stream it is not
/// entitled to costs it that stream (MODULE_TRANSPORT "Stream Identification").
#[derive(serde::Serialize, serde::Deserialize)]
pub struct AuthOkPayload {
    #[serde(rename = "type")] pub kind: String,   // always "auth_ok"
    pub role: Role,                  // EFFECTIVE role
    /// Present ONLY when `role != requested_role`.
    #[serde(skip_serializing_if = "Option::is_none")] pub requested_role: Option<Role>,
    /// Present ONLY when `role != requested_role`: "controller_slot_taken" |
    /// "coop_disabled" | "unauthenticated" | "takeover_not_permitted" — enough for
    /// the client to render an accurate banner without guessing.
    #[serde(skip_serializing_if = "Option::is_none")] pub downgrade_reason: Option<String>,
    pub gamepad_slot: Option<u8>,    // global virtual-pad index this client owns
                                     // (0 for the controller, 1..max_controllers-1
                                     // for a co-op player); null for "view"
    pub session_id: u64,             // opaque per-session ordinal, also the `client`
                                     // metric label. Stable for the session's life,
                                     // never reused in a process, carries no identity.
    pub takeover_allowed: bool,      // [auth] allow_takeover — whether re-authing
                                     // with "takeover":true would succeed
    pub resumed: bool,               // true when this was a successful resume
}
// FLAT. There is no nested `session` object and never was one on the wire: the
// client-visible spelling is `auth_ok.session_id`, not `auth_ok.session.session_id`.

/// ConfigBase is the session-INDEPENDENT half of the config message: everything
/// the pipeline knows. `Server::set_config_provider`'s closure returns this and
/// `Server::send_config` takes this — neither can carry a session credential, so a
/// broadcast can no longer overwrite one client's resume token with another's.
#[derive(Clone, serde::Serialize, serde::Deserialize)]
pub struct ConfigBase {
    pub version: u32,
    pub codec: String,                                          // full WebCodecs string
    pub width: u32,
    pub height: u32,
    pub fps: u32,
    pub hdr: bool,
    pub color_space: String,                                    // "bt709" | "bt2020"
    pub chroma: String,                                         // "420" | "422" | "444" (negotiated)
    pub audio: bool,
    #[serde(rename = "audioCodec")] pub audio_codec: String,    // "opus" | "pcm/s16le" | "" when audio = false
    #[serde(rename = "audioSampleRate")] pub audio_sample_rate: u32,
    #[serde(rename = "audioChannels")] pub audio_channels: u8,  // 1..8 (stereo / 5.1 / 7.1)
    #[serde(rename = "audioLayout")] pub audio_layout: String,  // "stereo" | "5.1" | "7.1"
    /// base64 OpusHead (RFC 7845 5.1); "" for PCM and for mono/stereo Opus.
    #[serde(rename = "audioDescription")] pub audio_description: String,
    /// True iff the host has a working AudioSink add-on AND [audio] mic_enabled.
    /// It advertises that a mic sink EXISTS, never that the client should start
    /// transmitting: the client requires an explicit user action regardless
    /// (MODULE_WEB_CLIENT "Microphone control"). False on every macOS host in v1.
    #[serde(rename = "micEnabled")] pub mic_enabled: bool,
    /// Frame duration the client must use for AUDIO_MIC packets, from
    /// [audio] mic_frame_ms. The mic is not on the A/V-sync path, so this is
    /// independent of the playback frame_ms above.
    #[serde(rename = "micFrameMs")] pub mic_frame_ms: u32,
    #[serde(rename = "cursorMode")] pub cursor_mode: String,    // "separate" | "embedded"
    /// [clipboard] direction, or "disabled" when [clipboard] enabled = false. The
    /// controller opens the 0x02 stream iff this is not "disabled".
    pub clipboard: String,   // "disabled"|"client_to_host"|"host_to_client"|"bidirectional"
    /// How many file-transfer streams the client may hold open at once:
    /// filetransfer.max_concurrent + filetransfer.queue_depth.
    #[serde(rename = "fileStreamBudget")] pub file_stream_budget: u32,
}
// There is deliberately NO bitrate, QP or keyframe_interval field: none of them is
// visible to the decoder, which is why an adaptive bitrate change sends no `config`
// message at all. The HUD shows the client's own measured throughput instead
// (MODULE_WEB_CLIENT R-CLI-13).

/// ConfigPayload is what actually goes on ONE session's control stream. The
/// SERVER builds it, per recipient, from a ConfigBase plus that session's own
/// state. No other component can construct one, which is what makes the
/// per-session fields impossible to fan out.
#[derive(serde::Serialize, serde::Deserialize)]
pub struct ConfigPayload {
    #[serde(rename = "type")] pub kind: String,   // always "config"
    #[serde(flatten)] pub base: ConfigBase,
    /// This session's carrier: "webtransport" | "websocket".
    pub carrier: String,
    pub session_token: String,   // THIS session's resume credential
    pub session_ttl_sec: u32,    // remaining life of THIS session's token, in seconds
    pub resumed: bool,           // true only on the config line that follows a
                                 // successful resume handshake
}

/// ControlMessage is the closed set of server-originated control-stream lines that
/// are not `auth_ok` / `auth_failed` (written inline on the auth path) or `config`
/// (which has per-recipient rewrite rules). Serialized as one newline-terminated
/// JSON object whose "type" is the variant name in snake_case, so the enum and the
/// wire can never drift. `Server::send_control(Recipients, ControlMessage)` is the
/// sole producer.
#[derive(serde::Serialize, serde::Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ControlMessage {
    /// {"type":"hdr_unavailable","reason":…} — an HDR request no loaded encoder,
    /// no 10-bit capture path, or no attached client can satisfy.
    HdrUnavailable   { reason: HdrUnavailableReason },
    /// {"type":"codec_unavailable","codec":…,"reason":…} — this session cannot
    /// decode the active codec. Sent to ONE session; the stream is unchanged for
    /// the others.
    CodecUnavailable { codec: String, reason: String },   // "client_cannot_decode"
    /// {"type":"resize_suppressed","width":…,"height":…} — a resize dropped by
    /// hysteresis; the payload is the EFFECTIVE stream size, not the request.
    ResizeSuppressed { width: u32, height: u32 },
    /// {"type":"server_shutdown"} — graceful shutdown notice, sent before the QUIC
    /// close with close::SERVER_SHUTDOWN (4503).
    ServerShutdown,
}

#[derive(serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum HdrUnavailableReason {
    NoHevcEncoder,               // no HEVC-Main10-capable encoder add-on is loaded
    NoTenBitCapture,             // the capture add-on cannot produce a 10-bit surface
    AttachedClientCannotDecode,  // an attached session reported decode.hevc10 == false
    DegradedToSoftware,          // mid-session HW failure forced the session out of HDR
}

/// CursorUpdate is the payload of a CURSOR_UPDATE (type 11) control datagram:
/// pointer position and visibility, latest-wins. It is FIXED SIZE — 14 bytes —
/// so it always fits one datagram and can never fragment. The cursor BITMAP is
/// not here: shapes travel as reliable records on the cursor stream
/// (`stream_type::CURSOR`, 0x11) and are referenced from here by `shape_id`.
///
/// There is deliberately NO sequence field. Ordering lives in
/// `DatagramHeader.FrameID`, which the Server stamps at serialization time; a
/// producer upstack of the Server has no way to reach it and must not hold one.
///
/// `x`/`y` are in the **stream space** (MODULE_STREAM_PARAMS "Coordinate space")
/// — the same space as `InputRecord.X`/`Y`.
///
/// Binary little-endian:
///   [x:u16][y:u16][stream_w:u16][stream_h:u16][shape_id:u32][visible:u8][reserved:u8]
#[derive(Copy, Clone)]
pub struct CursorUpdate {
    pub x: u16,          // hotspot X, in the stream coordinate space (see MODULE_CAPTURE
                         // "Cursor coordinate space"); 0 <= x < stream_w
    pub y: u16,          // hotspot Y; 0 <= y < stream_h
    pub stream_w: u16,   // the stream width  x/y were computed against
    pub stream_h: u16,   // the stream height x/y were computed against
    pub shape_id: u32,   // which CursorShapeRecord applies; 0 = no shape published yet
    pub visible: bool,   // false = pointer hidden (fullscreen video, hidden-cursor app)
}

/// CursorShapeRecord is one record on the cursor stream. `pixels` is always
/// exactly `pixel_w * pixel_h * 4` bytes of STRAIGHT (non-premultiplied) RGBA,
/// top-down, tightly packed — the wire carries no pixel-format discriminator
/// because there is only one format (see MODULE_CAPTURE "Cursor pixel format").
///
/// `1..=128` on `pixel_w`/`pixel_h` is a SENDER obligation, not only a receiver
/// check: it is what makes the record's maximum close at 22 + 128*128*4 = 65558
/// bytes. A bitmap larger than that in either dimension is reduced to fit BEFORE
/// this record is serialized; nothing on this path may emit an oversized record.
pub struct CursorShapeRecord {
    pub shape_id: u32,   // content hash; see MODULE_CAPTURE "Shape identity"
    pub pixel_w: u16,    // bitmap width  in bitmap pixels, 1..=128 (sender-enforced)
    pub pixel_h: u16,    // bitmap height in bitmap pixels, 1..=128 (sender-enforced)
    pub draw_w: u16,     // width  to draw at, in stream pixels
    pub draw_h: u16,     // height to draw at, in stream pixels
    pub hotspot_x: u16,  // hotspot X within draw_w, in stream pixels
    pub hotspot_y: u16,  // hotspot Y within draw_h, in stream pixels
    pub pixels: Vec<u8>, // pixel_w * pixel_h * 4 (boundary form: RVec<u8>, owned — MODULE_ABI)
}
```

These live in `featherdesk-protocol` alongside `FrameHeader`, shared by server and (conceptually) any native client.

### Decode capability

A client reports what it can decode **at auth time**, so the server never
advertises a codec no attached client can render. This is a capability report,
never an authorization input: a client that lies only harms itself.

```rust
/// What a client can decode. Every field is a probe result, not a preference.
/// Every comment below is the FIXED probe string for that field: it is exactly
/// `featherdesk_abi::codec_string(profile, 1920, 1080, 60)` — the default session
/// geometry (`stream.fps` default 60 at 1920x1080). A `true` bit is a claim about
/// THAT geometry and nothing larger.
/// Absent from the auth message ⇒ CONSERVATIVE: H.264 4:2:0 only, which is what
/// keeps a native or third-party client working without teaching it this schema.
#[derive(Copy, Clone, serde::Serialize, serde::Deserialize)]
pub struct DecodeCaps {
    pub h264: bool,      // avc1.64002A          — H.264 High, 4:2:0, 8-bit, Level 4.2
    pub h264_422: bool,  // avc1.7A002A          — H.264 High 4:2:2, Level 4.2
    pub h264_444: bool,  // avc1.F4002A          — H.264 High 4:4:4 Predictive, Level 4.2
    pub hevc: bool,      // hvc1.1.6.L123.B0     — HEVC Main, 4:2:0, 8-bit, Level 4.1
    pub hevc10: bool,    // hvc1.2.4.L123.B0     — HEVC Main10, Level 4.1 (the HDR requirement)
}

impl DecodeCaps {
    pub const CONSERVATIVE: DecodeCaps = DecodeCaps {
        h264: true, h264_422: false, h264_444: false, hevc: false, hevc10: false,
    };
}

impl Default for DecodeCaps {
    fn default() -> Self { Self::CONSERVATIVE }   // what `#[serde(default)]` yields
}
```

The probe strings above are **fixed** — the client probes those five with
`VideoDecoder.isConfigSupported({codec, codedWidth: 1920, codedHeight: 1080,
optimizeForLatency: true})` at 1080p and reports the results; a throw is a "no",
never a connect failure. It is deliberately not the *active* codec string: the
report has to be sent before the client has seen a `config` message.

The five strings are exactly `codec_string(profile, 1920, 1080, 60)` (MODULE_ABI
"Codec-string computation") for `H264High`, `H264High422`, `H264High444`,
`HevcMain` and `HevcMain10` — the level the server actually advertises at the
default session geometry. A `true` bit therefore means precisely "this client
decodes that profile at 1920x1080 @ 60 fps" and is NOT a claim about a larger
geometry; a session resized above that is covered by
`{"type":"decode_unsupported"}`, the runtime backstop. The probe must never be
stated at a LOWER level than the advertised string: `avc1.640028` is Level 4.0,
which is 1080p30, and a client whose 4:2:2 or HEVC decode caps at Level 4.0 would
answer `true` and then throw `NotSupportedError` on `configure()` — the black
canvas this probe exists to prevent.

The server records `SessionState.decode` and gates three things with it: a join
into a session whose active codec this client cannot decode (answered with
`codec_unavailable`, never a black canvas), `set_hdr` (refused with
`hdr_unavailable` reason `attached_client_cannot_decode` while any attached
session has `hevc10 == false`, because HDR is session-global and one-way), and
chroma selection (`Manager::apply` clamps to the **minimum** every attached
session reports). See [`MODULE_SERVER.md`](./MODULE_SERVER.md) and
[`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md).

`{"type":"decode_unsupported"}` is the runtime backstop for a browser whose
`isConfigSupported` was optimistic — the client sends it instead of calling
`configure()` on a config that probed unsupported, because `configure()` throws
`NotSupportedError` and there is no recovery from it. The server answers with at
most one rung per message, each rung re-sending `config` and forcing an IDR, and
each rung recorded on the session so the same client is never offered the same
rung twice:

| Rung | Condition | Action |
|------|-----------|--------|
| 1 | `chroma != "420"` | downgrade chroma to `"420"` session-wide |
| 2 | `hdr == true` | the reporting client cannot decode HEVC Main10. Send it `codec_unavailable` with reason `client_cannot_decode`; the stream is unchanged for the others. Also clear `SessionState.decode.hevc10` so the next `set_hdr` is refused |
| 3 | neither | the client cannot decode H.264 High at the advertised level. Send `codec_unavailable` and log at `warn` with the codec string — there is no lower rung, and a client that cannot decode `avc1.6400*` is outside the supported browser set |

### R-PRO-02: Add Bounds Check to MarshalHeader
`MarshalHeader` should return an error if `buf` is shorter than `HeaderSize`, rather than panicking. This is a low-cost check (single comparison) that prevents crashes from propagating.

### R-PRO-03: Audio Header Semantics (overload retired)
The old Width/Height overload (sample rate / channel count) is **gone**. Audio's `FrameHeader.Width`/`Height` are now `0`; codec, sample rate, channel count and the decoder-init blob are advertised once in the `config` message (`audioCodec`, `audioSampleRate`, `audioChannels`, `audioDescription`). The audio `FrameHeader` is still present (it's a media type) for its capture `Timestamp` + the independent audio `Sequence` — whose consumer is the client's decode-site loss detector, which conceals `gap - 1` missing 20 ms packets and advances the audio playout clock across them (see [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) → "Audio Playback Pipeline").

### R-PRO-04: Annex B Per-Frame Concatenation (REVISED from round 1)
Serialize each video frame as ONE message containing the **complete access unit** in Annex B (start codes retained), all NALs concatenated. Do NOT length-prefix and do NOT split NALs across messages. Rationale: the browser `VideoDecoder` consumes a whole access unit as a single `EncodedVideoChunk` and never iterates NALs, so length-prefixing adds client-side AVCC `avcC` complexity for zero benefit. This also makes keyframe caching trivially correct (the IDR message already contains SPS+PPS+IDR).

> This reverses the round-1 length-prefix decision. If a non-browser native client is ever added that needs O(1) NAL access, length-prefixing can be offered as an opt-in via a Config capability flag — but the default and only browser-facing format is Annex B.

### R-PRO-05: Canonical Monotonic Clock for All Media Timestamps
Define a single process-wide `CLOCK_MONOTONIC` epoch. Every video frame (from every capture backend) and every audio chunk MUST be stamped from this clock, in nanoseconds, **at capture time**. The pipeline must NOT stamp audio at channel-read time. This is a hard requirement for A/V sync — mixing wall-clock (`UnixMilli`) and monotonic, or ms and ns, silently breaks sync.

### R-PRO-06: Server Owns Sequence Counters, **Per Session**
The server is the sole owner of the video and audio sequence counters (independent, per type). They are assigned in `Broadcast`/`BroadcastAudio` at send time. The pipeline must NOT maintain its own frame sequence counter.

The counters are **per session**, not global. With per-client quality tiers
(`MODULE_STREAM_PARAMS` "Per-client quality tiers") two tiers share one frame
loop but not one frame stream, so a single global counter would show every
client a gap for every frame belonging to the other tier — and gap detection
would have each of them request an IDR on every frame. Per-session counters make
a frame a client never receives invisible to it, which is also what makes
temporal-layer subsetting possible later without a wire change. This costs one
`u64` per session and is internal: the server already owns the counters, and
`bootstrap_seq` is already per-session, so the join path is unchanged.

### R-PRO-07: Handshake Discriminator
The first control-stream message after `auth_ok` is always the `config` JSON
message (`{"type":"config",...}`). There are no binary magic bytes — the control
stream is newline-delimited JSON from its first byte, and the `"type"` field is
the discriminator for every message on it.

### R-PRO-08: Lives in the `featherdesk-protocol` crate
This crate is pure (no dependencies). It is the canonical, transport-agnostic wire format (bytes in / bytes out) shared by the server and any future native client.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Round-trip marshal/unmarshal for all frame types | No |
| Unit | Short buffer and unsupported version error handling | No |
| Unit | Sequence number overflow/wrap behavior — `is_newer()` across the 2^32 boundary, and that no comparison uses plain `>` | No |
| Unit | Deterministic encoding (same input → same bytes) | No |
| Unit | Buffer reuse safety | No |
| Unit | Annex B access-unit assembly (SPS+PPS+IDR ordering, start codes) | No |
| Unit | Control-stream JSON parsing both directions — every row of the exhaustive message table (auth, auth_ok, auth_failed, config, hdr_unavailable, codec_unavailable, resize_suppressed, server_shutdown, keyframe, pong, stats, decode_unsupported, resize, set_*), plus an unlisted `"type"` rejected as a protocol error | No |
| Unit | Ping nonce round-trip across the binary/JSON boundary: `u32` → `[u8;4]` LE → `getUint32(…, true)` → JSON number → `serde_json` → `u32`, for 0, 1, `u32::MAX` and a random sample; plus an unmatched and a missing `nonce`, which must be ignored rather than raise | No |
| Unit | Cursor wire formats: the 14-byte CursorUpdate body round-trips at exactly 22 bytes on the wire; cursor-stream Kind 0x01/0x02 records round-trip; and each rejection rule (`Len > 65558`, `Version != 1`, `PixelBytes != PixelW*PixelH*4`, `PixelW`/`PixelH` outside 1..=128, `HotspotX >= DrawW`) is a protocol error while an unknown `Kind` is skipped by `Len`; and a Kind 0x01 record whose `Len` disagrees with `22 + PixelBytes` in either direction, or a Kind 0x02 record whose `Len != 16`, cancels the cursor stream and the session survives | No |
| Unit | `ConfigPayload` serializes `ConfigBase` flattened plus `carrier`/`session_token`/`session_ttl_sec`/`resumed`, and `ConfigBase` has no field that could carry a session credential | No |
| Unit | Clipboard-stream JSON parsing (offer/data, >4 KiB payloads up to the 1 MiB cap), and a `[u32 Len]` prefix over `6 × [clipboard] max_bytes + 1024` rejected **before** the payload buffer is allocated | No |
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
