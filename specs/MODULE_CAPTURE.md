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
// Used by the SW encoder path (pixels → I420 → encoder).
type Frame struct {
    Data      []byte      // Pixel buffer (Stride * Height bytes)
    Stride    int         // Bytes per row. MAY exceed Width*4 (GPU readback
                          // rows are often padded to a power-of-two pitch).
                          // libyuv ARGBToI420/ABGRToI420 take this stride
                          // directly; assuming Stride == Width*4 corrupts the
                          // image on padded backends (e.g. DXGI). Width*4 is
                          // the valid byte count per row.
    PixelFmt  PixelFormat // PixelBGRA (macOS/Windows) or PixelRGBA (Linux GL)
    Width     int
    Height    int
    Timestamp uint64      // CLOCK_MONOTONIC ns, sampled at capture
}

type PixelFormat uint8
const (
    PixelBGRA PixelFormat = iota  // BGRA in memory = libyuv ARGB → use ARGBToI420
    PixelRGBA                     // RGBA in memory = libyuv ABGR → use ABGRToI420
)

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
    Release       func()  // Platform-specific cleanup; set by capture add-on

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
    // NextFrame returns the most recent screen frame as CPU pixels. It BLOCKS
    // until either a new frame is available or the per-frame deadline (~one
    // frame interval) elapses. Three outcomes:
    //   (*Frame, nil) — a new frame (borrowed: valid only until the next call).
    //   (nil,    nil) — no new frame within the deadline (screen idle). The
    //                   caller skips this tick; it does NOT re-encode. A newly
    //                   joined client is still served from the IDR cache via the
    //                   bootstrap stream, so idle screens cost ~zero bandwidth.
    //   (nil,    err) — capture failed (device lost, etc.).
    // It never returns a stale frame as if it were new, and never blocks
    // forever on an idle screen.
    NextFrame() (*Frame, error)

    // Close releases all resources (DRM fds, EGL contexts, COM refs).
    Close() error
}

// SurfaceCapturer is the optional zero-copy contract.
// Capture add-ons that can produce GPU surfaces (KMS+EGL, NvFBC, SCK, DXGI DD)
// implement this in addition to Capturer.
//
// The pipeline uses this when a hardware encoder is selected.
// NOTE: the old name "DMABufCapturer" was Linux-specific. The interface is
// cross-platform — the returned FBInfo uses a tagged-union pattern with
// per-OS fields (DMAFD, IOSurface, D3DTexture).
type SurfaceCapturer interface {
    Capturer

    // NextSurface returns a GPU-resident surface handle (same blocking + nil
    // semantics as NextFrame: nil,nil means "no new surface this interval").
    // OWNERSHIP: the handle is normally passed straight to a HW encoder's
    // EncodeSurface, which calls fb.Release() exactly once on every path. The
    // pipeline does NOT release it itself in that case. ONLY if the surface is
    // never handed to an encoder (e.g. probe/teardown) must the caller invoke
    // fb.Release(). Release is set per-platform by the add-on.
    NextSurface() (*FBInfo, error)
}

// CaptureConfig holds the capturer's INITIAL configuration. Once running,
// dynamic parameters (Width, Height, FPS, HDR) flow through stream.Params
// and ConfigurableCapturer (see MODULE_STREAM_PARAMS.md).
//
// Per-add-on STATIC tuning (DRM card path, IOSurface format, DXGI adapter
// index) comes from the [addon_module_<tag>] TOML section.
type CaptureConfig struct {
    InitialParams stream.Params  // initial Width/Height/FPS/HDR/BitDepth
    Logger        *slog.Logger
}

// ConfigurableCapturer lets the pipeline change capture parameters at
// runtime (resolution, HDR mode). Add-ons that don't implement this are
// torn down + recreated whenever capture parameters change.
type ConfigurableCapturer interface {
    Capturer
    UpdateStreamParams(p stream.Params) error
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
5. Passes the resulting `Capturer` (and optionally `SurfaceCapturer`) to the
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
- `SurfaceCapturer.NextSurface()` returns an `*FBInfo` whose ownership passes to
  the HW encoder: `EncodeSurface` calls `fb.Release()` **exactly once on every
  path** (success, error, and `ErrFallbackToSoftware`). The capturer and pipeline
  never release it. The single exception is a surface that is never handed to an
  encoder, which the caller must release itself. (This is the single-owner rule
  that resolves the prior capture/encode/pipeline ambiguity — see
  [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md).)
- The Capturer is **not safe** for concurrent `NextFrame()` calls from
  multiple goroutines. The pipeline ensures single-goroutine access.

### Resolution: capture is always native; the encoder scales

Capturers always emit frames/surfaces at the **display's native resolution**.
They do **not** downscale to the stream resolution — that is the encoder's job
(HW: in-encoder VPP/scaler; SW: libyuv `I420Scale`). Consequently a
`ConfigurableCapturer.UpdateStreamParams` call uses only the HDR / bit-depth /
FPS fields; a Width/Height change does **not** resize capture output (the encoder
absorbs it). This keeps the invariant **encoder-output dims == `config` dims ==
input-coordinate range** without the capturer and encoder both trying to scale.

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
