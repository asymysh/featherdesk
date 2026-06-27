# Windows HW Encoder Add-On: AMD AMF Direct

## Purpose

Direct AMD Advanced Media Framework (AMF) access on Windows. AMF is **first-class on
Windows** (much more mature than the AMF-on-ROCm Linux add-on) and is what OBS,
Sunshine, and ReLive all use for AMD streaming.

Bypasses MediaFoundation to unlock AMF-specific features:
- **Pre-Analysis (PA)** — better quality per bit (~10% smaller frames at same quality)
- **AMF Smart Access Video (SAV)** — RDNA2+ low-latency mode
- Direct rate-control tuning beyond what MF exposes
- AV1 encode on RDNA3+ (RX 7000+) with full AMF tuning

For AMD-only Windows deployments where quality and tuning matter, this is the
preferred encoder.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| AMD AMF SDK | **Apache 2.0** ✅ | Cleanest of the vendor SDK licenses |
| `libamf` runtime | proprietary AMD | Ships with AMD GPU driver (no separate install) |
| Our CGo binding | MIT | We own this code |

Apache 2.0 is genuinely the friendliest license of the three vendor encoder SDKs
(NVIDIA's is a custom license, Intel's is MIT, AMD's is Apache 2.0). Headers can
be redistributed without restriction.

---

## Hardware Compatibility

| AMD GPU | H.264 | HEVC | AV1 | AMF version |
|---------|-------|------|-----|------------|
| GCN 1.0–3.0 (HD 7000–R9 200) | ✅ | ❌ | ❌ | 1.x |
| Polaris (RX 400/500) | ✅ | ✅ | ❌ | 1.4+ |
| Vega | ✅ | ✅ | ❌ | 1.4+ |
| RDNA 1 (RX 5000) | ✅ | ✅ | ❌ | 1.4+ |
| RDNA 2 (RX 6000) | ✅ | ✅ | ❌ decode only | 1.4+ |
| RDNA 3 (RX 7000) | ✅ | ✅ | ✅ | 1.4+ |
| RDNA 4 (RX 9000) | ✅ | ✅ | ✅ | 1.4+ |

Probe via `AMFCreateContext` + `InitDX11` + enumerate available codec components.

---

## Build & Distribution

```bash
go build -buildmode=c-shared -o featherdesk-addon-amf.dll ./internal/encode/amf
```

CGo config:

```go
/*
#cgo CFLAGS: -I${SRCDIR}/amf/include
#cgo LDFLAGS: -lole32 -loleaut32 -ld3d11 -ldxgi

#include <core/Factory.h>
#include <core/Context.h>
#include <components/VideoEncoderVCE.h>
#include <components/VideoEncoderHEVC.h>
#include <components/VideoEncoderAV1.h>
#include <d3d11.h>
*/
import "C"
```

`amf.lib` is loaded dynamically via `LoadLibrary` (the AMF SDK doesn't ship a
static import library). SDK headers vendored under `internal/encode/amf/amf/`.

---

## CGo Implementation Sketch

```c
// 1. Load AMF DLL and get factory
HMODULE amfModule = LoadLibraryW(L"amfrt64.dll");
typedef AMF_RESULT (AMF_CDECL_CALL *AMFInit_Fn)(amf_uint64 version, AMFFactory** factory);
AMFInit_Fn amfInit = (AMFInit_Fn)GetProcAddress(amfModule, "AMFInit");
AMFFactory *factory = NULL;
amfInit(AMF_FULL_VERSION, &factory);

// 2. Create context bound to D3D11 device (shared with DXGI capture)
AMFContext *ctx = NULL;
factory->lpVtbl->CreateContext(factory, &ctx);
ctx->lpVtbl->InitDX11(ctx, d3d11Device, AMF_DX11_0);

// 3. Create encoder
AMFComponent *encoder = NULL;
factory->lpVtbl->CreateComponent(factory, ctx, AMFVideoEncoderVCE_AVC, &encoder);

// 4. Configure for ultra low latency
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_USAGE, AMF_VIDEO_ENCODER_USAGE_ULTRA_LOW_LATENCY);
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_PROFILE, AMF_VIDEO_ENCODER_PROFILE_BASELINE);
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_RATE_CONTROL_METHOD, AMF_VIDEO_ENCODER_RATE_CONTROL_METHOD_CBR);
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_TARGET_BITRATE, bitrate);
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_B_PIC_PATTERN, 0);
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_IDR_PERIOD, 0);
encoder->lpVtbl->SetProperty(encoder, AMF_VIDEO_ENCODER_PRE_ANALYSIS_ENABLE, true);   // PA!
encoder->lpVtbl->Init(encoder, AMF_SURFACE_NV12, W, H);

// 5. Per frame: wrap D3D11 texture as AMFSurface, submit
AMFSurface *surface = NULL;
ctx->lpVtbl->CreateSurfaceFromDX11Native(ctx, d3d11Texture, &surface, NULL);
encoder->lpVtbl->SubmitInput(encoder, (AMFData*)surface);

// 6. Read output
AMFData *output = NULL;
while (encoder->lpVtbl->QueryOutput(encoder, &output) == AMF_OK) {
    AMFBuffer *buf = AMFBufferFromData(output);
    // buf bytes = encoded NALs (Annex B)
}
```

### Zero-copy DXGI → AMF

`ctx->CreateSurfaceFromDX11Native` wraps an existing `ID3D11Texture2D` as an
`AMFSurface` without copying. This is the canonical Windows AMD path:
DXGI Desktop Duplication → ID3D11Texture2D → AMFSurface → encoder.

---

## Performance

### Measured (real hardware, Ryzen 9 5900X host)

| AMD GPU | Codec | 1080p ms | 1440p ms | FPS @ 1080p |
|---------|-------|---------|---------|------------|
| **RX 6800 XT (RDNA2)** | H.264 | **5.9ms** | **9.3ms** | 169 |
| **RX 6800 XT (RDNA2)** | HEVC | **5.0ms** | **7.5ms** | 199 |

**Interesting finding:** HEVC is faster than H.264 on the RX 6800 XT. AMD's
VCN3 encoder is HEVC-optimized — the H.264 path goes through a less-optimized
code path.

### Estimated (no measured hardware — earlier rough projections)

| AMD GPU | 1080p p50 | 1440p p50 |
|---------|----------|----------|
| RX 6600 (RDNA2) | ~6ms | ~10ms |
| RX 7900 XTX (RDNA3) | ~3ms | ~5ms |

Pre-Analysis reduces bandwidth by ~10–20% at the same visual quality for a small
encode latency cost — often worth it for bandwidth-constrained scenarios.

---

## File Structure

```
internal/encode/amf/
├── amf.go
├── amf_cgo_windows.go    // built into the amf add-on's shared library (//go:build windows)
├── amf_cgo_linux.go      // built into the amf_rocm add-on's shared library, Linux ROCm variant (//go:build linux)
├── d3d11_interop.go      // CreateSurfaceFromDX11Native wrapping
├── probe.go
├── amf/                  // AMF SDK headers (Apache 2.0)
└── amf_test.go
```

No `!amf` / `!amf_rocm` stub files are needed — each variant is its own shared library
(`amf` on Windows, `amf_rocm` on Linux); an absent add-on is simply a `.dll`/`.so`
that isn't in the add-ons directory.

---

## When to use this add-on

Use this add-on when:
- AMD-only deployment
- Quality at given bitrate matters (Pre-Analysis advantage)
- RDNA3+ with AV1 needs full AMF tuning (not just MF defaults). RDNA 2 has no AV1 encode.

Skip when:
- Heterogeneous fleet (use MF HW for cross-vendor in one binary)
- NVIDIA or Intel deployment

---

## Status

📋 Specced — not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_amf]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements `stream.ConfigurableHardwareEncoder` (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). AMF supports hot reconfiguration for most parameters via `SetProperty` on the running VCE component.

| Param change | AMF API | Hot? |
|--------------|---------|------|
| `FPS` | `SetProperty(AMF_VIDEO_ENCODER_FRAMERATE, AMFRate{num,den})` | yes |
| `BitrateBps` | `SetProperty(AMF_VIDEO_ENCODER_TARGET_BITRATE, b)` | yes |
| `QP` | `SetProperty(AMF_VIDEO_ENCODER_QP_I/QP_P, qp)` | yes |
| `KeyframeInterval` | `SetProperty(AMF_VIDEO_ENCODER_IDR_PERIOD, ki)` | yes |
| `Width`, `Height` | `Terminate` + `ReInit` (returns `stream.ErrRequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | HEVC Main10 -- `AMF_VIDEO_ENCODER_HEVC_PROFILE_MAIN_10`; requires session-start negotiation (returns `stream.ErrRequiresRestart`) | no |
| `NetworkRTTMs`, `PacketLossPct` | Feeds `HQVBR_QVBR` quality boost | yes |