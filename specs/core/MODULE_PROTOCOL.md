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

```rust
// crate: featherdesk-protocol

pub const HEADER_SIZE: usize = 22;

// FrameHeader Type constants. The 22-byte FrameHeader is carried only by the
// MEDIA datagram types (video + audio) and on the bootstrap stream. The control
// datagram types (Ping/CursorUpdate/GamepadRumble) and the control/input/
// clipboard streams do NOT use FrameHeader (see "Channel Model" + "media vs
// control" below).
pub mod frame_type {
    pub const VIDEO_H264: u8 = 1;     // datagram media (S→C); fragmented + bootstrap-stream join IDR
    pub const PING: u8 = 2;           // datagram control (S→C); 8-byte nonce, no FrameHeader
    // 3 reserved (client Pong routed on the control stream as JSON)
    pub const AUDIO_PCM: u8 = 4;      // datagram media (S→C); fragmented; raw S16LE (PCM fallback codec)
    // 5 reserved (formerly VideoVP8 — VP8 rejected; never reuse without protocol version bump)
    // 6 retired (was Config; Config is now a control-stream JSON message)
    pub const VIDEO_HEVC: u8 = 7;     // datagram media (S→C); fragmented + bootstrap-stream join IDR
    pub const AUDIO_OPUS: u8 = 8;     // datagram media (S→C); single-datagram Opus packet (default audio codec)
    pub const CURSOR_UPDATE: u8 = 11; // datagram control (S→C); latest-wins, no FrameHeader
    // 12 retired (was Clipboard; clipboard is now a clipboard-stream message)
    pub const INPUT_ACK: u8 = 14;     // input stream (S→C), length-prefixed
    pub const GAMEPAD_RUMBLE: u8 = 15;// datagram control (S→C); MODULE_GAMEPAD, no FrameHeader
    pub const VIDEO_AV1: u8 = 16;     // datagram media (S→C); fragmented + bootstrap-stream join.
                                      // Payload is a raw low-overhead OBU temporal unit, NOT Annex B
                                      // (see MODULE_HARDWARE_ENCODE.md). Reserved for the `av1` HW
                                      // add-on tier; no add-on implements it yet (impl deferred).
    // 0x50 reserved for future webcam redirection (deferred from v1).
    // Do NOT reuse 6, 12, 16, or 0x50 without a protocol version bump.
}

// Application-layer close codes (carried by transport `Session::close_with_error`
// and stream cancellation). featherdesk-protocol is the SOLE owner of these; the
// transport crate references these rather than redefining them.
pub mod close {
    pub const NORMAL: u32 = 0;
    pub const PROTOCOL_ERROR: u32 = 4400;      // malformed message
    pub const AUTH_FAILED: u32 = 4401;         // bad / expired session token (also: resume token expired)
    pub const AUTH_TIMEOUT: u32 = 4408;        // client didn't open + auth the control stream within 5 s
    pub const CONTROLLER_TAKEOVER: u32 = 4410; // controller slot seized
    pub const SERVER_SHUTDOWN: u32 = 4503;
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
bootstrap-stream message. It is NOT used on the control, input, or clipboard
streams — those carry their own compact framing (JSON lines, length-prefixed
records). `Config`, `Clipboard`, and `InputAck` are **not** FrameHeader frames.

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
1       1     uint8    Type          Frame type (FrameTypeVideoH264 / VideoHEVC / AudioOpus / AudioPCM / CursorUpdate / Ping / GamepadRumble)
2       4     uint32   FrameID       Little-endian; == the FrameHeader.Sequence of this access unit
6       2     uint16   FragIndex     Little-endian; bit 15 = LAST flag; bits 0-14 = fragment index (0-based, max 32767)
------- Total: 8 bytes -------

[DatagramHeader: 8 bytes][Fragment payload: up to ~1192 bytes]
```

**FrameID == Sequence.** `DatagramHeader.FrameID` carries the same value as the
`FrameHeader.Sequence` of the access unit it belongs to. It is duplicated into
the compact datagram header so the receiver can group fragments by frame
without first reassembling and parsing the 22-byte FrameHeader. (For audio the
audio Sequence is used; counters remain per-Type.)

There are **two classes** of datagram Type, split by whether a 22-byte
FrameHeader is present — **media** (needs a wire Timestamp + Sequence) vs
**control** (self-describing). Note this is independent of fragmentation: a media
frame may be fragmented (video, PCM audio) OR fit in one datagram (Opus audio),
but it ALWAYS carries the FrameHeader.

- **Media** (carry the 22-byte FrameHeader) — `VideoH264 (1)`, `VideoHEVC (7)`,
  `AudioPCM (4)`, `AudioOpus (8)`. **Fragment 0** (FragIndex == 0) starts its
  payload with the FrameHeader, then the leading access-unit bytes; subsequent
  fragments carry only raw access-unit bytes. `FrameHeader.PayloadSize` is the
  size of the **complete access unit** (the bytes after the 22-byte header), NOT
  this fragment. Video and 20 ms PCM audio fragment into several datagrams; a
  20 ms **Opus** packet (~100–300 B) fits one datagram (`FragIndex = 0 | LAST`)
  but still carries the FrameHeader (for its capture Timestamp + audio Sequence).
- **Control** (no FrameHeader) — `Ping (2)`, `CursorUpdate (11)`, `GamepadRumble
  (15)`. The datagram is `[8-byte DatagramHeader][raw payload]`, always one
  datagram (`FragIndex = 0 | LAST`). E.g. a Ping is the 8-byte header + an 8-byte
  nonce (16 B total); GamepadRumble is the header + a 9-byte payload. Their
  payloads are self-describing (CursorUpdate carries its own x/y/w/h), so a
  FrameHeader would be redundant.

**The LAST fragment** sets the `LAST` flag (bit 15 of `FragIndex`). Its index
value N (bits 0-14) means the frame has **N+1 fragments, indices 0..N** — no
separate fragment-count field is needed because the last fragment's index
encodes the count.

### Datagram reassembly rules

- Receiver maintains one reassembly buffer per `(Type, FrameID)`.
- **Control types** (Ping/CursorUpdate/GamepadRumble) arrive as one
  `FragIndex = 0 | LAST` datagram and are delivered immediately (no buffer, no
  FrameHeader, no `+22` accounting).
- **Media types** (video, PCM audio, Opus audio) are **complete** when the LAST
  fragment (index N) has arrived AND fragments 0..N are all present AND the
  accumulated payload size equals `FrameHeader.PayloadSize + 22`. Then deliver
  upstack. For single-datagram media (a 20 ms Opus packet) this is trivially one
  fragment (N == 0); for video / PCM audio it is several.
- Reassembly **deadline**: one frame interval (default 17 ms ≈ one 60 fps frame;
  `[transport] fragment_reassembly_ms = 17`). If the LAST fragment never arrives, or
  a middle fragment is lost, the deadline fires: the partial frame is
  **dropped**, a metric increments, and (for video Types) the client sends a
  JSON `{"type":"keyframe"}` on the control stream to recover. The decoder is
  marked "needs IDR" until a keyframe arrives.
- A **new FrameID arriving while an older one is in progress** for the same Type
  discards the older buffer immediately (latest-wins — old frames are useless).
- Out-of-order fragments are tolerated; duplicate fragments are ignored.
- A frame whose `FrameHeader.PayloadSize` exceeds a safety cap (16 MB default)
  is rejected and dropped before allocation.

This gives "late frame = useless = dropped" semantics for free, without
retransmit overhead. The reliable **bootstrap stream** (see Channel Model)
guarantees a *newly-joined* client a decodable first keyframe even though live
video is unreliable.

### Frame Types

Each Type appears on **exactly one transport channel** with one framing. The
22-byte `FrameHeader` is used **only on the media channels** (datagram fragment 0
and the bootstrap stream). Control, input, and clipboard messages use their own
compact framing (described per channel below), so no stream ever carries two
framings in the same direction.

| Type | Value | Dir | Channel | Payload |
|------|-------|-----|---------|---------|
| VideoH264 | 1 | S→C | **datagram media** (fragmented) + bootstrap stream for the join IDR | One access unit, Annex B (keyframe = SPS+PPS+IDR) |
| Ping | 2 | S→C | datagram control (no FrameHeader) | 8-byte nonce; client replies `{"type":"pong","nonce":…}` on the control stream |
| _(reserved)_ | 3 | — | — | Reserved |
| AudioPCM | 4 | S→C | **datagram media** (fragmented) | Raw S16LE PCM (the no-codec fallback; carries the FrameHeader for capture Timestamp) |
| _(reserved)_ | 5 | — | — | Formerly VideoVP8 — rejected. Do not reuse without version bump. |
| VideoHEVC | 7 | S→C | **datagram media** (fragmented) + bootstrap stream for the join IDR | HEVC access unit, Annex B (keyframe = VPS+SPS+PPS+IDR) |
| AudioOpus | 8 | S→C | **datagram media** (single datagram) | One 20 ms Opus packet (default audio codec; FrameHeader carries capture Timestamp + audio Sequence) |
| CursorUpdate | 11 | S→C | datagram control (no FrameHeader) | Cursor position + optional image (latest-wins) |
| InputAck | 14 | S→C | **input stream** (length-prefixed) | 13-byte `[Type=14 u8][Seq u32][RecvTimestampNs u64]` |
| GamepadRumble | 15 | S→C | datagram | 9 bytes `[Index u8][WeakMag u16][StrongMag u16][DurationMs u32]` (see [`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md)) |
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

### Channel Model (over WebTransport)

Each stream is **single-framing and self-delimiting**:

```
SESSION (one per client)
  ├─ DATAGRAMS (unreliable, fire-and-forget)
  │     S → C: VideoH264 / VideoHEVC / AudioPCM (fragmented; 8-byte DatagramHeader)
  │            CursorUpdate / Ping / GamepadRumble (single datagram each)
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
  ├─ CLIPBOARD STREAM  (reliable bidi; opened on demand if clipboard enabled)
  │     Framing: [u32 Len LE][JSON clipboard message], BOTH directions.
  │     See MODULE_CLIPBOARD.
  │
  └─ FILE-TRANSFER STREAMS  (reliable bidi; one per active transfer)
        Framing: 18-byte header with PayloadLen (self-delimiting). See MODULE_FILETRANSFER.
```

**Why this split:** datagrams give "late = useless = drop" for free; reliable
streams give "must arrive, in order" for free; independent QUIC streams give
HOL-isolation for free. Each message type lands on the channel whose guarantees
it needs. See [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md) for the rationale
and the per-stream identification rules.

### Control-stream JSON messages

Newline-delimited JSON (one message per line, `\n` terminator), reliable,
ordered, **both directions**. A reader frames a message by reading to the next
`\n`. The control reader caps any single line at `server.max_message_bytes`
(default 4 KiB) — bulk payloads (clipboard) never travel here.

```json
// Server → client:
{"type": "auth_ok",   "session": { ... }}
{"type": "auth_failed","reason": "bad token"}
{"type": "config",    "codec": "avc1.42E01F", "width": 1920, "height": 1080,
                      "fps": 60, "hdr": false, "color_space": "bt709", "chroma": "420",
                      "cursorMode": "separate", "session_token": "…",
                      "session_ttl_sec": 3600, "resumed": false}
{"type": "hdr_unavailable"}                          // HDR requested but no HEVC/10-bit encoder
{"type": "resize_suppressed", "width": 1920, "height": 1080} // resize ignored (below hysteresis);
                                                     // client letterboxes — see MODULE_STREAM_PARAMS
{"type": "server_shutdown"}                          // graceful shutdown notice (then QUIC close 4503)

// Client → server:
{"type": "auth", "token": "…", "role": "control|view|player", "resume": false}
{"type": "keyframe"}                                 // request an IDR after a gap
{"type": "pong", "nonce": 12345}                     // reply to a server Ping datagram
{"type": "stats", "decodeMs": 3.2, "dropped": 0, "fps": 60} // 1 Hz client telemetry; dropped = per-window delta (adaptive slow path; see MODULE_WEB_CLIENT R-CLI-06)
{"type": "resize", "width": 1280, "height": 720}     // dynamic resolution change
{"type": "set_bitrate", "kbps": 8000}                // dynamic bitrate (control role only)
{"type": "set_fps", "fps": 30}                       // dynamic frame rate (control role only)
{"type": "set_hdr", "hdr": true}                     // toggle HDR pipeline
{"type": "chroma_unsupported"}                       // client can't DECODE the config's chroma
                                                     // → server downgrades to 4:2:0 + new config + keyframe
```

- **Input events are NOT here** — they are binary on the input stream.
- **Clipboard is NOT here** — it rides the clipboard stream (it can be up to
  1 MiB, far beyond the 4 KiB control-line cap).
- `keyframe`/`pong`/`stats`/`resize`/`set_*` carry no input `seq`.
- `resize`, `set_*` are gated by authorization role (see [`MODULE_AUTH.md`](./MODULE_AUTH.md));
  the server silently drops them from `view`-role clients.
- Parameter-change messages flow through the `stream.Params` contract (see
  [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)); the server emits a new
  `config` message if the codec or color space changed.

### Resume Path

The session token from a previous connection IS the resume credential. On a
new WebTransport session the client opens the control stream and sends the auth
message with `resume: true`:

```json
{ "type":"auth", "token":"<session_token>", "role":"control", "resume":true }
```

If the server still has the session cached AND the token verifies:

- Server sends `{"type":"config", "resumed":true, ...}` on the control stream.
- Server opens a **bootstrap stream** and writes the most recent cached IDR
  (reliable — guaranteed decodable, see "Fast-Join" below).
- Server resumes the live datagram stream from the next encoder frame.

If the token is unknown / expired / fails verification the server closes the
WebTransport session with `CloseAuthFailed (4401)` and the client falls back to
a fresh `POST /auth` + new WebTransport session.

> There is **no `last_video_seq`** field. The server always seeds a resumed
> client with a fresh bootstrap IDR; a stale client-provided sequence offers no
> useful optimization (the cached IDR is what makes the stream decodable, and
> it is sent reliably regardless). See [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md)
> + [`MODULE_AUTH.md`](./MODULE_AUTH.md).

### Sequence Number Semantics (server → client)

- The **server** owns and increments the sequence counters. Video and Audio have **independent** counters (both start at 0).
- Sequence increments by 1 for each message the server *sends* of that type (a frame dropped at the server for a slow client still consumed a sequence number for the stream as a whole — see note).
- **Per-client drop note:** The server may drop a frame for an individual slow client (full send buffer). Because the sequence is stream-global, that client will see a gap and may request a keyframe. This is intentional and bounded by the keyframe-request rate limit (see MODULE_SERVER).
- Client gap rule: `if (seq > lastSeq + 1)` → frames were missed → request a keyframe via a `{"type":"keyframe"}` JSON message on the control stream. **Exception:** the first live datagram frame after the bootstrap-stream IDR is NOT treated as a gap (see "Fast-Join").
- Sequence wraps at `2^32` (≈ 828 days at 60 fps — acceptable; client handles wrap with modular comparison).

### Fast-Join (bootstrap stream + gap detection)

A newly-joined (or resumed) client must start from a decodable keyframe, but live
video is unreliable datagrams (a lost fragment of a datagram IDR would be
undecodable). So the join keyframe is sent over a **reliable bootstrap stream**:

1. Server sends `{"type":"config",...}` on the control stream.
2. Server opens a unidirectional **bootstrap stream** and writes the cached IDR
   as `[u32 Len][22-byte FrameHeader || access unit]`, then closes the stream.
   (If no IDR is cached yet — very first client — the server forces a keyframe on
   the encoder and sends that first IDR on the bootstrap stream.)
3. Client reads the bootstrap stream, seeds its `VideoDecoder` with the IDR
   (`type:"key"`), and sets `lastSeq` from that frame's Sequence.
4. The cached IDR may carry an OLD Sequence (it was encoded earlier), while the
   first live datagram carries the current, much higher Sequence — so the client
   does **not** gap-check the first live frame: it simply re-seeds
   `lastSeq = <first live frame's Sequence>`. Gap detection (`seq > lastSeq+1`)
   runs only from the **second** live datagram frame onward. The bootstrap→live
   transition is therefore never a false gap.

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
- **Framing is per-channel, not uniform:** datagram boundaries are intrinsic; the control + clipboard streams are newline-delimited JSON; the input stream and bootstrap stream are length-prefixed binary. See the "Channel Model" section above and [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md).
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
    "codec": "avc1.42E01F",
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
    "cursorMode": "separate",
    "session_token": "Yhgz...43chars...AbCd",
    "session_ttl_sec": 3600,
    "resumed": false
}
```
- `codec` is the **full WebCodecs codec string** (e.g., `avc1.42E01F` for H.264 Constrained Baseline L3.1, or `hvc1.2.4.L93.B0` for HEVC Main10 HDR), not a short label — the client passes it straight to `VideoDecoder.configure({codec})`.
- `hdr` and `color_space` advertise the HDR mode (see [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)).
- `chroma` is the negotiated subsampling (`420`/`422`/`444`). The client probes the
  `codec` string with `VideoDecoder.isConfigSupported()`; if it can't decode the
  advertised chroma it replies `{"type":"chroma_unsupported"}` and the server
  downgrades to `420` (see MODULE_STREAM_PARAMS "Chroma Subsampling").
- `cursorMode` is `"separate"` (client renders cursor from `CursorUpdate` messages) or `"embedded"` (cursor is burned into the video frame).
- `session_token` is issued by the server after successful auth (see [`MODULE_AUTH.md`](./MODULE_AUTH.md)); client stores it (in-memory only) for reconnection.
- `session_ttl_sec` is the auth session lifetime (from `[auth] session_ttl_minutes`). The reconnect state cache has a separate, shorter TTL (`[reconnect] cache_ttl_seconds`, default 300s).
- `resumed = true` on a `config` message sent in response to a successful resume — client skips full decoder re-init and waits for the bootstrap-stream IDR.
- If capabilities change (resolution, codec, cursor mode, HDR), the server sends a **new** `config` message; the client reconfigures its decoder and input scaling.
- Send order on connect: **`config` (control stream) → IDR (bootstrap stream) → live frames (datagrams).**

### Shared Payload Types (Rust)

```rust
/// ConfigPayload is a control-stream JSON message (newline-delimited). Like all
/// control messages it carries a "type" discriminator; here type == "config".
/// Wire JSON keys are preserved exactly (the browser client parses them).
#[derive(serde::Serialize, serde::Deserialize)]
pub struct ConfigPayload {
    #[serde(rename = "type")] pub kind: String,                 // always "config"
    pub version: u32,
    pub codec: String,                                          // full WebCodecs string
    pub width: u32,
    pub height: u32,
    pub fps: u32,
    pub hdr: bool,
    pub color_space: String,                                    // "bt709" | "bt2020"
    pub chroma: String,                                         // "420" | "422" | "444" (negotiated)
    pub audio: bool,
    #[serde(rename = "audioCodec")] pub audio_codec: String,    // "opus" | "pcm/s16le"
    #[serde(rename = "audioSampleRate")] pub audio_sample_rate: u32,
    #[serde(rename = "audioChannels")] pub audio_channels: u8,  // 1..8 (stereo / 5.1 / 7.1)
    #[serde(rename = "audioLayout")] pub audio_layout: String,  // "stereo" | "5.1" | "7.1"
    #[serde(rename = "cursorMode")] pub cursor_mode: String,    // "separate" | "embedded"
    pub session_token: String,                                  // for reconnection
    pub session_ttl_sec: u32,
    pub resumed: bool,                                          // true on successful resume
}

/// CursorUpdate is the payload of a CURSOR_UPDATE (type 11) message.
/// Binary little-endian: [x:u16][y:u16][visible:u8][image_changed:u8][w:u16][h:u16][rgba...]
pub struct CursorUpdate {
    pub x: u16,
    pub y: u16,
    pub visible: bool,
    pub image_changed: bool,
    pub w: u16,
    pub h: u16,
    pub rgba: Vec<u8>, // present only when image_changed
}
```

These live in `featherdesk-protocol` alongside `FrameHeader`, shared by server and (conceptually) any native client.

### R-PRO-02: Add Bounds Check to MarshalHeader
`MarshalHeader` should return an error if `buf` is shorter than `HeaderSize`, rather than panicking. This is a low-cost check (single comparison) that prevents crashes from propagating.

### R-PRO-03: Audio Header Semantics (overload retired)
The old Width/Height overload (sample rate / channel count) is **gone**. Audio's `FrameHeader.Width`/`Height` are now `0`; codec, sample rate, and channel count are advertised once in the `config` message (`audioCodec`, `audioSampleRate`, `audioChannels`). The audio `FrameHeader` is still present (it's a media type) for its capture `Timestamp` + the independent audio `Sequence`.

### R-PRO-04: Annex B Per-Frame Concatenation (REVISED from round 1)
Serialize each video frame as ONE message containing the **complete access unit** in Annex B (start codes retained), all NALs concatenated. Do NOT length-prefix and do NOT split NALs across messages. Rationale: the browser `VideoDecoder` consumes a whole access unit as a single `EncodedVideoChunk` and never iterates NALs, so length-prefixing adds client-side AVCC `avcC` complexity for zero benefit. This also makes keyframe caching trivially correct (the IDR message already contains SPS+PPS+IDR).

> This reverses the round-1 length-prefix decision. If a non-browser native client is ever added that needs O(1) NAL access, length-prefixing can be offered as an opt-in via a Config capability flag — but the default and only browser-facing format is Annex B.

### R-PRO-05: Canonical Monotonic Clock for All Media Timestamps
Define a single process-wide `CLOCK_MONOTONIC` epoch. Every video frame (from every capture backend) and every audio chunk MUST be stamped from this clock, in nanoseconds, **at capture time**. The pipeline must NOT stamp audio at channel-read time. This is a hard requirement for A/V sync — mixing wall-clock (`UnixMilli`) and monotonic, or ms and ns, silently breaks sync.

### R-PRO-06: Server Owns Sequence Counters
The server is the sole owner of the video and audio sequence counters (independent, per type). They are assigned in `Broadcast`/`BroadcastAudio` at send time. The pipeline must NOT maintain its own frame sequence counter.

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
| Unit | Sequence number overflow/wrap behavior | No |
| Unit | Deterministic encoding (same input → same bytes) | No |
| Unit | Buffer reuse safety | No |
| Unit | Annex B access-unit assembly (SPS+PPS+IDR ordering, start codes) | No |
| Unit | Control-stream JSON parsing both directions (auth, config, keyframe, pong, stats, resize, set_*) | No |
| Unit | Clipboard-stream JSON parsing (offer/data, >4 KiB payloads up to 1 MiB cap) | No |
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
