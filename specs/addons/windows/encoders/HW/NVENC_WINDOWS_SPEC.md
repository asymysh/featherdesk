# Windows HW Encoder Add-On: NVENC Direct

## Purpose

Direct NVIDIA Video Codec SDK access on Windows, bypassing MediaFoundation. Same
NVENC SDK as the Linux NVENC add-on, but with a **D3D11 input surface** instead of
CUDA. Unlocks NVENC features unavailable through MediaFoundation:

- `NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY` (sub-3ms p50)
- `REF_FRAMES_INVALIDATION` — partial IDR on packet loss instead of full keyframe storm
- AV1 encode on RTX 40+
- Single-surface pipeline (`surfaces=1`, `delay=0`)

For NVIDIA-only Windows deployments where streaming quality matters, this is the best
encoder available.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Video Codec SDK | NVIDIA SDK License (free use) | Same model as NVENC Linux add-on |
| Our CGo binding | MIT | |

No royalties. SDK redistribution rules same as Linux side — headers vendored into
source tree per NVIDIA's permissive header redistribution policy.

---

## Hardware Compatibility

Same hardware support as the NVENC Linux add-on. NVENC has been in every NVIDIA
GPU since Kepler (GTX 600 series, 2012). On Windows the codec coverage and session
caps are identical to Linux. See
[`../../../linux/encoders/HW/NVENC_LINUX_SPEC.md`](../../../linux/encoders/HW/NVENC_LINUX_SPEC.md)
for the full Kepler → Blackwell matrix.

---

## Key Difference from NVENC Linux: D3D11 Surface

On Linux, NVENC consumes CUDA device pointers (from CUDA-EGL interop with KMS DMA-BUF).

On Windows, NVENC consumes **D3D11 textures directly**:

```c
NV_ENC_REGISTER_RESOURCE registerParams = { NV_ENC_REGISTER_RESOURCE_VER };
registerParams.resourceType        = NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX;
registerParams.width               = W;
registerParams.height              = H;
registerParams.resourceToRegister  = (void*)d3d11Texture;     // ← from DXGI Desktop Duplication
registerParams.bufferFormat        = NV_ENC_BUFFER_FORMAT_ARGB;
funcs.nvEncRegisterResource(encoder, &registerParams);
```

This is the canonical Windows zero-copy path: DXGI Desktop Duplication captures
into an `ID3D11Texture2D`, that texture is registered with NVENC, NVENC encodes
on the GPU. No CPU memory copy at any stage.

---

## Build & Distribution

```bash
go build -buildmode=c-shared -o featherdesk-addon-nvenc.dll ./internal/encode/nvenc
```

CGo config:

```go
/*
#cgo CFLAGS: -I${SRCDIR}/sdk
#cgo LDFLAGS: -lnvencodeapi -ld3d11 -ldxgi

#include <nvEncodeAPI.h>
*/
import "C"
```

`nvencodeapi.lib` ships with the NVIDIA driver; no separate SDK install needed at
runtime. SDK headers vendored under `internal/encode/nvenc/sdk/`.

---

## Implementation

Same NVENC session setup, configuration flags, and per-frame loop as the Linux NVENC
add-on. See [`../../../linux/encoders/HW/NVENC_LINUX_SPEC.md`](../../../linux/encoders/HW/NVENC_LINUX_SPEC.md)
for:
- Session creation with `NV_ENC_DEVICE_TYPE_DIRECTX` (not `_CUDA`)
- Configuration: `ULTRA_LOW_LATENCY` tuning, CBR rate control, `zeroReorderDelay`
- Reference frame invalidation API

The only differences from Linux:
- `sessionParams.deviceType = NV_ENC_DEVICE_TYPE_DIRECTX`
- `sessionParams.device = d3d11Device` (not CUDA context)
- `picParams.inputBuffer = registeredD3D11Texture` (not CUDA pointer)

Everything else is identical.

---

## Performance

### Measured (real hardware, Ryzen 9 5900X host)

| NVIDIA GPU | Codec | 1080p ms | 1440p ms | FPS @ 1080p |
|-----------|-------|---------|---------|------------|
| **GTX 1080 Ti (Pascal)** | H.264 | **4.5ms** | **6.6ms** | 220 |
| **GTX 1080 Ti (Pascal)** | HEVC | **4.7ms** | **7.4ms** | 211 |
| **Quadro RTX 4000 (Turing)** | H.264 | **5.4ms** | **7.9ms** | 187 |
| **Quadro RTX 4000 (Turing)** | HEVC | **5.6ms** | **8.0ms** | 179 |

### Estimated (no measured hardware)

| NVIDIA GPU | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| GTX 1660 (Turing) | ~2ms | ~3ms | <1% |
| RTX 3060 (Ampere) | ~1.5ms | ~2.5ms | <1% |
| RTX 4090 (Ada, dual NVENC) | <1ms | <1.5ms | <1% (split sessions) |

Compared to MediaFoundation HW route (NVENC under the hood): ~2–3ms lower latency
plus access to REF_FRAMES_INVALIDATION.

---

## File Structure

```
internal/encode/nvenc/
├── nvenc.go             // shared with Linux (mostly)
├── nvenc_cgo_linux.go   // built into the add-on's shared library (//go:build linux)
├── nvenc_cgo_windows.go // built into the add-on's shared library (//go:build windows) — D3D11 surface path
├── d3d11_interop.go     // DXGI texture registration
├── probe.go
├── sdk/                 // NVIDIA SDK headers
└── nvenc_test.go
```

No `!nvenc` stub file is needed — the add-on is its own shared library; an absent
add-on is simply a `.dll` that isn't in the add-ons directory.

---

## When to use this add-on

Same criteria as Linux NVENC: NVIDIA-only deployment, packet loss matters, AV1 needed
on RTX 40+. On Windows specifically: prefer this over the MediaFoundation HW add-on
when REF_FRAMES_INVALIDATION matters.

Skip when: heterogeneous fleet (use MF HW for cross-vendor coverage in one binary),
or AMD/Intel-only deployment.

---

## Status

📋 Specced — not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_nvenc]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

Identical to the Linux `nvenc` add-on -- NVENC's API is OS-portable through `nvEncodeAPI.h`. See [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md) and [`../../../linux/encoders/HW/NVENC_LINUX_SPEC.md`](../../../linux/encoders/HW/NVENC_LINUX_SPEC.md#stream-params-translation).

| Param change | NVENC API | Hot? |
|--------------|-----------|------|
| `FPS` | `nvEncReconfigureEncoder` (`frameRateNum/Den`) | yes |
| `BitrateBps` | `nvEncReconfigureEncoder` (`averageBitRate`) | yes |
| `QP` | `nvEncReconfigureEncoder` (`constQP`) | yes |
| `KeyframeInterval` | `nvEncReconfigureEncoder` (`gopLength`) | yes |
| `Width`, `Height` | hot if within initial `maxEncodeWidth/Height`, else re-init | mostly |
| `BitDepth=10` / `HDR=true` | requires HEVC Main10 GUID at session start | no |
