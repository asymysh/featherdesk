# Module Spec: Capture (Cross-Platform Capturer Interface Contract)

## Overview

The Capture module defines the **abstract capturer interface contract** that
every capture add-on implements. It owns no capturer implementation itself —
concrete capturers live in their respective add-on specs:

- `addons/capture/kms_egl/` — Linux KMS+EGL DMA-BUF (add-on ID `kms_egl`)
- `addons/capture/nvfbc/` — Linux NvFBC NVIDIA proprietary (add-on ID `nvfbc`)
- `addons/capture/sck/` — macOS ScreenCaptureKit (add-on ID `sck`)
- `addons/capture/dxgi_dd/` — Windows DXGI Desktop Duplication (add-on ID `dxgi_dd`)

This separation keeps the module spec stable while letting per-OS capture
implementations evolve independently.

---

## Public Interface

```rust
// crate: featherdesk-capture

// Frame holds raw screen pixels (CPU-resident).
// Used by the SW encoder path (pixels → I420 → encoder). Cleanup is RAII (Drop)
// — no Close(). Across the add-on ABI `data` is an owned RVec<u8> whose
// ownership transfers to the host (deterministic drop).
pub struct Frame {
    pub data: RVec<u8>,         // Pixel buffer (stride * height bytes), owned
    pub stride: u32,            // Bytes per row. MAY exceed width*4 (GPU readback
                                // rows are often padded to a power-of-two pitch).
                                // libyuv ARGBToI420/ABGRToI420 take this stride
                                // directly; assuming stride == width*4 corrupts the
                                // image on padded backends (e.g. DXGI). width*4 is
                                // the valid byte count per row.
    pub pixel_fmt: PixelFormat, // Bgra (macOS/Windows) or Rgba (Linux GL)
    pub width: u32,
    pub height: u32,
    pub timestamp_ns: u64,      // CLOCK_MONOTONIC ns, sampled at capture
}

#[repr(u8)]
pub enum PixelFormat {
    Bgra = 0,  // BGRA in memory = libyuv ARGB → use ARGBToI420
    Rgba = 1,  // RGBA in memory = libyuv ABGR → use ABGRToI420
}

// FbInfo holds a GPU-resident surface handle (zero-copy path).
// Used by the HW encoder path — the capturer never touches CPU memory.
// `handle` is a tagged enum (SurfaceHandle), not a struct of nullable per-OS
// fields; FbInfo's Drop releases the underlying resource exactly once (RAII
// replaces the manual `Release func()`). Per-OS handle variants:
//   Linux:   DMA-BUF fd + DRM format + modifier
//   macOS:   IOSurface backing the CMSampleBuffer
//   Windows: ID3D11Texture2D
pub struct FbInfo {
    pub width: u32,
    pub height: u32,
    pub timestamp_ns: u64,        // CLOCK_MONOTONIC ns, stamped at capture
    pub handle: SurfaceHandle,
}

pub enum SurfaceHandle {
    // Linux: DMA-BUF. `OwnedFd` closes the fd on Drop (no manual close).
    DmaBuf { fd: std::os::fd::OwnedFd, stride: u32, fourcc: u32, modifier: u64 },
    // macOS: CVPixelBuffer-backed IOSurface (retained; released on Drop).
    IoSurface(objc2_io_surface::IOSurface),
    // Windows: ID3D11Texture2D (COM ref released on Drop via windows-rs).
    D3D11Texture(windows::Win32::Graphics::Direct3D11::ID3D11Texture2D),
}

// Capturer is the contract every capture add-on must satisfy.
//
// CPU readback path: next_frame() returns CPU-resident BGRA pixels.
// Used by the SW encoder path. Cleanup is RAII (Drop) — no Close().
pub trait Capturer {
    /// next_frame returns the most recent screen frame as CPU pixels. It BLOCKS
    /// until either a new frame is available or the per-frame deadline (~one
    /// frame interval) elapses. Three outcomes:
    ///   Ok(Some(frame)) — a new frame (owned: `frame.data` is an RVec<u8> whose
    ///                     ownership transfers to the caller — no borrowed-slice
    ///                     footgun).
    ///   Ok(None)        — no new frame within the deadline (screen idle). The
    ///                     caller skips this tick; it does NOT re-encode. A newly
    ///                     joined client is still served from the IDR cache via the
    ///                     bootstrap stream, so idle screens cost ~zero bandwidth.
    ///   Err(_)          — capture failed (device lost, etc.).
    /// It never returns a stale frame as if it were new, and never blocks
    /// forever on an idle screen.
    fn next_frame(&mut self) -> Result<Option<Frame>, CaptureError>;
}

// SurfaceCapturer is the optional zero-copy contract.
// Capture add-ons that can produce GPU surfaces (KMS+EGL, NvFBC, SCK, DXGI DD)
// implement this in addition to Capturer.
//
// The pipeline uses this when a hardware encoder is selected.
// NOTE: the old name "DMABufCapturer" was Linux-specific. The trait is
// cross-platform — the returned FbInfo carries a SurfaceHandle tagged enum with
// per-OS variants (DmaBuf, IoSurface, D3D11Texture).
pub trait SurfaceCapturer: Capturer {
    /// next_surface returns a GPU-resident surface handle (same blocking + None
    /// semantics as next_frame: Ok(None) means "no new surface this interval").
    /// OWNERSHIP: the FbInfo is normally passed BY VALUE straight into a HW
    /// encoder's encode_surface, where FbInfo's Drop releases the resource
    /// exactly once on every path. The pipeline does NOT release it itself in
    /// that case. ONLY if the surface is never handed to an encoder (e.g.
    /// probe/teardown) does dropping the FbInfo here release it — release is
    /// automatic (RAII) per-platform via SurfaceHandle's Drop.
    fn next_surface(&mut self) -> Result<Option<FbInfo>, CaptureError>;
}

// CaptureConfig holds the capturer's INITIAL configuration. Once running,
// dynamic parameters (width, height, fps, HDR) flow through stream::Params
// and ConfigurableCapturer (see MODULE_STREAM_PARAMS.md). Logging is via the
// `tracing` crate (no logger handle is passed in).
//
// Per-add-on STATIC tuning (DRM card path, IOSurface format, DXGI adapter
// index) comes from the [addon_module_<id>] TOML section.
pub struct CaptureConfig {
    pub initial_params: stream::Params,  // initial width/height/fps/HDR/bit_depth
}

// ConfigurableCapturer lets the pipeline change capture parameters at
// runtime (resolution, HDR mode). Add-ons that don't implement this are
// torn down + recreated whenever capture parameters change.
pub trait ConfigurableCapturer: Capturer {
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), StreamError>;
}
```

---

## Surface Handle Abstraction Across OSes

The `FbInfo` struct carries a `SurfaceHandle` tagged enum — only the variant
corresponding to the current OS is constructed. HW encoder add-ons `match` on the
variant and consume the appropriate handle:

| OS | Capture output | HW encoder input |
|----|----------------|------------------|
| Linux | DMA-BUF fd | libva imports via `vaCreateSurfaces` + `VASurfaceAttribExternalBuffers` |
| Linux | DMA-BUF fd | NVENC imports via `cuGraphicsEGLRegisterImage` |
| macOS | IOSurface | VideoToolbox accepts CVPixelBuffer directly |
| Windows | ID3D11Texture2D | MF HW / NVENC / AMF / QSV all accept D3D11 textures |

The pipeline never converts between formats — it pairs a capturer with an
encoder both on the same platform, and the HW encoder add-on knows which
`SurfaceHandle` variant of `FbInfo` to read.

---

## Capturer Selection (Pipeline Owns This)

The Capture module does **not** decide which capturer to use. That dispatch
lives in [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md), which:

1. Reads `[capture]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes each loaded capture add-on (NvFBC > KMS+EGL on Linux; SCK on
   macOS; DXGI DD on Windows)
3. On Windows headless: triggers IddCx VDD auto-install before retrying probe
4. Calls the chosen add-on's constructor with `CaptureConfig`
5. Passes the resulting `Capturer` (and optionally `SurfaceCapturer`) to the
   frame loop

There is **no `CaptureBackend` enum** in this module. Selection is purely
runtime — loaded add-ons + TOML config decide.

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
  each `next_frame()` returns the most recent frame, not a queued one.

---

## Per-Add-On Implementation Pointers

| Add-on | Add-on ID | OS | Spec |
|--------|-----------|----|------|
| KMS+EGL DMA-BUF | `kms_egl` | Linux | [`specs/addons/linux/capture/KMS_EGL_LINUX_SPEC.md`](../addons/linux/capture/KMS_EGL_LINUX_SPEC.md) |
| NvFBC | `nvfbc` | Linux | [`specs/addons/linux/capture/NVFBC_LINUX_SPEC.md`](../addons/linux/capture/NVFBC_LINUX_SPEC.md) |
| ScreenCaptureKit | `sck` | macOS | [`specs/addons/macos/capture/SCK_MACOS_SPEC.md`](../addons/macos/capture/SCK_MACOS_SPEC.md) |
| DXGI Desktop Duplication (+ IddCx headless) | `dxgi_dd` | Windows | [`specs/addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md`](../addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md) |

---

## Buffer Ownership Contract

- `Capturer::next_frame()` returns an **owned** `Frame` whose `data` is an
  `RVec<u8>` — ownership transfers to the caller (the Go "borrowed slice valid
  only until the next call" hazard is gone). Callers (the SW path's Converter)
  read it directly or copy into pinned encoder input buffers as needed.
- `SurfaceCapturer::next_surface()` returns an `FbInfo` whose ownership passes
  **by value** to the HW encoder: `encode_surface(surface: FbInfo)` consumes it
  and `FbInfo`'s `Drop` releases the resource **exactly once on every path**
  (success, error, and `StreamError::FallbackToSoftware`). The capturer and
  pipeline never release it. The single exception is a surface that is never
  handed to an encoder, which the caller drops itself. (This is the single-owner
  rule — RAII — that resolves the prior capture/encode/pipeline ambiguity, the
  M-1/TD-01 win; see
  [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md).)
- The Capturer takes `&mut self`, so the borrow checker statically prevents
  concurrent `next_frame()` calls; the pipeline drives it from a single task.

### Resolution: capture is always native; the encoder scales

Capturers always emit frames/surfaces at the **display's native resolution**.
They do **not** downscale to the stream resolution — that is the encoder's job
(HW: in-encoder VPP/scaler; SW: libyuv `I420Scale`). Consequently a
`ConfigurableCapturer::update_stream_params` call uses only the HDR / bit-depth /
FPS fields; a width/height change does **not** resize capture output (the encoder
absorbs it). This keeps the invariant **encoder-output dims == `config` dims ==
input-coordinate range** without the capturer and encoder both trying to scale.

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| KMS+EGL DMA-BUF | ✅ Working (the original Linux capture path; refactor moves into `addons/capture/kms_egl/`) |
| NvFBC | 📋 Specced; Rust FFI bindings pending |
| ScreenCaptureKit | ✅ Working (Hackintosh benchmark: 91 FPS @ 1080p, P50 10.5ms) |
| DXGI Desktop Duplication | ✅ Working — **VALIDATED on this hardware: ~7 ms p50 acquire, ~2.4× better than GDI BitBlt** (also benchmarked sub-microsecond raw copy overhead on GTX 1080 Ti + RX 6800 XT). The virtual-display `DXGI_ERROR_UNSUPPORTED` case is handled by the IddCx virtual-display fallback (capture stays add-on-based). |

The old Go `internal/capture/x11grab.go` (subprocess-based X11 capture) and
`internal/capture/screencast.py` (Mutter/PipeWire ScreenCast helper) from the
working `feature-libav-vp8s8` branch are **rejected** — they are not ported to
the Rust rewrite.
