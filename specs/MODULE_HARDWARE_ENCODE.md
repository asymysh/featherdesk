# Module Spec: Hardware Encode (Cross-Platform Interface Contract)

## Overview

The Hardware Encode module defines the **abstract zero-copy hardware encoder
interface contract** that every HW encoder add-on implements. It owns no
encoder implementation itself — concrete encoders live in their respective
add-on specs:

- `internal/encode/libva/` — Linux VA-API (build tag `libva`, MIT)
- `internal/encode/nvenc/` — NVENC SDK direct (build tag `nvenc`, NVIDIA proprietary)
- `internal/encode/amf/` — AMD AMF (build tag `amf` Windows, `amf_rocm` Linux, Apache 2.0)
- `internal/encode/mf/` — Windows MediaFoundation (build tag `mf_hw`, Microsoft system)
- `internal/encode/qsv/` — Intel oneVPL / QSV (build tag `qsv`, MIT)
- `internal/encode/vt/` — macOS VideoToolbox HW (build tag `vt_hw`, Apple system)

> **Software encoders** implement a different contract in
> [`MODULE_ENCODE.md`](./MODULE_ENCODE.md). Hardware encoders consume GPU
> surface handles directly (zero-copy); software encoders consume CPU-resident
> I420 frames. Both produce identical `EncodedFrame` output for the pipeline.

---

## Public Interface

```go
package hwencode

// SurfaceHandle is the opaque GPU-resident input to a hardware encoder.
// Per-platform concrete types — the HW encoder add-on type-switches on
// the runtime platform.
//
// Platform mapping:
//   Linux:   DMA-BUF fd from KMS+EGL or NvFBC, imported via VA-API or NVENC SDK
//   macOS:   IOSurface from ScreenCaptureKit, consumed by VTCompressionSession
//   Windows: ID3D11Texture2D from DXGI Desktop Duplication, consumed by MF / NVENC / AMF / QSV
//
// This is the SAME struct as capture.FBInfo, exported here for clarity.
type SurfaceHandle = capture.FBInfo

// EncodedFrame is what a HardwareEncoder produces — identical to what an
// encode.Encoder (SW) produces, so the pipeline doesn't care which path
// produced the bitstream.
type EncodedFrame struct {
    NALs      [][]byte // Annex B H.264 NAL units
    Width     uint16
    Height    uint16
    Timestamp uint64   // CLOCK_MONOTONIC ns, carried through from capture
    Keyframe  bool     // True if this access unit contains SPS+PPS+IDR
}

// HardwareEncoder is the contract every HW encoder add-on must satisfy.
type HardwareEncoder interface {
    // EncodeSurface takes a GPU-resident surface and returns encoded H.264.
    // The surface is owned by the caller after EncodeSurface returns —
    // the encoder releases its internal reference.
    //
    // Returns ErrFallbackToSoftware if the surface cannot be imported
    // (format mismatch, GPU reset, driver constraint). The pipeline
    // catches this and switches to the SW path for the rest of the session.
    EncodeSurface(handle *SurfaceHandle) (*EncodedFrame, error)

    // ForceKeyframe requests that the next encoded frame be an IDR.
    // Thread-safe.
    ForceKeyframe()

    // Codec returns the WebCodecs codec string for the Config handshake
    // (e.g. "avc1.42E01E" for H.264 Constrained Baseline 3.0, "hvc1.*"
    // for HEVC variants).
    Codec() string

    // Close releases all encoder resources (D3D11 / VA / MF / Metal contexts).
    Close() error
}

// HWEncoderConfig holds codec-agnostic HW encoder parameters.
// Per-add-on tuning comes from [addon_module_<tag>] TOML sections.
type HWEncoderConfig struct {
    Width      int
    Height     int
    FPS        int
    BitrateBps int // 0 = use QP mode
    QP         int // 0-51 for H.264, codec-dependent ranges otherwise
    CodecHint  string // "h264" | "hevc" | "av1" — add-on picks the best match
}

// ErrFallbackToSoftware is returned when the HW encoder cannot proceed
// (surface import failure, driver constraint, GPU reset). The pipeline
// catches this once per session and switches to the SW path permanently.
var ErrFallbackToSoftware = errors.New("hardware encode unavailable; fall back to SW")
```

### NAL Output Contract

Identical to the SW path (see MODULE_ENCODE):
- Annex B form (start code `00 00 00 01` retained)
- Keyframe access unit contains SPS + PPS + IDR concatenated
- One NAL unit per `[]byte` element

---

## Zero-Copy Pipeline

```
Capture add-on              Hardware encoder add-on
┌──────────────────┐        ┌────────────────────────┐
│ NextDMABuf()     │───────>│ EncodeSurface(handle)  │
│                  │ handle │                        │
│ Returns          │        │ Imports GPU resource   │
│ SurfaceHandle    │        │ encodes on-GPU         │
│ (DMA-BUF /       │        │ returns NALs           │
│  IOSurface /     │        │                        │
│  D3D11 texture)  │        │ ~30KB compressed       │
└──────────────────┘        └────────────────────────┘
                                       │
                                       v
                            ┌────────────────────────┐
                            │ EncodedFrame to server │
                            │ (only NALs cross       │
                            │  GPU→CPU boundary)     │
                            └────────────────────────┘
```

Bandwidth across GPU→CPU boundary: ~30 KB compressed bitstream per frame
(vs ~8 MB uncompressed BGRA in the CPU readback path).

---

## Selection (Pipeline Owns This)

The Hardware Encode module does **not** decide which HW encoder to use.
That dispatch lives in [`MODULE_PIPELINE.md`](./MODULE_PIPELINE.md), which:

1. Reads `[encode]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes compiled-in HW add-ons in priority order — vendor-specific SDKs
   before generic abstractions (NVENC > libva on NVIDIA; AMF > libva on AMD;
   MF HW falls back to whatever vendor MFT is registered)
3. Verifies the capture add-on's `SurfaceHandle` format is acceptable to the
   chosen HW encoder
4. On `ErrFallbackToSoftware`, switches to a SW encoder add-on for the
   remainder of the session

There is **no `HWCodec` enum** in this module. Codec selection is by the
add-on advertising what it supports via `Codec()` and the pipeline picking
based on browser handshake preferences.

---

## Per-Add-On Implementation Pointers

Each HW encoder add-on owns its own spec. The Hardware Encode module spec is
the interface contract above; implementation details, performance numbers,
licensing, build tags, and CGo specifics all live in the add-on specs.

| Add-on | Build tag | Linux | macOS | Windows |
|--------|-----------|-------|-------|---------|
| libva (VA-API) | `libva` | [`ADD-ON-SPECS/Linux/encoders/HW/LIBVA_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/HW/LIBVA_LINUX_SPEC.md) | — | — |
| NVENC | `nvenc` | [`ADD-ON-SPECS/Linux/encoders/HW/NVENC_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/HW/NVENC_LINUX_SPEC.md) | — | [`ADD-ON-SPECS/Windows/encoders/HW/NVENC_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/NVENC_WINDOWS_SPEC.md) |
| AMF | `amf_rocm` (Linux) / `amf` (Windows) | [`ADD-ON-SPECS/Linux/encoders/HW/AMF_ROCM_SPEC.md`](../ADD-ON-SPECS/Linux/encoders/HW/AMF_ROCM_SPEC.md) | — | [`ADD-ON-SPECS/Windows/encoders/HW/AMF_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/AMF_WINDOWS_SPEC.md) |
| MediaFoundation HW | `mf_hw` | — | — | [`ADD-ON-SPECS/Windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md) |
| QSV (oneVPL) | `qsv` | — | — | [`ADD-ON-SPECS/Windows/encoders/HW/QSV_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/encoders/HW/QSV_WINDOWS_SPEC.md) |
| VideoToolbox HW | `vt_hw` | — | [`ADD-ON-SPECS/macOS/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md) | — |

---

## What This Module Does NOT Do

- **No platform-specific code in the interface.** No DMA-BUF imports, no
  VA-API context creation, no MFT setup. Those all live in add-on specs.
- **No subprocess management.** Hardware encoders are always in-process via
  CGo. (Subprocess-isolated GPL is only relevant for SW x264.)
- **No codec selection algorithm.** Each add-on advertises supported codecs
  via `Codec()`; the pipeline matches against browser handshake preferences.
- **No keyframe interval logic.** Periodic IDRs are not configured — every
  IDR is on-demand via `ForceKeyframe()` (triggered by client gap detection).

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| NVENC (Windows) | ✅ Benchmarked: GTX 1080 Ti 4.5ms @ 1080p, Quadro RTX 4000 5.4ms |
| AMF (Windows) | ✅ Benchmarked: RX 6800 XT 5.9ms H.264 / 5.0ms HEVC @ 1080p |
| MF HW (Windows) | ✅ Benchmarked: 6.9-7.7ms @ 1080p depending on GPU |
| libva (Linux) | 📋 Specced; CGo bindings pending |
| NVENC (Linux) | 📋 Specced; CGo bindings pending |
| AMF (Linux ROCm) | 📋 Specced; CGo bindings pending |
| QSV (Windows) | 📋 Specced (on good-faith; no Intel hardware to benchmark) |
| VideoToolbox HW (macOS) | 📋 Specced; Hackintosh measured 8.6ms @ 1080p (AMD VCE) |
