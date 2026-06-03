# Implementation Plan: KMS Capture + Software H.264 Encode + WebSocket Viewer

## Phase 1: Project Foundation [checkpoint: e2d5bea]

- [x] Task: Initialize Go module and directory structure [71e1084]
    - [ ] Create `go.mod` with module path `github.com/aseem/viewport-rds`
    - [ ] Create directories: `cmd/server/`, `internal/capture/`, `internal/encode/`, `internal/server/`, `internal/protocol/`, `client/`
    - [ ] Create Makefile (build, test, lint, fmt, clean targets)
    - [ ] Create `.gitignore` for Go project
    - [ ] Run `go mod tidy` to verify module compiles
- [x] Task: Implement CLI flags and logging [9765bb2]
    - [ ] Parse flags: `--port`, `--fps`, `--verbose`, `--quiet`, `--log-file`
    - [ ] Implement leveled logger (DEBUG, INFO, WARN, ERROR) with timestamps
    - [ ] Pattern: `2026-06-04 12:30:45.123 [module] message`
    - [ ] Write unit tests for logger level filtering
- [x] Task: Implement signal handling and main entrypoint [81e5147]
    - [ ] Listen for SIGINT/SIGTERM
    - [ ] Create context with cancel for graceful shutdown propagation
    - [ ] Wire up placeholder capture/server start+stop in `cmd/server/main.go`
    - [ ] Verify clean exit on Ctrl+C
- [x] Task: Conductor - User Manual Verification 'Project Foundation' (Protocol in workflow.md) [e2d5bea]

## Phase 2: Wire Protocol [checkpoint: 56791a2]

- [x] Task: Design and implement binary frame header [2ce20c22]
    - [ ] Define `protocol.FrameHeader` struct: Type(uint8), Timestamp(uint64), Width(uint16), Height(uint16), PayloadSize(uint32)
    - [ ] Define constants: FrameTypeVideoH264, FrameTypePing, FrameTypePong
    - [ ] Implement `Marshal(header) []byte` and `Unmarshal([]byte) (header, error)`
    - [ ] Pre-allocate header buffer (reusable, no alloc per frame)
- [x] Task: Write protocol unit tests [2ce20c22]
    - [ ] Test round-trip marshal/unmarshal
    - [ ] Test invalid header detection (short buffer, wrong magic)
    - [ ] Test all frame types
    - [ ] Benchmark marshal/unmarshal (target: <100ns per op)
- [x] Task: Conductor - User Manual Verification 'Wire Protocol' (Protocol in workflow.md) [2ce20c22]

## Phase 3: KMS Screen Capture [checkpoint: 5ae4826]

- [x] Task: Implement DRM card discovery and plane enumeration (cgo) [1812a1a]
    - [ ] Write cgo bindings: open card, drmSetClientCap, drmModeGetPlaneResources
    - [ ] Find primary plane with active fb_id (skip cursor planes)
    - [ ] Get CRTC dimensions and refresh rate
    - [ ] Export DMA-BUF fd via drmPrimeHandleToFD
    - [ ] Implement proper cleanup (close fds, free resources)
- [x] Task: Implement EGL context and DMA-BUF import (cgo) [bf62022]
    - [ ] Create GBM device from card fd
    - [ ] Initialize EGL display (EGL_PLATFORM_GBM_KHR)
    - [ ] Create surfaceless EGL context
    - [ ] Import DMA-BUF as EGLImage (EGL_LINUX_DMA_BUF_EXT with format+modifier)
    - [ ] Bind EGLImage to GL texture (glEGLImageTargetTexture2DOES)
- [x] Task: Implement pixel readback and capture loop [693b129]
    - [ ] glGetTextureSubImage → pre-allocated BGRA buffer
    - [ ] Frame change detection: poll drmModeGetPlane, check fb_id
    - [ ] Frame pacing: sleep remaining budget after capture
    - [ ] Handle access-lost (display reconfiguration) with retry+backoff
    - [ ] Expose `capture.Capturer` interface: `NextFrame(ctx) (*Frame, error)`
    - [ ] Frame struct: BGRA []byte, Width, Height, Timestamp
- [x] Task: Write capture integration test [693b129]
    - [ ] Test with real KMS (build tag `//go:build integration`)
    - [ ] Verify frame dimensions match CRTC
    - [ ] Verify non-zero pixel data returned
    - [ ] Verify cleanup on context cancel
- [x] Task: Conductor - User Manual Verification 'KMS Screen Capture' (Protocol in workflow.md) [693b129]

## Phase 4: Software Video Encoding [checkpoint: a0cc3c6]

- [x] Task: Implement libyuv BGRA-to-I420 conversion (cgo) [a0cc3c6]
    - [ ] cgo bindings for `ARGBToI420`
    - [ ] Pre-allocate I420 buffer (Y + U + V planes) at startup
    - [ ] Implement `Convert(bgra []byte, w, h int) *I420Frame`
    - [ ] Write unit test: feed known BGRA, verify I420 plane sizes
- [x] Task: Implement OpenH264 encoder (cgo) [a0cc3c6]
    - [ ] cgo bindings: `WelsCreateSVCEncoder`, `Initialize`, `EncodeFrame`, `Uninitialize`
    - [ ] Configure: CAMERA_VIDEO_REAL_TIME, SM_SINGLE_SLICE, QP 26, no B-frames
    - [ ] Accept I420 frame, return NAL unit slices
    - [ ] Implement IDR-on-demand (force keyframe)
    - [ ] Implement `encode.Encoder` interface: `Encode(*I420Frame) ([][]byte, error)`
- [x] Task: Write encoder unit tests [a0cc3c6]
    - [ ] Test encode produces valid NAL units (check start codes)
    - [ ] Test IDR request produces SPS+PPS+IDR
    - [ ] Test multiple frames in sequence (P-frames after IDR)
    - [ ] Verify no memory leaks (encode 1000 frames, check RSS)
- [x] Task: Conductor - User Manual Verification 'Software Video Encoding' (Protocol in workflow.md) [a0cc3c6]

## Phase 5: WebSocket Server

- [ ] Task: Implement HTTP server with embedded client
    - [ ] Set up `net/http` on configured port
    - [ ] Embed `client/` directory via `go:embed`
    - [ ] Serve index.html at `/`, compositor.js at `/compositor.js`
    - [ ] Implement `/status` endpoint (JSON: capture state, fps, client count)
- [ ] Task: Implement WebSocket upgrade and client handling
    - [ ] Upgrade `/ws` using `github.com/coder/websocket`
    - [ ] Accept connection, create client goroutine
    - [ ] Read text messages (for future input, and ping/pong)
    - [ ] Implement ping/pong for RTT measurement
    - [ ] Handle client disconnect cleanly (close, remove)
- [ ] Task: Implement video frame broadcast
    - [ ] Receive encoded NALs from encoder via channel
    - [ ] Prepend protocol header (type, timestamp, dimensions, payload size)
    - [ ] Send binary WebSocket frame to connected client
    - [ ] Send IDR frame immediately on new client connect (checkpoint)
    - [ ] Drop frames if client write buffer is full (don't block capture)
- [ ] Task: Write server unit tests
    - [ ] Test WebSocket upgrade succeeds
    - [ ] Test binary frame is well-formed (header + payload)
    - [ ] Test client disconnect doesn't panic
    - [ ] Test /status endpoint returns valid JSON
- [ ] Task: Conductor - User Manual Verification 'WebSocket Server' (Protocol in workflow.md)

## Phase 6: Web Client

- [ ] Task: Implement H.264 WebCodecs decode and canvas render
    - [ ] Create index.html with canvas element and status bar
    - [ ] Create compositor.js: WebSocket connect, binary frame parsing
    - [ ] Initialize WebCodecs VideoDecoder (avc1.42E01E, optimizeForLatency)
    - [ ] Detect keyframes from NAL type byte
    - [ ] Decode and drawImage to canvas
    - [ ] Auto-resize canvas to fit window (maintain aspect ratio)
- [ ] Task: Implement connection management and status UI
    - [ ] Auto-reconnect on WebSocket close (2s backoff)
    - [ ] Status indicator (green dot connected, red dot disconnected)
    - [ ] FPS counter (decoded frames per second)
    - [ ] Bandwidth display (bytes received per second)
    - [ ] Implement ping/pong for RTT display
- [ ] Task: Conductor - User Manual Verification 'Web Client' (Protocol in workflow.md)

## Phase 7: End-to-End Integration

- [ ] Task: Wire pipeline and verify end-to-end
    - [ ] Connect capture → encode → server broadcast pipeline in main.go
    - [ ] Verify browser sees live desktop stream
    - [ ] Verify frame rate matches target (--fps flag)
    - [ ] Profile memory: confirm <50MB RSS after 5 minutes of streaming
    - [ ] Profile latency: measure capture-to-websocket-send time
- [ ] Task: Implement graceful shutdown
    - [ ] SIGINT cancels context → capture stops → encoder drains → server closes clients
    - [ ] Verify no goroutine leaks (runtime.NumGoroutine after shutdown)
    - [ ] Verify no fd leaks (check /proc/self/fd count)
- [ ] Task: Conductor - User Manual Verification 'End-to-End Integration' (Protocol in workflow.md)
