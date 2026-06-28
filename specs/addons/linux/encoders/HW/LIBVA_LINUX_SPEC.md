# Linux HW Encoder Add-On: libva (Direct VA-API Rust FFI Bindings)

## Purpose

The `libva` add-on is the Linux HW encoder implementation backing the
`hwencode::HardwareEncoder` trait defined in
[`specs/media/MODULE_HARDWARE_ENCODE.md`](../../../../media/MODULE_HARDWARE_ENCODE.md).
It binds directly to `libva` via Rust FFI — no subprocess, no ffmpeg, no LGPL/GPL
dependencies.

VA-API is the universal Linux HW encode abstraction. The same `libva` add-on
covers Intel Quick Sync (all generations from Sandy Bridge through Arc), AMD
GCN/RDNA via Mesa, and NVIDIA via the open-source VA-API wrapper.

**Why it is a separate spec from MODULE_HARDWARE_ENCODE:**
`MODULE_HARDWARE_ENCODE.md` defines the abstract `HardwareEncoder` trait
that all HW encoder add-ons implement. This document defines *how to build the
libva add-on specifically* — the specific VA-API calls, the SPS/PPS
serialization approach, the reference implementations to draw from, and the
implementation plan.

---

## License Chain (critical — verify before writing a line)

| Component | License | Notes |
|-----------|---------|-------|
| `libva` (Intel) | **MIT** | github.com/intel/libva — confirmed |
| `libva-utils` h264encode.c | **MIT** | Reference implementation we copy SPS/PPS from |
| Our Rust FFI bindings | **MIT** | We own it, no contamination |
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
| AMD RDNA2 / RX 6000+ (2020+) | `amdgpu` | Mesa | ✅ | ✅ | ❌ decode only |
| AMD RDNA3 / RX 7000+ (2022+) | `amdgpu` | Mesa | ✅ | ✅ | ✅ |
| NVIDIA (unofficial path) | `nvidia` | `nvidia-vaapi-driver` | ✅ wraps NVENC | ✅ | ❌ |
| Qualcomm (some ARM Linux SoCs) | varies | `msm` / `freedreno` | device-specific | ❌ typically | ❌ |

**Runtime capability check (mandatory at startup):**
```rust
// Never hardcode what the GPU supports. Always query.
let caps = probe_vaapi(render_node)?;
// caps.h264_encode, caps.hevc_encode, caps.av1_encode
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
- **Strategy:** copy the bitstream serialization functions verbatim into our FFI C shim. This is the hardest part and it already exists as MIT code.

### 2. pion/mediadevices — `pkg/codec/vaapi/vp8.go`
- **URL:** https://github.com/pion/mediadevices/blob/master/pkg/codec/vaapi/vp8.go
- **572 lines** — a compact CGo example; a good template to mirror in Rust FFI
- Template for how to structure the Rust side
- Shows the bitfield helper pattern (VA-API structs have bitfields that bindgen can't access directly — wrap them in C accessors)
- Shows the `new` / `next_frame()` / `Drop` architecture to follow

---

## Shared Library Build

```rust
// crate: featherdesk-addon-libva  (cfg(target_os = "linux"))
```

The `libva` add-on is built as a standalone cdylib from the
`internal/encode/libva/` crate; its `libva` FFI dependencies are linked into
that library, never into the host. If the library isn't dropped into the
add-ons directory, the host simply never loads it.

```
cargo build --release -p featherdesk-addon-libva   # cdylib → featherdesk-addon-libva.so
```

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

```rust
// internal/encode/libva/probe.rs
pub struct VaapiCapabilities {
    pub available:     bool,
    pub render_node:   String,  // e.g. /dev/dri/renderD128
    pub vendor_string: String,  // "Intel", "AMD/ATI", etc.
    pub h264_encode:   bool,
    pub hevc_encode:   bool,
    pub av1_encode:    bool,
    pub max_width:     u32,
    pub max_height:    u32,
}

pub fn probe_vaapi(render_node: &str) -> Result<VaapiCapabilities, EncodeError>;
```

VA-API calls needed:
1. `open(renderNode, O_RDWR)` → fd
2. `vaGetDisplayDRM(fd)` → display
3. `vaInitialize(display, &major, &minor)` → confirm init
4. `vaQueryConfigEntrypoints(display, VAProfileH264Baseline, ...)` → check H.264
5. `vaQueryConfigEntrypoints(display, VAProfileHEVCMain, ...)` → check HEVC
6. `vaTerminate(display)` → cleanup

This is ~80 lines of FFI. **Deliverable:** the `test_probe_vaapi` integration test passes on a machine with VA-API GPU.

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

**Deliverable:** `VaapiEncoder::new()` returns without error on real hardware.

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

These ~350 lines of C live in the FFI C shim. They are not Rust-translated — they stay as C, compiled via the `cc` crate in `build.rs` and called through Rust FFI. Rust never needs to see bitstream internals.

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

**Deliverable:** DMA-BUF from `KmsCapturer::next_surface()` → `encode_surface()` → NALs with zero CPU pixel copies. Validate with `iotop` showing no memory bus traffic during encode.

---

### Phase 6 — ForceKeyframe + Rate Control
**Goal:** support `force_keyframe()` and `EncoderConfig`'s `qp` / `bitrate_bps`.

```rust
// force_keyframe: set flag, next encode call sets slice_type to IFRAME
// and rebuilds SPS+PPS packed headers

// QP mode (RC_OFF_MODE): set VAEncMiscParameterRateControl with constant QP
// CBR mode: set VAEncMiscParameterRateControl with target_bits_per_second
```

---

## FFI C Shim Skeleton

This is the full set of includes and declarations needed. The body of each function is filled in during Phases 1–5. Linking (`pkg-config: libva libva-drm`; `-lva -lva-drm`) is configured in `build.rs`; the `cc` crate compiles this shim and `bindgen` generates the Rust declarations.

```c
// vaapi.h / vaapi.c — the C shim, compiled by build.rs and called via Rust FFI.

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
```

---

## File Structure

```
internal/encode/libva/
├── probe.rs          // Phase 1: VaapiCapabilities, probe_vaapi()
├── encoder.rs        // Phases 2+4: VaapiEncoder struct, VaapiEncoder::new(), encode(), Drop
├── surface.rs         // Phase 5: encode_surface() zero-copy path
├── ratecontrol.rs    // Phase 6: QP / CBR rate control
├── vaapi.c           // C shim: bitstream + SPS/PPS + VA-API wrappers
│                     // (kept in a .c file, compiled via build.rs `cc`)
├── vaapi.h           // VA264Ctx struct, function declarations
└── tests.rs          // Integration tests (cfg(feature = "integration"))
```

> Note: keeping the C in a `.c` file (rather than inline) gives better compiler errors, IDE support, and build caching. The `cc` crate compiles it in `build.rs` and `bindgen` generates the Rust declarations from `vaapi.h`.

---

## Testing Strategy

| Test | What | Requires |
|------|------|---------|
| `test_probe_vaapi` | probe_vaapi() returns Ok, h264_encode=true | VA-API GPU |
| `test_encoder_init` | VaapiEncoder::new() doesn't error | VA-API GPU |
| `test_encode_synthetic` | 300 frames encoded, NALs parseable by ffprobe | VA-API GPU |
| `test_surface_encode` | DMA-BUF from real KMS → NALs (zero-copy verified) | Root + GPU |
| `test_force_keyframe` | IDR produced on demand | VA-API GPU |
| `bench_encode_1080p` | Latency p50/p95/p99, fps ceiling | VA-API GPU |
| `bench_encode_1440p` | Same at 1440p | VA-API GPU |

All tests behind `cfg(feature = "integration")` — a normal `cargo test` skips them.

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
| `MODULE_HARDWARE_ENCODE.md` | Defines the `HardwareEncoder` trait this implements |
| `MODULE_CAPTURE.md` | `SurfaceCapturer::next_surface()` provides the fd for Phase 5 |
| `MODULE_PIPELINE.md` | Selects this encoder when `caps.h264_encode == true` |
| `MODULE_ENCODE.md` | Software fallback when this module returns `StreamError::FallbackToSoftware` |

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_libva]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements `stream::ConfigurableHardwareEncoder` (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). VA-API supports limited hot reconfiguration -- rate control parameters can change between frames, but resolution and profile changes require full context teardown.

| Param change | VA-API mechanism | Hot? |
|--------------|-----------------|------|
| `FPS` | Adjust frame timing in `VAEncMiscParameterFrameRate` + `vaRenderPicture` | yes |
| `BitrateBps` | `VAEncMiscParameterRateControl.bits_per_second` via `vaRenderPicture` per-frame | yes |
| `QP` | `VAEncPictureParameterBufferH264.pic_init_qp` per-frame (CQP mode) | yes |
| `KeyframeInterval` | `VAEncSequenceParameterBufferH264.intra_period` -- requires `vaCreateContext` reinit (returns `StreamError::RequiresRestart`) | no |
| `Width`, `Height` | `vaDestroyContext` + `vaCreateContext` + `vaCreateSurfaces` (returns `StreamError::RequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | HEVC Main10 profile -- `VAProfileHEVCMain10`; requires full context recreation and HEVC codec selection at session start | no |