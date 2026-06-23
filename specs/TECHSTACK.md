# Tech Stack — Confirmed Decisions

> **Single source of truth for technology choices.** Every decision in this
> document is locked unless explicitly revisited. Other specs reference this
> file rather than re-stating choices.

## Status legend

| Status | Meaning |
|--------|---------|
| ✅ Confirmed | Decision made, code direction set |
| ❌ Rejected | Explicitly out of scope |
| ⏸️ Deferred | Will be decided later (with the trigger condition) |

---

## Language, runtime, and FFI

| Choice | Decision | Notes |
|--------|----------|-------|
| Server language | ✅ Go 1.26 | Current `go.mod` |
| FFI mechanism | ✅ CGo only | No `purego`, no `dlopen` |
| Add-on composition | ✅ Go build tags | No plugin system, no shared libraries — static compilation per binary variant |
| Module name | ✅ `github.com/aseem/viewport-rds` | (Repo named `featherdesk` but Go module is `viewport-rds`) |
| Cross-compilation | ✅ Required | `GOOS=darwin/linux/windows`, build tag matrix |

---

## Codecs and encoders

| Codec / Path | Decision | Notes |
|------------|----------|-------|
| H.264 — hardware | ✅ Universal default | All target browsers + future clients support it natively |
| H.264 — software | ✅ OpenH264 (Cisco) | Royalty-paid by Cisco. Linux: system package. macOS: Cisco prebuilt binary. Windows: Cisco prebuilt binary |
| HEVC — hardware | ✅ Used where available | Royalties paid by GPU vendor |
| HEVC — software | ❌ Permanently rejected | Triple patent pool (MPEG LA / Velos Media / HEVC Advance) makes redistribution toxic; **also performance gap vs H.264 doesn't justify the bandwidth savings** — users prefer speed |
| VP8 / VP9 | ❌ Rejected | All target browsers do H.264 natively; VP8 adds an entire libavcodec dependency for zero practical benefit |
| AV1 | ⏸️ Deferred | Revisit when SVT-AV1 / libdav1d hit production maturity AND we have a use case |
| libavcodec / libavformat / libswscale | ❌ Rejected | No ffmpeg dependency anywhere |
| libx264 / libx265 | ❌ Rejected | GPL contamination + we don't need them |
| libvpx | ❌ Rejected | Same as VP8 |

---

## Hardware encoder SDKs (all direct CGo, no ffmpeg)

| Vendor | SDK | License | Coverage |
|--------|-----|---------|----------|
| Intel (Linux) | libva | MIT | Quick Sync Sandy Bridge through Arc |
| Intel (Windows) | Intel oneVPL | MIT | Quick Sync Sandy Bridge through Arc |
| AMD (Linux) | AMF on ROCm | Apache 2.0 | GCN+ via ROCm runtime |
| AMD (Windows) | AMF | Apache 2.0 | GCN+ via Windows driver |
| NVIDIA (Linux) | NVENC SDK | NVIDIA proprietary | Kepler+ |
| NVIDIA (Windows) | NVENC SDK | NVIDIA proprietary | Kepler+ |
| Cross-vendor Windows | MediaFoundation MFT | Microsoft system API | All vendors via MFT routing |
| Cross-vendor (Vulkan) | Vulkan Video 1.3 | MIT/Apache | Linux + Windows; vendor-neutral |
| macOS | VideoToolbox | Apple system framework | Intel QS + AMD VCE + Apple Media Engine through one API |

---

## Capture stacks

| OS | Path | API | Notes |
|----|------|-----|-------|
| Linux | KMS+EGL DMA-BUF | libdrm + libgbm + EGL | Default; requires `CAP_SYS_ADMIN` |
| Linux | NvFBC | NVIDIA Capture SDK | NVIDIA proprietary only |
| macOS | ScreenCaptureKit | Apple framework | Only supported macOS 12.3+ API |
| Windows | TBD | DXGI DD vs WGC vs NvFBC vs AMF | ⏸️ Deferred until real Win hardware available |
| All | Subprocess capture (X11grab, ffmpeg) | — | ❌ Rejected |

---

## Color conversion + auxiliary native libs

| Need | Library | License |
|------|---------|---------|
| RGBA → I420 | libyuv | BSD-3 |
| Pure-Go alternatives | — | ❌ Not used; libyuv stays everywhere |

---

## Transport, protocol, server

| Concern | Decision |
|---------|----------|
| Wire protocol | ✅ Custom binary, 22-byte header, defined in `MODULE_PROTOCOL.md` |
| Protocol is transport-agnostic | ✅ Required — see `FUTURE_NATIVE_CLIENT.md` |
| Browser transport | ✅ HTTPS + WebSocket |
| WebSocket library | ✅ `github.com/coder/websocket` v1.8.x |
| HTTP framework | ✅ Stdlib `net/http` only — no chi/gin/echo |
| TLS | ✅ Mandatory — WebCodecs requires secure context |
| Cert handling | ✅ Built-in self-signed for dev, `cert` / `key` config keys for prod |
| Let's Encrypt integration | ❌ Reverse proxy handles that |
| Future native transport | ⏸️ QUIC + datagrams (see `FUTURE_NATIVE_CLIENT.md`) |

---

## Client (browser today)

| Choice | Decision |
|--------|----------|
| Language | ✅ Vanilla JS (`compositor.js`) — for now |
| Bundle | ✅ `//go:embed all:client` — single binary distribution |
| Decoder | ✅ WebCodecs API (Chrome 107+, Edge, Safari 14.1+) |
| Firefox support | ❌ Not supported |
| Future direction | ⏸️ Native client = separate project — see `FUTURE_NATIVE_CLIENT.md` |
| Robust frontend WASM rewrite | ⏸️ Deferred to native client project |

---

## Configuration

| Choice | Decision |
|--------|----------|
| Mechanism | ✅ Single TOML config file |
| Flag surface | ✅ Only `--config <path>` |
| Env vars | ❌ Not supported |
| Schema | ✅ Our own — defined per release in `MODULE_CONFIG.md` (to be written) |
| Reload | ✅ On `SIGHUP` (Linux/macOS), service control on Windows |

---

## Logging

| Choice | Decision |
|--------|----------|
| Library | ✅ Stdlib `log/slog` |
| Default format dev | ✅ Text handler when stdout is a TTY |
| Default format prod | ✅ JSON handler when stdout is not a TTY |
| Override | ✅ Config: `log.format = "json"` \| `"text"` \| `"auto"` |
| Migration | ✅ Delete `internal/logger`, replace call sites with `slog` |

---

## Metrics

| Choice | Decision |
|--------|----------|
| Format | ✅ Prometheus exposition format |
| Library | ✅ `github.com/prometheus/client_golang` |
| Endpoint | ✅ Separate metrics port (default `9090`), plain HTTP, `/metrics` path |
| Auth | ❌ None — assume scraping happens on private network |
| Disable | ✅ Config: `metrics.enabled = false` (default enabled) |

---

## Removed (legacy code to delete during cleanup)

| File | Why it's going |
|------|---------------|
| `internal/encode/ffmpeg.go` | FFmpeg subprocess encoder — rejected |
| `internal/encode/vp8.go` | VP8 via libavcodec — rejected |
| `internal/encode/vaapi.go` | Probe stub — replaced by `libva` add-on's own probe |
| `internal/encode/types.go` `EncoderBackend` enum | Hardcoded backends; pluggable model has no static enum |
| `internal/capture/x11grab.go` | X11 subprocess capture — rejected |
| `internal/capture/screencast.py` | Python helper for x11grab — rejected |
| `internal/logger/*` | Replaced by stdlib `slog` |
| `cmd/server/main.go` references to deleted encoders | Will be replaced by build-tagged factory |

---

## Audio + Input

| Concern | Decision |
|---------|----------|
| Audio module | ⏸️ Deferred until video capture + encode is stable across all 3 OSes |
| Input module | ⏸️ Deferred until video capture + encode is stable across all 3 OSes |
| Specs (`MODULE_AUDIO.md`, `MODULE_INPUT.md`) | ✅ Retained for reference; status banner added on next cleanup pass |
| Core module status | ❌ No longer "core" — both removed from CENTRAL_SPEC Module Map on cleanup |

---

## Versioning + release format

⏸️ **Deferred.** Decide once we have first benchmarkable binaries.

Likely: semantic versioning of the server binary, per-add-on capability advertised in the Config handshake so a client can detect what the server supports.

---

## Where each decision was made

| Decision | Conversation reference |
|----------|-----------------------|
| Pluggable add-on architecture | Earlier session — encoder + capture refactor commits |
| No GPL contamination | Earlier session — `libx264` / `libx265` rejection |
| No ffmpeg subprocess anywhere | Earlier session — direct CGo bindings policy |
| No HEVC SW | This session — patent pool + perf gap reasoning |
| OpenH264 Cisco binary on macOS | This session |
| TOML config, only `--config` flag | This session |
| stdlib slog logging | This session |
| Prometheus on separate port | This session |
| Frontend stays vanilla JS for now | This session |
| Native client = separate future project | This session — `FUTURE_NATIVE_CLIENT.md` |
| libyuv stays | This session |
| Mandatory TLS, self-signed dev cert | This session |
