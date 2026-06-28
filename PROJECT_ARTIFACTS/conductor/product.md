> ⚠️ **HISTORICAL — original Go prototype.** Superseded by `specs/`. The Rust
> redesign is authoritative (see `specs/CENTRAL_SPEC.md`). Retained as a
> historical artifact. The working Go reference lives on `feature-libav-vp8s8`.

# Initial Concept

A low-latency remote desktop streaming server in Go, porting the Windows FeatherDesk project to Linux with KMS capture, VA-API/OpenH264 encoding, WebSocket streaming, PipeWire audio, and uinput injection.

# FeatherDesk for Linux

## Vision

FeatherDesk is a high-performance, low-latency remote desktop streaming server for Linux. It captures the host screen, encodes it using hardware-accelerated VA-API or software OpenH264, and streams H.264 NAL units over WebSocket to connected browser clients. The system supports multi-viewer sessions with role-based access (one controller, many viewers), PipeWire audio capture, and kernel-level input injection via uinput.

The goal is to deliver Parsec/Sunshine-level latency and quality on LAN while remaining lightweight, self-contained, and deployable as a single Go binary.

## Target Users

- **Lab/team environments** with 20-25 concurrent users viewing the same session over WebSocket
- **Role model:** 1 controller (keyboard+mouse input), multiple passive viewers
- **Use cases:** Shared workstations, training sessions, VM access, remote pair programming, monitoring dashboards

## Network Environment

- **Primary:** LAN (sub-1ms network latency, no NAT traversal)
- **Optional (future):** WAN support with TLS encryption and authentication
- No STUN/TURN required for MVP

## Performance Targets

- Motion-to-photon latency: <20ms on LAN at 1080p60 (competitive with Parsec/Sunshine)
- Support up to 2560x1440 capture resolution
- Target 60fps encode/stream with hardware encoding; 30fps acceptable with software fallback
- Bandwidth: 5-15 Mbps per viewer for H.264 at good quality
- Server memory footprint: <50MB RSS (excluding ffmpeg subprocess if used)

## MVP Features

1. **Dual-mode video encoding**
   - Hardware: VA-API H.264 encode (Intel Quick Sync, zero CPU)
   - Software: libyuv BGRA-to-I420 + OpenH264 encode (fallback for CPU-only systems)
   - Runtime selectable via `--hardware` / `--software` flags

2. **Screen capture**
   - KMS/DRM direct capture (primary, lowest latency, requires CAP_SYS_ADMIN)
   - PipeWire ScreenCast portal (fallback, no special permissions)
   - EGL GPU blit for CPU-accessible pixel readback

3. **Audio streaming**
   - PipeWire monitor source capture (system audio loopback)
   - Raw PCM/Float32 over WebSocket as separate frame type
   - No audio compression for MVP (low latency on LAN)

4. **Input injection**
   - uinput virtual keyboard + absolute-position mouse
   - JSON text frames over WebSocket
   - Role-based: only the controller client can send input

5. **Web client (embedded in server)**
   - HTML5 + WebCodecs H.264 decode + Canvas rendering
   - AudioWorklet PCM playback
   - Keyboard/mouse capture and forwarding
   - Reuse and adapt existing compositor.js frontend

6. **Metrics and monitoring**
   - Real-time FPS, bandwidth, RTT display in client UI
   - Bandwidth test (bidirectional throughput measurement)
   - Server-side status endpoint (JSON API)

## Protocol

The wire protocol will be designed during implementation. Key design constraints:
- Binary WebSocket frames for video/audio (low overhead)
- Text WebSocket frames for input/control messages (JSON)
- Must support multi-client broadcast efficiently
- Header should be minimal (only what's needed for decode: type, timestamp, dimensions, payload size)
- Protocol design is a dedicated task in the implementation plan

## Architecture Principles

- Single static Go binary (cgo for capture/encode, pure Go for networking/protocol)
- Pluggable capture and encode interfaces for runtime selection
- Pre-allocated buffers, zero per-frame allocations in hot path
- Goroutines + channels for pipeline stages (capture -> encode -> broadcast)
- go:embed for static frontend assets

## Out of Scope (for MVP)

- H.265/HEVC encoding (hardware doesn't support encode on Kaby Lake)
- WAN/internet deployment (TLS, authentication, NAT traversal)
- Native client application
- Multi-monitor capture
- Clipboard sharing
- File transfer
- Audio compression (Opus/AAC)
