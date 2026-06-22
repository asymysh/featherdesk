# Module Spec: Hardware Encode (Zero-Copy DMA-BUF Path)

## Overview

The Hardware Encode module provides a zero-copy GPU-resident encoding path. Unlike the software encode path (which round-trips pixels through CPU via `glReadPixels` → libyuv → ffmpeg stdin), this module keeps the framebuffer on the GPU: `DMA-BUF fd → VA-API encoder → encoded NALs`. This eliminates two expensive GPU↔CPU copies and is the performance-critical path for 1080p60 / 1440p60 streaming.

---

## Public Interface

```go
package hwencode

// HardwareEncoder extends the base Encoder interface with a zero-copy path.
type HardwareEncoder interface {
    encode.Encoder

    // EncodeDMABuf encodes directly from a DMA-BUF file descriptor.
    // The framebuffer stays on the GPU — no CPU-side pixel copy occurs.
    // The fd is NOT consumed (caller retains ownership and must close it).
    EncodeDMABuf(params DMABufParams) ([][]byte, error)

    // SupportsFormat returns true if the encoder can handle this DRM format+modifier
    // without a GPU-side conversion pass.
    SupportsFormat(format uint32, modifier uint64) bool
}

// DMABufParams describes a GPU framebuffer to encode.
type DMABufParams struct {
    FD       int    // DMA-BUF file descriptor (not consumed, caller closes)
    Width    int
    Height   int
    Stride   int    // Row stride in bytes
    Format   uint32 // DRM fourcc format (e.g., DRM_FORMAT_XRGB8888)
    Modifier uint64 // DRM format modifier (e.g., I915_FORMAT_MOD_Y_TILED)
}

// HWEncoderConfig extends EncoderConfig with hardware-specific settings.
type HWEncoderConfig struct {
    encode.EncoderConfig
    RenderNode string // e.g., "/dev/dri/renderD128"
    Codec      HWCodec
    Profile    HWProfile
}

type HWCodec int
const (
    HWCodecH264 HWCodec = iota
    HWCodecH265
    HWCodecAV1
)

type HWProfile int
const (
    HWProfileConstrained HWProfile = iota // Baseline/Constrained (maximum compatibility)
    HWProfileMain                         // Main profile (better compression)
    HWProfileHigh                         // High profile (best compression)
)
```

---

## Internal Architecture

### Zero-Copy Pipeline

```
DRM Primary Plane
    → Framebuffer (GPU memory, possibly tiled/compressed)
    → drmPrimeHandleToFD → DMA-BUF fd
    → VA-API: vaCreateSurfaces + VASurfaceAttribExternalBuffers (import DMA-BUF as VASurface)
    → VA-API: vaBeginPicture / vaRenderPicture / vaEndPicture
    → VA-API: vaSyncSurface
    → VA-API: vaMapBuffer (coded buffer) → H.264 NAL units (Annex B)
    → Copy NALs to Go memory → [][]byte
    → vaUnmapBuffer
```

> There is no `vaCreateSurfaceFromFD` in libva. DMA-BUF import is done via `vaCreateSurfaces` with a `VASurfaceAttribExternalBuffers` attribute carrying the fd(s), as the Surface Import section states. The encoder's NAL output is Annex B, matching the software path so the server treats both identically.

**Total GPU↔CPU copies: 1** (only the final compressed NALs, ~10-50KB vs ~24MB for raw 1440p RGBA)

### Comparison with Software Path

```
Software (current):
    DMA-BUF → EGL import → glReadPixels (GPU→CPU: 24MB) → libyuv (CPU) → 
    ffmpeg stdin (pipe) → VAAPI hwupload (CPU→GPU: 12MB NV12) → encode → NALs
    Total copies: 2 bulk transfers (~36MB/frame at 1440p)

Hardware (this module):
    DMA-BUF → VA-API import (stays on GPU) → encode → NALs (GPU→CPU: ~30KB)
    Total copies: 1 small transfer (~30KB/frame)
```

### VA-API Session Management

```go
type VAAPIEncoder struct {
    display    VADisplay       // From vaGetDisplayDRM(renderNodeFD)
    context    VAContextID
    vaConfig   VAConfigID      // VA-API config handle (renamed to avoid clash)
    surfaces   []VASurfaceID   // Ring buffer of imported surfaces
    coded      []VABufferID    // Coded output buffers
    seqParam   VAEncSequenceParameterBufferH264
    picParam   VAEncPictureParameterBufferH264
    sliceParam VAEncSliceParameterBufferH264
    frameNum   uint32
    idr        atomic.Bool
    cfg        HWEncoderConfig // module configuration
}
```

> Fix: the two fields were both named `config` in round 1 (a compile error). The VA-API handle is now `vaConfig`; the module config is `cfg`.

### Surface Import Strategy

Rather than importing a new surface every frame:
1. Maintain a small ring buffer of `VASurfaceID` (4 surfaces)
2. Import DMA-BUF → `vaCreateSurfaces` with `VASurfaceAttribExternalBuffers`
3. Reuse surface slots round-robin (surface must not be in-flight when reused)
4. `vaSyncSurface` before reuse ensures previous encode is complete

### Format Negotiation

Not all DRM formats can be directly imported by VA-API. Common supported formats:
- `DRM_FORMAT_XRGB8888` → requires GPU-side colorspace conversion to NV12
- `DRM_FORMAT_NV12` → direct encode (no conversion needed)
- `DRM_FORMAT_P010` → direct encode for 10-bit (HDR path, future)

If the framebuffer format requires conversion:
```
DMA-BUF (XRGB8888) → VA-API vaProcPipeline (GPU colorspace convert) → NV12 VASurface → encode
```
This is STILL zero-copy from the CPU perspective — the conversion happens entirely on GPU.

### Fallback Behavior

If hardware encoding fails or is unavailable:
```go
func (e *VAAPIEncoder) EncodeDMABuf(params DMABufParams) ([][]byte, error) {
    // ... attempt VA-API encode ...
    if err == ErrHardwareUnavailable {
        return nil, ErrFallbackToSoftware // Caller should switch to software path
    }
    return nals, nil
}
```

The pipeline orchestrator handles the fallback:
1. Try hardware encode
2. On `ErrFallbackToSoftware`, degrade to software pipeline permanently for this session
3. Log the fallback reason for debugging

---

## Integration with Capture Module

The capture module needs a minor interface extension to support both paths:

```go
// Extended Capturer interface for hardware encode path
type DMABufCapturer interface {
    Capturer

    // NextDMABuf returns frame metadata without reading pixels from GPU.
    // The returned FBInfo contains the DMA-BUF fd (caller must close) and a
    // capture-time Timestamp (CLOCK_MONOTONIC ns) used for A/V sync.
    // Returns nil, nil if no new frame is available (static screen).
    // Returns nil, ErrNotSupported if DMA-BUF export is unavailable.
    NextDMABuf() (*FBInfo, error)
}

// FBInfo includes Timestamp (see pkg/capture). The pipeline pairs the encoder's
// NAL output with FBInfo.Timestamp before broadcasting — EncodeDMABuf itself does
// not handle timestamps.
```

**Pipeline selection at startup:**
```
if capturer implements DMABufCapturer AND hwEncoder available:
    USE: capturer.NextDMABuf() → hwEncoder.EncodeDMABuf()
else:
    USE: capturer.NextFrame() → converter.Convert() → swEncoder.Encode()
```

---

## Cursor Handling in Hardware Path

The hardware path presents a challenge for cursor compositing:
- Software path: cursor is alpha-blended onto the RGBA frame (CPU, trivial)
- Hardware path: framebuffer goes directly to encoder without CPU access

**DECISION (confirmed): client-side cursor.** The hardware path sets `Config.cursorMode = "separate"`. The server reads the cursor plane (position every frame, image only when it changes) and sends `FrameTypeCursorUpdate`; the client renders the cursor as a CSS/canvas overlay. This is lower latency than burning it into the frame (the cursor tracks the pointer without waiting for the next encoded frame) and keeps the frame fully GPU-resident.

The cursor source (DRM cursor plane position + image) is exposed by the capturer regardless of path, so the software path MAY also use `cursorMode = "separate"` for consistency, or keep `"embedded"` (CPU blend) — both are valid and announced via the handshake.

(Rejected alternatives: GPU `vaProcPipeline` compositing — extra GPU pass, cursor still lags a frame; per-cursor software fallback — latency spikes.)

---

## Refactoring Directives

### R-HWE-01: Implement VA-API CGo Bindings
Create CGo wrappers for libva:
- `vaGetDisplayDRM`, `vaInitialize`, `vaTerminate`
- `vaCreateConfig`, `vaCreateContext`
- `vaCreateSurfaces` (with external buffer import)
- `vaBeginPicture`, `vaRenderPicture`, `vaEndPicture`, `vaSyncSurface`
- `vaCreateBuffer`, `vaMapBuffer`, `vaUnmapBuffer`, `vaDestroyBuffer`

### R-HWE-02: Implement DMA-BUF Surface Import
Use `VASurfaceAttribExternalBuffers` to import DMA-BUF fds as VA-API surfaces without any GPU→CPU→GPU round-trip.

### R-HWE-03: Handle Format Mismatch
If the DRM framebuffer format doesn't match what VA-API expects (NV12), implement a GPU-side colorspace conversion pass using `vaCreateBuffer(VAProcPipelineParameterBufferType)`.

### R-HWE-04: Implement Client-Side Cursor
Add `FrameTypeCursorUpdate` protocol messages:
```json
Payload: [x:uint16][y:uint16][visible:uint8][imageChanged:uint8][w:uint16][h:uint16][rgba_data...]
```
Server sends position updates at high frequency (every frame) and image data only when the cursor image actually changes.

### R-HWE-05: Probe Hardware Capabilities at Startup
Detect supported VA-API entrypoints, profiles, and formats:
```go
func ProbeHardwareCapabilities(renderNode string) (*HWCapabilities, error)
```
Returns supported codecs, max resolution, supported input formats.

### R-HWE-06: Rate Control Configuration
Support VA-API rate control modes:
- `VA_RC_CQP` — Constant QP (current default, predictable quality)
- `VA_RC_CBR` — Constant Bitrate (for bandwidth-constrained scenarios)
- `VA_RC_VBR` — Variable Bitrate (best quality per bit)

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | DMABufParams validation, config building | No |
| Unit | Surface ring buffer management logic | No |
| Integration | Full encode pipeline (DMA-BUF → NALs) | Yes (VA-API GPU) |
| Integration | Format conversion (XRGB8888 → NV12 → encode) | Yes (VA-API GPU) |
| Integration | IDR force and sequence continuity | Yes (VA-API GPU) |
| Integration | Fallback to software on hardware failure | Yes (can simulate) |
| Benchmark | Encode throughput at 1080p60 and 1440p60 | Yes (VA-API GPU) |
| Comparison | Latency: hw path vs sw path side-by-side | Yes |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Encode latency (1080p) | <3ms |
| Encode latency (1440p) | <5ms |
| CPU usage (60fps) | <3% (GPU does all work) |
| GPU utilization | <40% (encoder is a fixed-function unit) |
| Memory (GPU-side) | <50MB (4 surfaces + coded buffers) |
| Memory (CPU-side) | <5MB (NAL output buffers only) |
| Bandwidth savings vs SW | ~36MB/frame eliminated from bus |
| Startup time | <500ms (VA-API init + surface allocation) |

---

## Dependencies

| Library | Purpose | Package |
|---------|---------|---------|
| `libva` | VA-API encode + surface management | `pkg-config: libva` |
| `libva-drm` | DRM render node display creation | `pkg-config: libva-drm` |
| `libdrm` | DMA-BUF and format constants | `pkg-config: libdrm` |
