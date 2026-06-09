# Specification: KMS Capture + Software H.264 Encode + WebSocket Viewer

## Overview

Build the foundational end-to-end streaming pipeline: capture the screen via KMS/DRM, convert to I420 via libyuv, encode to H.264 via OpenH264, stream NAL units over WebSocket, and decode/render in a browser client using WebCodecs.

This is the first working demo - a single viewer connecting to the server and seeing the host's desktop in real-time.

## Functional Requirements

### FR-1: Project Foundation
- Go module with clean package structure
- CLI flags: `--port`, `--fps`, `--verbose`, `--quiet`
- Logging module (timestamped, leveled, human-readable)
- Signal handling for graceful shutdown (SIGINT, SIGTERM)
- Makefile with build, test, lint, fmt targets

### FR-2: Wire Protocol
- Minimal binary frame header for WebSocket messages
- Fields: type (uint8), timestamp (uint64), width (uint16), height (uint16), payload_size (uint32)
- Frame types: video_h264, ping/pong
- Marshal/unmarshal functions with unit tests

### FR-3: KMS Screen Capture
- Open DRM card device with CAP_SYS_ADMIN
- Enumerate planes, find primary plane with active framebuffer
- Export scanout buffer as DMA-BUF fd (drmPrimeHandleToFD)
- Create EGL surfaceless context from GBM device
- Import DMA-BUF as EGLImage, bind to GL texture
- glGetTextureSubImage to read BGRA pixels into pre-allocated buffer
- Frame change detection (poll fb_id)
- Frame pacing to target FPS
- Handle display reconfiguration with retry

### FR-4: Software Video Encoding
- libyuv ARGBToI420 conversion (cgo, pre-allocated I420 buffer)
- OpenH264 ISVCEncoder (cgo): create, configure, encode, destroy
- Configuration: CAMERA_VIDEO_REAL_TIME, SM_SINGLE_SLICE, QP-based quality
- Output: raw H.264 NAL units (SPS/PPS + IDR + slices)
- IDR frame on demand (for new client connect)

### FR-5: WebSocket Server
- HTTP server on configurable port (default 30084)
- Serve embedded web client at `/`
- WebSocket upgrade at `/ws`
- Binary frame broadcast (protocol header + NALs)
- Single client support (multi-client is Track 5)
- Graceful client disconnect handling

### FR-6: Web Client
- HTML5 page embedded via go:embed
- WebCodecs VideoDecoder (avc1.42E01E)
- Parse binary frames, detect keyframes from NAL type
- Render decoded frames to Canvas
- Auto-resize to fit window
- Connection status indicator
- Auto-reconnect on disconnect

## Non-Functional Requirements

- Memory: <50MB RSS
- Capture + encode latency: <15ms per frame at 1080p
- Startup to first frame: <3 seconds
- Zero per-frame allocations in hot path (pre-allocate all buffers)

## Acceptance Criteria

1. `featherdesk --port 30084` starts and captures screen via KMS
2. Opening `http://host:30084/` in Chrome shows the live desktop
3. Stream runs at target FPS (configurable, default 30)
4. No memory growth over time (stable RSS)
5. Server handles client disconnect + reconnect cleanly
6. `go test ./...` passes with >80% coverage on protocol and encode packages

## Out of Scope

- Hardware encoding (Track 2)
- Input injection (Track 3)
- Audio (Track 4)
- Multi-client / roles (Track 5)
- PipeWire capture fallback
