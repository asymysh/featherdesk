# Specification: MVP Streaming Server

## Overview

Build a complete end-to-end remote desktop streaming server in Go for Linux. The server captures the screen via KMS/DRM, encodes to H.264 using either VA-API (hardware) or libyuv+OpenH264 (software), streams NAL units over WebSocket to browser clients, captures system audio via PipeWire, and injects keyboard/mouse input via uinput.

## Functional Requirements

### FR-1: Screen Capture (KMS/DRM)
- Open DRM card device with CAP_SYS_ADMIN
- Enumerate planes, find primary plane for target CRTC
- Export scanout framebuffer as DMA-BUF fd via drmPrimeHandleToFD
- For hardware encode: pass DMA-BUF fd directly to VA-API
- For software encode: import DMA-BUF into EGL, blit via GL, readback BGRA pixels
- Detect frame changes by polling plane fb_id
- Handle display reconfiguration (resolution change, DPMS)
- Target: 60fps capture at 2560x1440

### FR-2: Hardware Encoding (VA-API)
- Initialize VA-API display from render device (/dev/dri/renderD128)
- Create H.264 encode context (High profile, EncSliceLP entrypoint)
- Accept DMA-BUF input or NV12 surfaces
- Configure: CQP mode (qp=20), no B-frames, IDR interval configurable
- Output raw H.264 NAL units (no container)
- Alternative: ffmpeg subprocess pipe with h264_vaapi

### FR-3: Software Encoding (libyuv + OpenH264)
- Receive BGRA pixel buffer from EGL readback
- Convert BGRA to I420 using libyuv ARGBToI420 (SIMD-optimized)
- Encode I420 frame using OpenH264 ISVCEncoder
- Configure: CAMERA_VIDEO_REAL_TIME usage, SM_SINGLE_SLICE, target QP or bitrate
- Output raw H.264 NAL units

### FR-4: Encoder Selection
- `--hardware` flag: use VA-API (default when GPU supports encode)
- `--software` flag: use libyuv + OpenH264
- Auto-detect: probe VA-API at startup, fall back to software if unavailable

### FR-5: WebSocket Server
- HTTP server on configurable port (default 30084)
- Serve embedded web client at `/`
- WebSocket upgrade at `/ws?role=control` or `/ws?role=view`
- Binary frames: video NALs and audio PCM with lightweight header
- Text frames: JSON input messages from clients
- Support up to 25 concurrent clients
- Broadcast video/audio to all clients under mutex
- Role-based: only controller can send input

### FR-6: Wire Protocol
- Design minimal binary header for video/audio frames
- Must include: frame type, timestamp, width, height, payload size
- Must be decodable by browser WebCodecs
- Text protocol for input: JSON with type, event, coordinates/keycodes

### FR-7: Audio Capture (PipeWire)
- Connect to PipeWire as stream consumer
- Capture monitor source of default audio sink (system audio loopback)
- Receive raw PCM Float32 samples (48kHz stereo)
- Wrap in protocol frame and broadcast to clients
- Handle PipeWire disconnection gracefully

### FR-8: Input Injection (uinput)
- Create virtual keyboard device via /dev/uinput
- Create virtual mouse device with absolute positioning
- Parse JSON input messages from controller client
- Map browser key codes to Linux input event codes
- Inject key down/up, mouse move (absolute), button down/up, wheel events
- Only accept input from the designated controller client

### FR-9: Web Client (Embedded)
- HTML5 page with Canvas rendering
- WebCodecs VideoDecoder for H.264 (avc1.42E01E)
- AudioWorklet for PCM playback
- Keyboard and mouse capture with JSON forwarding
- Status bar: FPS, bandwidth, RTT, input latency
- Fullscreen support with keyboard lock
- Auto-reconnect on disconnect
- Embedded into Go binary via go:embed

### FR-10: Metrics and Monitoring
- FPS counter (client-side, frames decoded per second)
- Bandwidth measurement (bytes/sec per client)
- RTT measurement (ping/pong round-trip)
- Input latency measurement (input -> ACK round-trip)
- Server status JSON endpoint at /status

## Non-Functional Requirements

- Memory: <50MB RSS for server process
- Latency: <20ms motion-to-photon on LAN at 1080p60
- CPU (hardware mode): <5% during streaming
- CPU (software mode): <80% at 1080p30
- Startup time: <2 seconds to first frame
- No external config files required (all via CLI flags)
- Graceful degradation: lost frames logged, never crash

## Acceptance Criteria

1. Server starts, captures screen via KMS, encodes H.264, streams to browser
2. Browser client connects, decodes H.264 via WebCodecs, renders to canvas
3. Keyboard/mouse input from browser is injected into host via uinput
4. Audio from host plays in browser via AudioWorklet
5. 25 viewers can connect simultaneously with acceptable performance
6. `--hardware` uses VA-API with near-zero CPU
7. `--software` uses OpenH264 as fallback
8. FPS/bandwidth/RTT metrics displayed in client UI

## Out of Scope

- H.265/HEVC encoding
- WAN/internet deployment (TLS, auth, NAT traversal)
- Native client application
- Multi-monitor capture
- Clipboard/file transfer
- Audio compression (Opus/AAC)
- Cursor plane capture (can be added later)
