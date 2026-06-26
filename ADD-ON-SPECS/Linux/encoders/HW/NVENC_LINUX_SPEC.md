# Linux Add-On: NVENC Direct (NVIDIA Video Codec SDK)

## Purpose

Optional add-on binary for **NVIDIA GPUs on Linux** that calls NVIDIA's NVENC hardware
encoder directly via the official NVIDIA Video Codec SDK. Replaces the default VA-API
path (which on NVIDIA uses the unofficial `nvidia-vaapi-driver` wrapper) with the
official, full-featured API.

**Why this exists:** the VA-API → `nvidia-vaapi-driver` wrapper does not expose
NVENC-specific features. Direct NVENC unlocks:

| Feature | VA-API wrapper | NVENC direct |
|---------|---------------|--------------|
| H.264 / HEVC encode | ✅ | ✅ |
| AV1 encode (Ada Lovelace+) | partial | ✅ |
| `REF_FRAMES_INVALIDATION` | ❌ | ✅ — **biggest streaming win** |
| `NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY` | ❌ | ✅ |
| Single-surface pipeline (`surfaces=1`, `delay=0`) | ❌ | ✅ |
| CUDA device interop (zero-copy from CUDA buffers) | ❌ | ✅ |
| Vendor support | community | official NVIDIA |

`REF_FRAMES_INVALIDATION` alone justifies a separate binary. On packet loss, NVENC can
invalidate only the affected reference frames and re-encode the dependent macroblocks
instead of forcing a full IDR keyframe. This is the single biggest latency/bandwidth
advantage NVIDIA has over Intel and AMD for streaming. The VA-API wrapper cannot
surface this control.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Video Codec SDK headers | NVIDIA Software License (free use) | Cannot redistribute headers separately; users install SDK |
| `libnvidia-encode` (driver-shipped) | proprietary NVIDIA | Ships with the NVIDIA driver |
| Our CGo binding | MIT | We own this code |

No royalties. No GPL/LGPL contamination. The NVIDIA SDK license is "free to use, can't
redistribute the SDK as a standalone package." This matches the pattern of every
production product using NVENC (OBS, Sunshine, ffmpeg).

---

## Hardware Compatibility

NVENC is in every NVIDIA GPU from **Kepler (GTX 600 series, 2012) onward**.

| GPU generation | H.264 | HEVC | AV1 | Concurrent sessions |
|----------------|-------|------|-----|---------------------|
| Kepler (GTX 600–700) | ✅ | ❌ | ❌ | 2 (consumer cap) |
| Maxwell 1 (GTX 750) | ✅ | ❌ | ❌ | 2 |
| Maxwell 2 (GTX 900) | ✅ | ✅ 8-bit | ❌ | 2 |
| Pascal (GTX 1000) | ✅ | ✅ 10-bit | ❌ | 2 |
| Volta / Turing (GTX 1600, RTX 2000) | ✅ | ✅ | ❌ | 3 |
| Ampere (RTX 3000) | ✅ | ✅ | ❌ | 3 |
| Ada Lovelace (RTX 40-series — 4070/4080/4090) | ✅ | ✅ | ✅ | 3+ (dual NVENC on 4090) |
| Blackwell (RTX 5000) | ✅ | ✅ | ✅ | 3+ |

Consumer cards historically capped at 2-3 concurrent sessions. Quadro / Tesla / Data
Center cards have unlimited sessions. For remote desktop (one viewer = one session),
the cap rarely matters.

---

## Build & Distribution

### Go build tag

```bash
go build -tags nvenc -o viewport-rds-linux-nvenc ./cmd/server
```

The `nvenc` build tag pulls in `internal/hwencode/nvenc/` package. Without the tag the
binary compiles without any NVIDIA SDK dependency.

### Runtime dependencies

Users installing this binary need:
- NVIDIA proprietary driver 470+ (libnvidia-encode.so ships with it)
- NVIDIA Video Codec SDK 12+ headers at build time only (not at runtime)

### CGo configuration

```go
/*
#cgo CFLAGS: -I/usr/local/cuda/include -I${SRCDIR}/sdk
#cgo LDFLAGS: -L/usr/lib/x86_64-linux-gnu -lnvidia-encode -lcuda -ldl

#include <cuda.h>
#include <nvEncodeAPI.h>
*/
import "C"
```

The SDK headers (`nvEncodeAPI.h`, `cuda.h`) are checked into the source tree under
`internal/hwencode/nvenc/sdk/` — NVIDIA's SDK license permits redistributing the headers
inside an application source tree (this is what ffmpeg, Sunshine, OBS all do).

---

## CGo Implementation Sketch

```c
// Encode session setup (one-time at startup)
NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS sessionParams = { 0 };
sessionParams.version = NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS_VER;
sessionParams.deviceType = NV_ENC_DEVICE_TYPE_CUDA;
sessionParams.device = cuContext;
sessionParams.apiVersion = NVENCAPI_VERSION;

NV_ENCODE_API_FUNCTION_LIST funcs = { NV_ENCODE_API_FUNCTION_LIST_VER };
NvEncodeAPICreateInstance(&funcs);
funcs.nvEncOpenEncodeSessionEx(&sessionParams, &encoder);

// Configure for low-latency streaming
NV_ENC_INITIALIZE_PARAMS initParams = { NV_ENC_INITIALIZE_PARAMS_VER };
initParams.encodeGUID  = NV_ENC_CODEC_H264_GUID;
initParams.presetGUID  = NV_ENC_PRESET_P1_GUID;       // P1 = fastest
initParams.tuningInfo  = NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY;
initParams.encodeWidth = w;
initParams.encodeHeight = h;
initParams.frameRateNum = fps;
initParams.frameRateDen = 1;
initParams.enablePTD = 1;

NV_ENC_CONFIG cfg = { NV_ENC_CONFIG_VER };
funcs.nvEncGetEncodePresetConfigEx(encoder, encodeGUID, presetGUID, tuning, &presetCfg);
memcpy(&cfg, &presetCfg.presetCfg, sizeof(cfg));
cfg.gopLength = NVENC_INFINITE_GOPLENGTH;             // IDR on demand only
cfg.frameIntervalP = 1;                                // no B-frames
cfg.rcParams.rateControlMode = NV_ENC_PARAMS_RC_CBR;
cfg.rcParams.averageBitRate = bitrate;
cfg.rcParams.disableBadapt = 1;
cfg.rcParams.zeroReorderDelay = 1;                     // critical for low latency
cfg.rcParams.enableLookahead = 0;

// Enable REF_FRAMES_INVALIDATION
cfg.rcParams.enableNonRefP = 1;
initParams.encodeConfig = &cfg;

funcs.nvEncInitializeEncoder(encoder, &initParams);

// Per-frame encode (CUDA surface input → bitstream output)
NV_ENC_PIC_PARAMS picParams = { NV_ENC_PIC_PARAMS_VER };
picParams.pictureStruct = NV_ENC_PIC_STRUCT_FRAME;
picParams.inputBuffer   = registeredSurface;
picParams.bufferFmt     = NV_ENC_BUFFER_FORMAT_NV12;
picParams.outputBitstream = bitstreamBuffer;
picParams.inputWidth    = w;
picParams.inputHeight   = h;
funcs.nvEncEncodePicture(encoder, &picParams);

// Read encoded output
NV_ENC_LOCK_BITSTREAM lockParams = { NV_ENC_LOCK_BITSTREAM_VER };
lockParams.outputBitstream = bitstreamBuffer;
funcs.nvEncLockBitstream(encoder, &lockParams);
// lockParams.bitstreamBufferPtr / bitstreamSizeInBytes = NALs
funcs.nvEncUnlockBitstream(encoder, bitstreamBuffer);
```

### Reference frame invalidation (NVENC-only superpower)

```c
// On client packet-loss report: invalidate specific reference frames
// rather than forcing a full IDR
NV_ENC_INVALIDATE_REFERENCE_FRAMES invalidate = { 0 };
invalidate.invalidRefFrameTimeStamp = lostFrameTimestamp;
funcs.nvEncInvalidateRefFrames(encoder, &invalidate);
```

This is the kernel of low-latency streaming on NVIDIA. No full keyframe needed on loss.

---

## Performance Targets

| GPU class | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| GTX 1660 (Turing) | <2ms | <3ms | <1% |
| RTX 3060 (Ampere) | <1.5ms | <2.5ms | <1% |
| RTX 4090 (Ada) | <1ms | <1.5ms | <1% (dual NVENC) |

Compared to VA-API wrapper on the same GPUs: roughly 2× lower latency due to direct
API access and the lack of wrapper translation layer.

---

## Capture Integration

NVENC accepts inputs as:
- **CUDA arrays / device pointers** (zero-copy from CUDA-based capture)
- **D3D11 textures** (Windows only)
- **Vulkan VkImage** (cross-platform, Linux applies)
- System memory NV12 / YUV420 (fallback, requires CPU upload)

The Linux capture options for zero-copy to NVENC:

| Capture source | Zero-copy to NVENC? |
|---------------|---------------------|
| KMS DMA-BUF | Via Vulkan import (best) or CUDA EGL interop |
| X11 / XShm | CPU upload only (~5ms overhead at 1440p) |
| PipeWire | Possible via EGL → CUDA interop |

For the zero-copy path, the cleanest route is:
```
KMS DMA-BUF → CUDA EGL interop (cuGraphicsEGLRegisterImage)
            → CUDA array → NVENC NV_ENC_BUFFER_FORMAT_NV12
```

Documented in `MODULE_HARDWARE_ENCODE.md` as one of the supported zero-copy paths.

---

## Probe & Selection

```go
//go:build nvenc

func ProbeNVENC() (*NVENCCapabilities, error) {
    // 1. dlopen libnvidia-encode.so
    // 2. NvEncodeAPICreateInstance
    // 3. Enumerate encode GUIDs (H.264, HEVC, AV1)
    // 4. Query max width/height per codec
    // 5. Return capabilities or error if no NVIDIA driver/GPU
}
```

Pipeline probes in this order on Linux:
```
NVENC available?      → use NVENC (this add-on, if compiled in)
AMF-ROCm available?   → use AMF (other add-on)
VA-API (libva)?       → use VA-API
x264 subprocess?      → use x264 (GPL builds only)
OpenH264?             → universal SW fallback
```

---

## File Structure

```
internal/hwencode/nvenc/
├── nvenc.go              // Encoder struct, NewNVENCEncoder
├── nvenc_cgo.go          // CGo binding (build tag: nvenc)
├── nvenc_stub.go         // No-op stub (build tag: !nvenc) for non-NVENC builds
├── probe.go              // ProbeNVENC()
├── cuda_interop.go       // KMS DMA-BUF → CUDA array import
├── sdk/                  // NVIDIA SDK headers (redistributable per NVIDIA license)
│   ├── nvEncodeAPI.h
│   └── cuda.h
└── nvenc_test.go         // Integration tests (build tag: nvenc,integration)
```

---

## Testing

| Test | Hardware |
|------|---------|
| `TestProbeNVENC` | NVIDIA GPU + driver |
| `TestEncodeH264` | NVIDIA GPU |
| `TestEncodeHEVC` | Maxwell 2+ |
| `TestEncodeAV1` | Ada Lovelace+ |
| `TestRefFrameInvalidation` | NVIDIA GPU + simulated packet loss |
| `BenchmarkEncode1080p60` | NVIDIA GPU |
| `BenchmarkEncode1440p60` | NVIDIA GPU |

All gated behind `//go:build nvenc,integration`.

---

## When to use this add-on

Use this add-on when:
- Target deployment has NVIDIA GPUs
- Streaming over lossy networks where REF_FRAMES_INVALIDATION matters
- Need AV1 hardware encode on RTX 40+
- Need the absolute minimum latency NVIDIA can produce

Stick with default VA-API binary when:
- Heterogeneous fleet (mix of Intel/AMD/NVIDIA) where one binary is simpler
- LAN-only deployment (loss rarely happens, REF_FRAMES_INVALIDATION less important)
- Container size matters (NVIDIA SDK adds ~50MB)

---

## Status

📋 Specced — not yet built. Implementation order: probe → session init → per-frame
encode → reference frame invalidation → CUDA interop zero-copy.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_nvenc]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary`;` unknown
keys in this section will cause startup to fail.

