# Product Guidelines - ViewPort RDS

## Naming & Identity

- **Project name:** ViewPort RDS
- **Binary name:** `viewport-rds`
- **CLI prefix:** `viewport-rds [flags]`
- **Log prefix:** `[viewport-rds]`
- **Service name:** `viewport-rds.service` (systemd)

## Code Style

- **Style:** Readable, idiomatic Go
- Descriptive variable and function names (no single-letter names outside tight loops)
- Doc comments on all exported types, functions, and interfaces
- Clear separation of concerns via Go interfaces
- Package-per-domain: `capture`, `encode`, `audio`, `input`, `server`, `protocol`
- Follow `gofmt` and `go vet` unconditionally
- Use `golangci-lint` with default rules

## Error Handling

- **Philosophy:** Resilient, auto-recovering. The server should never crash during streaming.
- Lost frames: log warning, skip, continue
- Audio glitches: log, fill silence, continue
- Capture failure (DXGI access lost equivalent): log error, wait, retry acquisition in loop
- Encoder failure: log error, attempt restart of encode pipeline
- WebSocket client disconnect: clean up, continue serving other clients
- Only fatal on startup failures (no GPU, no display, port in use)

## Logging

- **Format:** Human-readable timestamped text lines
- **Pattern:** `2026-06-04 12:30:45.123 [module] message`
- **Levels:** DEBUG, INFO, WARN, ERROR, FATAL
- **Default level:** INFO (configurable via `--verbose` / `--quiet`)
- Log to stderr by default, optionally to file via `--log-file`
- No structured JSON for MVP (can add later)

## UX Principles (Web Client)

- Dark theme by default (matches original project aesthetic)
- Minimal chrome: status bar + canvas, nothing else visible during streaming
- Status information (FPS, bandwidth, RTT) visible but non-intrusive
- Controls accessible but not in the way of the stream
- Keyboard/mouse capture should feel invisible when active
- Mobile-responsive layout for tablet viewers

## Performance Principles

- Zero allocations in the hot path (capture -> encode -> broadcast)
- Pre-allocate all frame buffers at startup
- Use sync.Pool only where unavoidable (per-client send buffers)
- Measure before optimizing: build with pprof endpoints enabled
- Profile memory and CPU under load before declaring MVP complete

## Deployment

- Single binary, no external config files required (all via CLI flags)
- Optional systemd unit file provided
- `CAP_SYS_ADMIN` applied via setcap for KMS capture
- No Docker requirement, but Dockerfile provided for convenience
- Dependencies (libyuv, openh264, libva, libdrm, libpipewire) linked at build time
