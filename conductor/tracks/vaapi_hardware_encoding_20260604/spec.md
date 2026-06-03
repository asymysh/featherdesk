# Specification: VA-API Hardware Encoding

## Overview

Add hardware-accelerated H.264 encoding via Intel VA-API as an alternative to the software OpenH264 path. This uses the Intel HD 630's Quick Sync encoder (EncSliceLP entrypoint) to offload encoding entirely to the GPU, freeing the CPU for other work.

## Dependencies

- Track 1 (KMS capture + software encode + WebSocket viewer) must be complete
- Existing `encode.Encoder` interface from Track 1

## Functional Requirements

### FR-1: VA-API Encoder via ffmpeg Pipe
- Spawn ffmpeg subprocess with h264_vaapi encode pipeline
- Pipe raw BGRA frames from KMS capture to ffmpeg stdin
- Read H.264 NAL units from ffmpeg stdout
- Parse NAL start codes (00 00 00 01) to split access units
- Implement the same `encode.Encoder` interface as OpenH264
- Handle ffmpeg crash with automatic restart

### FR-2: Encoder Auto-Detection
- At startup, probe VA-API: open /dev/dri/renderD128, query H264 encode entrypoint
- If VA-API H.264 encode is available, report it in logs
- Default behavior: use hardware if available, fall back to software

### FR-3: CLI Flag Selection
- `--hardware`: force VA-API encoder (fail if unavailable)
- `--software`: force OpenH264 encoder
- No flag: auto-detect (hardware preferred)
- Log which encoder was selected at startup

### FR-4: ffmpeg Pipeline Configuration
- Input: `-f rawvideo -pix_fmt bgra -s WxH -r FPS -i pipe:0`
- Filter: `-vf 'format=nv12,hwupload'`
- Encoder: `-c:v h264_vaapi -qp 20 -bf 0 -g <gop>`
- Output: `-f h264 pipe:1`
- Stderr: redirect to log file for debugging

## Non-Functional Requirements

- CPU usage in hardware mode: <5% during streaming
- Encode latency: <5ms per frame (GPU-side)
- Memory: ffmpeg subprocess adds ~50-80MB (acceptable, external process)
- Startup time: <2s for ffmpeg initialization

## Acceptance Criteria

1. `viewport-rds --hardware` streams using VA-API with <5% CPU
2. `viewport-rds --software` streams using OpenH264 (unchanged from Track 1)
3. `viewport-rds` (no flag) auto-detects and picks hardware if available
4. ffmpeg crash triggers automatic pipeline restart without dropping the WebSocket connection
5. Browser client cannot tell which encoder is active (same protocol)
6. /status endpoint reports active encoder type

## Out of Scope

- Direct libva API (no ffmpeg) - future optimization
- H.265/HEVC encoding
- VA-API DMA-BUF zero-copy (bypass EGL readback) - future optimization
