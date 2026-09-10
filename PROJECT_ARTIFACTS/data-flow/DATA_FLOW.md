# Data Flow — One Video Frame, Capture to Decode

Traces a single frame through both encode paths, per
`specs/media/MODULE_CAPTURE.md`, `MODULE_ENCODE.md`, `MODULE_HARDWARE_ENCODE.md`,
`specs/core/MODULE_SERVER.md`, and `MODULE_TRANSPORT.md`. The two paths are
mutually exclusive per session (the Pipeline picks one encoder) but converge on
an identical `EncodedUnit` → `EncodedFrame` → wire contract.

```mermaid
flowchart TD
    START(["Capturer::next_frame() /<br/>next_surface()"])
    CURSOR(["CursorCapturer::next_cursor()<br/>polled BEFORE the frame-skip check"])

    START -->|"CPU path:<br/>CPU-resident Frame<br/>(BGRA/RGBA + stride)"| CONV["Converter<br/>libyuv ARGBToI420Matrix / ARGBToI422Matrix / ARGBToI444Matrix<br/>(kArgbH709Constants / kAbgrH709Constants, BT.709 limited)<br/>(SW path only)"]
    START -->|"Zero-copy path:<br/>FbInfo (DMA-BUF fd /<br/>IOSurface / D3D11Texture)"| HWSURF["GPU-resident surface<br/>(never touches CPU memory)"]

    CONV --> SWENC["Encoder::encode(&amp;YuvFrame)<br/>(openh264 / vt_sw / x264 opt-in)"]
    HWSURF --> HWENC["HardwareEncoder::encode_surface(FbInfo)<br/>(libva / nvenc / amf / qsv / vt_hw)<br/>FbInfo consumed by value &mdash; Drop<br/>releases the surface exactly once"]

    SWENC --> UNIT["EncodedUnit<br/>{ data: Annex B access unit, keyframe: bool, timestamp_ns }"]
    HWENC --> UNIT

    UNIT --> FRAME["stream::EncodedFrame<br/>(+ width, height, timestamp_ns, codec_type)"]
    FRAME --> WRAP["Server: prepend 22-byte FrameHeader<br/>(Version, Type, Sequence, Timestamp, W, H, PayloadSize)"]
    WRAP --> IDRCHECK{"f.keyframe?"}
    IDRCHECK -->|yes| IDRCACHE["Store as cached IDR under idr_mu<br/>with its sequence (fast-join / resume)"]
    IDRCHECK -->|no| QUEUE
    IDRCACHE --> QUEUE["Per-session out-queue<br/>(assembled access units, cap 8, drop-oldest)<br/>FRAME-GRANULAR, not fragment-granular"]

    QUEUE --> CARRIER{"carrier?"}
    CARRIER -->|"WebTransport (default)"| PUMP["datagram_pump: fragment into<br/>max_datagram_size() &minus; 8 byte chunks,<br/>re-read from the transport per access unit"]
    CARRIER -->|"WebSocket fallback"| WSMSG["one reliable 0x20 message per access unit<br/>(no fragmentation; TCP HOL applies)"]
    PUMP --> SEND["Transport::send_datagram()<br/>per fragment, back-to-back"]
    SEND --> NET(["QUIC datagrams over the wire<br/>(fire-and-forget, best-effort)"])
    WSMSG --> NET2(["WebSocket frames (reliable, ordered)"])

    NET --> REASSEMBLE["Client: buffer fragments by (Type, FrameID)<br/>until complete or reassembly deadline"]
    NET2 --> DECODE
    REASSEMBLE -->|complete| DECODE["WebCodecs decode<br/>(avc1.* / hvc1.* / av01.* per Config codec string)"]
    REASSEMBLE -->|deadline expires| DROP["Drop partial frame,<br/>request keyframe on control stream"]

    CURSOR --> CUR2["Server::send_cursor(CursorUpdate)<br/>14-byte datagram; a new bitmap goes on<br/>the cursor stream (tag 0x11) instead"]
    CUR2 --> NET
    NET --> OVERLAY["Client: cursor overlay<br/>(latest-wins; never blocks on a video frame)"]
```

**Why the two paths converge so early.** The Server, IDR cache, datagram
fragmentation, and client reassembly logic are all encoder-agnostic — they
operate on `EncodedFrame`/`EncodedUnit`, never on pixels or GPU handles. This
is deliberate: it means the entire back half of the pipeline (from `EncodedUnit`
onward) is implemented and tested exactly once, regardless of which of the six
encoder add-ons produced the frame.

**The cursor leaves the diagram's main spine on purpose.** `next_cursor()` is
polled on the frame-loop tick *before* the frame-skip check, and its output goes
straight to `send_cursor` — it never enters the converter, the encoder, the IDR
cache, or the out-queue. That is what keeps the pointer moving while video is
skipped, dropped, or static, and it is why a cursor-path error must not advance
the capture-error ladder (see CENTRAL_SPEC Contract 8).

**Ownership discipline on the hardware path.** `FbInfo` is moved *by value*
into `encode_surface`; its `Drop` releases the GPU resource exactly once on
every outcome (success, error, or `StreamError::FallbackToSoftware`). This
replaces the Go capturer's two-site fd discipline — `internal/capture/kms.go`
closes the previous DMA-BUF fd at the top of the next `NextFrame` and the last
one in `Close`, which is correct but depends on every early return leaving
exactly one fd in `lastDMAFD` (TD-01).
