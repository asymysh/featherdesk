# Windows Capture Add-On: DXGI Desktop Duplication

## Purpose

DXGI Desktop Duplication API provides full-desktop screen capture via Direct3D 11.
The output is a GPU-resident `ID3D11Texture2D` — the exact format that every
Windows hardware encoder (MediaFoundation, NVENC, AMF, QSV) consumes natively.
Zero-copy capture-to-encode on every GPU vendor.

This is the **universal default** Windows capture add-on. Equivalent of
KMS+EGL on Linux and ScreenCaptureKit on macOS. Used by Sunshine, OBS, Parsec,
and Moonlight as their Windows default.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| DXGI API | Windows system API | Usage governed by Windows SDK license |
| D3D11 API | Windows system API | Same |
| Our CGo binding | MIT | We own this code |

System APIs; no redistribution concerns, no royalties, no GPL exposure.

---

## Hardware & OS Compatibility

| GPU | Driver model | Support |
|-----|-------------|---------|
| Intel HD 2500+ (Ivy Bridge 2012+) | WDDM 1.2+ | ✅ |
| Intel Arc | WDDM 3.0+ | ✅ |
| AMD GCN 1.0+ (HD 7000+, 2012+) | WDDM 1.2+ | ✅ |
| AMD RDNA1/2/3/4 | WDDM 2.0+ | ✅ |
| NVIDIA Kepler+ (GTX 600+, 2012+) | WDDM 1.2+ | ✅ |
| NVIDIA RTX 20/30/40/50 | WDDM 2.7+ | ✅ |
| Any GPU with WDDM 1.2+ driver | | ✅ |

| OS | Support |
|----|---------|
| Windows 8 / Server 2012 | ✅ Minimum |
| Windows 10 (all versions) | ✅ |
| Windows 11 (all versions) | ✅ |
| Windows Server 2016/2019/2022/2025 | ✅ |
| Windows 7 | ❌ No DXGI DD API |

**Minimum requirement: Windows 8 + WDDM 1.2.** Effectively every Windows PC
from 2012 onward.

---

## Permission Requirements

**None.** DXGI Desktop Duplication runs in user-session context. No elevation,
no special permissions, no capabilities required. The user must be logged in
to a desktop session (no headless / no RDP shadow session without workaround).

### Headless / RDP constraint

The DXGI output adapter must have an active desktop attached. In headless
deployments (GPU-only server with no monitor), a virtual display driver
(e.g. IddSampleDriver or Parsec's virtual display) is needed. Under RDP,
Desktop Duplication is blocked by default — use a virtual display adapter or
disable the Microsoft Basic Display Adapter to work around this.

---

## Build & Distribution

### Go build tag

```bash
GOOS=windows go build -tags dxgi_dd -o viewport-rds.exe ./cmd/server
```

### Runtime dependencies

- `dxgi.dll` (ships with Windows 8+)
- `d3d11.dll` (ships with Windows 8+)

Both are system DLLs — nothing to install. No pkg-config, no development
headers on the target machine; the Windows SDK is needed at **build time only**.

### CGo configuration

```go
/*
#cgo CFLAGS: -DCOBJMACROS -DUNICODE
#cgo LDFLAGS: -ld3d11 -ldxgi -lole32

#include <d3d11.h>
#include <dxgi1_2.h>
#include <dxgi1_5.h>
#include <stdio.h>
*/
import "C"
```

Alternatively, use Go's `golang.org/x/sys/windows` + `unsafe.Pointer` for
COM vtable calls without CGo overhead. The CGo approach is used for
consistency with other platform add-ons (KMS+EGL, SCK) that need C headers.

---

## CGo Implementation Sketch

```c
// 1. Create D3D11 device on the target adapter
D3D_FEATURE_LEVEL featureLevel;
ID3D11Device *device = NULL;
ID3D11DeviceContext *ctx = NULL;
D3D11CreateDevice(
    adapter, D3D_DRIVER_TYPE_UNKNOWN, NULL,
    0, NULL, 0, D3D11_SDK_VERSION,
    &device, &featureLevel, &ctx
);

// 2. Get DXGI Output (the monitor to capture)
IDXGIOutput1 *output1 = NULL;
IDXGIOutput *output = NULL;
adapter->EnumOutputs(0, &output);
output->QueryInterface(__uuidof(IDXGIOutput1), (void**)&output1);

// 3. Create Desktop Duplication
IDXGIOutputDuplication *dupl = NULL;
output1->DuplicateOutput(device, &dupl);

// 4. Per-frame: acquire next frame
DXGI_OUTDUPL_FRAME_INFO frameInfo;
IDXGIResource *resource = NULL;
HRESULT hr = dupl->AcquireNextFrame(16 /*timeout ms*/, &frameInfo, &resource);
if (hr == DXGI_ERROR_WAIT_TIMEOUT) return; // no new frame

// 5. Get the D3D11 texture (GPU-resident)
ID3D11Texture2D *frameTex = NULL;
resource->QueryInterface(__uuidof(ID3D11Texture2D), (void**)&frameTex);

// 6. Path A: Zero-copy to HW encoder
// Pass frameTex directly to MF/NVENC/AMF/QSV encoder — no GPU→CPU copy
// Encode operates on the same D3D11 device.
EncodeFrame(frameTex, frameInfo.LastPresentTime);

// 6. Path B: CPU readback for SW encoder (OpenH264)
// Create staging texture, CopyResource, Map, read BGRA pixels
ID3D11Texture2D *staging = CreateStagingTexture(device, w, h);
ctx->CopyResource(staging, frameTex);
D3D11_MAPPED_SUBRESOURCE mapped;
ctx->Map(staging, 0, D3D11_MAP_READ, 0, &mapped);
memcpy(pixels, mapped.pData, w * h * 4);
ctx->Unmap(staging, 0);

// 7. Release
dupl->ReleaseFrame();
```

---

## Two Output Paths

| Interface | Method | Output | Use case |
|-----------|--------|--------|----------|
| `Capturer` (CPU readback) | `NextFrame()` | BGRA `[]byte` via staging texture + Map | Pair with SW encoder (OpenH264, MF SW) |
| `D3D11Capturer` (zero-copy) | `NextTexture()` | `*D3D11Texture2D` handle | Pair with HW encoder (MF HW, NVENC, AMF, QSV) |

The pipeline picks the right method based on the paired encoder add-on.
The zero-copy path is the Windows equivalent of DMA-BUF on Linux and
IOSurface on macOS — the texture never leaves the GPU.

---

## Performance Targets

| Path | 1080p p50 | 1440p p50 | Notes |
|------|----------|----------|-------|
| **D3D11 texture (zero-copy)** | **~2ms** | **~3ms** | Just acquires the texture; cost paid by encoder |
| CPU readback (staging + Map) | ~8ms | ~12ms | CopyResource + Map is heavier than glReadPixels |

Expected (based on Sunshine/OBS benchmarks on comparable hardware):
- DXGI DD initialization: ~50ms (one-time)
- AcquireNextFrame: ~2ms (waits for vsync or new frame, whichever first)
- CopyResource to staging: ~3–5ms at 1440p (GPU→CPU DMA)
- Map + memcpy: ~2–4ms (CPU-side)

The D3D11 texture zero-copy path is the primary design target.

---

## Cursor Handling

DXGI Desktop Duplication reports cursor separately via `DXGI_OUTDUPL_POINTER_POSITION`
and `DXGI_OUTDUPL_POINTER_SHAPE_INFO`. This is a perfect match for our "separate"
cursor model:

- Cursor position reported every `AcquireNextFrame` (even when the desktop
  content hasn't changed — the cursor moved)
- Cursor shape (RGBA bitmap, ~64×64) reported only on shape change
- Pipeline sends `CursorUpdate` protocol frame to client; client composites

The cursor is **never** in the captured texture — ideal for the zero-copy
HW encode path.

---

## Multi-Monitor

Each `IDXGIOutput` represents one monitor. To capture a specific display:
enumerate outputs, pick by index or by HMONITOR coordinates.

For full multi-monitor capture (stitched into one surface), iterate all outputs
and composite — but this is rarely wanted for remote desktop (typically one
display is streamed). The TOML config would specify which output index to capture
via a future `[capture] display = 0` key.

---

## Error Recovery

| Error | Handling |
|-------|---------|
| `DXGI_ERROR_WAIT_TIMEOUT` | Normal — no new frame available. Return immediately; pipeline paces. |
| `DXGI_ERROR_ACCESS_LOST` | Desktop mode changed (resolution switch, secure desktop, UAC dialog). Recreate `DuplicateOutput`. |
| `DXGI_ERROR_ACCESS_DENIED` | Another process has exclusive fullscreen. Wait and retry. |
| `DXGI_ERROR_DEVICE_REMOVED` | GPU reset (driver crash, TDR). Fatal for this session — pipeline shuts down. |
| `E_ACCESSDENIED` at `DuplicateOutput` | Running in Session 0, or RDP without virtual display. Fatal with descriptive error. |

---

## Probe & Selection

```go
//go:build dxgi_dd

func ProbeDXGIDD() (*DXGIDDCapabilities, error) {
    // 1. CoInitializeEx (COM required)
    // 2. CreateDXGIFactory1 → enumerate adapters
    // 3. For each adapter: enumerate outputs
    // 4. For each output with active desktop: try DuplicateOutput
    // 5. Return per-output dimensions + refresh rate, or error
}
```

Pipeline probe order (Windows, multiple capture add-ons compiled in):
```
1. nvfbc_win compiled in AND NVIDIA GPU present?    → use NvFBC
2. amf_capture compiled in AND AMD GPU present?     → use AMF Display Capture
3. dxgi_dd compiled in?                              → use DXGI DD (universal)
4. None?                                             → fatal: no capture
```

---

## File Structure

```
internal/capture/dxgi/
├── dxgi.go                     // DXGICapturer struct, NewDXGICapturer
├── duplication.go              // IDXGIOutputDuplication wrapper
├── device.go                   // D3D11 device + adapter discovery
├── dxgi_cgo.go                 // CGo binding (build tag: dxgi_dd)
├── dxgi_stub.go                // No-op stub (build tag: !dxgi_dd)
├── cursor.go                   // DXGI_OUTDUPL_POINTER handling
├── probe.go                    // ProbeDXGIDD()
└── dxgi_integration_test.go    // build tag: dxgi_dd,integration
```

---

## HDR / 10-bit Support

DXGI Desktop Duplication supports `DXGI_FORMAT_R10G10B10A2_UNORM` (HDR10) on
Windows 10 1803+ with HDR enabled in Display Settings. The captured texture
format changes from `B8G8R8A8_UNORM` to `R10G10B10A2_UNORM` automatically.

For now, the implementation ignores HDR and treats everything as 8-bit BGRA.
HDR support is a future enhancement that would require:
- Tonemapping for 8-bit encode (SW path)
- Passing 10-bit surfaces to HW encoders that support HEVC Main10 profile

---

## When to use this add-on

Always, on Windows 8+. This is the universal default.

Prefer vendor-specific capture add-ons when:
- NVIDIA GPU: NvFBC gives ~50% lower capture latency
- AMD GPU: AMF Display Capture gives native AMFContext zero-copy to AMF encoder

Fall back to this when:
- Multi-vendor setup (Intel iGPU + discrete)
- Vendor-specific add-on not compiled in
- Vendor-specific capture probe fails

---

## Status

📋 **Specced — not yet implemented.** To be benchmarked on user's Windows
machine with NVIDIA + AMD GPUs once implementation lands.
