# Module Spec: custom-libva (Direct VA-API CGo Bindings)

## Purpose

This module is a **standalone implementation task** — not a new public interface. It is the concrete implementation of the `hwencode.HardwareEncoder` interface defined in `MODULE_HARDWARE_ENCODE.md`, written directly against `libva` via CGo with no subprocess, no ffmpeg, and no LGPL/GPL dependencies.

**Why it is a separate spec from MODULE_HARDWARE_ENCODE:**
`MODULE_HARDWARE_ENCODE.md` defines what the module does (the interface contract). This document defines *how to build it* — the specific VA-API calls, the SPS/PPS serialization approach, the reference implementations to draw from, and the exact implementation plan.

---

## License Chain (critical — verify before writing a line)

| Component | License | Notes |
|-----------|---------|-------|
| `libva` (Intel) | **MIT** | github.com/intel/libva — confirmed |
| `libva-utils` h264encode.c | **MIT** | Reference implementation we copy SPS/PPS from |
| Our CGo bindings | **MIT** | We own it, no contamination |
| `libva-drm` | **MIT** | DRM display backend |

No GPL. No LGPL. The entire chain is MIT. This is the reason to write our own bindings rather than going through ffmpeg (LGPL).

---

## VA-API GPU Compatibility

The same binary works on all of these — the driver handles vendor differences. Our code never branches on vendor. We query capabilities at runtime via `vaQueryConfigEntrypoints` and use what's there.

| GPU | Kernel Driver | VA-API Driver | H.264 Encode | HEVC Encode | AV1 Encode |
|-----|-------------|--------------|-------------|------------|-----------|
| Intel Sandy Bridge–Broadwell (2011–2015) | `i915` | `i965` (libva-intel-driver) | ✅ | ❌ | ❌ |
| Intel Skylake–Ice Lake (2015–2019) | `i915` | `iHD` (intel-media-driver) | ✅ | ✅ | ❌ |
| Intel Xe / Arc (2021+) | `xe` / `i915` | `iHD` | ✅ | ✅ | ✅ |
| AMD GCN / RX 400+ (2016+) | `amdgpu` | Mesa `radeonsi` | ✅ | ✅ | ❌ |
| AMD RDNA2 / RX 6000+ (2020+) | `amdgpu` | Mesa | ✅ | ✅ | ✅ |
| AMD RDNA3 / RX 7000+ (2022+) | `amdgpu` | Mesa | ✅ | ✅ | ✅ |
| NVIDIA (unofficial path) | `nvidia` | `nvidia-vaapi-driver` | ✅ wraps NVENC | ✅ | ❌ |
| Qualcomm (some ARM Linux SoCs) | varies | `msm` / `freedreno` | device-specific | ❌ typically | ❌ |

**Runtime capability check (mandatory at startup):**
```go
// Never hardcode what the GPU supports. Always query.
caps, err := probeVAAPI(renderNode)
// caps.H264Encode, caps.HEVCEncode, caps.AV1Encode
// If a profile is absent, fall back to software gracefully.
```

---

## Reference Implementations

Two files to study before writing a single line of code. Both MIT licensed.

### 1. Intel libva-utils — `encode/h264encode.c`
- **URL:** https://github.com/intel/libva-utils/blob/master/encode/h264encode.c
- **~1,650 lines** — complete, working, production-tested reference
- Contains the exact SPS/PPS/slice_header serialization needed
- Has correct Exp-Golomb bit packing (`put_ue`, `put_se`, `bitstream_*`)
- Has reference frame management, POC calculation, DPB logic
- **Strategy:** copy the bitstream serialization functions verbatim into our CGo preamble. This is the hardest part and it already exists as MIT code.

### 2. pion/mediadevices — `pkg/codec/vaapi/vp8.go`
- **URL:** https://github.com/pion/mediadevices/blob/master/pkg/codec/vaapi/vp8.go
- **572 lines** — pure CGo, no separate .c file, Go-native style
- Template for how to structure the Go side
- Shows the CGo bitfield helper pattern (VA-API structs have bitfields CGo can't access)
- Shows the `newEncoder` / `Read()` / `Close()` architecture to follow

---

## What Makes H.264 Harder Than VP8

Understanding this prevents wasted time on false-start implementations.

**VP8:** The hardware produces a self-contained bitstream. No header construction needed.

**H.264:** The hardware encodes the slice data but you must provide complete SPS, PPS, and slice headers as manually constructed packed buffers via `VAEncPackedHeaderType`. These are raw H.264 Annex B syntax elements coded in Exp-Golomb. If any bit is wrong, the stream fails to decode — often silently.

| Complexity factor | VP8 | H.264 |
|-------------------|-----|-------|
| Header serialization | None | ~350 lines (Exp-Golomb bitstream writer + SPS + PPS + slice_header) |
| Reference frames | Simple (3 frames max) | DPB + RefPicList0/1 + POC ordering |
| VA buffer types per frame | 5–7 | 8–12 (adds 3–4 packed header buffers) |
| Profile variants | One | Baseline / Main / High (different SPS fields) |

**For FeatherDesk's use case (remote desktop streaming, Baseline profile, no B-frames):**
- Use `VAProfileH264Baseline` — simplest profile, widest browser compatibility
- Disable B-frames (`num_b_in_gop = 0`) — eliminates the DPB reorder complexity
- This reduces the H.264 implementation to ~60% of the full reference complexity

---

## Implementation Plan

### Phase 1 — Capability Probe (standalone, no encoding)
**Goal:** detect VA-API hardware at startup, no ffmpeg involved.

```go
// internal/hwencode/vaapi/probe.go
type VAAPICapabilities struct {
    Available    bool
    RenderNode   string   // e.g. /dev/dri/renderD128
    VendorString string   // "Intel", "AMD/ATI", etc.
    H264Encode   bool
    HEVCEncode   bool
    AV1Encode    bool
    MaxWidth     int
    MaxHeight    int
}

func ProbeVAAPI(renderNode string) (*VAAPICapabilities, error)
```

VA-API calls needed:
1. `open(renderNode, O_RDWR)` → fd
2. `vaGetDisplayDRM(fd)` → display
3. `vaInitialize(display, &major, &minor)` → confirm init
4. `vaQueryConfigEntrypoints(display, VAProfileH264Baseline, ...)` → check H.264
5. `vaQueryConfigEntrypoints(display, VAProfileHEVCMain, ...)` → check HEVC
6. `vaTerminate(display)` → cleanup

This is ~80 lines of CGo. **Deliverable:** `go test -run TestProbeVAAPI -tags integration` passes on a machine with VA-API GPU.

---

### Phase 2 — Session Init + Surface Allocation
**Goal:** create a working encode session without encoding any frames.

VA-API calls:
1. `vaGetDisplayDRM` + `vaInitialize`
2. `vaGetConfigAttributes(display, VAProfileH264Baseline, VAEntrypointEncSlice, attrs, nAttrs)`
3. `vaCreateConfig(display, profile, entrypoint, attrs, nAttrs, &configID)`
4. `vaCreateContext(display, configID, width, height, VA_PROGRESSIVE, surfaces, nSurfaces, &contextID)`
5. `vaCreateSurfaces(display, VA_RT_FORMAT_YUV420, width, height, surfaces, nSurfaces, nil, 0)` — for software-path surfaces
6. `vaCreateSurfaces(...)` with `VASurfaceAttribExternalBuffers` — for DMA-BUF import surfaces

**Deliverable:** `NewVAAPIEncoder()` returns without error on real hardware.

---

### Phase 3 — Packed Header Serialization (the hard phase)
**Goal:** serialize SPS + PPS + slice_header as `VAEncPackedHeaderH264` buffers.

**Copy directly from `libva-utils/encode/h264encode.c`** (MIT):
- `bitstream_start/end` — manage a growing byte buffer
- `bitstream_put_ui(bs, val, nbits)` — put unsigned integer
- `bitstream_put_ue(bs, val)` — Exp-Golomb unsigned
- `bitstream_put_se(bs, val)` — Exp-Golomb signed
- `bitstream_byte_aligning(bs, bit)` — padding
- `sps_rbsp(ctx, bs)` — SPS RBSP: profile_idc, level_idc, SPS fields
- `pps_rbsp(ctx, bs)` — PPS RBSP: pic_parameter_set_id, entropy_coding_mode_flag, etc.
- `slice_header(ctx, bs)` — slice_type, first_mb_in_slice, frame_num, etc.
- `build_packed_seq_buffer` — wraps SPS as VAEncPackedHeaderSequence
- `build_packed_pic_buffer` — wraps PPS as VAEncPackedHeaderPicture
- `build_packed_slice_buffer` — wraps slice_header as VAEncPackedHeaderSlice

These ~350 lines of C live in the CGo preamble. They are not Go-translated — they stay as C inside the `/* */` CGo block. Go never needs to see bitstream internals.

**Deliverable:** `sps_rbsp` + `pps_rbsp` produce valid SPS/PPS bytes verifiable with `h264parse` from GStreamer or `ffprobe -show_streams`.

---

### Phase 4 — Per-Frame Encode Loop
**Goal:** encode synthetic I420 frames and produce valid H.264 NAL units.

Per-frame VA-API sequence (Baseline profile, I and P frames only):
```
vaBeginPicture(display, context, surface)
    vaRenderPicture → VAEncSequenceParameterBufferH264   (every IDR)
    vaRenderPicture → VAEncPictureParameterBufferH264    (every frame)
    vaRenderPicture → VAEncSliceParameterBufferH264      (every frame)
    vaRenderPicture → VAEncPackedHeaderSequence + Data   (IDR only)
    vaRenderPicture → VAEncPackedHeaderPicture + Data    (IDR only)
    vaRenderPicture → VAEncPackedHeaderSlice + Data      (every frame)
    vaRenderPicture → VAEncCodedBuffer                   (coded output)
vaEndPicture(display, context)
vaSyncSurface(display, surface)
vaMapBuffer(display, codedBufID, &codedBuf)
    → read NAL units from VACodedBufferSegment chain
vaUnmapBuffer(display, codedBufID)
vaDestroyBuffer(display, codedBufID)
```

**Deliverable:** 300 synthetic frames encoded at 1080p, output validated with `ffprobe`.

---

### Phase 5 — DMA-BUF Zero-Copy Path
**Goal:** import KMS framebuffer directly as VASurface (eliminates GPU→CPU copy entirely).

```c
VASurfaceAttribExternalBuffers extBuf = {
    .pixel_format = VA_FOURCC_XRGB,  // or NV12 if driver converts
    .width  = fbInfo.Width,
    .height = fbInfo.Height,
    .data_size = fbInfo.Stride * fbInfo.Height,
    .num_planes = 1,
    .pitches = { fbInfo.Stride },
    .offsets = { 0 },
    .buffers = (uintptr_t[]){ fbInfo.DMAFD },
    .num_buffers = 1,
    .flags = VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME,
};
VASurfaceAttrib attrs[2] = {
    { .type = VASurfaceAttribMemoryType,
      .value = { .type = VAGenericValueTypeInteger,
                 .value = { .i = VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME }}},
    { .type = VASurfaceAttribExternalBufferDescriptor,
      .value = { .type = VAGenericValueTypePointer,
                 .value = { .p = &extBuf }}},
};
vaCreateSurfaces(display, VA_RT_FORMAT_RGB32, w, h, &surface, 1, attrs, 2);
```

**Deliverable:** DMA-BUF from `KMSCapturer.NextDMABuf()` → `EncodeDMABuf()` → NALs with zero CPU pixel copies. Validate with `iotop` showing no memory bus traffic during encode.

---

### Phase 6 — ForceKeyframe + Rate Control
**Goal:** support `ForceKeyframe()` and `EncoderConfig.QP` / `BitrateBps`.

```go
// ForceKeyframe: set flag, next encode call sets slice_type to IFRAME
// and rebuilds SPS+PPS packed headers

// QP mode (RC_OFF_MODE): set VAEncMiscParameterRateControl with constant QP
// CBR mode: set VAEncMiscParameterRateControl with target_bits_per_second
```

---

## CGo Preamble Skeleton

This is the full set of includes and declarations needed. The body of each function is filled in during Phases 1–5.

```c
/*
#cgo pkg-config: libva libva-drm
#cgo LDFLAGS: -lva -lva-drm

#include <va/va.h>
#include <va/va_drm.h>
#include <va/va_enc_h264.h>
#include <va/va_vpp.h>        // for format conversion pass if needed
#include <drm/drm_fourcc.h>
#include <fcntl.h>
#include <stdint.h>
#include <string.h>
#include <stdlib.h>

// ── Bitstream writer (copy verbatim from libva-utils/encode/h264encode.c MIT) ──
typedef struct { uint8_t *buffer; int bit_offset; int max_size_in_dword; } bitstream;
static void bitstream_start(bitstream *bs) { ... }
static void bitstream_put_ui(bitstream *bs, uint32_t val, int nbits) { ... }
static void bitstream_put_ue(bitstream *bs, uint32_t val) { ... }  // Exp-Golomb
static void bitstream_put_se(bitstream *bs, int32_t val) { ... }
static void bitstream_byte_aligning(bitstream *bs, int bit) { ... }
static void bitstream_end(bitstream *bs) { ... }

// ── H.264 header serializers (copy from libva-utils MIT) ──
static void sps_rbsp(VA264Ctx *ctx, bitstream *bs) { ... }
static void pps_rbsp(VA264Ctx *ctx, bitstream *bs) { ... }
static void slice_header(VA264Ctx *ctx, bitstream *bs) { ... }
static int  build_packed_seq_buffer(VA264Ctx *ctx, uint8_t **header, uint32_t *len) { ... }
static int  build_packed_pic_buffer(VA264Ctx *ctx, uint8_t **header, uint32_t *len) { ... }
static int  build_packed_slice_buffer(VA264Ctx *ctx, uint8_t **header, uint32_t *len) { ... }

// ── VA-API session management ──
int       va_open_display_drm(const char *renderNode, VADisplay *display);
VAStatus  va_init_encoder_h264(VADisplay dpy, int w, int h, int fps, int qp,
                               VAConfigID *cfgID, VAContextID *ctxID,
                               VASurfaceID *surfaces, int nSurfaces);
void      va_close(VADisplay dpy, VAConfigID cfg, VAContextID ctx,
                   VASurfaceID *surfaces, int nSurfaces);

// ── Per-frame encode ──
// Returns pointer to coded NAL data; caller must call va_release_coded_buf() after copy.
int   va_encode_frame(VADisplay dpy, VAContextID ctx, VASurfaceID surface,
                      VABufferID codedBuf, VA264Ctx *params, int isIDR,
                      uint8_t **nalData, size_t *nalSize);
void  va_release_coded_buf(VADisplay dpy, VABufferID buf);

// ── DMA-BUF surface import ──
VAStatus va_import_dmabuf(VADisplay dpy, int dmabufFD,
                          uint32_t width, uint32_t height,
                          uint32_t stride, uint32_t format,
                          uint64_t modifier, VASurfaceID *surface);
*/
import "C"
```

---

## File Structure

```
internal/hwencode/vaapi/
├── probe.go          // Phase 1: VAAPICapabilities, ProbeVAAPI()
├── encoder.go        // Phases 2+4: VAAPIEncoder struct, NewVAAPIEncoder(), Encode(), Close()
├── dmabuf.go         // Phase 5: EncodeDMABuf() zero-copy path
├── ratecontrol.go    // Phase 6: QP / CBR rate control
├── vaapi.c           // CGo preamble: bitstream + SPS/PPS + VA-API wrappers
│                     // (keep C in a .c file for better IDE support and build isolation)
├── vaapi.h           // VA264Ctx struct, function declarations
└── vaapi_test.go     // Integration tests (build tag: //go:build integration)
```

> Note: splitting the C into a `.c` file (rather than the CGo `/* */` preamble) gives better compiler errors, IDE support, and build caching. CGo supports this via `#cgo CFLAGS` pointing to local includes.

---

## Testing Strategy

| Test | What | Requires |
|------|------|---------|
| `TestProbeVAAPI` | ProbeVAAPI() returns non-nil, H264Encode=true | VA-API GPU |
| `TestEncoderInit` | NewVAAPIEncoder() doesn't error | VA-API GPU |
| `TestEncodeSynthetic` | 300 frames encoded, NALs parseable by ffprobe | VA-API GPU |
| `TestDMABufEncode` | DMA-BUF from real KMS → NALs (zero-copy verified) | Root + GPU |
| `TestForceKeyframe` | IDR produced on demand | VA-API GPU |
| `BenchmarkEncode1080p` | Latency p50/p95/p99, fps ceiling | VA-API GPU |
| `BenchmarkEncode1440p` | Same at 1440p | VA-API GPU |

All tests behind `//go:build integration` — normal `go test` skips them.

---

## Performance Targets

| Metric | Intel Skylake | AMD RDNA2 | Notes |
|--------|-------------|----------|-------|
| Encode latency 1080p p50 | <3ms | <4ms | GPU-side only |
| Encode latency 1440p p50 | <5ms | <6ms | |
| CPU usage at 60fps 1080p | <2% | <3% | GPU does the work |
| DMA-BUF import overhead | <0.5ms | <0.5ms | vs glReadPixels: 50ms |
| Memory bandwidth (sw path) | ~36MB/frame | ~36MB/frame | eliminated by DMA-BUF |
| Memory bandwidth (hw path) | ~30KB/frame | ~30KB/frame | compressed output only |

---

## Dependencies

```
libva        MIT  pkg-config: libva
libva-drm    MIT  pkg-config: libva-drm
libdrm       MIT  pkg-config: libdrm    (DRM_FORMAT_* constants)
```

Runtime (user must have installed):
- Intel: `intel-media-va-driver` (Skylake+) or `i965-va-driver` (older)
- AMD: `mesa-va-drivers` (ships with Mesa, usually already installed)
- NVIDIA: `nvidia-vaapi-driver` (unofficial, user installs manually)

---

## Relationship to Other Modules

| Module | Relationship |
|--------|-------------|
| `MODULE_HARDWARE_ENCODE.md` | Defines the `HardwareEncoder` interface this implements |
| `MODULE_CAPTURE.md` | `DMABufCapturer.NextDMABuf()` provides the fd for Phase 5 |
| `MODULE_PIPELINE.md` | Selects this encoder when `caps.H264Encode == true` |
| `MODULE_ENCODE.md` | Software fallback when this module returns `ErrFallbackToSoftware` |
