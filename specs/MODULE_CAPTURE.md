# Module Spec: Capture (Cross-Platform Capturer Interface Contract)

## Overview

The Capture module defines the **abstract capturer interface contract** that
every capture add-on implements. It owns no capturer implementation itself —
concrete capturers live in their respective add-on specs:

- `internal/capture/kms/` — Linux KMS+EGL DMA-BUF (build tag `kms_egl`)
- `internal/capture/nvfbc/` — Linux NvFBC NVIDIA proprietary (build tag `nvfbc`)
- `internal/capture/sck/` — macOS ScreenCaptureKit (build tag `sck`)
- `internal/capture/dxgi/` — Windows DXGI Desktop Duplication (build tag `dxgi_dd`)

This separation keeps the module spec stable while letting per-OS capture
implementations evolve independently.

---

## Public Interface

```go
package capture

// Frame holds raw screen pixels (CPU-resident).
// Used by the SW encoder path (RGBA → I420 → encoder).
type Frame struct {
    Data      []byte // BGRA pixels (or RGBA on some platforms)
    Width     int
    Height    int
    Timestamp uint64 // CLOCK_MONOTONIC ns, sampled at capture
}

// FBInfo holds a GPU-resident surface handle (zero-copy path).
// Used by the HW encoder path — the capturer never touches CPU memory.
// Per-OS handle types:
//   Linux:   DMA-BUF fd + DRM format + modifier
//   macOS:   IOSurface backing the CMSampleBuffer
//   Windows: ID3D11Texture2D shared handle
type FBInfo struct {
    // Generic fields
    Width, Height int
    Timestamp     uint64

    // Linux fields (set when platform == "linux")
    DMAFD     int
    Stride    int
    Format    uint32 // DRM fourcc
    Modifier  uint64

    // macOS field (set when platform == "darwin")
    IOSurface uintptr // CVPixelBufferRef (CMSampleBuffer-backed)

    // Windows field (set when platform == "windows")
    D3DTexture uintptr // ID3D11Texture2D*
}

// Capturer is the contract every capture add-on must satisfy.
//
// CPU readback path: NextFrame() returns CPU-resident BGRA pixels.
// Used by SW encoder path.
type Capturer interface {
    // NextFrame returns the next screen frame as CPU pixels.
    // Borrowed: valid only until the next NextFrame() call.
    NextFrame() (*Frame, error)

    // Close releases all resources (DRM fds, EGL contexts, COM refs).
    Close() error
}

// DMABufCapturer is the optional zero-copy contract.
// Capture add-ons that can produce GPU surfaces (KMS+EGL, NvFBC, SCK, DXGI DD)
// implement this in addition to Capturer.
//
// The pipeline uses this when a hardware encoder is selected.
type DMABufCapturer interface {
    Capturer

    // NextDMABuf returns a GPU-resident surface handle.
    // Caller must release the underlying handle via the FBInfo's Release()
    // function (set per-platform by the add-on).
    NextDMABuf() (*FBInfo, error)
}

// CaptureConfig holds capture parameters shared across all add-ons.
// Per-add-on tuning (DRM card path, IOSurface format, DXGI adapter index)
// comes from [addon_module_<tag>] TOML sections.
type CaptureConfig struct {
    Width  int // 0 = use native display resolution
    Height int // 0 = use native display resolution
    FPS    int // Target capture rate (server may pace below this)
    Logger *slog.Logger
}
```

---

## Surface Handle Abstraction Across OSes

The `FBInfo` struct uses a tagged-union pattern — only the field corresponding
to the current OS is populated. HW encoder add-ons type-switch on the platform
and consume the appropriate handle:

| OS | Capture output | HW encoder input |
|----|----------------|------------------|
| Linux | DMA-BUF fd | libva imports via `vaCreateSurfaces` + `VASurfaceAttribExternalBuffers` |
| Linux | DMA-BUF fd | NVENC imports via `cuGraphicsEGLRegisterImage` |
| macOS | IOSurface | VideoToolbox accepts CVPixelBuffer directly |
| Windows | ID3D11Texture2D | MF HW / NVENC / AMF / QSV all accept D3D11 textures |

The pipeline never converts between formats — it pairs a capturer with an
encoder both on the same platform, and the HW encoder add-on knows which
field of `FBInfo` to read.

---

## Capturer Selection (Pipeline Owns This)

The Capture module does **not** decide which capturer to use. That dispatch
lives in [`MODULE_PIPELINE.md`](./MODULE_PIPELINE.md), which:

1. Reads `[capture]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes each compiled-in capture add-on (NvFBC > KMS+EGL on Linux; SCK on
   macOS; DXGI DD on Windows)
3. On Windows headless: triggers IddCx VDD auto-install before retrying probe
4. Calls the chosen add-on's constructor with `CaptureConfig`
5. Passes the resulting `Capturer` (and optionally `DMABufCapturer`) to the
   frame loop

There is **no `CaptureBackend` enum** in this module. Selection is purely
runtime — compiled-in add-ons + TOML config decide.

---

## What Was Rejected

The module intentionally does NOT support:

- **X11grab via ffmpeg subprocess** — rejected (subprocess overhead, no
  zero-copy path, deprecated in favor of KMS+EGL)
- **PipeWire ScreenCast** — rejected (GNOME-specific, Mutter D-Bus
  dependency, no zero-copy)
- **wlr-screencopy** — rejected (wlroots-specific, no obvious advantage
  over KMS+EGL)
- **Windows WGC / GDI / Magnification** — rejected (slower than DXGI DD
  with no benefit)
- **Pipeline-level frame buffering** — capture add-ons are pull-latest:
  each `NextFrame()` returns the most recent frame, not a queued one.

---

## Per-Add-On Implementation Pointers

| Add-on | Build tag | OS | Spec |
|--------|-----------|----|------|
| KMS+EGL DMA-BUF | `kms_egl` | Linux | [`ADD-ON-SPECS/Linux/capture/KMS_EGL_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/capture/KMS_EGL_LINUX_SPEC.md) |
| NvFBC | `nvfbc` | Linux | [`ADD-ON-SPECS/Linux/capture/NVFBC_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/capture/NVFBC_LINUX_SPEC.md) |
| ScreenCaptureKit | `sck` | macOS | [`ADD-ON-SPECS/macOS/capture/SCK_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/capture/SCK_MACOS_SPEC.md) |
| DXGI Desktop Duplication (+ IddCx headless) | `dxgi_dd` | Windows | [`ADD-ON-SPECS/Windows/capture/DXGI_DD_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/capture/DXGI_DD_WINDOWS_SPEC.md) |

---

## Buffer Ownership Contract

- `Capturer.NextFrame()` returns a **borrowed** `*Frame.Data` — valid only
  until the next `NextFrame()` call. Callers (the SW path's Converter) copy
  into pinned encoder input buffers as needed.
- `DMABufCapturer.NextDMABuf()` returns an **owned** `*FBInfo` — caller must
  hand it to a HW encoder for consumption, which releases the underlying
  handle after `Encode()` completes.
- The Capturer is **not safe** for concurrent `NextFrame()` calls from
  multiple goroutines. The pipeline ensures single-goroutine access.

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| KMS+EGL DMA-BUF | ✅ Working (the original Linux capture path; refactor moves into `internal/capture/kms/`) |
| NvFBC | 📋 Specced; CGo bindings pending |
| ScreenCaptureKit | ✅ Working (Hackintosh benchmark: 91 FPS @ 1080p, P50 10.5ms) |
| DXGI Desktop Duplication | ✅ Working (benchmarked sub-microsecond raw overhead on GTX 1080 Ti + RX 6800 XT) |

The old `internal/capture/x11grab.go` (subprocess-based X11 capture) and
`internal/capture/screencast.py` (Mutter/PipeWire ScreenCast helper) are
**rejected** and will be removed as part of the implementation refactor.
