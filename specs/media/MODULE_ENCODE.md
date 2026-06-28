# Module Spec: Encode (Software Encoder Interface Contract)

## Overview

The Encode module defines the **abstract software encoder interface contract**
that every SW encoder add-on implements. It owns no encoder implementation
itself — concrete encoders live in their respective add-on specs:

- `internal/encode/openh264/` — Cisco OpenH264 via Rust FFI (add-on ID `openh264`, BSD)
- `internal/encode/x264/` — x264 via ffmpeg subprocess (add-on ID `x264`, GPL-isolated)
- `internal/encode/vt/` — VideoToolbox SW (add-on ID `vt_sw`, macOS-only)

This separation keeps the module spec stable while allowing add-on
implementations to evolve independently.

> **Hardware encoders** implement a separate contract in
> [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md) (zero-copy GPU
> surface input). Both contracts produce identical `EncodedFrame` output for
> the pipeline; the dispatch decision lives in
> [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md).

---

## Public Interface

```rust
// crate: featherdesk-encode

// Encoder is the contract every software encoder add-on must satisfy.
// Cleanup is RAII (Drop) — no Close() (Drop releases all encoder resources,
// including subprocesses for out-of-process add-ons like x264).
pub trait Encoder {
    /// encode takes a YUV I420 frame and returns the encoded bitstream as one
    /// contiguous Annex B access unit (start codes retained) plus a keyframe
    /// flag, wrapped as an EncodedUnit. The data is ONE complete access unit --
    /// NO per-NAL splitting (the Vec<Vec<u8>> per-NAL contract is rejected).
    ///
    /// keyframe is set by the encoder itself (it knows when it emitted an
    /// IDR/IRAP) — the server does NOT re-scan the bitstream. This flag becomes
    /// stream::EncodedFrame::keyframe, driving the IDR cache + bootstrap stream.
    ///
    /// Returns Ok(None) if the frame was intentionally skipped (rate control).
    /// `unit.data` is an owned RVec<u8> whose ownership transfers to the caller
    /// (deterministic drop across the add-on ABI).
    fn encode(&mut self, frame: &I420Frame) -> Result<Option<EncodedUnit>, StreamError>;

    /// force_keyframe requests that the next encoded frame be an IDR. This is the
    /// ONLY method safe to call concurrently with encode: it is signalled to the
    /// frame loop via an AtomicBool/channel that the loop checks before the next
    /// encode. All other mutations go through update_stream_params on the
    /// frame-loop task; `&mut self` means encode and reconfigure can never alias
    /// (borrow-checker enforced).
    fn force_keyframe(&mut self);
}

// EncodedUnit is ONE contiguous Annex B access unit + a keyframe flag (shared
// with the HW path; the pipeline wraps it into stream::EncodedFrame).
pub struct EncodedUnit {
    pub data: RVec<u8>,  // ONE contiguous Annex B access unit (owned)
    pub keyframe: bool,
}

// I420Frame holds planar YUV 4:2:0 data — the universal input format.
pub struct I420Frame {
    pub y: Vec<u8>, // Luma plane (width * height bytes)
    pub u: Vec<u8>, // Chroma-U plane (width/2 * height/2 bytes)
    pub v: Vec<u8>, // Chroma-V plane (width/2 * height/2 bytes)
    pub width: u32,
    pub height: u32,
}

// EncoderConfig holds the encoder's INITIAL configuration. Once running,
// dynamic parameters (width, height, fps, bitrate, qp, HDR) flow through
// the stream::Params contract and the ConfigurableEncoder trait (see
// MODULE_STREAM_PARAMS.md).
//
// Per-add-on STATIC tuning (preset, threads, profile) comes from the
// [addon_module_<id>] TOML section. This struct holds only the initial
// dynamic values needed for first-frame encoding.
pub struct EncoderConfig {
    pub initial_params: stream::Params,  // initial width/height/fps/bitrate_bps/qp/HDR/etc.
}

// ConfigurableEncoder lets the pipeline change stream parameters at runtime
// without restarting the encoder. Add-ons that don't implement this
// trait are torn down + recreated whenever parameters change.
pub trait ConfigurableEncoder: Encoder {
    /// update_stream_params applies new parameters to the running encoder.
    /// Called ONLY on the frame-loop task, serialized with encode via the
    /// pipeline's param-change channel (MODULE_PIPELINE) — so `&mut self` plus
    /// single-task drive means no locking between encode and
    /// update_stream_params. Returns StreamError::RequiresRestart if the change
    /// cannot be applied mid-stream (caller tears down and recreates the
    /// encoder).
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), StreamError>;
}

// Converter handles pixel format -> I420 color space conversion.
// Required by the software path because every SW encoder accepts I420.
// HW encoders bypass this entirely (they consume GPU surface handles).
//
// Not a trait — single implementation in internal/encode/convert/.
// Selects the libyuv conversion function based on input pixel format:
//   PixelFormat::Bgra (macOS/Windows) → libyuv ARGBToI420
//   PixelFormat::Rgba (Linux GL)      → libyuv ABGRToI420
// libyuv is reached via Rust FFI (bindgen).
pub struct Converter { /* … */ }

impl Converter {
    /// convert transforms pixel data to I420 based on the frame's pixel_fmt.
    /// The returned I420Frame is reused on the next call (zero-alloc steady
    /// state) — hence the borrow tied to `&mut self`.
    pub fn convert(&mut self, f: &capture::Frame) -> &I420Frame { /* … */ }
}
// Drop on Converter releases its scratch buffers (RAII — no Close()).
```

### Bitstream Output Contract

`encode()` returns an `EncodedUnit` whose `data` is a **contiguous Annex B
bitstream** (start codes `00 00 00 01` retained) plus a `keyframe: bool`. A
keyframe access unit contains SPS + PPS + IDR (H.264) or VPS + SPS + PPS + IDR
(HEVC, e.g. VideoToolbox SW on macOS 12+) in order. The output is NOT split
per-NAL -- this avoids the decompose/recompose copy overhead. The server
prepends the 22-byte `FrameHeader` and queues the bitstream as one whole access
unit (the datagram pump fragments it later). `data` ownership transfers (owned
`RVec<u8>`) -- steady-state encoding is zero-alloc after warmup (the add-on
reuses internal scratch buffers).

---

## Encoder Selection (Pipeline Owns This)

The Encode module does **not** decide which encoder to use. That dispatch lives
in [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md), which:

1. Reads `[encode]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes each loaded HW encoder add-on (NVENC, AMF, libva, MF HW, QSV, VT HW)
3. Falls through to loaded SW encoder add-ons (x264 > VT SW > OpenH264)
4. Calls the chosen add-on's constructor with `EncoderConfig`
5. Passes the resulting `Encoder` to the frame loop

There is **no `EncoderBackend` enum** in this module. Selection is purely
runtime — the loaded set of add-ons determines what's available, and
the TOML config decides how to choose among them.

---

## Color Space Conversion

Every SW encoder accepts I420. Capture add-ons produce BGRA (CPU readback path)
or GPU surfaces (zero-copy HW path). For the SW path, BGRA must be converted
to I420 via libyuv:

```
BGRA &[u8] → libyuv ARGBToI420() → Y/U/V planes   (macOS, Windows)
RGBA &[u8] → libyuv ABGRToI420() → Y/U/V planes   (Linux GL)
```

- Links: `-lyuv`
- SIMD-optimized (SSE2/AVX2/NEON depending on platform)
- Pre-allocated output buffers (zero per-frame allocation)
- Color matrix: BT.601 limited range
- libyuv naming convention: names are by 32-bit register value (big-endian),
  NOT memory byte order. So BGRA-in-memory = libyuv "ARGB", RGBA-in-memory
  = libyuv "ABGR". The Converter selects based on `Frame.pixel_fmt`.

The Converter is **shared across all SW encoder add-ons** — it lives in
`internal/encode/convert/` and is built unconditionally when any SW encoder
add-on is loaded.

### Chroma subsampling (4:2:0 / 4:2:2 / 4:4:4)

`I420` is 4:2:0 — the default. For `Params.chroma_subsampling = "422"|"444"` the
path generalizes: the Converter emits **I422** (`*ToI422`) or **I444** (`*ToI444`)
instead, and the input frame carries its subsampling (the `I420Frame` type is the
4:2:0 case of a `YuvFrame { subsampling }`). The SW encoder must accept the matching
format:

- **OpenH264** is **4:2:0-only** → it returns `StreamError::ChromaUnsupported` for
  422/444; the pipeline falls back to 4:2:0 (see MODULE_STREAM_PARAMS).
- **x264** supports 4:2:0 / 4:2:2 / 4:4:4 (`-pix_fmt yuv420p|yuv422p|yuv444p` +
  the matching High profile).
- HW encoders subsample **inside the encoder** from the GPU surface (see
  MODULE_HARDWARE_ENCODE), so the converter is not involved on that path.

Each SW encoder add-on **advertises its supported chroma** in its probe
capabilities; the pipeline never asks an encoder for a chroma it can't produce.

---

## Per-Add-On Implementation Pointers

Each SW encoder add-on owns its own spec. The Encode module spec is the
interface contract above; the implementation details, performance numbers,
licensing, add-on IDs, and FFI / subprocess details all live in the add-on
specs.

| Add-on | Add-on ID | License | Linux | macOS | Windows |
|--------|-----------|---------|-------|-------|---------|
| OpenH264 (FFI) | `openh264` | BSD-2 (Cisco) | [`specs/addons/linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md`](../addons/linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md) | [`specs/addons/macos/encoders/SW/OPENH264_CGO_MACOS_SPEC.md`](../addons/macos/encoders/SW/OPENH264_CGO_MACOS_SPEC.md) | [`specs/addons/windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md`](../addons/windows/encoders/SW/OPENH264_CGO_WINDOWS_SPEC.md) |
| x264 subprocess | `x264` | GPL-2 (isolated) | [`specs/addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md`](../addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md) | [`specs/addons/macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md`](../addons/macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md) | [`specs/addons/windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](../addons/windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md) |
| VideoToolbox SW | `vt_sw` | Apple system | — | [`specs/addons/macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](../addons/macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) | — |

---

## What This Module Does NOT Do

To avoid leaking implementation details into the interface contract, the
Encode module deliberately excludes:

- **No backend enum.** Selection is by loaded add-ons + runtime config.
- **No subprocess management.** The x264 add-on owns its ffmpeg subprocess
  lifecycle internally; the pipeline sees only the `Encoder` interface.
- **No codec parameters beyond `EncoderConfig`.** Per-add-on tuning (x264
  preset, OpenH264 slice count, VT realtime flag) lives in
  `[addon_module_<id>]` TOML sections.
- **No server-side NAL parsing.** Encoders return one Annex B access unit **plus
  a `keyframe bool`** (the encoder knows when it produced an IDR/IRAP). The
  server trusts that flag for IDR caching + the bootstrap stream and never
  re-scans NALs. (The client independently inspects the first VCL NAL only to
  set the WebCodecs key/delta hint — see MODULE_PROTOCOL "Video Payload Framing".)
- **No rate control switching.** Each add-on implements its own RC mode
  selection from `EncoderConfig.initial_params.bitrate_bps` (0 = QP mode)
  and its own TOML section.

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| OpenH264 (FFI) | ✅ Working in current code; refactor moves to `internal/encode/openh264/` under add-on ID `openh264` |
| x264 subprocess | ✅ Benchmarked via ffmpeg pipe (3.3ms @ 1080p on Ryzen 9 5900X); implementation pending |
| VideoToolbox SW | 📋 Specced; macOS native benchmarks pending |

The old `internal/encode/{ffmpeg,vp8,vaapi}.rs` files (FFmpeg subprocess
encoder, libvpx VP8 via libavcodec, VA-API probe stub) are **rejected** and
will be removed as part of the implementation refactor. They served the
pre-pluggable architecture and are superseded by:

- FFmpeg subprocess (in-process libavcodec) → replaced by `x264` subprocess add-on
- VP8 → rejected codec (no demand, libavcodec dependency)
- VA-API probe → moved into `libva` HW encoder add-on
