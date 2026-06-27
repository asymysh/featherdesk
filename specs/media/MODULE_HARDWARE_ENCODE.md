# Module Spec: Hardware Encode (Cross-Platform Interface Contract)

## Overview

The Hardware Encode module defines the **abstract zero-copy hardware encoder
interface contract** that every HW encoder add-on implements. It owns no
encoder implementation itself — concrete encoders live in their respective
add-on specs:

- `internal/encode/libva/` — Linux VA-API (add-on ID `libva`, MIT)
- `internal/encode/nvenc/` — NVENC SDK direct (add-on ID `nvenc`, NVIDIA proprietary)
- `internal/encode/amf/` — AMD AMF (add-on ID `amf` Windows, `amf_rocm` Linux, Apache 2.0)
- `internal/encode/mf/` — Windows MediaFoundation (add-on ID `mf_hw`, Microsoft system)
- `internal/encode/qsv/` — Intel oneVPL / QSV (add-on ID `qsv`, MIT)
- `internal/encode/vt/` — macOS VideoToolbox HW (add-on ID `vt_hw`, Apple system)

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

// EncodedFrame lives in pkg/stream/frame.go — shared by SW and HW paths:
//
//   type EncodedFrame struct {
//       Data      []byte  // Contiguous Annex B bitstream (NOT split per-NAL)
//       Width     uint16
//       Height    uint16
//       Timestamp uint64
//       Keyframe  bool
//       CodecType uint8   // FrameTypeVideoH264 or FrameTypeVideoHEVC
//   }
type EncodedFrame = stream.EncodedFrame

// HardwareEncoder is the contract every HW encoder add-on must satisfy.
type HardwareEncoder interface {
    // EncodeSurface takes a GPU-resident surface and returns one encoded access
    // unit (H.264 or HEVC) with EncodedFrame.Keyframe set BY THE ENCODER.
    //
    // SURFACE OWNERSHIP (single owner — resolves the prior 4-way ambiguity):
    // the HW encoder calls handle.Release() exactly ONCE before returning, on
    // EVERY path including the error and ErrFallbackToSoftware paths. The
    // capturer does NOT release it; the pipeline does NOT release it. After
    // EncodeSurface returns, the handle is invalid to the caller.
    //
    // The input surface is at native capture resolution; the encoder scales it
    // on-GPU to InitialParams.Width/Height (the Config dims) so its output
    // always matches the advertised stream dimensions (see "Scaling invariant").
    //
    // Returns ErrFallbackToSoftware if the surface cannot be imported (format
    // mismatch, GPU reset, driver constraint) — and STILL releases the handle.
    // The pipeline catches this and switches to the SW path for the session.
    EncodeSurface(handle *SurfaceHandle) (*EncodedFrame, error)

    // ForceKeyframe requests that the next encoded frame be an IDR. The only
    // method safe to call concurrently with EncodeSurface (atomic flag read by
    // the frame loop); all other mutations go through UpdateStreamParams.
    ForceKeyframe()

    // Codec returns the WebCodecs codec string for the Config handshake
    // (e.g. "avc1.42E01F" for H.264 Constrained Baseline 3.1, "hvc1.*"
    // for HEVC variants).
    Codec() string

    // Close releases all encoder resources (D3D11 / VA / MF / Metal contexts).
    Close() error
}

// HWEncoderConfig holds the encoder's INITIAL configuration. Once running,
// dynamic parameters flow through the stream.Params contract and the
// ConfigurableHardwareEncoder interface (see MODULE_STREAM_PARAMS.md).
type HWEncoderConfig struct {
    InitialParams stream.Params  // initial Width/Height/FPS/BitrateBps/QP/HDR/etc.
    CodecHint     string         // "h264" | "hevc" | "av1" — add-on picks the best match
}

// ConfigurableHardwareEncoder lets the pipeline change stream parameters
// at runtime. HW encoders that don't implement this are torn down + recreated.
type ConfigurableHardwareEncoder interface {
    HardwareEncoder
    // Called only on the frame-loop goroutine, serialized with EncodeSurface
    // via the pipeline's param-change channel — no locking vs EncodeSurface.
    // Returns stream.ErrRequiresRestart if the change needs a fresh session.
    UpdateStreamParams(p stream.Params) error
}

// ErrFallbackToSoftware lives in the stream package (not hwencode) to
// avoid import cycles -- capture add-ons also need to return it when
// GPU surface export fails.
// Defined at: stream.ErrFallbackToSoftware
// See MODULE_STREAM_PARAMS.md for all error sentinels.
```

### Bitstream Output Contract

Identical to the SW path (see MODULE_ENCODE):
- `EncodedFrame.Data` is ONE **contiguous** Annex B access unit (start code
  `00 00 00 01` retained) — **NOT** split per-NAL. (The old "one NAL per
  `[]byte`" contract is rejected.)
- Keyframe access unit contains SPS + PPS + IDR (H.264) or VPS + SPS + PPS + IDR
  (HEVC) concatenated.
- `EncodedFrame.Keyframe` is set by the encoder; the server never re-scans NALs.

---

## Zero-Copy Pipeline

```
Capture add-on              Hardware encoder add-on
┌──────────────────┐        ┌────────────────────────┐
│ NextSurface()    │───────>│ EncodeSurface(handle)  │
│                  │ handle │                        │
│ Returns          │        │ Imports GPU resource   │
│ SurfaceHandle    │        │ scale + encode on-GPU  │
│ (DMA-BUF /       │        │ returns 1 access unit  │
│  IOSurface /     │        │ Release(handle) once   │
│  D3D11 texture)  │        │ ~30KB compressed       │
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
MFT scaler / VideoToolbox scaling) to `InitialParams.Width × Height`. The SW path
achieves the same via libyuv `I420Scale` (see MODULE_ENCODE). The invariant the
rest of the system relies on:

> **encoder-output dims == `config` dims == input-coordinate range.**

So the client's input scaling (which maps pointer coordinates into the advertised
`config` width/height) is always correct regardless of native capture size.

### Chroma subsampling

HW encoders subsample **inside the encoder** — the input GPU surface is RGB/4:4:4,
and the encoder produces `Params.ChromaSubsampling` (4:2:0 default, or 4:2:2/4:4:4
where the silicon supports it: NVENC 4:4:4 on Turing+, others vendor-dependent).
No CPU converter is involved. The add-on **advertises its supported chroma** in its
probe capabilities and returns `stream.ErrChromaUnsupported` if asked for one it
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
licensing, add-on IDs, and CGo specifics all live in the add-on specs.

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
