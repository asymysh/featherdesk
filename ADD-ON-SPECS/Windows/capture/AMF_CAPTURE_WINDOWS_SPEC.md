# Windows Capture Add-On: AMD AMF Display Capture

## Purpose

AMD's Advanced Media Framework (AMF) includes a display capture component
(`AMFComponentTypeDisplayCapture`) that captures the desktop framebuffer
directly through the AMD GPU driver. The output is an `AMFSurface` backed by
a D3D11 texture — the exact type the AMF encoder component consumes.

The key advantage: **same `AMFContext` for capture and encode.** When compiled
alongside the `amf` encoder add-on, capture and encode share a single AMF
context with native zero-copy surface passing. No texture copies, no
`CopyResource`, no interop — the captured surface IS the encoder input.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| AMF SDK | Apache 2.0 | Headers + runtime loader — fully open |
| AMF runtime (`amfrt64.dll`) | Ships with AMD Adrenalin driver | Part of the standard AMD driver package |
| Our CGo binding | MIT | We own this code |

**Apache 2.0 everywhere.** No driver patcher, no proprietary SDK restrictions,
no GeForce-style product gating. AMF Display Capture works on every AMD GPU
that supports VCN (Video Core Next) or VCE (Video Coding Engine).

This is a significant advantage over NvFBC, which requires a driver patcher
on consumer cards.

---

## Hardware Compatibility

| GPU | Driver | AMF Display Capture Support |
|-----|--------|-----------------------------|
| AMD RX 400+ (Polaris / GCN 4.0, 2016+) | Adrenalin 21.5+ | ✅ |
| AMD RX 5000+ (RDNA 1, 2019+) | Adrenalin 21.5+ | ✅ |
| AMD RX 6000+ (RDNA 2, 2020+) | Adrenalin 21.5+ | ✅ |
| AMD RX 7000+ (RDNA 3, 2023+) | Adrenalin 23.1+ | ✅ |
| AMD RX 9000+ (RDNA 4, 2025+) | Adrenalin 25.1+ | ✅ |
| AMD APU (Ryzen iGPU, Vega/RDNA) | Adrenalin 21.5+ | ✅ |
| AMD HD 7000–R9 300 (GCN 1.0–3.0) | ❌ | AMF runtime not available |

**Minimum: AMD Polaris (RX 400) + Adrenalin 21.5.** The user's RX GPU falls
in this range.

---

## Permission Requirements

**None.** AMF Display Capture runs in user-session context. Same requirements
as DXGI DD — an active desktop session must exist.

---

## Build & Distribution

### Go build tag

```bash
GOOS=windows go build -tags amf_capture -o viewport-rds.exe ./cmd/server
```

### Runtime dependencies

- `amfrt64.dll` (ships with AMD Adrenalin driver)
- `d3d11.dll` (system)

The AMF runtime DLL is loaded at runtime via `LoadLibrary` — the binary
compiles fine without AMD hardware (graceful probe failure).

### CGo configuration

```go
/*
#cgo CFLAGS: -I${SRCDIR}/vendor/amf/include
#cgo LDFLAGS: -ld3d11 -lole32

#include "core/Factory.h"
#include "core/Context.h"
#include "components/DisplayCapture.h"
#include "components/Component.h"
*/
import "C"
```

AMF SDK headers vendored in repo (Apache 2.0 — redistribution explicitly
permitted). Runtime is loaded dynamically at startup.

---

## CGo Implementation Sketch

```c
// 1. Load AMF runtime + create factory
AMF_RESULT res;
amf_handle hLib = amf_load_library(AMF_DLL_NAME); // "amfrt64.dll"
AMFInit_Fn initFn = (AMFInit_Fn)amf_get_proc_address(hLib, AMF_INIT_FUNCTION_NAME);
AMFFactory *factory = NULL;
initFn(AMF_FULL_VERSION, &factory);

// 2. Create AMF context with D3D11 device
AMFContext *context = NULL;
factory->CreateContext(&context);
// Reuse the SAME D3D11 device as the AMF encoder if compiled in
context->InitDX11(device, AMF_DX11_1);

// 3. Create DisplayCapture component
AMFComponent *captureComponent = NULL;
factory->CreateComponent(context, AMFDisplayCapture, &captureComponent);

// 4. Configure
captureComponent->SetProperty(AMF_DISPLAYCAPTURE_MONITOR_INDEX, 0);
captureComponent->SetProperty(AMF_DISPLAYCAPTURE_FRAMERATE, 60);
captureComponent->SetProperty(AMF_DISPLAYCAPTURE_FORMAT, AMF_SURFACE_BGRA);
captureComponent->Init(AMF_SURFACE_BGRA, 1920, 1080);

// 5. Per-frame: query output surface
AMFData *data = NULL;
res = captureComponent->QueryOutput(&data);
if (res == AMF_OK) {
    AMFSurface *surface = (AMFSurface*)data;
    // surface is D3D11-backed — pass directly to AMF encoder component
    // via SubmitInput(surface) on the encoder component

    // Path B: CPU readback
    // surface->Convert(AMF_MEMORY_HOST) → copies to system memory
    // AMFPlane *plane = surface->GetPlane(AMF_PLANE_PACKED);
    // void *pixels = plane->GetNative();
}
```

---

## Zero-Copy with AMF Encoder

When both `amf_capture` and `amf` encoder add-ons are compiled into the same
binary, they share a single `AMFContext`. The capture-to-encode path is:

```
AMFDisplayCapture.QueryOutput() → AMFSurface (D3D11 texture, GPU-resident)
    → AMFEncoder.SubmitInput(surface) — same context, zero-copy
    → AMFEncoder.QueryOutput() → AMFBuffer containing H.264/HEVC NALs
```

No `CopyResource`, no staging, no CPU access. The surface object flows
directly from the capture component to the encode component within the same
AMF pipeline. This is architecturally cleaner than NvFBC + NVENC (which
requires explicit texture registration) because AMF treats both capture and
encode as components in the same DAG.

---

## Two Output Paths

| Interface | Method | Output | Use case |
|-----------|--------|--------|----------|
| `Capturer` (CPU readback) | `NextFrame()` | BGRA `[]byte` via `surface->Convert(AMF_MEMORY_HOST)` | SW encoder — but defeats the purpose |
| `D3D11Capturer` (zero-copy) | `NextTexture()` | `AMFSurface*` (wraps `ID3D11Texture2D`) | AMF encoder (primary), also works with MF HW / NVENC via D3D11 interop |

The AMFSurface wraps a D3D11 texture, so it can technically be passed to
non-AMF HW encoders (MF, NVENC) via `GetPlane(AMF_PLANE_PACKED)->GetNative()`,
which returns the underlying `ID3D11Texture2D*`. But the native zero-copy path
is with the AMF encoder component.

---

## Performance Targets

| Path | 1080p p50 | 1440p p50 | Notes |
|------|----------|----------|-------|
| **AMFSurface zero-copy** | **~2ms** | **~2.5ms** | QueryOutput returns immediately if frame ready |
| CPU readback (`Convert(HOST)`) | ~6ms | ~10ms | GPU→CPU copy + format conversion |

Expected (based on AMF SDK samples and community measurements):
- AMF context init: ~100ms (one-time, includes D3D11 device creation)
- DisplayCapture component init: ~30ms (one-time)
- QueryOutput per frame: ~2ms (GPU compositor wait)
- Surface→Encoder submit: <0.1ms (pointer pass within same context)

Slightly higher than NvFBC (~1ms) but lower overhead than DXGI DD (~2–4ms)
because AMF capture is tightly integrated with the driver's compositor path.

---

## Cursor Handling

AMF Display Capture includes cursor state in the captured surface by default.
To match our "separate" cursor model:

- Set `AMF_DISPLAYCAPTURE_DRAW_CURSOR = false` to exclude cursor from the
  captured frame
- Query cursor position separately via Win32 `GetCursorInfo()` API
- Pipeline sends `CursorUpdate` protocol frames to client

This gives us the same cursor-free capture surface needed for zero-copy HW
encode (cursor composited client-side).

---

## Multi-Monitor

`AMF_DISPLAYCAPTURE_MONITOR_INDEX` selects which display to capture. Enumerate
available monitors via `AMF_DISPLAYCAPTURE_MONITOR_COUNT` property on the
component before initialization.

---

## Error Recovery

| Error | Handling |
|-------|---------|
| `AMF_NOT_FOUND` | `amfrt64.dll` not present — AMD driver not installed. Fatal at probe time. |
| `AMF_NO_DEVICE` | No AMD GPU found. Fatal at probe time. |
| `AMF_RESOLUTION_CHANGED` | Desktop resolution changed. Reinitialize capture component with new dimensions. |
| `AMF_INPUT_FULL` | Encoder input queue full (backpressure). Drop this capture frame, continue. |
| `AMF_EOF` / component error | Unexpected component death. Recreate the capture pipeline. |

---

## Probe & Selection

```go
//go:build amf_capture

func ProbeAMFCapture() (*AMFCaptureCapabilities, error) {
    // 1. LoadLibrary("amfrt64.dll") — fail if DLL not found
    // 2. AMFInit → get factory
    // 3. CreateContext → InitDX11 (find AMD adapter first)
    // 4. CreateComponent(AMFDisplayCapture) → if fails, no capture support
    // 5. Query monitor count + dimensions
    // 6. Return per-monitor info or ErrNoAMDGPU
}
```

Pipeline probe order (Windows):
```
1. nvfbc_win compiled in AND NVIDIA GPU present?    → use NvFBC
2. amf_capture compiled in AND AMD GPU present?     → use AMF Display Capture
3. dxgi_dd compiled in?                              → use DXGI DD (universal)
4. None?                                             → fatal: no capture
```

---

## File Structure

```
internal/capture/amf_cap/
├── amf_capture.go              // AMFCaptureCapturer struct, NewAMFCaptureCapturer
├── context.go                  // AMFContext creation + D3D11 device init
├── component.go                // DisplayCapture component lifecycle
├── amf_capture_cgo.go          // CGo binding (build tag: amf_capture)
├── amf_capture_stub.go         // No-op stub (build tag: !amf_capture)
├── cursor.go                   // Win32 GetCursorInfo polling
├── probe.go                    // ProbeAMFCapture()
└── amf_capture_integration_test.go  // build tag: amf_capture,integration
vendor/amf/include/
├── core/Factory.h              // Vendored AMF SDK headers (Apache 2.0)
├── core/Context.h
├── components/DisplayCapture.h
└── components/Component.h
```

---

## Shared AMFContext Pattern

When both `amf_capture` and `amf` (encoder) build tags are active:

```go
// internal/amfctx/ (shared package, build tag: amf_capture || amf)
var sharedContext *AMFContext  // initialized once, shared by both

func GetOrCreateContext() *AMFContext {
    // Creates D3D11 device on AMD adapter, initializes AMFContext once.
    // Both capture and encoder components attach to this same context.
}
```

This ensures zero-copy surface passing between capture → encode without
needing an explicit "interop" step. The shared context owns the D3D11 device
that both components operate on.

---

## When to use this add-on

Use when:
- AMD GPU present (Polaris / RX 400 or newer)
- Pairing with `amf` encoder add-on — native zero-copy within same context
- Want a fully Apache-2.0 capture+encode stack (no proprietary SDK constraints)
- Multi-GPU setup with AMD as the streaming GPU

Skip when:
- Non-AMD GPU (Intel, NVIDIA) — use DXGI DD or NvFBC
- AMD GPU too old (pre-Polaris) — use DXGI DD
- Cross-vendor portability needed in a single binary — DXGI DD

---

## Status

📋 **Specced — not yet implemented.** To be benchmarked on user's AMD GPU
alongside DXGI DD to validate the latency and zero-copy path with the AMF
encoder.
