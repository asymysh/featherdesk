# Linux Add-On: NVIDIA NvFBC Capture

## Purpose

Optional add-on capture backend for **NVIDIA proprietary driver users**. Uses NvFBC
(NVIDIA Frame Buffer Capture) — NVIDIA's official direct-from-GPU framebuffer capture
API — to bypass the display compositor and reduce capture latency to the absolute
minimum NVIDIA hardware can produce.

**Why this matters vs KMS+EGL on NVIDIA:**

| Method | Latency | Notes |
|--------|---------|-------|
| KMS+EGL on NVIDIA proprietary | ~3–5ms | Requires `nvidia-drm.modeset=1`, sometimes finicky on multi-monitor |
| **NvFBC** | **~1–2ms** | Reads GPU framebuffer directly, no compositor mediation |

NvFBC is the same capture path NVIDIA's own GeForce Experience / GameStream used.
It's what Sunshine prefers on NVIDIA hardware. The ~2–3ms reduction matters for
60fps+ streaming.

---

## License & Distribution Constraint

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Video Codec SDK (NvFBC headers) | NVIDIA Software License (free use) | Cannot redistribute headers as standalone package |
| `libnvidia-fbc.so` (driver-shipped) | proprietary NVIDIA | Ships with NVIDIA proprietary driver |
| `NvFBCUnlock` patcher (consumer cards) | community / various | Required on GeForce; **not** required on Quadro/Tesla |
| Our CGo binding | MIT | We own this code |

**No royalties.** No GPL/LGPL contamination. Same SDK license model as NVENC.

---

## The Consumer Card Restriction (important)

NVIDIA artificially restricts NvFBC on consumer hardware:

| GPU class | Native NvFBC support |
|-----------|---------------------|
| GeForce (GTX, RTX) | ❌ Disabled by driver |
| Quadro / RTX Workstation | ✅ Enabled |
| Tesla / Data Center | ✅ Enabled |

**The community workaround:** `nvfbc-patcher` / `NvFBCUnlock` modifies the proprietary
driver's `libnvidia-fbc.so` to enable NvFBC on consumer cards. This is what Sunshine
documents as standard procedure for its NVIDIA users.

> **FeatherDesk does NOT ship a patcher.** We document that users on consumer cards
> need to run the patcher themselves (well-known projects on GitHub). On Quadro/Tesla
> the add-on works out of the box.

---

## Hardware Compatibility

NvFBC has been in every NVIDIA proprietary driver from ~2014 onward. Supported on:

| GPU | NvFBC available | Patcher needed? |
|-----|----------------|----------------|
| Kepler Quadro (K-series) | ✅ | No |
| Maxwell+ Quadro / RTX Workstation | ✅ | No |
| Tesla / Data Center (any) | ✅ | No |
| Kepler+ GeForce (GTX 600–RTX 5000) | ✅ via patcher | Yes |

---

## Build & Distribution

### Go build tag

```bash
go build -tags nvfbc -o featherdesk-linux-nvfbc-capture ./cmd/server
```

The `nvfbc` build tag pulls in `internal/capture/nvfbc/` package.

### Runtime dependencies

- NVIDIA proprietary driver 470+
- `libnvidia-fbc.so` (ships with driver)
- If GeForce: NvFBCUnlock patcher applied to driver
- Video Codec SDK headers (build-time only — checked into source tree per NVIDIA
  SDK license, same as the NVENC add-on)

### CGo configuration

```go
/*
#cgo CFLAGS: -I${SRCDIR}/sdk
#cgo LDFLAGS: -L/usr/lib/x86_64-linux-gnu -lnvidia-fbc -lcuda -ldl

#include <NvFBC.h>            // Unified NvFBC 7.x+ header (replaces old NvFBCToSys.h / NvFBCToCuda.h)
*/
import "C"
```

---

## CGo Implementation Sketch

```c
// Session init (one-time)
NVFBC_GET_STATUS_PARAMS statusParams = { 0 };
statusParams.dwVersion = NVFBC_GET_STATUS_PARAMS_VER;
pFnList->nvFBCGetStatus(handle, &statusParams);
// statusParams.bIsCapturePossible must be true

NvFBC_CreateHandleParams createParams = { NVFBC_CREATE_HANDLE_VER };
NVFBC_SESSION_HANDLE session;
pNvFBCAPI->nvFBCCreateHandle(&session, &createParams);

// Create capture session
NvFBC_CreateCaptureSessionParams sessionParams = { NVFBC_CREATE_CAPTURE_SESSION_VER };
sessionParams.eCaptureType    = NVFBC_CAPTURE_SHARED_CUDA;
sessionParams.eTrackingType   = NVFBC_TRACKING_DEFAULT;
sessionParams.bWithCursor     = NVFBC_FALSE;   // cursor sent via CursorUpdate protocol
sessionParams.dwSamplingRateMs = 0;             // capture on demand, not periodic
sessionParams.bDisableAutoModesetRecovery = NVFBC_FALSE;
pNvFBCAPI->nvFBCCreateCaptureSession(session, &sessionParams);

// Set output format
NvFBC_ToCudaSetUpParams cudaParams = { NVFBC_TOCUDA_SETUP_PARAMS_VER };
cudaParams.eBufferFormat = NVFBC_BUFFER_FORMAT_NV12;  // direct feed to NVENC
pNvFBCAPI->nvFBCToCudaSetUp(session, &cudaParams);

// Per-frame capture
NvFBC_FrameGrabInfo frameInfo = { 0 };
CUdeviceptr cudaPtr;
NvFBC_ToCudaGrabFrameParams grabParams = { NVFBC_TOCUDA_GRAB_FRAME_PARAMS_VER };
grabParams.dwFlags = NVFBC_TOCUDA_GRAB_FLAGS_NOFLAGS;  // blocking, full frame
grabParams.pCUDADeviceBuffer = &cudaPtr;
grabParams.pNvFBCFrameGrabInfo = &frameInfo;
pNvFBCAPI->nvFBCToCudaGrabFrame(session, &grabParams);

// cudaPtr now points to an NV12 frame in GPU memory
// Pass directly to NVENC encoder — true zero-copy
```

---

## Integration with NVENC encoder

NvFBC pairs perfectly with the NVENC add-on:

```
NvFBC (GPU framebuffer)
    → CUDA buffer (NV12, GPU memory)
    → NVENC encoder (same GPU, no CPU touch)
    → H.264 / HEVC / AV1 NAL units (small CPU readback)
```

This is the same zero-copy GPU-resident pipeline Sunshine uses for NVIDIA streaming.
Total bus bandwidth per frame: ~30KB compressed NALs only. Sub-2ms total capture +
encode latency on RTX 3060+.

If NvFBC is paired with VA-API instead, an extra GPU→GPU copy is needed to
bridge CUDA → VA-API. Still better than KMS+EGL but loses the purest zero-copy
story. For NVIDIA deployments, **ship NvFBC and NVENC together**.

---

## Performance Targets

| GPU class | 1080p capture p50 | 1440p capture p50 | CPU |
|-----------|------------------|------------------|-----|
| RTX 3060 | ~1ms | ~1.5ms | <1% |
| RTX 4090 | <1ms | ~1ms | <1% |

Compared to KMS+EGL on the same GPU (NVIDIA proprietary driver): roughly 2× faster
capture and significantly more reliable on multi-monitor setups.

---

## Capture Modes

NvFBC supports multiple output buffer types:

| Mode | Surface | Use case |
|------|---------|----------|
| `NVFBC_CAPTURE_SHARED_CUDA` | CUDA device pointer | **Preferred** — direct feed to NVENC |
| `NVFBC_CAPTURE_TO_SYS` | System memory NV12 | Fallback — CPU readback, ~5ms slower |
| `NVFBC_CAPTURE_TO_GL` | OpenGL texture | For interop with EGL/Vulkan |
| `NVFBC_CAPTURE_TO_HW_ENCODER` | Direct-to-NVENC | Internal, used by GFE |

We default to `NVFBC_CAPTURE_SHARED_CUDA` because it's the cleanest interop with the
NVENC add-on encoder.

---

## Probe & Selection

```go
//go:build nvfbc

func ProbeNvFBC() (*NvFBCCapabilities, error) {
    // 1. dlopen libnvidia-fbc.so (check NVIDIA proprietary driver presence)
    // 2. NvFBC_GetStatus → check bIsCapturePossible
    // 3. If false on consumer card, return ErrNvFBCRestricted (suggest patcher)
    // 4. Enumerate display outputs, return resolution/refresh per output
}
```

Pipeline probes capture in this order on Linux:
```
NvFBC available AND on NVIDIA?      → use NvFBC (this add-on)
KMS+EGL with root / CAP_SYS_ADMIN?  → use KMS+EGL
None?                                → fatal: no capture add-on configured
```

---

## File Structure

```
internal/capture/nvfbc/
├── nvfbc.go                 // Capturer struct, NewNvFBCCapturer
├── nvfbc_cgo.go             // CGo binding (build tag: nvfbc)
├── nvfbc_stub.go            // No-op stub (build tag: !nvfbc)
├── probe.go                 // ProbeNvFBC()
├── cuda_to_nvenc.go         // Direct CUDA → NVENC handoff
├── sdk/                     // NVIDIA SDK headers (NvFBC.h etc.)
└── nvfbc_test.go            // Integration tests
```

---

## When to use this add-on

Use this add-on when:
- Target deployment has NVIDIA GPUs running the proprietary driver
- Minimum capture latency matters (60fps+ streaming, gaming-grade)
- Paired with NVENC add-on for full zero-copy GPU-resident pipeline
- Quadro/Tesla deployment (no patcher needed) — biggest win

Stick with default KMS+EGL when:
- NVIDIA open-source driver (Nouveau / NVK) — NvFBC unavailable
- Don't want to run a driver patcher on consumer cards
- Single-monitor 30fps remote control is sufficient (latency budget already met)

---

## Status

📋 Specced — not yet built. Implementation priority: medium-high for NVIDIA-targeted
deployments. Pairs with NVENC encoder add-on for the canonical NVIDIA streaming
pipeline (same approach as Sunshine).

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_nvfbc]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements `stream.ConfigurableCapturer` (see [`../../../../specs/MODULE_STREAM_PARAMS.md`](../../../../specs/MODULE_STREAM_PARAMS.md)). NvFBC captures at native resolution; the pipeline handles scaling.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Output is native -- pipeline scales. No capturer change needed. | n/a (pipeline) |
| `FPS` | Pipeline pacing. NvFBC grabs are synchronous (`NvFBCFrameGrab`). | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | NvFBC does NOT have a dedicated HDR buffer format constant. Use `NVFBC_BUFFER_FORMAT_BGRA` -- HDR metadata (if any) comes from the display driver. NvFBC HDR support is limited and Quadro/Tesla only. | requires re-init |
| `ColorSpace` | Reported per frame; pipeline annotates encoder | n/a (read-only) |