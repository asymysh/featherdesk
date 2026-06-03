# Tech Stack - ViewPort RDS

## Language

- **Go 1.26+** (primary language)
- **cgo** for native library bindings (capture, encode)
- **C** headers for DRM/EGL/VA-API/libyuv/OpenH264 interop

## Core Libraries (cgo bindings)

| Library | Purpose | Package |
| --- | --- | --- |
| libdrm | KMS/DRM framebuffer access, plane enumeration | `capture/kms` |
| libgbm | GBM device for EGL context creation | `capture/kms` |
| EGL (libEGL) | Surfaceless GPU context, DMA-BUF import | `capture/kms` |
| OpenGL ES / GL | GPU texture blit, pixel readback | `capture/kms` |
| libva | VA-API hardware H.264 encoding | `encode/vaapi` |
| libyuv (Google) | BGRA to I420 color space conversion (SIMD) | `encode/software` |
| OpenH264 (Cisco) | Software H.264 encoding | `encode/software` |
| libpipewire-0.3 | Audio monitor capture, optional screen capture | `audio`, `capture/pipewire` |

## Go Libraries

| Library | Purpose |
| --- | --- |
| `github.com/coder/websocket` | WebSocket server (binary + text frames, modern API) |
| `net/http` (stdlib) | HTTP server, static file serving, mode selection |
| `embed` (stdlib) | Embed HTML/JS client assets into binary |
| `flag` (stdlib) | CLI argument parsing |
| `sync` (stdlib) | Mutex for client broadcast, sync.Pool |
| `os/exec` (stdlib) | ffmpeg subprocess management (VA-API pipe mode) |

## System Dependencies

| Package | Ubuntu/Debian apt name | Purpose |
| --- | --- | --- |
| libdrm | `libdrm-dev` | DRM/KMS headers |
| libgbm | `libgbm-dev` | GBM device creation |
| EGL | `libegl-dev` | EGL context |
| OpenGL | `libgl-dev` | GL functions |
| VA-API | `libva-dev` | Hardware encode |
| libyuv | `libyuv-dev` | Color conversion |
| OpenH264 | `libopenh264-dev` | Software encode |
| PipeWire | `libpipewire-0.3-dev` | Audio capture |
| libcap | `libcap-dev` | CAP_SYS_ADMIN management |

## Runtime Dependencies

- Intel VA-API driver: `intel-media-va-driver` (iHD)
- PipeWire daemon (running, for audio capture)
- ffmpeg binary (optional, for VA-API pipe mode)

## Build Tools

- Go 1.26+ with cgo enabled (`CGO_ENABLED=1`)
- gcc/g++ (for cgo compilation)
- pkg-config (for library discovery)
- Make (build orchestration)

## Frontend (Embedded Web Client)

- Vanilla JavaScript (no framework, no build step)
- WebCodecs API for H.264 decoding
- AudioWorklet for PCM playback
- Canvas 2D for rendering
- go:embed to bundle into binary

## Testing

- `testing` (stdlib) for unit tests
- Integration tests via actual capture/encode on CI with virtual display (Xvfb)
- Manual latency measurement via input ACK round-trip

## Deployment

- Single static binary (with cgo, dynamically links system libs)
- systemd service unit
- `setcap cap_sys_admin+p` for KMS capture permissions
