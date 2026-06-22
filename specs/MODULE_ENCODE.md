# Module Spec: Encode

## Overview

The Encode module is the **software** (CPU, in-process) video encoder. It transforms `I420Frame` input into compressed packets (H.264 or VP8) behind a unified `Encoder` interface. The zero-copy GPU path lives in a separate module ([`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md)); this module never touches DMA-BUFs or VA-API.

> Scope note: the legacy ffmpeg-`h264_vaapi` "hardware via subprocess" path is **removed**. Hardware encoding is exclusively the zero-copy `hwencode` module. This module is OpenH264 / libx264 / VP8 only.

### NAL Output Contract

- For H.264, `Encode` returns each NAL unit as a separate `[]byte` **in Annex B form (start code `00 00 00 01` retained)**. A keyframe's slice includes SPS, PPS, and the IDR NAL, in that order.
- For VP8, `Encode` returns exactly one `[]byte` (the whole frame).
- The server concatenates these verbatim into one per-frame message (it does NOT re-frame or strip start codes).

---

## Public Interface

```go
package encode

// Encoder is the contract all video encoder backends must satisfy.
type Encoder interface {
    // Encode takes a YUV I420 frame and returns encoded packets.
    // Returns nil, nil if the frame was intentionally skipped.
    // Returned byte slices are freshly allocated (safe to hold across calls).
    Encode(frame *I420Frame) ([][]byte, error)

    // ForceKeyframe requests that the next encoded frame be an IDR/keyframe.
    // Thread-safe. May be called from any goroutine.
    ForceKeyframe()

    // Close releases all encoder resources.
    Close() error
}

// I420Frame holds planar YUV 4:2:0 data.
type I420Frame struct {
    Y      []byte // Luma plane (width * height bytes)
    U      []byte // Chroma-U plane (width/2 * height/2 bytes)
    V      []byte // Chroma-V plane (width/2 * height/2 bytes)
    Width  int
    Height int
}

// EncoderConfig holds codec-agnostic encoder parameters.
type EncoderConfig struct {
    Width      int
    Height     int
    FPS        int
    BitrateBps int // 0 = use QP mode
    QP         int // Quantization parameter (lower = better quality)
}

// Converter handles RGBA -> I420 color space conversion.
type Converter interface {
    // Convert transforms RGBA pixels to I420.
    // Returned *I420Frame is reused on next call (zero-alloc steady state).
    Convert(rgba []byte) *I420Frame
    Close()
}

// EncoderBackend identifies the SOFTWARE encoder implementation.
// (Hardware/zero-copy lives in the hwencode module, selected by the pipeline.)
type EncoderBackend int

const (
    BackendAuto     EncoderBackend = iota // Pick best software encoder
    BackendOpenH264                       // CGo OpenH264 (H.264 baseline, lowest latency)
    BackendFFmpeg                         // FFmpeg subprocess (libx264, software)
    BackendVP8                            // CGo libvpx via libavcodec
)
// NOTE: There is no BackendVAAPI. VA-API is the hwencode module's zero-copy path.
```

---

## Internal Architecture

### Backend: OpenH264 (Primary Software Path)

```
I420Frame → CGo → WelsCreateSVCEncoder → EncodeFrame → SFrameBSInfo → extractNALs → [][]byte
```

**Encoding Parameters:**
- Profile: Baseline (maximum decoder compatibility)
- Entropy: CAVLC
- Slices: 1 (single-slice for low latency)
- Reference frames: 1
- B-frames: 0
- IDR: On-demand only (no periodic)
- Threading: Single-threaded (frame-at-a-time model)
- Rate control: Fixed QP (RC_OFF_MODE) or Bitrate (RC_BITRATE_MODE, max=1.5x target)

**CGo Binding:**
- Links: `-lopenh264`
- Memory safety: `runtime.Pinner` pins Go slices during C calls
- NAL extraction: Pointer arithmetic over `SFrameBSInfo.sLayerInfo[].pBsBuf`

### Backend: FFmpeg Subprocess

```
I420 planes → stdin pipe (rawvideo) → ffmpeg process → stdout pipe (h264 Annex B) → NAL splitting → [][]byte
```

**libx264 (software only):**
- Preset: ultrafast
- Tune: zerolatency
- CRF: 26
- Profile: baseline
- GOP: 30, no B-frames
- Output: Annex B H.264 on stdout

(There is no `h264_vaapi` mode here anymore — hardware is the zero-copy `hwencode` module.)

**Process Management:**
- Separate goroutines for stdin writes (`writeLoop`) and stdout reads (`readNALs`)
- NAL splitting on `00 00 00 01` start codes
- Bounded channel (cap 4) for NAL delivery
- Auto-restart on process death

### Backend: VP8 (CGo via libavcodec)

```
I420Frame → CGo → avcodec_send_frame → avcodec_receive_packet → AVPacket.data → []byte
```

**Parameters:**
- Codec: libvpx
- Quality: realtime, cpu-used=8 (maximum speed)
- CRF: 26
- GOP: 30, no B-frames
- Threading: 1

### Color Space Conversion (libyuv)

```
RGBA []byte → C.ABGRToI420() → Y/U/V planes
```

- Links: `-lyuv`
- Uses SIMD-optimized conversion (SSE2/AVX2/NEON depending on platform)
- Pre-allocated output buffers (zero per-frame allocation)
- Color matrix: BT.601 limited range
- Note: libyuv's "ABGR" = memory byte order R,G,B,A (matches GL_RGBA output)

---

## Encoder Selection Logic (software only)

The pipeline decides hardware-vs-software FIRST. This module is only consulted when the software path is chosen.

```
switch config.Backend:
case Auto:      return NewH264Encoder(cfg)   // OpenH264 baseline (best latency/compat)
case OpenH264:  return NewH264Encoder(cfg)
case FFmpeg:    return NewFFmpegEncoder(cfg, log) // libx264 ultrafast/zerolatency
case VP8:       return NewVP8Encoder(cfg)
```

> The current code selects VP8 as its software default and ffmpeg-vaapi for hardware. Post-refactor: software default = OpenH264 (H.264 baseline); VP8 remains available via `--encoder vp8`. The codec chosen here is advertised to clients in the Config handshake so the decoder matches.

### ProbeVAAPI relocation
`ProbeVAAPI()` moves to the pipeline's capability probing (it decides hw-vs-sw). This module no longer references VA-API.

---

## Refactoring Directives

### R-ENC-01: Fix FFmpeg ForceKeyframe
The current `ForceKeyframe()` stores an atomic flag but never communicates it to the ffmpeg process. Implement one of:
- Send `SIGUSR1` to ffmpeg (custom patch required)
- Close and reopen stdin pipe to force a new GOP
- Use `-force_key_frames` with a control socket
- **Recommended:** Kill and restart ffmpeg with a keyframe flush

### R-ENC-02: Extract Interface to `pkg/encode`
Move `Encoder`, `I420Frame`, `EncoderConfig`, and `Converter` to a public package. Keep implementations in `internal/encode/openh264/`, `internal/encode/ffmpeg/`, etc.

### R-ENC-03: Add Backpressure to FFmpeg NAL Channel
Replace the `default:` drop case in `readNALs` with a blocking send + timeout. Log dropped NALs as a metric for monitoring.

### R-ENC-04: Graceful FFmpeg Shutdown
Replace `os.Kill` (SIGKILL) with `SIGTERM` + wait with timeout, falling back to SIGKILL. This allows ffmpeg to flush its output buffer.

### R-ENC-05: (moved) VA-API probing lives in the pipeline
VA-API probing has moved to the pipeline's capability probe (`ProbeCapabilities`), which runs once at startup and caches the result. This module no longer probes VA-API. (Hardware capability detection details: see MODULE_HARDWARE_ENCODE R-HWE-05.)

### R-ENC-06: Add Encoder Metrics
Expose per-frame encode timing, output size, keyframe frequency, and drop count via a `Stats()` method or metrics interface.

### R-ENC-07: Separate Converter from Encoder Package
Move `Converter` to its own sub-package (`internal/encode/convert/`) to clarify the boundary between color conversion and encoding.

### R-ENC-08: Thread-Safe Converter Option
Add a `NewPooledConverter` that maintains per-goroutine buffers, or document that `Converter` is single-goroutine only.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | EncoderConfig validation, I420Frame plane sizes | No |
| Unit | Converter output correctness (BT.601 values) | No (needs libyuv) |
| Unit | Converter buffer reuse guarantee | No (needs libyuv) |
| Unit | OpenH264 create/close, NAL production, IDR detection | No (needs libopenh264) |
| Unit | FFmpeg argument building, NAL start-code splitting | No |
| Integration | FFmpeg software (libx264) encode (30 frames) | No (needs ffmpeg) |
| Benchmark | Converter 1080p throughput | No (needs libyuv) |
| Benchmark | OpenH264 320x240 encode throughput | No (needs libopenh264) |

---

## Performance Targets

| Metric | OpenH264 | FFmpeg SW (libx264) | VP8 |
|--------|----------|---------------------|-----|
| Encode latency (1080p) | <8ms | <12ms | <10ms |
| CPU usage (60fps) | ~25% 1 core | ~40% 1 core | ~30% 1 core |
| Output quality (SSIM) | 0.92+ | 0.94+ | 0.91+ |
| Startup time | <10ms | <200ms | <10ms |

(Hardware/zero-copy latency targets are in MODULE_HARDWARE_ENCODE: <3ms @1080p, <5% CPU.)
