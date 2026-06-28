# Module Spec: Hardware Encode (Cross-Platform Interface Contract)

## Overview

The Hardware Encode module defines the **abstract zero-copy hardware encoder
interface contract** that every HW encoder add-on implements. It owns no
encoder implementation itself — concrete encoders live in their respective
add-on specs:

- `addons/encode/libva/` — Linux VA-API (add-on ID `libva`, MIT)
- `addons/encode/nvenc/` — NVENC SDK direct (add-on ID `nvenc`, NVIDIA proprietary)
- `addons/encode/amf/` — AMD AMF (add-on ID `amf` Windows, `amf_rocm` Linux, Apache 2.0)
- `addons/encode/mf_hw/` — Windows MediaFoundation (add-on ID `mf_hw`, Microsoft system)
- `addons/encode/qsv/` — Intel oneVPL / QSV (add-on ID `qsv`, MIT)
- `addons/encode/vt/` — macOS VideoToolbox HW (add-on ID `vt_hw`, Apple system)

> **Software encoders** implement a different contract in
> [`MODULE_ENCODE.md`](./MODULE_ENCODE.md). Hardware encoders consume GPU
> surface handles directly (zero-copy); software encoders consume CPU-resident
> I420 frames. Both produce identical `EncodedFrame` output for the pipeline.

---

## Public Interface

```rust
// crate: featherdesk-hwencode

// The opaque GPU-resident input to a hardware encoder is `capture::FbInfo`,
// which carries a `SurfaceHandle` tagged enum — the HW encoder add-on `match`es
// on the runtime SurfaceHandle variant (there is no separate `SurfaceHandle`
// alias here anymore; CENTRAL's SurfaceHandle IS the enum inside FbInfo).
//
// Platform mapping:
//   Linux:   DMA-BUF fd from KMS+EGL or NvFBC, imported via VA-API or NVENC SDK
//   macOS:   IOSurface from ScreenCaptureKit, consumed by VTCompressionSession
//   Windows: ID3D11Texture2D from DXGI Desktop Duplication, consumed by MF / NVENC / AMF / QSV
//
// This is the SAME type as capture::FbInfo, re-exported here for clarity.
pub use capture::FbInfo;

// EncodedUnit (ONE contiguous Annex B access unit + keyframe flag) is the
// encoder's direct output, shared by the SW and HW paths (see MODULE_ENCODE).
// The pipeline wraps it into stream::EncodedFrame, which lives in
// featherdesk-stream — shared by SW and HW paths:
//
//   pub struct EncodedFrame {
//       pub data: bytes::Bytes,// Contiguous Annex B bitstream (NOT split per-NAL), host-side
//       pub width: u16,
//       pub height: u16,
//       pub timestamp_ns: u64,
//       pub keyframe: bool,
//       pub codec_type: u8,    // frame_type::VIDEO_H264 or frame_type::VIDEO_HEVC
//   }
pub use stream::{EncodedFrame, EncodedUnit};

// HardwareEncoder is the contract every HW encoder add-on must satisfy.
// Cleanup is RAII (Drop) — no Close() (Drop releases all encoder resources:
// D3D11 / VA / MF / Metal contexts).
pub trait HardwareEncoder {
    /// encode_surface CONSUMES a GPU-resident surface (moved in BY VALUE) and
    /// returns one encoded access unit (H.264 or HEVC) as an EncodedUnit with
    /// `keyframe` set BY THE ENCODER.
    ///
    /// SURFACE OWNERSHIP (single owner — RAII resolves the prior 4-way
    /// ambiguity): the FbInfo is moved in and its Drop releases the resource
    /// exactly ONCE on EVERY path — success, error, AND
    /// StreamError::FallbackToSoftware. The capturer does NOT release it; the
    /// pipeline does NOT release it. After encode_surface returns the surface is
    /// already gone — this is the M-1/TD-01 win (released exactly once on every
    /// path, automatically).
    ///
    /// The input surface is at native capture resolution; the encoder scales it
    /// on-GPU to initial_params.width/height (the Config dims) so its output
    /// always matches the advertised stream dimensions (see "Scaling invariant").
    ///
    /// Returns StreamError::FallbackToSoftware if the surface cannot be imported
    /// (format mismatch, GPU reset, driver constraint) — and STILL drops (and so
    /// releases) the surface. The pipeline catches this and switches to the SW
    /// path for the session.
    fn encode_surface(&mut self, surface: FbInfo) -> Result<EncodedUnit, StreamError>;

    /// force_keyframe requests that the next encoded frame be an IDR. The only
    /// method safe to call concurrently with encode_surface (signalled via an
    /// AtomicBool/channel read by the frame loop); all other mutations go
    /// through update_stream_params.
    fn force_keyframe(&mut self);

    /// codec returns the WebCodecs codec string for the Config handshake
    /// (e.g. "avc1.42E01F" for H.264 Constrained Baseline 3.1, "hvc1.*"
    /// for HEVC variants).
    fn codec(&self) -> &str;
}

// HWEncoderConfig holds the encoder's INITIAL configuration. Once running,
// dynamic parameters flow through the stream::Params contract and the
// ConfigurableHardwareEncoder trait (see MODULE_STREAM_PARAMS.md).
pub struct HWEncoderConfig {
    pub initial_params: stream::Params,  // initial width/height/fps/bitrate_bps/qp/HDR/etc.
    pub codec_hint: String,              // "h264" | "hevc" | "av1" — add-on picks the best match
}

// ConfigurableHardwareEncoder lets the pipeline change stream parameters
// at runtime. HW encoders that don't implement this are torn down + recreated.
pub trait ConfigurableHardwareEncoder: HardwareEncoder {
    /// Called only on the frame-loop task, serialized with encode_surface via
    /// the pipeline's param-change channel — no locking vs encode_surface.
    /// Returns StreamError::RequiresRestart if the change needs a fresh session.
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), StreamError>;
}

// StreamError::FallbackToSoftware lives in the stream crate (not hwencode) to
// avoid dependency cycles -- capture add-ons also need to return it when
// GPU surface export fails.
// Defined at: stream::StreamError::FallbackToSoftware
// See MODULE_STREAM_PARAMS.md for all error variants.
```

### Bitstream Output Contract

Identical to the SW path (see MODULE_ENCODE):
- `EncodedFrame.data` is ONE **contiguous** Annex B access unit (start code
  `00 00 00 01` retained) — **NOT** split per-NAL. (The old "one NAL per
  `Vec<u8>`" contract is rejected.)
- Keyframe access unit contains SPS + PPS + IDR (H.264) or VPS + SPS + PPS + IDR
  (HEVC) concatenated.
- `EncodedFrame.keyframe` is set by the encoder; the server never re-scans NALs.

---

## Zero-Copy Pipeline

```
Capture add-on              Hardware encoder add-on
┌──────────────────┐        ┌────────────────────────┐
│ next_surface()   │───────>│ encode_surface(surface)│
│                  │surface │                        │
│ Returns          │(moved) │ Imports GPU resource   │
│ FbInfo           │        │ scale + encode on-GPU  │
│ (DMA-BUF /       │        │ returns 1 access unit  │
│  IOSurface /     │        │ surface dropped: freed │
│  D3D11 texture)  │        │ once  ~30KB compressed │
└──────────────────┘        └────────────────────────┘
                                       │
                                       v
                            ┌────────────────────────┐
                            │ EncodedFrame to server │
                            │ (only the compressed   │
                            │  bitstream crosses     │
                            │  GPU→CPU boundary)     │
                            └────────────────────────┘
```

Bandwidth across GPU→CPU boundary: ~30 KB compressed bitstream per frame
(vs ~8 MB uncompressed BGRA in the CPU readback path).

### Scaling invariant

Capture add-ons always produce a surface at the display's **native** resolution.
When the stream runs at a lower resolution (adaptive downscale, or a fixed
`[stream]` width/height), the HW encoder scales the surface **in-encoder** (VPP /
MFT scaler / VideoToolbox scaling) to `initial_params.width × height`. The SW path
achieves the same via libyuv `I420Scale` (see MODULE_ENCODE). The invariant the
rest of the system relies on:

> **encoder-output dims == `config` dims == input-coordinate range.**

So the client's input scaling (which maps pointer coordinates into the advertised
`config` width/height) is always correct regardless of native capture size.

### Chroma subsampling

HW encoders subsample **inside the encoder** — the input GPU surface is RGB/4:4:4,
and the encoder produces `Params.chroma_subsampling` (4:2:0 default, or 4:2:2/4:4:4
where the silicon supports it: NVENC 4:4:4 on Turing+, others vendor-dependent).
No CPU converter is involved. The add-on **advertises its supported chroma** in its
probe capabilities and returns `StreamError::ChromaUnsupported` if asked for one it
can't emit, so the pipeline falls back to 4:2:0 (see MODULE_STREAM_PARAMS). The
client-side decode gate (`isConfigSupported` → `chroma_unsupported`) is identical
to the SW path.

---

## Selection (Pipeline Owns This)

The Hardware Encode module does **not** decide which HW encoder to use.
That dispatch lives in [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md), which:

1. Reads `[encode]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes loaded HW add-ons in priority order — vendor-specific SDKs
   before generic abstractions (NVENC > libva on NVIDIA; AMF > libva on AMD;
   MF HW falls back to whatever vendor MFT is registered)
3. Verifies the capture add-on's `SurfaceHandle` variant is acceptable to the
   chosen HW encoder
4. On `StreamError::FallbackToSoftware`, switches to a SW encoder add-on for the
   remainder of the session

There is **no `HWCodec` enum** in this module. Codec selection is by the
add-on advertising what it supports via `codec()` and the pipeline picking
based on browser handshake preferences.

---

## Per-Add-On Implementation Pointers

Each HW encoder add-on owns its own spec. The Hardware Encode module spec is
the interface contract above; implementation details, performance numbers,
licensing, add-on IDs, and Rust FFI specifics all live in the add-on specs.

| Add-on | Add-on ID | Linux | macOS | Windows |
|--------|-----------|-------|-------|---------|
| libva (VA-API) | `libva` | [`specs/addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md`](../addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md) | — | — |
| NVENC | `nvenc` | [`specs/addons/linux/encoders/HW/NVENC_LINUX_SPEC.md`](../addons/linux/encoders/HW/NVENC_LINUX_SPEC.md) | — | [`specs/addons/windows/encoders/HW/NVENC_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/NVENC_WINDOWS_SPEC.md) |
| AMF | `amf_rocm` (Linux) / `amf` (Windows) | [`specs/addons/linux/encoders/HW/AMF_ROCM_SPEC.md`](../addons/linux/encoders/HW/AMF_ROCM_SPEC.md) | — | [`specs/addons/windows/encoders/HW/AMF_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/AMF_WINDOWS_SPEC.md) |
| MediaFoundation HW | `mf_hw` | — | — | [`specs/addons/windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md) |
| QSV (oneVPL) | `qsv` | — | — | [`specs/addons/windows/encoders/HW/QSV_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/QSV_WINDOWS_SPEC.md) |
| VideoToolbox HW | `vt_hw` | — | [`specs/addons/macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](../addons/macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md) | — |

---

## What This Module Does NOT Do

- **No platform-specific code in the interface.** No DMA-BUF imports, no
  VA-API context creation, no MFT setup. Those all live in add-on specs.
- **No subprocess management.** Hardware encoders are always in-process via
  Rust FFI. (Subprocess-isolated GPL is only relevant for SW x264.)
- **No codec selection algorithm.** Each add-on advertises supported codecs
  via `codec()`; the pipeline matches against browser handshake preferences.
- **No keyframe interval logic.** Periodic IDRs are not configured — every
  IDR is on-demand via `force_keyframe()` (triggered by client gap detection).

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| NVENC (Windows) | ✅ Benchmarked: GTX 1080 Ti 4.5ms @ 1080p, Quadro RTX 4000 5.4ms |
| AMF (Windows) | ✅ Benchmarked: RX 6800 XT 5.9ms H.264 / 5.0ms HEVC @ 1080p |
| MF HW (Windows) | ✅ Benchmarked: 6.9-7.7ms @ 1080p depending on GPU |
| libva (Linux) | 📋 Specced; Rust FFI bindings pending |
| NVENC (Linux) | 📋 Specced; Rust FFI bindings pending |
| AMF (Linux ROCm) | 📋 Specced; Rust FFI bindings pending |
| QSV (Windows) | 📋 Specced (on good-faith; no Intel hardware to benchmark) |
| VideoToolbox HW (macOS) | 📋 Specced; Hackintosh measured 8.6ms @ 1080p (AMD VCE) |
