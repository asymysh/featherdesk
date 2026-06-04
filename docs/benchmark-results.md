# ViewPort RDS - Encoder Benchmark Results

## System Under Test

| Component | Detail |
|-----------|--------|
| CPU | Intel Core i3-7100 @ 3.90GHz (Kaby Lake, 2C/4T) |
| GPU | Intel HD Graphics 630 (Gen 9.5) |
| VA-API Driver | Intel iHD 26.1.2 |
| VA-API Version | 1.23 (libva 2.22.0) |
| HW Encode Support | H.264 EncSliceLP (Main, High, ConstrainedBaseline) |
| HW Encode NOT Available | HEVC, VP8, VP9, AV1 (decode only) |
| OS | Ubuntu 25.10 (Resolute), kernel 7.0.0, Wayland/GNOME |
| ffmpeg | 8.0.1, libavcodec 62.11.100 |
| libx264 | 0.165.x |
| libvpx | 1.16.0 |
| OpenH264 | 2.6.0 |
| SVT-AV1 | 2.3.0 |
| x265 | 4.1 |
| Go | 1.26.0 |

## Methodology

### Benchmark Tool

Location: `cmd/bench/`

The benchmark generates synthetic YUV 4:2:0 frames with pseudo-random gradient patterns to simulate desktop content variability. Each encoder configuration runs through:

1. **Frame generation**: Pre-allocated I420 frames with per-frame variation (gradient + noise)
2. **Warmup phase**: First N frames are encoded but not measured (primes HW pipeline, JIT, caches)
3. **Measurement phase**: Each frame is timed independently with `time.Now()` precision
4. **Timeout protection**: 3-second per-frame timeout prevents hangs from stalling the suite
5. **Statistics**: Sorted latency array, percentiles computed directly from sorted position

### Parameters

- Resolution: **2560x1440** (native display, worst-case for encode)
- Frames measured: **120** (after 15 warmup)
- Primary metric: **P80** (80th percentile encode latency)
- Secondary metrics: P50, P95, P99, average NAL size
- Frame rate target: 30 fps (33.3ms budget)

### What P80 Means

P80 = 80% of frames encode within this time. It represents the realistic per-frame budget under normal operation, filtering out occasional OS scheduling jitter (P95/P99) while not being as optimistic as the median (P50).

### Encoder Interface

All encoders implement a common interface:

```go
type EncoderBench interface {
    Name() string
    Library() string
    Codec() string
    Encode(y, u, v []byte, width, height int) ([]byte, error)
    Close()
}
```

Input: raw I420 planes. Output: encoded bitstream bytes. Timing wraps the `Encode()` call only.

---

## Hardware Encoding Results

### Summary Table (sorted by P80, 5 libraries, 43 configurations)

| Rank | Configuration | Library | P80 | P50 | P95 | P99 | Avg | NAL Size |
|------|---------------|---------|-----|-----|-----|-----|-----|----------|
| 1 | vaapi-h264-qp36-lp | libavcodec-cgo | **6.0ms** | 5.7ms | 6.3ms | 6.8ms | 5.7ms | 122KB |
| 2 | vaapi-h264-main-qp32-lp | libavcodec-cgo | **6.0ms** | 5.7ms | 6.5ms | 7.0ms | 5.7ms | 160KB |
| 3 | vaapi-h264-qp32-full | libavcodec-cgo | **6.0ms** | 5.8ms | 6.5ms | 6.9ms | 5.7ms | 211KB |
| 4 | vaapi-h264-qp32-lp | libavcodec-cgo | **6.1ms** | 5.8ms | 6.4ms | 6.6ms | 5.8ms | 211KB |
| 5 | vaapi-h264-qp40-lp | libavcodec-cgo | **6.1ms** | 5.8ms | 6.4ms | 6.8ms | 5.7ms | 79KB |
| 6 | vaapi-h264-main-qp26-lp | libavcodec-cgo | **6.1ms** | 5.8ms | 6.7ms | 7.4ms | 5.8ms | 404KB |
| 7 | vaapi-h264-baseline-qp32-lp | libavcodec-cgo | **6.1ms** | 5.8ms | 6.5ms | 6.9ms | 5.8ms | 204KB |
| 8 | vaapi-h264-baseline-qp26-lp | libavcodec-cgo | **6.2ms** | 5.8ms | 6.8ms | 7.2ms | 5.8ms | 475KB |
| 9 | vaapi-h264-qp26-lp-async2 | libavcodec-cgo | **6.2ms** | 5.8ms | 6.7ms | 7.1ms | 5.8ms | 486KB |
| 10 | vaapi-h264-qp26-lp-async4 | libavcodec-cgo | **6.2ms** | 5.8ms | 6.6ms | 7.1ms | 5.8ms | 486KB |
| 11 | vaapi-h264-qp28-lp | libavcodec-cgo | **6.3ms** | 5.9ms | 6.8ms | 7.0ms | 5.9ms | 372KB |
| 12 | vaapi-h264-qp26-full | libavcodec-cgo | **6.3ms** | 5.9ms | 7.1ms | 10.7ms | 6.0ms | 486KB |
| 13 | vaapi-h264-qp26-lp | libavcodec-cgo | **6.5ms** | 6.0ms | 7.0ms | 11.6ms | 6.1ms | 486KB |
| 14 | vaapi-h264-qp20-full | libavcodec-cgo | **7.2ms** | 7.1ms | 7.4ms | 7.5ms | 7.1ms | 1303KB |
| 15 | vaapi-h264-qp20-lp | libavcodec-cgo | **7.3ms** | 7.1ms | 7.7ms | 10.9ms | 7.2ms | 1303KB |
| 16 | vaapi-h264-qp24-lp | libavcodec-cgo | **7.4ms** | 6.2ms | 8.7ms | 8.9ms | 6.5ms | 645KB |
| 17 | ffsub-vaapi-qp40-lp | ffmpeg-subprocess | **7.9ms** | 7.1ms | 10.1ms | 11.5ms | 7.4ms | 94KB |
| 18 | ffsub-vaapi-qp36-lp | ffmpeg-subprocess | **8.8ms** | 7.1ms | 10.4ms | 11.5ms | 7.5ms | 144KB |
| 19 | ffsub-vaapi-baseline-qp26-lp | ffmpeg-subprocess | **8.9ms** | 7.8ms | 11.5ms | 13.0ms | 7.9ms | 468KB |
| 20 | ffsub-vaapi-main-qp26-lp | ffmpeg-subprocess | **8.9ms** | 7.6ms | 10.5ms | 13.5ms | 7.9ms | 412KB |
| 21 | ffsub-vaapi-qp32-lp | ffmpeg-subprocess | **9.0ms** | 7.5ms | 11.5ms | 14.2ms | 7.9ms | 247KB |
| 22 | ffsub-vaapi-qp26-lp | ffmpeg-subprocess | **9.5ms** | 8.2ms | 12.1ms | 14.6ms | 8.7ms | 485KB |
| 23 | libva-direct-qp26-idr0 | libva-direct | **9.6ms** | 8.8ms | 11.5ms | 12.3ms | 9.1ms | 398KB |
| 24 | ffsub-vaapi-qp26-full | ffmpeg-subprocess | **9.7ms** | 7.9ms | 12.9ms | 14.9ms | 8.5ms | 487KB |
| 25 | libva-direct-qp36-idr30 | libva-direct | **9.8ms** | 8.7ms | 10.8ms | 11.5ms | 8.9ms | 90KB |
| 26 | ffsub-vaapi-qp20-lp | ffmpeg-subprocess | **10.0ms** | 7.9ms | 12.2ms | 15.2ms | 8.4ms | 544KB |
| 27 | libva-direct-qp32-idr30 | libva-direct | **10.1ms** | 8.7ms | 11.8ms | 12.9ms | 9.2ms | 161KB |
| 28 | libva-direct-qp40-idr30 | libva-direct | **10.4ms** | 8.8ms | 11.1ms | 12.4ms | 9.1ms | 64KB |
| 29 | libva-direct-qp26-idr30 | libva-direct | **10.5ms** | 9.5ms | 11.5ms | 12.2ms | 9.6ms | 404KB |
| 30 | libva-direct-qp20-idr30 | libva-direct | **11.8ms** | 11.3ms | 12.6ms | 14.0ms | 11.5ms | 1203KB |
| 31 | gst-vaapi-qp32-tu7 | gstreamer-subprocess | **21.9ms** | 20.4ms | 228.6ms | 246.1ms | 34.4ms | 228KB |
| 32 | gst-vaapi-qp32-tu4 | gstreamer-subprocess | **22.2ms** | 20.6ms | 229.6ms | 246.0ms | 34.9ms | 226KB |
| 33 | gst-vaapi-qp26-tu7 | gstreamer-subprocess | **22.5ms** | 21.0ms | 230.4ms | 245.4ms | 34.9ms | 514KB |
| 34 | gst-vaapi-qp26-tu1 | gstreamer-subprocess | **22.5ms** | 21.2ms | 232.3ms | 241.9ms | 35.1ms | 521KB |
| 35 | gst-vaapi-qp26-ref1 | gstreamer-subprocess | **22.5ms** | 20.5ms | 23.3ms | 34.4ms | 21.0ms | 486KB |
| 36 | gst-vaapi-qp26-tu4 | gstreamer-subprocess | **22.6ms** | 21.1ms | 228.7ms | 241.2ms | 34.9ms | 521KB |
| 37 | gst-vaapi-qp26-nocabac | gstreamer-subprocess | **22.6ms** | 20.9ms | 230.0ms | 245.0ms | 35.0ms | 612KB |
| 38 | gst-vaapi-qp20-tu4 | gstreamer-subprocess | **22.9ms** | 20.9ms | 228.5ms | 241.9ms | 35.0ms | 1389KB |
| - | vpl-qp26-tu1 | intel-vpl | SKIP | - | - | - | - | - |
| - | vpl-qp26-tu4 | intel-vpl | SKIP | - | - | - | - | - |
| - | vpl-qp26-tu7 | intel-vpl | SKIP | - | - | - | - | - |
| - | vpl-qp32-tu7 | intel-vpl | SKIP | - | - | - | - | - |
| - | vpl-qp20-tu7 | intel-vpl | SKIP | - | - | - | - | - |

### Libraries Tested

| Library | Integration | Configs | Status |
|---------|-------------|---------|--------|
| libavcodec-cgo (VA-API) | In-process cgo, hw_device_ctx + NV12 upload | 16 | Best performer |
| ffmpeg subprocess | Pipe stdin/stdout, `-c:v h264_vaapi` | 8 | +3ms pipe overhead |
| libva-direct | Raw VA-API calls (vaRenderPicture) via cgo | 6 | Slower than libavcodec |
| GStreamer (vah264lpenc) | `gst-launch-1.0` subprocess, rawvideoparse | 8 | 3x slower (internal buffering) |
| Intel VPL (libvpl) | In-process cgo, MFXVideoENCODE | 5 | BROKEN (Kaby Lake unsupported) |

### Configuration Key

| Parameter | Values Tested | Notes |
|-----------|---------------|-------|
| QP | 20, 24, 26, 28, 32, 36, 40 | Lower = better quality + larger NAL |
| low_power | true, false | EncSliceLP vs full-feature encode |
| profile | baseline, main, high | Complexity of headers/encoding tools |
| async_depth | 1, 2, 4 | libavcodec thread_count for async pipeline |
| target_usage | 1, 4, 7 | GStreamer/VPL speed preset (1=quality, 7=speed) |
| idr_period | 0, 30 | IDR frequency for libva-direct |
| cabac | true, false | GStreamer entropy coding toggle |
| ref_frames | 1, 3 | Reference frame count |
| library | libavcodec-cgo, ffmpeg-sub, libva-direct, gstreamer-sub, intel-vpl | 5 integration paths |

### Detailed Library Descriptions

**1. libavcodec-cgo (in-process, WINNER)**:
- Direct cgo binding to libavcodec/libavutil
- Creates hw_device_ctx for VA-API on `/dev/dri/renderD128`
- Allocates hw_frames_ctx with NV12 surface pool (8 surfaces)
- I420 → NV12 conversion in C (Y copy + UV interleave)
- `av_hwframe_transfer_data()` uploads CPU frame to GPU surface
- `avcodec_send_frame()` / `avcodec_receive_packet()` encode cycle
- Zero subprocess overhead, zero pipe I/O

**2. ffmpeg-subprocess (pipe)**:
- Spawns `ffmpeg` process with `-f rawvideo -pix_fmt yuv420p` stdin
- Writes raw I420 frame bytes to pipe
- `-init_hw_device vaapi` + `-vf format=nv12,hwupload` for GPU path
- Reads encoded NAL units from stdout pipe
- Overhead: process scheduling, pipe buffering, userspace→kernel copies

**3. libva-direct (raw VA-API cgo)**:
- Opens DRM render node directly
- Creates VAConfig, VAContext, VASurfaces manually
- Fills VAEncSequenceParameterBufferH264, VAEncPictureParameterBufferH264, VAEncSliceParameterBufferH264
- Calls vaBeginPicture/vaRenderPicture/vaEndPicture/vaSyncSurface
- Extracts coded buffer via vaMapBuffer
- ~200 lines of boilerplate vs ~20 lines for libavcodec
- Slower than libavcodec due to unoptimized surface management (vaDeriveImage per frame)

**4. GStreamer subprocess (gst-launch-1.0)**:
- Pipeline: `fdsrc ! rawvideoparse ! videoconvert ! vah264lpenc ! fdsink`
- Uses same vah264lpenc VA-API element as GNOME/PipeWire ecosystem
- Massive overhead from GStreamer framework: element negotiation, buffer copies, scheduling
- P95 latency spikes (228ms) from internal buffer pool management
- target-usage parameter has no measurable effect (HW is already maxed)

**5. Intel VPL (libvpl, BROKEN)**:
- oneVPL 2.16 dispatcher installed
- MFXLoad() succeeds, MFXCreateSession() fails
- Kaby Lake (Gen 9.5) not supported by the VPL runtime
- Requires Gen 11+ (Ice Lake) or newer for hardware dispatch
- Software fallback not tested (would use CPU, defeating purpose)

---

## Software Encoding Results (for comparison)

### Top 15 by P80 (2560x1440, 120 frames)

| Rank | Configuration | Library | P80 | NAL Size | Codec |
|------|---------------|---------|-----|----------|-------|
| 1 | x264-ultrafast-ze-4t | x264-cgo | **13.7ms** | 586KB | H.264 |
| 2 | x264-ultrafast-fa-4t | x264-cgo | **15.6ms** | 586KB | H.264 |
| 3 | libav-x264-ultrafast-4t | libavcodec-cgo | **15.7ms** | 400KB | H.264 |
| 4 | x264-ultrafast-ze-2t | x264-cgo | **18.3ms** | 586KB | H.264 |
| 5 | libav-vp8-speed8-4t | libavcodec-cgo | **22.5ms** | 58KB | VP8 |
| 6 | x264-superfast-ze-4t | x264-cgo | **24.7ms** | 607KB | H.264 |
| 7 | x264-ultrafast-ze-1t | x264-cgo | **25.9ms** | 586KB | H.264 |
| 8 | libav-x264-ultrafast-1t | libavcodec-cgo | **27.5ms** | 401KB | H.264 |
| 9 | libav-vp8-speed4-1t | libavcodec-cgo | **29.0ms** | 54KB | VP8 |
| 10 | libav-vp8-speed8-1t | libavcodec-cgo | **30.0ms** | 54KB | VP8 |
| 11 | libav-x264-superfast-4t | libavcodec-cgo | **31.0ms** | 287KB | H.264 |
| 12 | libav-x264-veryfast-4t | libavcodec-cgo | **41.4ms** | 172KB | H.264 |
| 13 | openh264-qp32 | openh264-cgo | **45.3ms** | 290KB | H.264 |
| 14 | openh264-qp26 | openh264-cgo | **55.7ms** | 589KB | H.264 |
| 15 | libav-svtav1-p12 | libavcodec-cgo | **67.0ms** | 717KB | AV1 |

### Codecs That Failed Real-Time at 1440p

| Codec | Best P80 | Reason |
|-------|----------|--------|
| x265 (HEVC) | 110-192ms | CPU-bound, no HW encode on Kaby Lake |
| VP9 | 70-200ms | CPU-bound, no HW encode |
| libaom-av1 | 181-365ms | CPU-bound, segfaults without `usage=realtime` |
| librav1e | hangs | Buffers all frames at 1440p |
| SVT-AV1 p8 | infinite | Pipeline-buffered, 0KB output per frame |

---

## Analysis

### Hardware vs Software Latency

| Path | Best P80 | Overhead vs HW |
|------|----------|----------------|
| libavcodec-cgo VA-API | 7.7ms | baseline |
| ffmpeg subprocess VA-API | 10.5ms | +2.8ms (36%) pipe overhead |
| x264 ultrafast 4 threads | 13.7ms | +6.0ms (software, CPU-bound) |
| x264 ultrafast 1 thread | 25.9ms | +18.2ms (single-core limited) |
| VP8 speed8 1 thread | 30.0ms | +22.3ms (but 10x smaller NAL) |

### Key Findings

1. **libavcodec cgo VA-API is the fastest path** at 6.0ms P80 - eliminates all subprocess/pipe overhead while leveraging optimized surface pool management.

2. **Direct libva is slower than libavcodec** (9.6ms vs 6.0ms) despite less abstraction. The extra latency comes from unoptimized surface handling: `vaDeriveImage` per frame for upload vs libavcodec's pre-allocated hw_frames pool with `av_hwframe_transfer_data`.

3. **ffmpeg subprocess adds 3ms pipe overhead** consistently across all QP values. P99 tail latency is 2x worse (14-15ms vs 7ms) due to OS scheduling variability.

4. **GStreamer is 3-4x slower than libavcodec** (22ms P80) with catastrophic P95 spikes (228ms). The framework's internal buffer management and element scheduling dominate over actual encode time. Not viable for real-time.

5. **Intel VPL is broken on Kaby Lake**. Requires Gen 11+ (Ice Lake or newer). The library is installed but the runtime dispatcher cannot find a compatible hardware implementation.

6. **QP has minimal effect on encode latency** at QP ≥ 28 (6.0-6.2ms range). Only QP ≤ 24 costs measurably more (7.2-7.4ms) due to larger coded output that saturates the memory bus.

7. **low_power vs full mode: identical** on Kaby Lake. EncSliceLP is the only hardware path, so both flags route to the same encoder block.

8. **Profile choice affects NAL size but not speed**: High (486KB) vs Main (404KB) vs Baseline (475KB) at QP26 are all within 0.2ms.

9. **async_depth is irrelevant** - hardware pipeline depth is 1 frame on Gen 9.5.

10. **QP32 + Main profile** is the sweet spot: 6.0ms encode, 160KB NAL (67% smaller than QP26 High).

### Recommendation Matrix

| Scenario | Encoder | Config | Expected P80 | NAL |
|----------|---------|--------|--------------|-----|
| Lowest latency | libavcodec-cgo VA-API | QP36, High, low_power | 6.0ms | 122KB |
| Best quality/speed | libavcodec-cgo VA-API | QP26, High, low_power | 6.5ms | 486KB |
| Balanced | libavcodec-cgo VA-API | QP32, Main, low_power | 6.0ms | 160KB |
| Maximum bandwidth savings | libavcodec-cgo VA-API | QP40, High, low_power | 6.1ms | 79KB |
| No cgo deps (deploy simplicity) | ffmpeg subprocess | QP32, low_power | 9.0ms | 247KB |
| Maximum control (custom rate ctrl) | libva-direct | QP26, IDR on-demand | 9.6ms | 398KB |

---

## Reproducing

```bash
cd /home/aseem

# Build benchmark tool
go build -o /tmp/bench ./cmd/bench/

# Run hardware-only configs (13 configs, ~3 minutes)
/tmp/bench -filter vaapi -n 120 -warmup 15

# Run all 72 configs (~30 minutes)
/tmp/bench -n 60 -warmup 5

# Run specific config
/tmp/bench -filter "x264-ultrafast-ze-1t" -n 120 -warmup 15

# Custom resolution
/tmp/bench -w 1920 -h 1080 -filter vaapi -n 120 -warmup 15
```

### Requirements

System packages:
```
libx264-dev libopenh264-dev libavcodec-dev libavutil-dev
libva-dev libvpx-dev libsvtav1enc-dev libyuv-dev
```

Runtime: DRI render node access (`/dev/dri/renderD128`) for VA-API benchmarks.

---

## Tested But Non-Viable on This Hardware

| Path | Status | Reason |
|------|--------|--------|
| Intel VPL/QSV (libvpl) | BROKEN | Kaby Lake not supported by oneVPL runtime dispatcher (requires Gen 11+) |
| GStreamer vah264lpenc | TOO SLOW | 22ms P80, 228ms P95 from framework overhead |
| VP8 VA-API encode | UNAVAILABLE | Only decode entrypoint in hardware |
| VP9 VA-API encode | UNAVAILABLE | Only decode entrypoint |
| HEVC VA-API encode | UNAVAILABLE | Only decode entrypoint |
| AV1 VA-API encode | UNAVAILABLE | Not present in Gen 9.5 |
| V4L2 M2M encode | NO DEVICE | Only USB webcam present, no stateful codec device |
| Direct libva cgo | SLOWER | Same hardware but 60% slower than libavcodec (unoptimized surface mgmt) |
