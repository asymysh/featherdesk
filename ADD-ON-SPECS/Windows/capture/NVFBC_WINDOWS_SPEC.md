# Windows Capture Add-On: NvFBC (NVIDIA Frame Buffer Capture)

## Purpose

NVIDIA's proprietary frame buffer capture API for Windows. Captures the
composed desktop framebuffer directly from the NVIDIA GPU before it reaches
the display — bypassing DXGI Desktop Duplication entirely. Produces a CUDA
buffer or D3D11 texture with ~50% lower latency than DXGI DD on the same
hardware.

The NVIDIA-specific capture add-on for Windows. Same API as the Linux NvFBC
add-on, nearly identical CGo binding, same licensing constraints. Pairs
perfectly with the `nvenc` encoder add-on for a 100% NVIDIA-resident
zero-copy pipeline (capture → encode without leaving the GPU).

---

## License & Distribution Constraint

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Capture SDK 8.0+ | NVIDIA proprietary | Free download, royalty-free use; redistribution of SDK headers allowed |
| NvFBC runtime (`nvfbc64.dll`) | Ships with NVIDIA driver | Part of the standard Game Ready / Studio driver |
| Our CGo binding | MIT | We own this code |

### The GeForce restriction (same as Linux)

NvFBC is **officially supported only on Quadro / Tesla / RTX Enterprise**
cards. On GeForce / consumer RTX cards, the API returns
`NVFBC_ERR_UNSUPPORTED` unless the driver is patched.

The community patcher (same tool as Linux: patches `nvEncodeAPI64.dll` and
`nvfbc64.dll` to remove the product-check) is widely used by Sunshine,
Moonlight, and GameStream users.

**Our position:** We don't ship or endorse the patcher. The add-on spec and
code work correctly on both patched-GeForce and official-Quadro drivers.
Distribution of the binary is clean (no patched NVIDIA code included).

---

## Hardware Compatibility

| GPU | Driver | NvFBC Support |
|-----|--------|---------------|
| Quadro K-series+ (Kepler 2012+) | Any R470+ | ✅ Official |
| Tesla K/M/V/A/H-series | Any R470+ | ✅ Official |
| RTX A-series (enterprise) | Any R470+ | ✅ Official |
| GeForce GTX 600+ (Kepler) | Patched driver | ⚠️ Community patcher required |
| GeForce RTX 20/30/40/50 | Patched driver | ⚠️ Community patcher required |

**Minimum driver:** R470+ (Capture SDK 8.0 baseline).

---

## Permission Requirements

**None.** Runs in user-session context. No elevation, no capabilities. The
NvFBC DLL is loaded from the standard NVIDIA driver path.

Same headless caveat as DXGI DD: a desktop session must be active. For
headless NVIDIA GPU servers, use NVIDIA's built-in virtual display
(available on Quadro/Tesla without third-party drivers).

---

## Build & Distribution

### Go build tag

```bash
GOOS=windows go build -tags nvfbc_win -o viewport-rds.exe ./cmd/server
```

### Runtime dependencies

- `nvfbc64.dll` (ships with NVIDIA display driver)
- `nvcuda.dll` (ships with NVIDIA display driver) — only if using CUDA output mode
- `d3d11.dll` (system) — if using D3D11 texture output mode

### CGo configuration

```go
/*
#cgo CFLAGS: -I${SRCDIR}/vendor/nvfbc
#cgo LDFLAGS: -lnvfbc64

#include "NvFBC/nvFBC.h"
#include "NvFBC/nvFBCToSys.h"
#include "NvFBC/nvFBCToDx.h"
#include "NvFBC/nvFBCToCuda.h"
*/
import "C"
```

The NvFBC SDK headers are vendored in our repo (redistribution permitted by
NVIDIA's SDK license). The runtime DLL is loaded from the driver installation
path — we never ship NVIDIA binaries.

---

## CGo Implementation Sketch

```c
// 1. Load NvFBC and create instance
NVFBC_API_FUNCTION_LIST nvFBC;
NvFBCCreateInstance(&nvFBC);

NVFBC_CREATE_HANDLE_PARAMS createParams = {0};
createParams.dwVersion = NVFBC_CREATE_HANDLE_PARAMS_VER;
NVFBCSTATUS status = nvFBC.nvFBCCreateHandle(&handle, &createParams);

// 2. Create capture session — D3D11 interop mode for encoder pairing
NVFBC_CREATE_CAPTURE_SESSION_PARAMS sessionParams = {0};
sessionParams.dwVersion = NVFBC_CREATE_CAPTURE_SESSION_PARAMS_VER;
sessionParams.eCaptureType = NVFBC_CAPTURE_TO_DX; // D3D11 texture output
sessionParams.eTrackingType = NVFBC_TRACKING_OUTPUT; // full output (monitor)
sessionParams.dwOutputId = 0; // primary display
sessionParams.frameSize.w = 1920;
sessionParams.frameSize.h = 1080;
nvFBC.nvFBCCreateCaptureSession(handle, &sessionParams);

// 3. Per-frame: grab
NVFBC_TODX_GRAB_FRAME_PARAMS grabParams = {0};
grabParams.dwVersion = NVFBC_TODX_GRAB_FRAME_PARAMS_VER;
grabParams.dwFlags = NVFBC_TODX_GRAB_FLAGS_NOWAIT;
nvFBC.nvFBCToDxGrabFrame(handle, &grabParams);

// grabParams contains the D3D11 texture handle (ID3D11Texture2D*)
ID3D11Texture2D *frameTex = grabParams.pTexture;
// Pass directly to NVENC encoder — same D3D11 device, zero-copy

// 4. Alternative: CUDA output (for direct NVENC CUDA path)
// sessionParams.eCaptureType = NVFBC_CAPTURE_TO_CUDA;
// grabParams.pCUDADeviceBuffer → pass to NVENC cuvidMapVideoFrame
```

---

## Output Modes

| Mode | Output | Best paired with |
|------|--------|------------------|
| `NVFBC_CAPTURE_TO_DX` | `ID3D11Texture2D*` | NVENC (D3D11 input), MF HW encoder |
| `NVFBC_CAPTURE_TO_CUDA` | `CUdeviceptr` (CUDA buffer) | NVENC (CUDA input) — lowest possible latency |
| `NVFBC_CAPTURE_TO_SYS` | System memory (CPU) | SW encoders (OpenH264) — defeats the purpose |

**Recommended: `CAPTURE_TO_DX`** for NVENC with D3D11 input surfaces. The
CUDA path (`CAPTURE_TO_CUDA`) is marginally faster (~0.5ms) but requires
NVENC to use its CUDA encoding path rather than D3D11, adding complexity.

---

## Two Output Paths

| Interface | Method | Output | Use case |
|-----------|--------|--------|----------|
| `Capturer` (CPU readback) | `NextFrame()` | BGRA `[]byte` via `CAPTURE_TO_SYS` | SW encoder — but defeats the purpose of NvFBC |
| `D3D11Capturer` (zero-copy) | `NextTexture()` | `*D3D11Texture2D` handle | NVENC encoder (the whole point) |

In practice, NvFBC is only compiled into a binary that also includes the
`nvenc` encoder add-on. Using NvFBC with a SW encoder is technically possible
but nonsensical — use DXGI DD + SW encoder instead.

---

## Performance Targets

| Path | 1080p p50 | 1440p p50 | Notes |
|------|----------|----------|-------|
| **D3D11 texture (zero-copy)** | **~1ms** | **~1.5ms** | Direct framebuffer grab — fastest Windows capture |
| **CUDA buffer** | **~0.8ms** | **~1.2ms** | Marginally faster; skips D3D11 interop overhead |
| CPU readback (`TO_SYS`) | ~5ms | ~8ms | Defeats purpose; use DXGI DD for CPU path |

Expected based on Sunshine measurements on RTX 3080:
- NvFBC session creation: ~20ms (one-time)
- Grab frame (D3D11): ~1ms (includes GPU-side compose wait)
- Grab frame (CUDA): ~0.8ms

Compare to DXGI DD on the same hardware: ~2–3ms. NvFBC is ~50% faster.

---

## Cursor Handling

NvFBC reports cursor state via `NVFBC_FRAME_GRAB_INFO.dwPointerFlags` and
the separate cursor capture API. Cursor is **not** in the grabbed texture
by default (matches our "separate" cursor model). The pipeline sends cursor
position + shape via `CursorUpdate` protocol frames.

---

## Integration with NVENC encoder

The ideal zero-copy pipeline:

```
NvFBC (CAPTURE_TO_DX) → ID3D11Texture2D
    → NVENC registers texture as input resource
    → nvEncEncodePicture() reads from the same texture
    → output: H.264/HEVC NALs

Total capture+encode: ~2–3ms at 1080p (measured on RTX 3080 by Sunshine)
```

No `CopyResource`, no staging texture, no CPU touch, no system memory
allocation in the hot path. The texture stays on the GPU from capture through
encode.

---

## Probe & Selection

```go
//go:build nvfbc_win

func ProbeNvFBCWin() (*NvFBCWinCapabilities, error) {
    // 1. LoadLibrary("nvfbc64.dll") — fail if DLL not found
    // 2. NvFBCCreateInstance → get function table
    // 3. NvFBCCreateHandle → checks driver version + product SKU
    //    Returns NVFBC_ERR_UNSUPPORTED on unpatched GeForce
    // 4. NvFBCGetStatus → output count, dimensions
    // 5. Return per-output info or ErrUnsupported
}
```

Pipeline probe order (Windows):
```
1. nvfbc_win compiled in AND probe succeeds?        → use NvFBC
2. amf_capture compiled in AND AMD GPU present?     → use AMF Display Capture
3. dxgi_dd compiled in?                              → use DXGI DD (universal)
4. None?                                             → fatal: no capture
```

---

## File Structure

```
internal/capture/nvfbc_win/
├── nvfbc.go                    // NvFBCWinCapturer struct, NewNvFBCWinCapturer
├── session.go                  // NvFBC session management (create/destroy/recreate)
├── nvfbc_cgo.go                // CGo binding (build tag: nvfbc_win)
├── nvfbc_stub.go               // No-op stub (build tag: !nvfbc_win)
├── cursor.go                   // NvFBC cursor capture
├── probe.go                    // ProbeNvFBCWin()
└── nvfbc_integration_test.go   // build tag: nvfbc_win,integration
vendor/nvfbc/
├── NvFBC/nvFBC.h               // Vendored SDK headers (redistribution permitted)
├── NvFBC/nvFBCToSys.h
├── NvFBC/nvFBCToDx.h
└── NvFBC/nvFBCToCuda.h
```

---

## Error Recovery

| Error | Handling |
|-------|---------|
| `NVFBC_ERR_UNSUPPORTED` | Unpatched GeForce driver. Fatal — probe should have caught this. |
| `NVFBC_ERR_INVALIDATED` | Session invalidated (desktop mode change, secure desktop). Recreate session. |
| `NVFBC_ERR_DRIVER_FAILURE` | Driver crash / TDR. Fatal for this session. |
| `NVFBC_ERR_NVIDIA_DRV` | Driver too old. Fatal with descriptive error (min R470). |

---

## When to use this add-on

Use when:
- NVIDIA GPU present (Quadro/Tesla official; GeForce with patcher)
- Want lowest possible capture latency on NVIDIA
- Pairing with NVENC encoder for full NVIDIA zero-copy pipeline
- Gaming / low-latency streaming scenario

Skip when:
- Non-NVIDIA GPU (Intel, AMD) — use DXGI DD or AMF capture
- Unpatched GeForce and unwilling to patch — use DXGI DD
- Multi-vendor setup where portability matters — DXGI DD

---

## Status

📋 **Specced — not yet implemented.** To be benchmarked alongside DXGI DD on
user's NVIDIA GPU to validate the ~50% latency delta.
