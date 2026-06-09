# 🪶 FeatherDesk

**Lightweight Remote Desktop Streaming — Fast as a Feather, Sharp as a Blade.**

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Linux-FCC624?logo=linux)](https://kernel.org)

> *Stream your entire Linux desktop to any modern browser. No client install. No Java. No Flash. Just open a URL.*

---

## What is FeatherDesk?

FeatherDesk is a **sub-40ms latency** remote desktop server for Linux, built from scratch in Go with zero-copy capture pipelines. It captures your screen via the Wayland compositor's native ScreenCast API, encodes with hardware-accelerated VA-API or fast software codecs, and streams over WebSocket to any browser that supports WebCodecs.

Think **Parsec-level responsiveness** in a single self-contained binary under 50MB RSS.

```
┌─────────────┐    ┌──────────┐    ┌───────────┐    ┌──────────────┐
│  PipeWire   │───▶│  libyuv  │───▶│  Encoder  │───▶│  WebSocket   │
│ ScreenCast  │    │ RGBA→I420│    │ VA-API/x264│    │  Broadcast   │
└─────────────┘    └──────────┘    └───────────┘    └──────┬───────┘
                                                           │
┌─────────────┐    ┌──────────┐                    ┌───────▼───────┐
│  PipeWire   │───▶│  PCM     │───────────────────▶│    Browser    │
│   Audio     │    │ S16LE 48k│                    │  WebCodecs +  │
└─────────────┘    └──────────┘                    │  AudioWorklet │
                                                   └───────▲───────┘
┌─────────────┐                                            │
│   uinput    │◀───── JSON input events ───────────────────┘
│ kbd + mouse │
└─────────────┘
```

---

## Features

| Feature | Status |
|---------|--------|
| 🖥️ Full desktop capture (Wayland + X11) | ✅ |
| 🎮 Hardware VA-API H.264 encoding (6ms P80) | ✅ |
| 💻 Software x264/OpenH264 encoding | ✅ |
| 🌐 Zero-install browser client (WebCodecs) | ✅ |
| ⌨️ Keyboard + mouse + scroll injection | ✅ |
| 🔊 System audio streaming (PipeWire) | ✅ |
| 👥 Multi-viewer (25 clients, role-based) | ✅ |
| 🔒 HTTPS with auto-generated TLS certs | ✅ |
| 📊 Real-time stats (FPS, bandwidth, latency) | ✅ |
| 🚀 Single binary, <50MB RSS | ✅ |

---

## Quick Start

### Prerequisites

```bash
# Ubuntu 24.04+ / Fedora 40+
sudo apt install libyuv-dev libopenh264-dev libx264-dev \
  libavcodec-dev libavutil-dev libswscale-dev libva-dev \
  pipewire gstreamer1.0-pipewire python3-gi gir1.2-gst-plugins-base-1.0
```

### Build & Run

```bash
git clone https://github.com/asymysh/featherdesk.git
cd featherdesk
go build -o featherdesk ./cmd/server/

# Hardware encoding (VA-API, recommended)
./featherdesk --port 30084 --bind 0.0.0.0 --hardware --verbose

# Software encoding (x264 ultrafast)
./featherdesk --port 30084 --bind 0.0.0.0 --software --verbose
```

Open **https://your-ip:30084** in Chrome/Edge. Accept the self-signed cert. Done.

### Control Mode

Append `?role=control` to the URL to enable keyboard and mouse input:
```
https://192.168.0.199:30084/?role=control
```

---

## Architecture

```
featherdesk/
├── cmd/server/          # Main binary + embedded web client
│   ├── main.go          # Pipeline orchestration, CLI flags
│   └── client/          # HTML/JS viewer (go:embed)
├── internal/
│   ├── capture/         # PipeWire ScreenCast + GStreamer pipe
│   ├── encode/          # OpenH264, x264, VA-API (libavcodec cgo)
│   ├── audio/           # PipeWire pw-cat monitor capture
│   ├── input/           # uinput virtual device injection
│   ├── server/          # HTTP/WebSocket, broadcast, roles
│   ├── protocol/        # 17-byte binary frame header
│   └── logger/          # Leveled structured logger
└── cmd/bench/           # Encoder benchmark suite (59 configs)
```

---

## Encoder Capabilities

### Current Hardware Encoders

| Encoder | GPU | Profile | Chroma | Latency (1440p) | Notes |
|---------|-----|---------|--------|-----------------|-------|
| VA-API H.264 (libavcodec) | Intel HD 630+ | High | 4:2:0 NV12 | **6.0ms P80** | Low-power mode, zero-copy surfaces |
| VA-API H.264 (ffmpeg pipe) | Intel HD 630+ | High | 4:2:0 NV12 | 7.9ms P80 | Fallback, +3ms pipe overhead |

### Current Software Encoders

| Encoder | Preset | Threads | Latency (1440p) | NAL Size | Notes |
|---------|--------|---------|-----------------|----------|-------|
| x264 (cgo) | ultrafast | 4 | **13.7ms P80** | 586 KB | Fastest software, zerolatency tune |
| x264 (cgo) | superfast | 4 | 24.7ms P80 | 607 KB | Better compression |
| libavcodec VP8 | speed 8 | 4 | 22.5ms P80 | 54 KB | 10x smaller frames |
| OpenH264 | QP 26 | 1 | 55.7ms P80 | 589 KB | Baseline only, no threading |

### Benchmarked but Rejected (1440p realtime infeasible)

| Encoder | Best P80 | Why Rejected |
|---------|----------|--------------|
| x265 (HEVC) | 110ms+ | Too slow even at ultrafast |
| SVT-AV1 | 67ms+ | Pipeline-buffered, not frame-at-a-time |
| libaom (AV1) | 181ms+ | Designed for offline encoding |
| VP9 | 70ms+ | No realtime preset competitive with VP8 |

---

## Future Roadmap

### Planned Encoder Support

| Encoder | Target GPU | Status |
|---------|-----------|--------|
| NVENC H.264/H.265 | NVIDIA GTX 1050+ | 🔲 Planned |
| NVENC AV1 | NVIDIA RTX 4000+ | 🔲 Planned |
| AMF H.264/H.265 | AMD RX 500+ | 🔲 Planned |
| AMF AV1 | AMD RX 7000+ | 🔲 Planned |
| VA-API H.265 | Intel Gen 9+ | 🔲 Planned |
| VA-API AV1 | Intel Arc | 🔲 Planned |
| Custom encoder plugin API | Any | 🔲 Planned |

### Planned Chroma Subsampling Modes

| Mode | Use Case | Encoder Requirement |
|------|----------|-------------------|
| 4:2:0 (current) | General desktop, video | All hardware encoders |
| 4:2:2 | Text-heavy, coding | Software x264 High 4:2:2 |
| 4:4:4 | Pixel-perfect, design | Software x264 High 4:4:4 Predictive |
| Mode selector (`--chroma`) | Per-session toggle | Auto-fallback to software |

> **Note:** 4:4:4 requires software encoding because no current consumer GPU hardware supports 4:4:4 subsampling in H.264 encode. Browser WebCodecs also doesn't support High 4:4:4 Predictive profile — this will require a custom WASM decoder or VP8/AV1 fallback.

### Other Planned Features

- 🖱️ Relative mouse mode (FPS games)
- 📋 Clipboard sync (text + images)
- 🗂️ File transfer drag-and-drop
- 🖼️ Adaptive quality (auto bitrate/resolution)
- 🔐 Authentication (password, OAuth2)
- 📱 Touch input support (mobile viewers)
- 🐳 Docker image with GPU passthrough
- 📦 systemd service unit

---

## CLI Flags

```
Usage: featherdesk [flags]

Flags:
  --port int        HTTP/HTTPS port (default 30084)
  --bind string     Bind address (default "127.0.0.1")
  --fps int         Target framerate (default 30)
  --hardware        Force VA-API hardware encoding
  --software        Force software encoding
  --no-audio        Disable audio capture
  --no-auth         Disable authentication
  --verbose         Enable debug logging
  --quiet           Errors only
  --log-file path   Log to file instead of stderr
```

---

## Performance

Measured on **Intel i3-7100** (2C/4T, HD 630 iGPU), 2560x1440@165Hz, GNOME Wayland:

| Metric | Hardware Mode | Software Mode |
|--------|-------------|---------------|
| Capture | 16ms | 16ms |
| Color Convert | 3.6ms | 3.0ms |
| Encode | 6.0ms | 13.7ms |
| **Total pipeline** | **~26ms** | **~33ms** |
| Bandwidth | 1.5-2.5 Mbps | 1.5-3.0 Mbps |
| CPU Usage | ~15% | ~45% |
| Memory (RSS) | <40MB | <40MB |

---

## How It Works

1. **Capture**: Connects to GNOME Mutter's ScreenCast D-Bus API, negotiates a PipeWire stream, and receives compositor frames as RGBA via GStreamer.

2. **Encode**: Converts RGBA→I420 via libyuv (1ms, zero-alloc), then encodes to H.264 NAL units using either VA-API hardware (6ms) or x264 software (14ms).

3. **Stream**: NAL units are framed with a 17-byte binary header (type, timestamp, dimensions, size) and broadcast over WebSocket to all connected clients.

4. **Decode**: Browser receives binary WebSocket messages, parses headers, feeds NAL data to WebCodecs VideoDecoder, and renders to canvas.

5. **Input**: Browser captures keyboard/mouse events, sends JSON over the same WebSocket. Server injects via Linux uinput virtual device.

6. **Audio**: PipeWire monitor source captured via `pw-cat`, raw S16LE 48kHz stereo sent as binary frames, played via AudioWorklet ring buffer.

---

## Browser Compatibility

| Browser | Video | Audio | Input | Notes |
|---------|-------|-------|-------|-------|
| Chrome 94+ | ✅ | ✅ | ✅ | Full support |
| Edge 94+ | ✅ | ✅ | ✅ | Full support |
| Firefox | ❌ | ❌ | ❌ | No WebCodecs yet |
| Safari 16.4+ | ⚠️ | ⚠️ | ✅ | WebCodecs partial |

> Requires **HTTPS** (secure context) for WebCodecs API access.

---

## Contributing

Contributions welcome. Areas of interest:

- NVENC/AMF encoder backends
- Pure WebRTC transport option
- Adaptive bitrate control
- Wayland input protocol (bypassing X11/uinput)
- Firefox WebCodecs polyfill

---

## License

MIT License. See [LICENSE](LICENSE).

---

<p align="center">
  <em>Built with obsessive attention to latency.<br>Every millisecond matters when you're 1000 miles from your desktop.</em>
</p>
