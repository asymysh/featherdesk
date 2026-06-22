# Linux Add-On: AMD AMF on ROCm

## Purpose

Optional add-on binary for **AMD GPUs on Linux** that calls AMD's Advanced Media
Framework (AMF) directly via the ROCm runtime. Replaces the default VA-API path
(which on AMD uses the Mesa `radeonsi` VA driver) with AMD's native API.

**When this matters vs default VA-API:** the Mesa VA-API path is excellent and is the
right default for AMD on Linux for almost all users. AMF on ROCm is **niche** — it
exists for users who already have a ROCm stack installed (typically AI/ML workloads)
and want to use AMD's own encode tuning, or who need AMF-specific features that Mesa
hasn't exposed yet.

| Feature | Mesa VA-API | AMF on ROCm |
|---------|------------|-------------|
| H.264 / HEVC encode | ✅ | ✅ |
| AV1 encode (RDNA2+) | ✅ | ✅ |
| Pre-analysis (PA) | partial | ✅ — better quality per bit |
| AMF Smart Access Video (SAV) | ❌ | ✅ |
| AMD-specific HW-side B-frame ordering | ❌ | ✅ |
| Latency tuning | good | identical / marginally better |
| Vendor support | community (Mesa) | official AMD |
| First-class on Linux | ✅ | ⚠️ Windows is AMF's main home |

**Bottom line:** AMF on Linux is less mature than AMF on Windows. For most production
remote-desktop use cases, Mesa VA-API matches or beats it. Ship this add-on when users
specifically request native AMD tuning or are already running ROCm.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| AMD AMF SDK | **Apache 2.0** | Genuinely permissive — best license of the three vendor SDKs |
| ROCm runtime | MIT / NCSA | AMD's open compute stack |
| `libamf-component-encoder` | proprietary AMD | Ships with AMD GPU PRO driver |
| Our CGo binding | MIT | We own this code |

Apache 2.0 on the SDK itself is the cleanest of the three vendor encoder SDKs (NVIDIA
is a custom NVIDIA license, Intel is MIT). No royalties, fully redistributable.

---

## Hardware & Driver Compatibility

| AMD GPU | Driver path | AMF Linux status |
|---------|------------|-----------------|
| GCN 1.0–3.0 (HD 7000–R9 200) | Mesa only | ❌ AMF Linux unsupported |
| GCN 4 / Polaris (RX 400/500) | Mesa or PRO | ⚠️ AMF works but unofficial |
| GCN 5 / Vega | Mesa or PRO | ✅ AMF supported on PRO |
| RDNA 1 (RX 5000) | Mesa or PRO | ✅ AMF supported |
| RDNA 2 (RX 6000) | Mesa or PRO | ✅ AMF + AV1 encode |
| RDNA 3 (RX 7000) | Mesa or PRO | ✅ AMF + AV1 + improved PA |
| RDNA 4 (RX 9000) | Mesa or PRO | ✅ AMF latest |

**Critical caveat:** AMF on Linux requires the **AMD GPU PRO driver** stack, not the
upstream `amdgpu` kernel module with Mesa. The PRO stack is what most ROCm installations
use. Users on pure open-source Mesa get VA-API (the default binary path) only.

---

## Build & Distribution

### Go build tag

```bash
go build -tags amf_rocm -o viewport-rds-linux-amf ./cmd/server
```

The `amf_rocm` build tag pulls in `internal/hwencode/amf/` package.

### Runtime dependencies

- AMD GPU PRO driver 22.40+ OR ROCm 5.7+
- `libamf-component-encoder` (ships with AMD PRO driver)
- ROCm runtime libraries (`/opt/rocm/lib/`)

### CGo configuration

```go
/*
#cgo CFLAGS: -I/opt/amdgpu-pro/include -I/opt/rocm/include
#cgo LDFLAGS: -L/opt/amdgpu-pro/lib -L/opt/rocm/lib -lamf -lhsa-runtime64 -ldl

#include <core/Factory.h>
#include <components/VideoEncoderVCE.h>
#include <components/VideoEncoderHEVC.h>
#include <components/VideoEncoderAV1.h>
*/
import "C"
```

AMF is a **C++ SDK** with a C wrapper. The CGo binding uses the C wrapper functions
(`AMFCreateContext`, `AMFCreateComponent`, etc.) rather than instantiating C++ objects
directly.

---

## CGo Implementation Sketch

```c
// Session setup
AMFFactory* factory = AMFCreateFactory();
AMFContext* ctx = NULL;
factory->CreateContext(&ctx);

// Initialize with Vulkan device (preferred on Linux) or OpenGL
ctx->InitVulkan(NULL);  // uses default Vulkan device

// Create H.264 encoder component
AMFComponent* encoder = NULL;
factory->CreateComponent(ctx, AMFVideoEncoderVCE_AVC, &encoder);

// Configure for low-latency streaming
encoder->SetProperty(AMF_VIDEO_ENCODER_USAGE,
                     AMF_VIDEO_ENCODER_USAGE_ULTRA_LOW_LATENCY);
encoder->SetProperty(AMF_VIDEO_ENCODER_PROFILE,
                     AMF_VIDEO_ENCODER_PROFILE_BASELINE);
encoder->SetProperty(AMF_VIDEO_ENCODER_RATE_CONTROL_METHOD,
                     AMF_VIDEO_ENCODER_RATE_CONTROL_METHOD_CBR);
encoder->SetProperty(AMF_VIDEO_ENCODER_TARGET_BITRATE, bitrate);
encoder->SetProperty(AMF_VIDEO_ENCODER_FRAMERATE, AMFRate(fps, 1));
encoder->SetProperty(AMF_VIDEO_ENCODER_B_PIC_PATTERN, 0);  // no B-frames
encoder->SetProperty(AMF_VIDEO_ENCODER_IDR_PERIOD, 0);     // IDR on demand
encoder->SetProperty(AMF_VIDEO_ENCODER_FILLER_DATA_ENABLE, false);

// Init
encoder->Init(AMF_SURFACE_NV12, w, h);

// Per-frame: submit input surface
AMFSurface* surface = ...; // from Vulkan/OpenGL interop
encoder->SubmitInput(surface);

// Pump output
AMFData* output = NULL;
while (encoder->QueryOutput(&output) == AMF_OK && output) {
    AMFBuffer* buf = AMFBufferFromData(output);
    // buf->GetNative() / buf->GetSize() → encoded NALs
    output->Release();
}
```

### Pre-analysis (PA) for better quality

```c
encoder->SetProperty(AMF_VIDEO_ENCODER_PRE_ANALYSIS_ENABLE, true);
encoder->SetProperty(AMF_VIDEO_ENCODER_QUALITY_PRESET,
                     AMF_VIDEO_ENCODER_QUALITY_PRESET_SPEED);
```

PA gives noticeably better quality per bit than Mesa VA-API at the cost of ~10% extra
GPU time. Worth enabling for bandwidth-constrained scenarios.

---

## Performance Targets

| GPU class | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| RX 6600 (RDNA2) | <3ms | <4ms | <2% |
| RX 6900 XT (RDNA2) | <2ms | <3ms | <2% |
| RX 7900 XTX (RDNA3) | <2ms | <2.5ms | <2% |

Compared to Mesa VA-API on the same GPUs: roughly equivalent latency, slightly better
quality per bit with PA enabled, marginally better tail latency at p99 (~10% lower).

The marginal improvement is why this is an add-on rather than the default — Mesa VA-API
is genuinely good on AMD.

---

## Capture Integration

AMF accepts:
- **Vulkan VkImage** (preferred on Linux)
- **OpenGL textures** via GL interop
- **DMA-BUF** via Vulkan external memory import
- System memory NV12 (fallback)

The zero-copy path from KMS DMA-BUF:
```
KMS DMA-BUF → Vulkan VkImage (VK_EXT_external_memory_dma_buf)
            → AMF AMFSurface via VkImage interop
            → encoder->SubmitInput()
```

This is the cleanest interop story of all three add-ons because Vulkan's DMA-BUF
import extension is mature on Mesa and AMD's PRO driver.

---

## Probe & Selection

```go
//go:build amf_rocm

func ProbeAMF() (*AMFCapabilities, error) {
    // 1. dlopen libamf.so
    // 2. AMFCreateFactory → AMFCreateContext → InitVulkan
    // 3. Enumerate available encoder components
    // 4. Query capabilities per component
    // 5. Return capabilities or error
}
```

Pipeline probes (Linux):
```
NVENC?               → other add-on, NVIDIA only
AMF (this add-on)?   → use AMF if available AND GPU is AMD
Vulkan Video?        → other add-on, cross-vendor
VA-API (default)?    → use VA-API (Mesa, the default for AMD)
OpenH264 (default)?  → SW fallback
```

---

## File Structure

```
internal/hwencode/amf/
├── amf.go                // Encoder struct, NewAMFEncoder
├── amf_cgo.go            // CGo binding (build tag: amf_rocm)
├── amf_stub.go           // No-op stub (build tag: !amf_rocm)
├── probe.go              // ProbeAMF()
├── vulkan_interop.go     // DMA-BUF → VkImage → AMFSurface
└── amf_test.go           // Integration tests
```

---

## When to use this add-on

Use this add-on when:
- Target deployment has AMD GPUs **and** is already running AMD GPU PRO driver or ROCm
- AMF-specific tuning (Pre-Analysis, AMF B-frame ordering) is needed
- AMD's official support is preferred over Mesa community drivers
- Quality-per-bit optimization matters more than minimal CPU/binary size

Stick with default VA-API binary when:
- AMD users on standard Mesa/upstream `amdgpu` (the vast majority)
- LAN remote desktop where Mesa VA-API quality is already sufficient
- Heterogeneous fleet — VA-API covers AMD + Intel uniformly

---

## Why this is a separate add-on

The AMD GPU PRO driver stack is a heavyweight install. Many AMD users on Linux
deliberately stay on upstream Mesa for stability, kernel compatibility, and avoiding
proprietary blobs. Forcing the PRO stack as a default dependency would alienate them.

By shipping AMF as an opt-in add-on:
- Mesa users get the default binary → VA-API path, no PRO driver required
- ROCm / AMF users get the add-on → opt in to AMF features
- No forced choice on the user

---

## Status

📋 Specced — not yet built. Lower priority than NVENC because Mesa VA-API already
covers AMD well; this is for power users who need AMD-specific tuning.
