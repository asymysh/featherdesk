# Module Spec: Config

## Overview

All runtime configuration lives in a **single TOML file**. The only CLI flag
the binary accepts is `--config <path>` (or `-c`). There are no other flags
and no environment variables.

This is a deliberate inversion of the original CLI-flag-first design — see
the conversation history (round-1 spec called for "single-binary philosophy,
no config file") for context.

### Why TOML

| Property | Why it matters |
|----------|---------------|
| Strict typing | Catches `port = "30084"` (string) vs `port = 30084` (int) at parse time |
| Comments | Operations team can annotate the file without breaking it |
| Sections | Natural grouping (`[server]`, `[log]`, `[metrics]`) maps to module boundaries |
| Single canonical encoding | No YAML-style anchor footguns, no JSON trailing-comma errors |
| Go support | `github.com/BurntSushi/toml` is BSD-3, mature, zero-allocation parse |

---

## Lifecycle

### Startup

1. Parse `--config <path>` flag. If absent, use the OS-conventional default:
   - Linux: `/etc/viewport/config.toml`, then `$XDG_CONFIG_HOME/viewport/config.toml`, then `./viewport.toml`
   - macOS: `/Library/Application Support/viewport/config.toml`, then `~/Library/Application Support/viewport/config.toml`, then `./viewport.toml`
   - Windows: `%PROGRAMDATA%\viewport\config.toml`, then `%APPDATA%\viewport\config.toml`, then `.\viewport.toml`
2. Read the file. Parse into typed Go struct.
3. Validate. Reject unknown keys (strict mode — typo guard).
4. Apply defaults for any section that's absent.
5. Bind subsystems with the parsed config.

### Reload

| OS | Mechanism |
|----|-----------|
| Linux, macOS | `SIGHUP` — re-parse the file, hot-apply where possible |
| Windows | Service control code 128 (custom) — same effect |

Not every key is hot-reloadable. Keys that require process restart are listed
per-section below with a `(restart required)` marker.

### Validation

Every section has a strict schema. Any unknown key fails parsing with a
clear error pointing at the offending line. This catches typos that would
otherwise silently fall back to defaults.

**Exception: add-on module sections.**

Sections matching `[addon_module_<name>]` are validated only when the
matching add-on is compiled into the binary (i.e. `<name>` matches an
active Go build tag).

- Add-on section present + add-on compiled in → strict validation (unknown keys fail)
- Add-on section present + add-on NOT compiled in → silently ignored
- Add-on section absent + add-on compiled in → add-on uses built-in defaults
- Add-on section absent + add-on NOT compiled in → no effect

This lets one config file serve any compiled variant of the binary
without having to maintain per-variant configs.

### Section naming convention

```
[addon_module_<build_tag>]
```

Where `<build_tag>` is the **exact** Go build tag used to compile the
add-on. Examples:

| Build tag | TOML section |
|-----------|-------------|
| `openh264` | `[addon_module_openh264]` |
| `x264` | `[addon_module_x264]` |
| `nvenc` | `[addon_module_nvenc]` |
| `amf` | `[addon_module_amf]` |
| `mf_hw` | `[addon_module_mf_hw]` |
| `libva` | `[addon_module_libva]` |
| `vt_hw` | `[addon_module_vt_hw]` |
| `dxgi_dd` | `[addon_module_dxgi_dd]` |
| `kms_egl` | `[addon_module_kms_egl]` |
| `sck` | `[addon_module_sck]` |

---

## Schema

```toml
# viewport.toml — example with every supported key shown at its default.

[server]
bind         = "0.0.0.0"          # interface to bind  (restart required)
port         = 30084              # HTTPS+WSS port     (restart required)
allow_origin = "*"                # CORS allow-origin for /ws upgrade

[server.tls]
# If both cert + key are empty, a self-signed cert is generated on startup
# (development only — browsers will warn). For production, set both to
# absolute paths of a certificate chain and matching private key in PEM form.
cert = ""                         # (restart required)
key  = ""                         # (restart required)

[log]
format = "auto"     # "auto" | "json" | "text"
                    #   auto = text when stdout is a TTY, JSON otherwise
level  = "info"     # "debug" | "info" | "warn" | "error"
output = "stderr"   # "stderr" | "stdout" | "/path/to/logfile"

[metrics]
enabled = true
bind    = "127.0.0.1"             # private by default; not authenticated
port    = 9090                    # Prometheus scrape port  (restart required)
path    = "/metrics"

[capture]
# Mode controls how the runtime picks among compiled-in capture add-ons.
#   auto    = probe in default order
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "kms_egl", "nvfbc", "sck", "dxgi_dd" (restart required)

[encode]
# Mode controls how the runtime picks among compiled-in encoder add-ons.
#   auto    = HW HEVC → HW H.264 → SW H.264 probe order
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "libva", "nvenc", "vt_hw", "openh264", "x264"

[encode.cursor]
mode = "separate"                 # "separate" (client renders) | "embedded" (server blends)

# ─────────────────────────────────────────────────────────────────────────
# STREAM PARAMETERS (initial defaults — runtime values may diverge)
# See specs/MODULE_STREAM_PARAMS.md for the full contract.
# ─────────────────────────────────────────────────────────────────────────

[stream]
# Initial resolution, frame rate, quality, color depth.
# These are the STARTING values; the pipeline may change them at runtime
# based on client window resize requests, network feedback (bandwidth
# adaptation), or admin actions.
width       = 0                  # 0 = use display native resolution
height      = 0                  # 0 = use display native
fps         = 60                 # target capture+encode rate

# Quality mode (mutually exclusive):
#   bitrate_bps > 0 → bandwidth-target mode (variable QP)
#   bitrate_bps = 0 → constant-QP mode using qp
bitrate_bps = 0
qp          = 26                 # 0..51 for H.264 (lower = higher quality)

# Keyframe behavior: 0 = on-demand only (client requests via JSON text)
keyframe_interval = 0

# Color depth / HDR
# Setting hdr = true forces bit_depth = 10, color_space = "bt2020", and
# switches encoder selection to HEVC Main10 (rejects H.264-only encoders).
bit_depth   = 8                  # 8 or 10
hdr         = false
color_space = "bt709"            # "bt709" (SDR) | "bt2020" (HDR)

[stream.adaptive]
# Bandwidth adaptation policy. Pipeline measures network telemetry every
# 500ms and adjusts bitrate based on packet loss + RTT.
enabled               = true
min_bitrate_bps       = 1_000_000    # 1 Mbps floor
max_bitrate_bps       = 25_000_000   # 25 Mbps ceiling
loss_threshold_pct    = 5.0          # trigger bitrate reduction
recovery_threshold_pct = 1.0         # allow bitrate increase
adjustment_factor     = 0.7          # multiply on degradation
recovery_factor       = 1.1          # multiply on recovery

# ─────────────────────────────────────────────────────────────────────────
# AUTHENTICATION (see specs/MODULE_AUTH.md for full details)
# ─────────────────────────────────────────────────────────────────────────

[auth]
# Mode: "none" (dev only) | "token" | "password" | "pin" | "oauth" (deferred)
mode = "none"

# Token mode
token              = ""              # explicit token; "" = auto-generate at startup
token_file         = ""              # write generated token here for ops tooling
session_ttl_minutes = 60             # successful auth lifetime

# Password mode (requires "viewport-rds hash-password" to generate)
password_hash      = ""              # argon2id hash

# PIN mode (first-launch pairing)
pairing_window_minutes = 5
paired_devices_file    = ""          # e.g. "/var/lib/viewport-rds/paired.json"

# Authorization
require_auth_for_view = false        # set true to require auth even for viewer role
allow_takeover        = true         # set false to lock the controller slot

# OAuth (deferred — interface defined, no implementation in v1)
# oauth_provider     = "google" | "github" | "azure" | "okta"
# oauth_client_id    = ""
# oauth_client_secret = ""
# oauth_redirect_url = ""
# oauth_allowed_emails = []

# ─────────────────────────────────────────────────────────────────────────
# RECONNECTION (session resume across network blips)
# ─────────────────────────────────────────────────────────────────────────

[reconnect]
enabled               = true
cache_ttl_seconds     = 300          # how long server holds session state after disconnect
max_concurrent_sessions = 25         # cap on active sessions
require_same_auth     = true         # don't allow resume with different credentials

# ═════════════════════════════════════════════════════════════════════════
# ADD-ON MODULE CONFIGS
# ═════════════════════════════════════════════════════════════════════════
#
# Each compiled-in add-on may have its own [addon_module_<name>] section.
# Section name MUST match the build tag exactly (e.g. `-tags openh264`
# pairs with `[addon_module_openh264]`).
#
# Rules:
#   - Sections for add-ons NOT compiled into the binary are SILENTLY IGNORED
#     (not strict-rejected). This lets a single config file work for any
#     binary variant.
#   - Sections for add-ons that ARE compiled in undergo strict validation —
#     unknown keys fail parsing.
#   - The active encoder reads ONLY its own [addon_module_*] section. The
#     main [encode] section provides selection (mode, force_addon, cursor);
#     dynamic per-frame parameters (width, height, fps, qp, bitrate, hdr)
#     live in [stream] and are passed via stream.Params; the add-on section
#     provides BUILD-TIME tuning that doesn't change at runtime.
#   - If an add-on section is absent, the add-on uses its built-in defaults.
#
# What lives where:
#   [stream]              — dynamic, runtime-mutable (width, fps, bitrate, qp, hdr)
#   [encode]              — codec-agnostic selection (mode, force_addon, cursor)
#   [addon_module_*]      — build-time tuning specific to one add-on
#
# ─────────────────────────────────────────────────────────────────────────
# SW H.264 add-ons
# ─────────────────────────────────────────────────────────────────────────

[addon_module_openh264]
# Cisco OpenH264 — BSD licensed, CGo in-process. For commercial deployments.
threads      = 0                  # 0 = auto (min(cpu_count, 4) — saturates at 4)
                                  # Range: 1–16. Above 4 has diminishing returns.
slice_mode   = "fixed"            # "single" (1 slice) | "fixed" (N slices = N threads)
profile      = "baseline"         # "baseline" | "main" | "high" — Constrained Baseline default

[addon_module_x264]
# libx264 via ffmpeg subprocess — GPL isolated. For home / personal / OSS.
# Requires ffmpeg in PATH or bundled.
ffmpeg_path  = ""                 # "" = auto-discover (see X264_SUBPROCESS spec)
threads      = 0                  # 0 = auto (cpu_count, capped at 12). Sweet spot is 8.
preset       = "ultrafast"        # "ultrafast" | "superfast" | "veryfast" | "faster" | "fast" | "medium"
                                  # ultrafast is mandatory for sub-5ms encode.
tune         = "zerolatency"      # Hardcoded; "zerolatency" required for streaming.
profile      = "baseline"         # "baseline" | "main" | "high" — ultrafast forces baseline

# ─────────────────────────────────────────────────────────────────────────
# HW encoder add-ons (Linux)
# ─────────────────────────────────────────────────────────────────────────

[addon_module_libva]
# Intel / AMD via Mesa, NVIDIA via vaapi wrapper.
render_node      = "/dev/dri/renderD128"
profile          = "h264_main"    # "h264_baseline" | "h264_main" | "h264_high" | "hevc_main" | "hevc_main10"
low_power        = true           # Use EncSliceLP entry point on Intel (faster on Gen 9+)
async_depth      = 1              # 1 = synchronous, higher = pipelined

[addon_module_nvenc]
# NVIDIA NVENC direct SDK binding. Linux + Windows.
gpu              = 0              # NVENC GPU index (0 = first NVIDIA GPU)
preset           = "p1"           # p1 (fastest) -- p7 (slowest/best). p1 for streaming.
tune             = "ull"          # "ull" (ultra-low-latency) | "ll" | "hq"
profile          = "high"         # "baseline" | "main" | "high" — high recommended for screen content
multipass        = "disabled"     # "disabled" | "qres" | "fullres"

[addon_module_amf]
# AMD AMF SDK on Windows. (Linux uses [addon_module_amf_rocm] — same keys, different build tag.)
usage            = "lowlatency"   # "transcoding" | "ultralowlatency" | "lowlatency" | "webcam"
quality          = "speed"        # "speed" | "balanced" | "quality"
profile          = "high"         # "baseline" | "main" | "high"

[addon_module_amf_rocm]
# AMD AMF SDK on Linux via ROCm runtime. Same keys as [addon_module_amf].
usage            = "lowlatency"
quality          = "speed"
profile          = "high"

# ─────────────────────────────────────────────────────────────────────────
# HW encoder add-ons (Windows)
# ─────────────────────────────────────────────────────────────────────────

[addon_module_mf_hw]
# MediaFoundation cross-vendor MFT routing. Picks GPU via D3D11VA device.
adapter_index    = -1             # -1 = system default (Windows picks). 0+ = specific d3d11va adapter.
rate_control_mode = "quality"     # "quality" | "cbr" | "pc_vbr" | "u_vbr" | "ld_vbr"

[addon_module_qsv]
# Intel oneVPL / QSV. Windows only (Linux uses libva).
adapter_index    = 0
target_usage     = 7              # 1 (quality) -- 7 (speed). 7 for streaming.

# ─────────────────────────────────────────────────────────────────────────
# macOS encoder add-ons
# ─────────────────────────────────────────────────────────────────────────

[addon_module_vt_hw]
# VideoToolbox hardware. Apple Media Engine on Apple Silicon, VCE on Intel+AMD.
realtime         = true           # kVTCompressionPropertyKey_RealTime
profile          = "h264_baseline" # "h264_baseline" | "h264_main" | "h264_high" | "hevc_main" | "hevc_main10"
allow_frame_reordering = false    # false = lower latency (no B-frames)

[addon_module_vt_sw]
# VideoToolbox software fallback. Uses Apple's tuned H.264 SW encoder.
# All keys are the same as [addon_module_vt_hw] — the parser treats vt_sw
# and vt_hw as schema-aliases. (Listed here for strict-validator clarity.)
realtime         = true
profile          = "h264_baseline"
allow_frame_reordering = false

# ─────────────────────────────────────────────────────────────────────────
# Capture add-ons
# ─────────────────────────────────────────────────────────────────────────

[addon_module_kms_egl]
# Linux KMS+EGL DMA-BUF capture.
drm_card         = ""             # "" = auto-discover. e.g. "/dev/dri/card0"
cursor_plane     = true           # Capture cursor plane separately for client-side compositing

[addon_module_nvfbc]
# NVIDIA NvFBC capture. Linux only (Windows uses DXGI DD).
output_index     = 0              # Which NVIDIA output to capture
capture_type     = "to_cuda"      # "to_cuda" | "to_sys" | "to_gl" — CUDA for direct NVENC pairing
with_cursor      = false          # false = cursor captured separately

[addon_module_dxgi_dd]
# Windows DXGI Desktop Duplication.
adapter_index    = -1             # -1 = adapter with active display. 0+ = specific DXGI adapter.
output_index     = 0              # Which display to capture (0 = primary).
# Headless: when no output exists, auto-install IddCx virtual display driver.
auto_install_vdd = true
virtual_display_width  = 1920
virtual_display_height = 1080
virtual_display_hz     = 60

[addon_module_sck]
# macOS ScreenCaptureKit.
display_id       = 0              # 0 = main display, or NSScreen index
show_cursor      = false          # false = cursor sent separately as CursorUpdate

# ═════════════════════════════════════════════════════════════════════════

# ─────────────────────────────────────────────────────────────────────────
# Deferred sections — not yet enforced. Reserved for when audio + input
# modules come out of deferral.
# ─────────────────────────────────────────────────────────────────────────

# [audio]
# enabled = false
# (full schema TBD when MODULE_AUDIO is un-deferred)

# [input]
# enabled = false
# (full schema TBD when MODULE_INPUT is un-deferred)
```

---

## Validation rules

| Section | Rule | Failure mode |
|---------|------|--------------|
| `server.port` | 1–65535 | startup error |
| `server.bind` | parseable as IP or hostname | startup error |
| `server.tls.cert` / `server.tls.key` | both empty OR both set + readable | startup error |
| `log.format` | one of `auto`/`json`/`text` | startup error |
| `log.level` | one of `debug`/`info`/`warn`/`error` | startup error |
| `log.output` | `stderr` / `stdout` / writable file path | startup error |
| `metrics.port` | 1–65535, must differ from `server.port` | startup error |
| `capture.mode` | `auto` or `forced` | startup error |
| `capture.force_addon` | required if `mode = "forced"`; must be a compiled-in build tag | startup error |
| `encode.mode` | `auto` or `forced` | startup error |
| `encode.force_addon` | required if `mode = "forced"`; must be a compiled-in build tag | startup error |
| `encode.fps` | 1–144 | startup error |
| `encode.bitrate_bps` | ≥ 0 | startup error |
| `encode.qp` | 0–51 | startup error |
| `encode.cursor.mode` | `separate` or `embedded` | startup error |
| Unknown key anywhere | strict mode | startup error |

---

## Default config

If the binary is started with `--config` pointing at a file that exists but
is empty, every section above defaults as shown in the schema example. If
the file does not exist, the binary writes a fully-defaulted config to that
path and continues startup (development convenience; for production deploy,
provision the file via configuration management before starting).

---

## Hot reload behavior

On SIGHUP (or Windows equivalent):

| Section | Hot-reloadable | Notes |
|---------|----------------|-------|
| `[server]` `bind` / `port` | ❌ | restart required |
| `[server.tls]` | ❌ | restart required |
| `[server]` `allow_origin` | ✅ | applies on next upgrade |
| `[log]` | ✅ | new handler created, in-flight writes complete on old handler |
| `[metrics]` `enabled` | ✅ | start/stop the listener |
| `[metrics]` `port`/`bind` | ❌ | restart required |
| `[capture]` `mode` | ✅ | active capture restarts |
| `[capture]` `force_addon` | ❌ | restart required |
| `[encode]` | ✅ all | active encoder reconfigured or recreated |
| `[encode.cursor.mode]` | ✅ | cursor pipeline rewired; cached IDR invalidated |

Reload always **revalidates the entire file** before applying anything.
Partial application is never observable — either the new config is fully
valid and applied atomically, or the reload is rejected with the previous
config still in effect.

---

## Implementation outline

```go
package config

type Config struct {
    Server  ServerSection  `toml:"server"`
    Log     LogSection     `toml:"log"`
    Metrics MetricsSection `toml:"metrics"`
    Capture CaptureSection `toml:"capture"`
    Encode  EncodeSection  `toml:"encode"`
    // Audio + Input added when those modules are un-deferred
}

// Load parses + validates the config at path, applies defaults, and returns
// a fully-populated Config. Errors are clear and actionable (file + line + key).
func Load(path string) (*Config, error)

// Watch sets up signal handling for hot reload. Calls fn on every successful
// reload. fn must not block — apply changes asynchronously.
func Watch(ctx context.Context, path string, fn func(*Config)) error
```

The TOML parser is `github.com/BurntSushi/toml` with strict mode
(`Decoder.DisallowUnknownFields()`).

---

## Status

📋 **Specced — not yet implemented.** Existing code reads CLI flags in
`cmd/server/main.go`. Migration:
1. Add `internal/config/` package with the types above.
2. Replace flag parsing in `main.go` with `config.Load`.
3. Update probe + selection logic to honor `capture.force_addon` / `encode.force_addon` when set.
4. Wire `SIGHUP` handler (Linux/macOS) and Service Control (Windows).
5. Delete legacy flags from main.go.

Implementation lands in the same sprint that wires the first cross-platform
binary (after the Foundation stage in the implementation plan).
