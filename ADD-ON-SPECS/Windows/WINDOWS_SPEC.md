# FeatherDesk — Windows Platform Spec

## Overview

Windows is a primary target for FeatherDesk. The use case covers both **remote control** and **gaming/high-fps streaming**. Target: 60fps at 1080p/1440p, <20ms total pipeline latency, H.264 as the default codec.

---

## Capture

### Two Backends: DXGI Desktop Duplication + WGC Fallback

This matches exactly what Sunshine uses. DXGI Desktop Duplication is the production-grade Windows capture path.

| API | Status | Notes |
|-----|--------|-------|
| **DXGI Desktop Duplication (DDup)** | ✅ Primary | DirectX 11.1+, all Windows 10+ machines. Low latency, GPU-resident frames. |
| **Windows.Graphics.Capture (WGC)** | ✅ Fallback | Windows 10 1803+. Works without admin. Handles some edge cases DDup misses. |
| GDI (BitBlt) | ⚠️ Last resort | Software path only. Works on virtual displays (Parsec, RDP). No GPU acceleration. |

### DXGI Desktop Duplication — Primary Path

**What it does:** Captures the composed desktop frame directly from the GPU output, as a `ID3D11Texture2D`. Zero CPU involvement until you explicitly copy to a staging texture.

**Key latency optimizations (from Sunshine source):**
```cpp
// Minimize GPU pipeline depth — single most important latency call
device->SetMaximumFrameLatency(1);

// Set GPU thread priority (fall back from REALTIME on NVIDIA+HAGS due to driver freeze bug)
D3DKMTSetProcessSchedulingPriorityClass(GetCurrentProcess(), D3DKMT_SCHEDULINGPRIORITYCLASS_HIGH);
```

**Zero-copy to hardware encoder:** `ID3D11Texture2D` from DDup → NVENC `NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX` or AMF/QSV D3D11 surface input. Frame never touches CPU RAM.

**Benchmark (this machine: AMD Ryzen 9 5900X, Parsec virtual display, Windows 11):**

| Variant | FPS | p50 | p95 | p99 |
|---------|-----|-----|-----|-----|
| GDI bitblt_only | 58.0 | 16.7ms | 20.0ms | 32.6ms |
| GDI bitblt_getdib | 53.9 | 16.9ms | 31.9ms | 35.8ms |
| DXGI DDup | pending | — | — | — |

> Note: GDI benchmarks run on a **Parsec virtual display adapter** — not representative of real hardware. DXGI Desktop Duplication returns `DXGI_ERROR_UNSUPPORTED` on virtual/software display adapters and on RDP sessions. Real hardware expected: 1–5ms p50.

**Raw CSV:** `bench_out/<session-id>/gdi_bitblt_*.csv`

### WGC Fallback

```csharp
// Windows.Graphics.Capture — WinRT, C# or Swift
var picker = new GraphicsCapturePicker();
var item = await picker.PickSingleItemAsync(); // shows picker UI
var session = Direct3D11CaptureFramePool.Create(device, format, 2, item.Size);
session.FrameArrived += (pool, _) => {
    using var frame = pool.TryGetNextFrame();
    // frame.Surface is an IDirect3DSurface — interop to ID3D11Texture2D
};
session.CreateCaptureSession(item).StartCapture();
```

WGC can capture individual windows (not just full desktop), handles DRM-protected content differently, and works as a portable app without service-mode restrictions. Use as fallback when DDup returns `DXGI_ERROR_UNSUPPORTED`.

### When to Use Each

```
Check DXGI_ERROR_UNSUPPORTED on IDXGIOutput1::DuplicateOutput()
    → if OK:     use DXGI Desktop Duplication
    → if UNSUP:  fall back to WGC
    → if WGC fails (older Windows / permissions): fall back to GDI BitBlt
```

---

## Video Encoding

### Every encoder is an add-on (pluggable architecture)

The Windows default binary contains **no encoders**. Every encoder — software and
hardware — is a build-tagged add-on. Users compile in exactly the encoders they
want. The full set:

```
encoders/
├── SW/
│   ├── OPENH264_CGO_WINDOWS_SPEC.md           ← cross-platform SW (same code as Linux + macOS)
│   └── MEDIAFOUNDATION_SW_WINDOWS_SPEC.md     ← Windows-native SW (no third-party DLL)
└── HW/
    ├── MEDIAFOUNDATION_HW_WINDOWS_SPEC.md     ← cross-vendor HW (NVIDIA + AMD + Intel + Qualcomm)
    ├── NVENC_WINDOWS_SPEC.md                  ← NVIDIA direct (REF_FRAMES_INVALIDATION)
    ├── AMF_WINDOWS_SPEC.md                    ← AMD direct (Pre-Analysis, Apache 2.0)
    ├── QSV_WINDOWS_SPEC.md                    ← Intel direct via oneVPL (covers Arc)
    └── VULKAN_VIDEO_WINDOWS_SPEC.md           ← cross-vendor royalty-free, future-facing
```

See [`encoders/README.md`](./encoders/README.md) for recommended combinations,
runtime probe order, and rationale.

### Why MF HW is the recommended cross-vendor default

Unlike Linux (where VA-API uniformly covers Intel + AMD + NVIDIA), Windows
historically fragmented per-vendor. **MediaFoundation Hardware Transform** is the
closest equivalent: a single Microsoft API that routes to whatever vendor MFT is
registered. For most deployments, `mf_hw` alone is sufficient.

For peak performance or vendor-specific features (NVENC's
`REF_FRAMES_INVALIDATION`, AMF's Pre-Analysis), add the vendor-direct add-on
alongside MF HW.

### TL;DR — recommended combinations

| Deployment | Add-ons |
|-----------|---------|
| Generic Windows (any GPU) | `openh264` + `mf_hw` |
| ARM Snapdragon | `mf_sw` + `mf_hw` |
| NVIDIA-only | `openh264` + `nvenc` |
| AMD-only | `openh264` + `amf` |
| Intel-only | `openh264` + `qsv` |

---

## Audio + Input

⏸️ **Deferred.** The Windows audio (WASAPI loopback) and input (SendInput, ViGEmBus,
InjectSyntheticPointerInput) sections have been deliberately removed from this
document to keep the focus on the capture and encode pipeline.

When we resume work on audio and input, the existing core specs remain authoritative:
- [`specs/MODULE_AUDIO.md`](../../specs/MODULE_AUDIO.md)
- [`specs/MODULE_INPUT.md`](../../specs/MODULE_INPUT.md)

This platform spec will be updated with Windows-specific details (WASAPI loopback,
SendInput, optional ViGEmBus for gamepads) at that point.

---

## Benchmark Tool

A working benchmark binary exists for Windows in `cmd/benchmark/`. It benchmarks:
- GDI capture (bitblt_only and bitblt_getdib variants)
- DXGI Desktop Duplication (stub — pending COM bindings)
- System probing (CPU, GPU, display, ffmpeg encoder availability)

Results stored in SQLite (`featherdesk_bench.db`) + raw per-frame CSVs.

**Build:** `go build -o bench.exe ./cmd/benchmark/` (pure Go, no CGo required)

**First run results on this machine (Ryzen 9 5900X, Windows 11, virtual display):**
```
GDI bitblt_only:    fps=58.0  p50=16.7ms  p95=20.0ms  p99=32.6ms
GDI bitblt_getdib:  fps=53.9  p50=16.9ms  p95=31.9ms  p99=35.8ms
DXGI:               UNAVAILABLE (Parsec virtual display, expected)
```

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum Windows | **Windows 10 1803** (WGC minimum; DXGI works on Windows 8+) |
| DirectX | **DirectX 11.1+** for DXGI Desktop Duplication |
| DXGI on RDP | ❌ DXGI DDup returns `DXGI_ERROR_UNSUPPORTED` in RDP sessions → fall back to WGC/GDI |
| DXGI on virtual adapters | ❌ Same — Parsec, VMware, VirtualBox display adapters block DDup |
| Admin rights | Not required for capture or encoding. DXGI DDup works as standard user. |
| Driver | Any GPU driver from the last 5 years supports DDup |
