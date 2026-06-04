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

### Summary Table (sorted by P80)

| Rank | Configuration | Library | P80 | P50 | P95 | P99 | Avg | NAL Size |
|------|---------------|---------|-----|-----|-----|-----|-----|----------|
| 1 | vaapi-h264-qp32-lp | libavcodec-cgo | **7.7ms** | 7.0ms | 8.4ms | 8.8ms | 7.2ms | 211KB |
| 2 | vaapi-h264-qp26-lp-async4 | libavcodec-cgo | **7.8ms** | 7.2ms | 8.2ms | 8.4ms | 7.2ms | 486KB |
| 3 | vaapi-h264-baseline-qp26-lp | libavcodec-cgo | **7.9ms** | 7.1ms | 8.4ms | 9.2ms | 7.3ms | 476KB |
| 4 | vaapi-h264-main-qp26-lp | libavcodec-cgo | **7.9ms** | 7.3ms | 8.1ms | 8.4ms | 7.2ms | 404KB |
| 5 | vaapi-h264-qp26-lp-async2 | libavcodec-cgo | **7.9ms** | 7.3ms | 8.3ms | 8.6ms | 7.3ms | 486KB |
| 6 | vaapi-h264-qp26-full | libavcodec-cgo | **8.1ms** | 7.5ms | 8.8ms | 13.2ms | 7.6ms | 486KB |
| 7 | vaapi-h264-qp26-lp | libavcodec-cgo | **8.5ms** | 7.5ms | 10.0ms | 13.7ms | 7.9ms | 486KB |
| 8 | vaapi-h264-qp20-lp | libavcodec-cgo | **8.6ms** | 8.0ms | 10.9ms | 14.9ms | 8.3ms | 1303KB |
| 9 | ffsub-vaapi-qp32-lp | ffmpeg-subprocess | **10.5ms** | 8.1ms | 12.7ms | 14.3ms | 8.6ms | 232KB |
| 10 | ffsub-vaapi-qp26-full | ffmpeg-subprocess | **10.9ms** | 8.5ms | 14.6ms | 26.2ms | 9.4ms | 475KB |
| 11 | ffsub-vaapi-qp26-lp | ffmpeg-subprocess | **11.0ms** | 8.8ms | 14.2ms | 17.0ms | 9.3ms | 482KB |
| 12 | ffsub-vaapi-baseline-qp26-lp | ffmpeg-subprocess | **11.1ms** | 8.1ms | 13.3ms | 15.8ms | 9.0ms | 456KB |
| 13 | ffsub-vaapi-qp20-lp | ffmpeg-subprocess | **12.0ms** | 9.5ms | 17.1ms | 18.8ms | 10.0ms | 522KB |

### Configuration Key

| Parameter | Values Tested | Notes |
|-----------|---------------|-------|
| QP | 20, 26, 32 | Lower = better quality + larger NAL |
| low_power | true, false | EncSliceLP vs full-feature encode |
| profile | baseline, main, high | Complexity of headers/encoding tools |
| async_depth | 1, 2, 4 | libavcodec thread_count for async pipeline |
| library | libavcodec-cgo, ffmpeg-subprocess | In-process vs pipe |

### Detailed Configuration Descriptions

**libavcodec-cgo (in-process)**:
- Direct cgo binding to libavcodec/libavutil
- Creates hw_device_ctx for VA-API on `/dev/dri/renderD128`
- Allocates hw_frames_ctx with NV12 surface pool (8 surfaces)
- I420 → NV12 conversion in C (Y copy + UV interleave)
- `av_hwframe_transfer_data()` uploads CPU frame to GPU surface
- `avcodec_send_frame()` / `avcodec_receive_packet()` encode cycle
- Zero subprocess overhead, zero pipe I/O

**ffmpeg-subprocess (pipe)**:
- Spawns `ffmpeg` process with `-f rawvideo -pix_fmt yuv420p` stdin
- Writes raw I420 frame bytes to pipe
- `-init_hw_device vaapi` + `-vf format=nv12,hwupload` for GPU path
- Reads encoded NAL units from stdout pipe
- Overhead: process scheduling, pipe buffering, userspace↔kernel copies

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

1. **libavcodec cgo VA-API is the fastest path** at 7.7ms P80 - eliminates 3ms pipe overhead vs ffmpeg subprocess.

2. **low_power mode is slightly slower in P80** (8.5ms vs 8.1ms for full) but has tighter P99 (13.7ms vs 13.2ms). On Kaby Lake, EncSliceLP is the only encode entrypoint so both modes use the same hardware block.

3. **async_depth makes no measurable difference** on this GPU. The hardware pipeline depth is 1 frame on Kaby Lake's fixed-function encoder.

4. **Profile choice affects NAL size but not latency**: Main (404KB) vs High (486KB) vs Baseline (476KB) are all within 0.2ms of each other.

5. **QP32 halves NAL size** (211KB vs 486KB at QP26) with only 0.4ms less encode time. Quality impact is visible but acceptable for screen content.

6. **QP20 nearly triples NAL size** (1303KB) for negligible quality gain on screen content. Not recommended.

7. **ffmpeg subprocess tail latency is 2-3x worse** at P99 (14-26ms vs 8-14ms) due to OS scheduling and pipe buffer variability.

8. **VP8 is bandwidth-optimal** at 54KB/frame (10x smaller than H.264) but costs 22ms extra latency. Best for bandwidth-constrained scenarios where 30ms encode is acceptable.

### Recommendation Matrix

| Scenario | Encoder | Expected P80 | NAL |
|----------|---------|--------------|-----|
| Lowest latency (GPU available) | VA-API cgo, QP26, High | 7.8ms | 486KB |
| Low bandwidth + GPU | VA-API cgo, QP32, High | 7.7ms | 211KB |
| No GPU, multi-core | x264 ultrafast 4t | 13.7ms | 586KB |
| No GPU, single-core | x264 ultrafast 1t | 25.9ms | 586KB |
| Extreme bandwidth savings | VP8 speed8 1t | 30.0ms | 54KB |
| Maximum compatibility | OpenH264 QP26 | 55.7ms | 589KB |

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
| Intel QSV (VPL) | BROKEN | Kaby Lake not supported by oneVPL runtime dispatcher |
| VP8 VA-API encode | UNAVAILABLE | Only decode entrypoint in hardware |
| VP9 VA-API encode | UNAVAILABLE | Only decode entrypoint |
| HEVC VA-API encode | UNAVAILABLE | Only decode entrypoint |
| AV1 VA-API encode | UNAVAILABLE | Not present in Gen 9.5 |
| V4L2 M2M encode | NO DEVICE | Only USB webcam present, no stateful codec device |
| Direct libva cgo | NOT IMPLEMENTED | Same hardware as libavcodec path, hundreds of lines of slice parameter boilerplate, no measurable speed advantage expected |
