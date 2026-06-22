# FeatherDesk

A low-latency remote desktop streaming server. Captures the host screen, encodes to H.264 or VP8, and streams over WebSocket to browser clients via WebCodecs. Single Go binary with an embedded web viewer.

> **Platform:** Linux only (current). Windows and macOS planned.

---

## Quick Start

```bash
# Software encoding (default)
./viewport-rds --port 30084

# Hardware encoding (Intel VA-API)
./viewport-rds --hardware --port 30084

# No audio
./viewport-rds --no-audio

# Verbose logging
./viewport-rds --verbose
```

Open `https://<host>:30084` in Chrome/Edge 94+. Accept the self-signed certificate.  
Append `?role=control` to take keyboard and mouse control.

---

## Compatibility

### Linux

#### Screen Capture

| Backend | Status | Notes |
|---------|--------|-------|
| KMS/DRM + EGL | ✅ Working | Primary path. Kernel-level capture via `/dev/dri/`. Requires root or `CAP_SYS_ADMIN`. Tested at 2560×1440 @ 165Hz on Intel HD 630. |
| X11grab (ffmpeg) | ✅ Working | Fallback. Spawns `ffmpeg -f x11grab` subprocess. No special permissions needed. |
| PipeWire ScreenCast | ✅ Working | Wayland/GNOME fallback via Mutter D-Bus + GStreamer pipeline. |

#### Video Encoding

| Encoder | Status | Notes |
|---------|--------|-------|
| Software — VP8 | ✅ Working | **Active default.** CGo bindings to `libvpx` via `libavcodec`. Realtime, single-thread. ~54 KB/frame at 1440p. |
| Software — H.264 | ✅ Working | OpenH264 (Cisco) CGo bindings. Available, superseded by VP8 as default. |
| Intel — VAAPI | ✅ Working | `h264_vaapi` via ffmpeg pipe. Auto-detected at startup; use `--hardware` to force. Tested on Intel HD 630 (Kaby Lake). |
| AMD — VAAPI / AMF | ❌ Not implemented | AMD GPUs expose VA-API via Mesa drivers; untested. AMF path not built. |
| NVIDIA — NVENC | ❌ Not implemented | — |

#### Features

| Feature | Status | Notes |
|---------|--------|-------|
| Audio streaming | ✅ Working | PipeWire monitor source → S16LE PCM → AudioWorklet. `--no-audio` to disable. |
| Keyboard + mouse | ✅ Working | uinput virtual device. `/dev/uinput` required. Gracefully disabled if unavailable. |
| Multi-client | ✅ Working | Up to 25 concurrent WebSocket clients. One controller, rest viewers. |
| Browser client | ✅ Working | WebCodecs VP8 decode, Canvas 2D render, AudioWorklet PCM playback. Chrome/Edge 94+ required. |
| Auto TLS | ✅ Working | Self-signed ECDSA P-256 cert generated at startup. Required for WebCodecs secure context. |
| Status endpoint | ✅ Working | `GET /status` — JSON: clients, encoder, fps, bytes broadcast. |

---

### Windows

| Component | Status |
|-----------|--------|
| DXGI Desktop Duplication capture | ❌ Planned |
| GDI capture | ❌ Planned |
| Software encoding (x264 / OpenH264) | ❌ Planned |
| Intel Quick Sync (D3D11 / MFX) | ❌ Planned |
| AMD AMF | ❌ Planned |
| NVIDIA NVENC | ❌ Planned |
| WASAPI audio | ❌ Planned |
| uinput equivalent (SendInput / virtual HID) | ❌ Planned |

---

### macOS

| Component | Status |
|-----------|--------|
| ScreenCaptureKit capture | ❌ Planned |
| AVFoundation capture (fallback) | ❌ Planned |
| Software encoding (VideoToolbox / x264) | ❌ Planned |
| VideoToolbox hardware H.264 | ❌ Planned |
| CoreAudio capture | ❌ Planned |

---

## Build

### Prerequisites (Linux)

```bash
sudo apt install \
  libdrm-dev libgbm-dev libegl-dev libgl-dev \
  libyuv-dev libopenh264-dev \
  libavcodec-dev libavutil-dev \
  golang-go gcc pkg-config
```

Runtime (optional):
```bash
# Intel VA-API hardware encoding
sudo apt install intel-media-va-driver ffmpeg

# PipeWire audio
# (usually already running on modern distros)
```

### Build & Run

```bash
make build
sudo ./viewport-rds          # KMS capture requires root
# OR
./viewport-rds               # X11/PipeWire capture, no root needed
```

### Permissions

KMS capture requires DRM master access:
```bash
# Option 1: run as root
sudo ./viewport-rds

# Option 2: grant capability
sudo setcap cap_sys_admin+p ./viewport-rds
```

uinput injection requires:
```bash
sudo usermod -aG input $USER   # then re-login
# OR
sudo chmod a+rw /dev/uinput
```

---

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `30084` | HTTPS listen port |
| `--bind` | `0.0.0.0` | Listen address |
| `--fps` | `30` | Target capture framerate |
| `--hardware` | — | Force Intel VA-API encoder (fatal if unavailable) |
| `--software` | — | Force software encoder (VP8) |
| `--no-audio` | — | Disable PipeWire audio capture |
| `--verbose` | — | DEBUG level logging |
| `--quiet` | — | ERROR level only |
| `--log-file` | stderr | Write logs to file |

---

## Architecture

```
Screen → [Capture] → RGBA frames → [Convert] → I420 → [Encode] → NALs → [Server] → Browser
                                                                              ↑
Audio → [PipeWire] → PCM chunks ──────────────────────────────────────────────┘
Browser input events (JSON) → WebSocket → [uinput] → Kernel
```

Refactor spec (modular redesign): [`featherdesk-refactor` branch](https://github.com/asymysh/featherdesk/tree/featherdesk-refactor/specs)

---

## Tested Hardware

| Component | Device |
|-----------|--------|
| CPU | Intel Core i7-7700K (Kaby Lake) |
| GPU / encoder | Intel HD Graphics 630 (VA-API Quick Sync) |
| Display | 2560×1440 @ 165Hz |
| OS | Linux (KMS/DRM, Mesa EGL) |

---

## License

See [LICENSE](LICENSE).
