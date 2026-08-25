# Data Flow — One Video Frame, Capture to Decode

Traces a single frame through both encode paths, per
`specs/media/MODULE_CAPTURE.md`, `MODULE_ENCODE.md`, `MODULE_HARDWARE_ENCODE.md`,
`specs/core/MODULE_SERVER.md`, and `MODULE_TRANSPORT.md`. The two paths are
mutually exclusive per session (the Pipeline picks one encoder) but converge on
an identical `EncodedUnit` → `EncodedFrame` → wire contract.

```mermaid
flowchart TD
    START(["Capturer::next_frame() /\nnext_surface()"])

    START -->|"CPU path:\nCPU-resident Frame\n(BGRA/RGBA + stride)"| CONV["Converter\nlibyuv ARGBToI420 / ABGRToI420\n(SW path only)"]
    START -->|"Zero-copy path:\nFbInfo (DMA-BUF fd /\nIOSurface / D3D11Texture)"| HWSURF["GPU-resident surface\n(never touches CPU memory)"]

    CONV --> SWENC["Encoder::encode(&I420Frame)\n(x264 / vt_sw / openh264)"]
    HWSURF --> HWENC["HardwareEncoder::encode_surface(FbInfo)\n(libva / nvenc / amf / qsv / vt_hw)\nFbInfo consumed by value -- Drop\nreleases the surface exactly once"]

    SWENC --> UNIT["EncodedUnit\n{ data: Annex B access unit, keyframe: bool }"]
    HWENC --> UNIT

    UNIT --> FRAME["stream::EncodedFrame\n(+ width, height, timestamp_ns, codec_type)"]
    FRAME --> WRAP["Server: prepend 22-byte FrameHeader\n(Version, Type, Sequence, Timestamp, W, H, PayloadSize)"]
    WRAP --> IDRCHECK{"f.keyframe?"}
    IDRCHECK -->|yes| IDRCACHE["Store as cached IDR under idr_mu\n(for fast-join / resume)"]
    IDRCHECK -->|no or yes| QUEUE["Per-session out-queue\n(assembled access units, cap ~8, drop-oldest)\nFRAME-GRANULAR, not fragment-granular"]
    IDRCACHE --> QUEUE

    QUEUE --> PUMP["datagram_pump: fragment into\n~1192-byte chunks, N ~= frame_bytes/1192"]
    PUMP --> SEND["Transport::send_datagram()\nper fragment, back-to-back"]
    SEND --> NET(["QUIC datagrams over the wire\n(fire-and-forget, best-effort)"])

    NET --> REASSEMBLE["Client: buffer fragments by (Type, FrameID)\nuntil complete or reassembly deadline"]
    REASSEMBLE -->|complete| DECODE["WebCodecs decode\n(avc1.* / hvc1.* / av01.* per Config codec string)"]
    REASSEMBLE -->|deadline expires| DROP["Drop partial frame,\nrequest keyframe on control stream"]
```

**Why the two paths converge so early.** The Server, IDR cache, datagram
fragmentation, and client reassembly logic are all encoder-agnostic — they
operate on `EncodedFrame`/`EncodedUnit`, never on pixels or GPU handles. This
is deliberate: it means the entire back half of the pipeline (from `EncodedUnit`
onward) is implemented and tested exactly once, regardless of which of the six
encoder add-ons produced the frame.

**Ownership discipline on the hardware path.** `FbInfo` is moved *by value*
into `encode_surface`; its `Drop` releases the GPU resource exactly once on
every outcome (success, error, or `StreamError::FallbackToSoftware`) — this is
the fix for the old Go code's TD-01 DMA-BUF file-descriptor leak, where cleanup
depended on manually calling a `Release func()` on every exit path.
