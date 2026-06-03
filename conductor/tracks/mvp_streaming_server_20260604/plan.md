# Implementation Plan: MVP Streaming Server

## Phase 1: Project Foundation & Protocol Design

- [ ] Task: Initialize Go module, directory structure, and Makefile
    - [ ] Create `go.mod` with module path `github.com/aseem/viewport-rds`
    - [ ] Create package directories: `cmd/server`, `internal/capture`, `internal/encode`, `internal/audio`, `internal/input`, `internal/server`, `internal/protocol`, `client/`
    - [ ] Create Makefile with build, test, lint, fmt targets
    - [ ] Create `.gitignore` for Go project
- [ ] Task: Design and implement wire protocol
    - [ ] Define binary frame header struct (minimal: type, timestamp, width, height, payload_size)
    - [ ] Define frame type constants (video_h264, audio_pcm, input_ack, bandwidth_test)
    - [ ] Implement header marshal/unmarshal with binary.LittleEndian
    - [ ] Write unit tests for protocol encode/decode round-trip
- [ ] Task: Implement CLI argument parsing and main entrypoint
    - [ ] Parse flags: `--port`, `--hardware`, `--software`, `--verbose`, `--quiet`, `--log-file`, `--capture` (kms/pipewire)
    - [ ] Implement logging module (timestamped, leveled, human-readable)
    - [ ] Wire up signal handling (SIGINT, SIGTERM) for graceful shutdown
    - [ ] Create main server startup orchestration in `cmd/server/main.go`
- [ ] Task: Conductor - User Manual Verification 'Project Foundation & Protocol Design' (Protocol in workflow.md)

## Phase 2: KMS Screen Capture

- [ ] Task: Implement DRM card discovery and plane enumeration
    - [ ] Open `/dev/dri/card*` with CAP_SYS_ADMIN via cgo
    - [ ] Call `drmSetClientCap(DRM_CLIENT_CAP_UNIVERSAL_PLANES)`
    - [ ] Enumerate planes, identify primary plane with active fb_id
    - [ ] Get CRTC info (resolution, refresh rate)
    - [ ] Export framebuffer handle as DMA-BUF fd via `drmPrimeHandleToFD`
- [ ] Task: Implement EGL surfaceless context for GPU blit (software path)
    - [ ] Create GBM device from card fd
    - [ ] Initialize EGL display from GBM device (EGL_PLATFORM_GBM_KHR)
    - [ ] Create surfaceless EGL context (no window needed)
    - [ ] Import DMA-BUF as EGLImage (EGL_LINUX_DMA_BUF_EXT with format/modifier)
    - [ ] Bind EGLImage to GL texture via glEGLImageTargetTexture2DOES
- [ ] Task: Implement pixel readback and frame loop
    - [ ] Use glGetTextureSubImage to read BGRA pixels into pre-allocated buffer
    - [ ] Implement frame change detection (poll fb_id, skip unchanged frames)
    - [ ] Implement frame pacing (target fps with sleep)
    - [ ] Handle DXGI-equivalent access-lost (display reconfiguration) with retry loop
    - [ ] Expose capture.Capturer interface: `NextFrame() ([]byte, FrameInfo, error)`
- [ ] Task: Conductor - User Manual Verification 'KMS Screen Capture' (Protocol in workflow.md)

## Phase 3: Video Encoding

- [ ] Task: Implement software encoder (libyuv + OpenH264)
    - [ ] cgo bindings for libyuv `ARGBToI420` with pre-allocated I420 buffer
    - [ ] cgo bindings for OpenH264 `ISVCEncoder` (create, initialize, encode, destroy)
    - [ ] Configure encoder: CAMERA_VIDEO_REAL_TIME, SM_SINGLE_SLICE, target QP
    - [ ] Implement `encode.Encoder` interface: `Encode(bgra []byte, info FrameInfo) ([][]byte, error)` returning NAL units
    - [ ] Write unit tests with synthetic BGRA frames
- [ ] Task: Implement hardware encoder (VA-API via ffmpeg pipe)
    - [ ] Spawn ffmpeg subprocess with VA-API encode pipeline
    - [ ] Pipe raw BGRA frames to ffmpeg stdin
    - [ ] Read H.264 NAL units from ffmpeg stdout
    - [ ] Parse NAL start codes (00 00 00 01) to split frames
    - [ ] Implement same `encode.Encoder` interface
    - [ ] Handle ffmpeg crash/restart gracefully
- [ ] Task: Implement encoder selection and auto-detection
    - [ ] Probe VA-API at startup: open render device, query H264 encode entrypoint
    - [ ] If `--hardware` and VA-API available: use hardware encoder
    - [ ] If `--software` or VA-API unavailable: use software encoder
    - [ ] Log selected encoder at startup
- [ ] Task: Conductor - User Manual Verification 'Video Encoding' (Protocol in workflow.md)

## Phase 4: WebSocket Server & Broadcast

- [ ] Task: Implement HTTP server with embedded static files
    - [ ] Set up `net/http` server on configured port
    - [ ] Embed `client/` directory via `go:embed`
    - [ ] Serve index.html at `/`, compositor.js at `/compositor.js`
    - [ ] Implement `/status` JSON endpoint (capture method, codec, fps, clients)
- [ ] Task: Implement WebSocket client management
    - [ ] Upgrade `/ws` to WebSocket using `github.com/coder/websocket`
    - [ ] Parse `role` query param (control/view)
    - [ ] Maintain client list with sync.RWMutex (max 25)
    - [ ] Handle client connect/disconnect lifecycle
    - [ ] Track controller client (only one allowed input)
- [ ] Task: Implement binary frame broadcast
    - [ ] Prepend protocol header to encoded NAL units
    - [ ] Broadcast binary frames to all connected clients
    - [ ] Handle slow clients (drop frames rather than block)
    - [ ] Implement checkpoint: send IDR frame on new client connect
- [ ] Task: Implement text message handling (input, ping/pong, control)
    - [ ] Parse JSON text frames from clients
    - [ ] Route by type: key, mouse, ping, checkpoint, bandwidth_test
    - [ ] Implement ping/pong RTT measurement
    - [ ] Implement input ACK for latency measurement
- [ ] Task: Conductor - User Manual Verification 'WebSocket Server & Broadcast' (Protocol in workflow.md)

## Phase 5: Audio Capture

- [ ] Task: Implement PipeWire audio monitor capture
    - [ ] cgo bindings for libpipewire-0.3 (pw_main_loop, pw_stream)
    - [ ] Connect as stream consumer to default sink monitor
    - [ ] Receive Float32 PCM buffers (48kHz, stereo)
    - [ ] Wrap in protocol frame (type=audio_pcm, sample_rate, channels, bits)
    - [ ] Broadcast audio frames to all clients
- [ ] Task: Handle PipeWire lifecycle and errors
    - [ ] Handle PipeWire daemon disconnect/reconnect
    - [ ] Handle audio device changes (sink switch)
    - [ ] Implement `--no-audio` flag to disable audio capture
    - [ ] Fill silence on buffer underrun rather than crash
- [ ] Task: Conductor - User Manual Verification 'Audio Capture' (Protocol in workflow.md)

## Phase 6: Input Injection

- [ ] Task: Implement uinput virtual device creation
    - [ ] Open `/dev/uinput` and configure virtual keyboard (all KEY_* codes)
    - [ ] Configure virtual mouse with absolute positioning (ABS_X, ABS_Y, BTN_LEFT/RIGHT/MIDDLE, REL_WHEEL)
    - [ ] Set device resolution to match capture dimensions
    - [ ] Create devices via UI_DEV_CREATE ioctl
    - [ ] Implement cleanup (UI_DEV_DESTROY) on shutdown
- [ ] Task: Implement input event translation and injection
    - [ ] Map browser event.code strings to Linux KEY_* constants
    - [ ] Translate mouse coordinates (client viewport -> absolute uinput coords)
    - [ ] Inject key down/up events with SYN_REPORT
    - [ ] Inject mouse move, button, and wheel events
    - [ ] Only accept input from the designated controller client
    - [ ] Send input ACK back to client for latency measurement
- [ ] Task: Conductor - User Manual Verification 'Input Injection' (Protocol in workflow.md)

## Phase 7: Web Client

- [ ] Task: Implement H.264 decode and canvas rendering
    - [ ] Set up WebCodecs VideoDecoder with avc1.42E01E codec string
    - [ ] Parse binary frames: extract header, detect keyframes from NAL type
    - [ ] Decode H.264 NALs and draw to canvas via drawImage
    - [ ] Implement auto-resize (fit canvas to window while preserving aspect ratio)
- [ ] Task: Implement audio playback
    - [ ] Create AudioContext and AudioWorklet processor
    - [ ] Receive PCM frames, convert to Float32 planar
    - [ ] Queue samples and play through AudioWorklet process() callback
    - [ ] Handle audio enable/disable button (user gesture required)
- [ ] Task: Implement input capture and forwarding
    - [ ] Capture keyboard events (keydown/keyup) and prevent defaults
    - [ ] Capture mouse events (move, down, up, wheel) with coordinate mapping
    - [ ] Send JSON text frames over WebSocket
    - [ ] Implement pressed-key tracking and release-all on blur/visibility change
    - [ ] Implement fullscreen with keyboard lock (Escape, F11)
- [ ] Task: Implement metrics UI
    - [ ] FPS counter (decoded frames per second)
    - [ ] Bandwidth display (bytes received per second)
    - [ ] RTT display (ping/pong measurement)
    - [ ] Input latency display (input ACK round-trip)
    - [ ] Bandwidth test button (bidirectional throughput)
    - [ ] Status button (fetch /status endpoint)
- [ ] Task: Conductor - User Manual Verification 'Web Client' (Protocol in workflow.md)

## Phase 8: Integration & Polish

- [ ] Task: End-to-end integration testing
    - [ ] Verify full pipeline: KMS capture -> encode -> WebSocket -> browser decode
    - [ ] Test 25 simultaneous viewer connections
    - [ ] Test controller + viewer role enforcement
    - [ ] Test hardware/software encoder switching
    - [ ] Verify memory stays under 50MB RSS during sustained streaming
- [ ] Task: Implement graceful shutdown and error recovery
    - [ ] SIGINT/SIGTERM stops capture, drains clients, closes cleanly
    - [ ] Encoder crash triggers automatic restart without dropping other clients
    - [ ] Capture loss (display off/reconfigured) triggers retry loop with backoff
    - [ ] PipeWire disconnect triggers reconnection attempt
- [ ] Task: Create systemd service unit and deployment docs
    - [ ] Write `viewport-rds.service` systemd unit file
    - [ ] Document setcap for CAP_SYS_ADMIN
    - [ ] Document required system packages
    - [ ] Write minimal README.md with build/run instructions
- [ ] Task: Conductor - User Manual Verification 'Integration & Polish' (Protocol in workflow.md)
