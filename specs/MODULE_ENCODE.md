# Module Spec: Encode (Software Encoder Interface Contract)

## Overview

The Encode module defines the **abstract software encoder interface contract**
that every SW encoder add-on implements. It owns no encoder implementation
itself — concrete encoders live in their respective add-on specs:

- `internal/encode/openh264/` — Cisco OpenH264 CGo (build tag `openh264`, BSD)
- `internal/encode/x264/` — x264 via ffmpeg subprocess (build tag `x264`, GPL-isolated)
- `internal/encode/vt/` — VideoToolbox SW (build tag `vt_sw`, macOS-only)

This separation keeps the module spec stable while allowing add-on
implementations to evolve independently.

> **Hardware encoders** implement a separate contract in
> [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md) (zero-copy GPU
> surface input). Both contracts produce identical `EncodedFrame` output for
> the pipeline; the dispatch decision lives in
> [`MODULE_PIPELINE.md`](./MODULE_PIPELINE.md).

---

## Public Interface

```go
package encode

// Encoder is the contract every software encoder add-on must satisfy.
type Encoder interface {
    // Encode takes a YUV I420 frame and returns the encoded bitstream as
    // contiguous Annex B bytes (start codes retained) plus a keyframe flag.
    // The data is ONE complete access unit -- NO per-NAL splitting (the
    // [][]byte per-NAL contract is rejected).
    //
    // keyframe is set by the encoder itself (it knows when it emitted an
    // IDR/IRAP) — the server does NOT re-scan the bitstream. This flag becomes
    // stream.EncodedFrame.Keyframe, driving the IDR cache + bootstrap stream.
    //
    // Returns nil, false, nil if the frame was intentionally skipped (rate
    // control). The returned buffer is from a sync.Pool and must not be
    // retained after the next Encode() call (the caller copies it into the
    // broadcast access unit before re-calling).
    Encode(frame *I420Frame) (data []byte, keyframe bool, err error)

    // ForceKeyframe requests that the next encoded frame be an IDR. This is the
    // ONLY method safe to call concurrently with Encode: implementations set an
    // atomic flag that the frame loop reads at the next Encode. All other
    // mutations go through UpdateStreamParams on the frame-loop goroutine.
    ForceKeyframe()

    // Close releases all encoder resources (including subprocesses for
    // out-of-process add-ons like x264).
    Close() error
}

// I420Frame holds planar YUV 4:2:0 data — the universal input format.
type I420Frame struct {
    Y      []byte // Luma plane (width * height bytes)
    U      []byte // Chroma-U plane (width/2 * height/2 bytes)
    V      []byte // Chroma-V plane (width/2 * height/2 bytes)
    Width  int
    Height int
}

// EncoderConfig holds the encoder's INITIAL configuration. Once running,
// dynamic parameters (width, height, fps, bitrate, qp, HDR) flow through
// the stream.Params contract and the ConfigurableEncoder interface (see
// MODULE_STREAM_PARAMS.md).
//
// Per-add-on STATIC tuning (preset, threads, profile) comes from the
// [addon_module_<tag>] TOML section. This struct holds only the initial
// dynamic values needed for first-frame encoding.
type EncoderConfig struct {
    InitialParams stream.Params  // initial Width/Height/FPS/BitrateBps/QP/HDR/etc.
}

// ConfigurableEncoder lets the pipeline change stream parameters at runtime
// without restarting the encoder. Add-ons that don't implement this
// interface are torn down + recreated whenever parameters change.
type ConfigurableEncoder interface {
    Encoder
    // UpdateStreamParams applies new parameters to the running encoder.
    // Called ONLY on the frame-loop goroutine, serialized with Encode via the
    // pipeline's param-change channel (MODULE_PIPELINE) — so implementations
    // need no locking between Encode and UpdateStreamParams. Returns
    // stream.ErrRequiresRestart if the change cannot be applied mid-stream
    // (caller tears down and recreates the encoder).
    UpdateStreamParams(p stream.Params) error
}

// Converter handles pixel format -> I420 color space conversion.
// Required by the software path because every SW encoder accepts I420.
// HW encoders bypass this entirely (they consume GPU surface handles).
type Converter struct {
    // Not an interface — single implementation in internal/encode/convert/.
    // Selects libyuv conversion function based on input pixel format:
    //   PixelBGRA (macOS/Windows) → libyuv ARGBToI420
    //   PixelRGBA (Linux GL)      → libyuv ABGRToI420
}

// Convert transforms pixel data to I420 based on the frame's PixelFmt.
// Returned *I420Frame is reused on next call (zero-alloc steady state).
func (c *Converter) Convert(f *capture.Frame) *I420Frame
func (c *Converter) Close()
```

### Bitstream Output Contract

`Encode()` returns a **contiguous Annex B bitstream** (start codes `00 00 00 01`
retained) and a `keyframe bool`. A keyframe access unit contains SPS + PPS + IDR
(H.264) or VPS + SPS + PPS + IDR (HEVC, e.g. VideoToolbox SW on macOS 12+) in
order. The output is NOT split per-NAL -- this avoids the decompose/recompose
copy overhead. The server prepends the 22-byte `FrameHeader` and queues the
bitstream as one whole access unit (the datagram pump fragments it later). The
buffer is from a `sync.Pool` -- steady-state encoding is zero-alloc after warmup.

---

## Encoder Selection (Pipeline Owns This)

The Encode module does **not** decide which encoder to use. That dispatch lives
in [`MODULE_PIPELINE.md`](./MODULE_PIPELINE.md), which:

1. Reads `[encode]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes each compiled-in HW encoder add-on (NVENC, AMF, libva, MF HW, QSV, VT HW)
3. Falls through to compiled-in SW encoder add-ons (x264 > VT SW > OpenH264)
4. Calls the chosen add-on's constructor with `EncoderConfig`
5. Passes the resulting `Encoder` to the frame loop

There is **no `EncoderBackend` enum** in this module. Selection is purely
runtime — the compiled-in set of add-ons determines what's available, and
the TOML config decides how to choose among them.

---

## Color Space Conversion

Every SW encoder accepts I420. Capture add-ons produce BGRA (CPU readback path)
or GPU surfaces (zero-copy HW path). For the SW path, BGRA must be converted
to I420 via libyuv:

```
BGRA []byte → libyuv ARGBToI420() → Y/U/V planes   (macOS, Windows)
RGBA []byte → libyuv ABGRToI420() → Y/U/V planes   (Linux GL)
```

- Links: `-lyuv`
- SIMD-optimized (SSE2/AVX2/NEON depending on platform)
- Pre-allocated output buffers (zero per-frame allocation)
- Color matrix: BT.601 limited range
- libyuv naming convention: names are by 32-bit register value (big-endian),
  NOT memory byte order. So BGRA-in-memory = libyuv "ARGB", RGBA-in-memory
  = libyuv "ABGR". The Converter selects based on `Frame.PixelFmt`.

The Converter is **shared across all SW encoder add-ons** — it lives in
`internal/encode/convert/` and is built unconditionally when any SW encoder
build tag is enabled.

---

## Per-Add-On Implementation Pointers

Each SW encoder add-on owns its own spec. The Encode module spec is the
interface contract above; the implementation details, performance numbers,
licensing, build tags, and CGo / subprocess details all live in the add-on
specs.

| Add-on | Build tag | License | Linux | macOS | Windows |
|--------|-----------|---------|-------|-------|---------|
| OpenH264 CGo | `openh264` | BSD-2 (Cisco) | [`ADD-ON-SPECS/Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md) | [`ADD-ON-SPECS/macOS/encoders/SW/OPENH264_CGO_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/SW/OPENH264_CGO_MACOS_SPEC.md) | [`ADD-ON-SPECS/Windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md) |
| x264 subprocess | `x264` | GPL-2 (isolated) | [`ADD-ON-SPECS/Linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md) | [`ADD-ON-SPECS/macOS/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md) | [`ADD-ON-SPECS/Windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md) |
| VideoToolbox SW | `vt_sw` | Apple system | — | [`ADD-ON-SPECS/macOS/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) | — |

---

## What This Module Does NOT Do

To avoid leaking implementation details into the interface contract, the
Encode module deliberately excludes:

- **No backend enum.** Selection is by compiled build tags + runtime config.
- **No subprocess management.** The x264 add-on owns its ffmpeg subprocess
  lifecycle internally; the pipeline sees only the `Encoder` interface.
- **No codec parameters beyond `EncoderConfig`.** Per-add-on tuning (x264
  preset, OpenH264 slice count, VT realtime flag) lives in
  `[addon_module_<tag>]` TOML sections.
- **No server-side NAL parsing.** Encoders return one Annex B access unit **plus
  a `keyframe bool`** (the encoder knows when it produced an IDR/IRAP). The
  server trusts that flag for IDR caching + the bootstrap stream and never
  re-scans NALs. (The client independently inspects the first VCL NAL only to
  set the WebCodecs key/delta hint — see MODULE_PROTOCOL "Video Payload Framing".)
- **No rate control switching.** Each add-on implements its own RC mode
  selection from `EncoderConfig.InitialParams.BitrateBps` (0 = QP mode)
  and its own TOML section.

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| OpenH264 CGo | ✅ Working in current code; refactor moves to `internal/encode/openh264/` under `openh264` build tag |
| x264 subprocess | ✅ Benchmarked via ffmpeg pipe (3.3ms @ 1080p on Ryzen 9 5900X); implementation pending |
| VideoToolbox SW | 📋 Specced; macOS native benchmarks pending |

The old `internal/encode/{ffmpeg,vp8,vaapi}.go` files (FFmpeg subprocess
encoder, libvpx VP8 via libavcodec, VA-API probe stub) are **rejected** and
will be removed as part of the implementation refactor. They served the
pre-pluggable architecture and are superseded by:

- FFmpeg subprocess (in-process libavcodec) → replaced by `x264` subprocess add-on
- VP8 → rejected codec (no demand, libavcodec dependency)
- VA-API probe → moved into `libva` HW encoder add-on
