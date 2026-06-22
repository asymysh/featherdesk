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

### H.264 Hardware — The Practical Default

H.264 hardware encoding is available on every machine with a dedicated GPU sold in the last 10+ years. Software fallback covers anything older. **This is all anyone needs for remote desktop.**

### Encoder Priority (matches Sunshine)

```
NVENC (NVIDIA)  →  QSV (Intel)  →  AMF (AMD)  →  MF (MediaFoundation/ARM)  →  libx264 (SW)
```

### Support Matrix

| Hardware | H.264 HW Encoder | HEVC HW | AV1 HW | API |
|----------|-----------------|---------|--------|-----|
| **NVIDIA GTX 600+** (Kepler, 2012+) | ✅ NVENC | ✅ Maxwell+ | ✅ Ada Lovelace (RTX 40+) | `h264_nvenc` |
| **AMD RX 400+** (Polaris, 2016+) | ✅ AMF/VCE | ✅ | ✅ RDNA2+ | `h264_amf` |
| **Intel HD/Iris (Sandy Bridge, 2011+)** | ✅ Quick Sync | ✅ Skylake+ | ✅ Arc/12th gen+ | `h264_qsv` |
| **Qualcomm Snapdragon** (ARM Windows) | ✅ MF | ✅ | limited | `h264_mf` |
| **No GPU / ancient GPU** | ❌ | ❌ | ❌ | `libx264` SW |

### Codec Decision (Remote Desktop)

```
Primary:   H.264 HW        ← universal compatibility, browser WebCodecs
Secondary: HEVC HW         ← better quality/bit when available (announce in Config)
Future:    AV1 HW          ← RDNA2+ AMD, RTX 40+ NVIDIA, Arc+ Intel
Skip:      VP8/VP9         ← no useful HW path on Windows, not worth SW cost
```

AV1 is a long-term improvement. Practical deployment today: H.264 HW everywhere.

### Key NVENC Flags (from Sunshine source — copy these exactly)

```cpp
// These are the production-tested latency settings:
NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY  // single most important flag
CBR rate control                       // constant bitrate for streaming
surfaces = 1                           // minimum pipeline depth
delay = 0                              // no frame reorder delay
```

**REF_FRAMES_INVALIDATION (NVENC only):** When a client reports packet loss, instead of forcing a full IDR keyframe (expensive, causes bitrate spike), NVENC can invalidate only the affected reference frames. This is the biggest latency advantage NVENC has over AMF/QSV for streaming. Worth implementing.

### Zero-Copy D3D11 → Encoder Path

For minimum latency and CPU load, frames from DXGI DDup should go directly to the hardware encoder without CPU involvement:

```
DXGI DDup → ID3D11Texture2D (GPU memory)
    → NVENC: NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX
    → AMF:   AMFSurface from D3D11 texture
    → QSV:   mfxFrameSurface1 from D3D11 texture (via DXVA interop)
```

CPU usage with this path: <3% at 1080p60. Without it (CPU copy then encode): 15–25%.

---

## Audio

| API | Status | Notes |
|-----|--------|-------|
| **WASAPI loopback** | ✅ Primary | `IAudioClient` with `AUDCLNT_STREAMFLAGS_LOOPBACK`. Captures system audio output. |
| **Steam Streaming Speakers** | Optional | Virtual audio device installed by Steam. Clean loopback point. Sunshine uses this. |

```cpp
// WASAPI loopback — exact flags used in production (from Sunshine)
AUDCLNT_SHAREMODE_SHARED
| AUDCLNT_STREAMFLAGS_LOOPBACK
| AUDCLNT_STREAMFLAGS_EVENTCALLBACK
| AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM
| AUDCLNT_STREAMFLAGS_SRC_DEFAULT_QUALITY

// Thread priority — use MMCSS "Pro Audio" task
AvSetMmThreadCharacteristics(L"Pro Audio", &taskIndex);
```

Format: 48kHz, stereo, auto-resampled to match device.

---

## Input Injection

| API | Status | Notes |
|-----|--------|-------|
| **SendInput** | ✅ Primary | Keyboard + absolute mouse. Simple, well-supported. |
| **ViGEmBus** | Optional | Virtual gamepad (Xbox 360, DS4). Install as driver. Gaming use case only. |
| **InjectSyntheticPointerInput** | Optional | Touch/pen input (Windows 10 1809+). Remote control edge case. |

```cpp
// Absolute mouse move (scale client coords to virtual desktop space)
INPUT input = {};
input.type = INPUT_MOUSE;
input.mi.dwFlags = MOUSEEVENTF_ABSOLUTE | MOUSEEVENTF_VIRTUALDESK | MOUSEEVENTF_MOVE;
input.mi.dx = (clientX * 65535) / virtualDesktopWidth;
input.mi.dy = (clientY * 65535) / virtualDesktopHeight;
SendInput(1, &input, sizeof(INPUT));
```

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
