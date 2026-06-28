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
| Rust support | the `toml` crate (+ `serde`) is mature, MIT/Apache, with strict `deny_unknown_fields` parsing |

---

## Lifecycle

### Startup

1. Parse `--config <path>` flag. If absent, use the OS-conventional default:
   - Linux: `/etc/featherdesk/config.toml`, then `$XDG_CONFIG_HOME/featherdesk/config.toml`, then `./featherdesk.toml`
   - macOS: `/Library/Application Support/featherdesk/config.toml`, then `~/Library/Application Support/featherdesk/config.toml`, then `./featherdesk.toml`
   - Windows: `%PROGRAMDATA%\featherdesk\config.toml`, then `%APPDATA%\featherdesk\config.toml`, then `.\featherdesk.toml`
2. Read the file. Parse into typed Rust struct (serde `Deserialize`).
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

Sections matching `[addon_module_<id>]` are validated only when the matching
add-on **library is loaded** (i.e. `<id>` matches a `featherdesk-addon-<id>.*`
library found in the add-ons directory; see `[addons] dir`).

- Add-on section present + add-on loaded → strict validation (unknown keys fail)
- Add-on section present + add-on NOT loaded → silently ignored
- Add-on section absent + add-on loaded → add-on uses built-in defaults
- Add-on section absent + add-on NOT loaded → no effect

This lets one config file serve any set of loaded add-ons without having to
maintain per-deployment configs.

### Section naming convention

```
[addon_module_<id>]
```

Where `<id>` is the **exact** add-on ID (the `<id>` in `featherdesk-addon-<id>`).
Examples:

| Add-on ID | TOML section |
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
| `opus` | _(no section — codec add-on; selects the audio codec)_ |
| `wasapi` | `[addon_module_wasapi]` |
| `sck_audio` | `[addon_module_sck_audio]` |
| `pipewire` | `[addon_module_pipewire]` |

---

## Schema

```toml
# featherdesk.toml — example with every supported key shown at its default.

[server]
bind         = "0.0.0.0:30084"    # UDP listen address for HTTP/3 + WebTransport (restart required)
allow_origin = ""                 # empty = same-origin only (SECURE DEFAULT).
                                  # Set to "*" only for trusted LANs. Server rejects
                                  # WebTransport upgrades where the Origin header doesn't match.
max_clients  = 25                 # max concurrent WebTransport sessions (reject with CloseAuthFailed 4401)
max_message_bytes = 4096          # max control-stream JSON message size (bytes). Rejects larger.
input_rate_limit  = 1000          # max input events/sec per client (mousemove coalesced)

[server.tls]
# Two trust modes (see MODULE_SERVER "TLS Configuration" + "Browser certificate
# trust"):
#   CA-trusted  — set cert + key to absolute paths of a PEM chain + private key
#                 (real domain, directly or behind an ACME reverse proxy).
#   self-signed — BOTH empty (the self-hosted/LAN default): the server manages a
#                 short-lived (≤14d) auto-rotated ECDSA P-256 cert. The browser
#                 reaches WebTransport via serverCertificateHashes (NOT a TLS
#                 click-through); the SPA reads the hash list from /cert-hashes.
# TLS 1.3 is MANDATORY under QUIC; no version knob.
cert = ""                         # (restart required)
key  = ""                         # (restart required)
extra_sans    = []                # self-signed mode: extra SANs, e.g. ["host.lan","10.0.0.5"]
rotate_before = "3d"              # self-signed mode: regenerate when < this validity remains

[transport]
# Tunables for the QUIC / WebTransport transport (see MODULE_TRANSPORT.md).
# Defaults are good; expose for ops debugging.
keepalive_period        = "15s"   # QUIC keepalive PINGs (transport-level liveness)
max_idle_timeout        = "30s"   # QUIC closes the session after this much silence (the
                                  # real dead-peer mechanism). Must be > keepalive_period.
ping_interval           = "2s"    # app-level Ping datagram cadence (RTT sampling, NOT liveness).
                                  # 0 disables app pings (QUIC RTT only). See MODULE_SERVER
                                  # "Keepalive, liveness & timeouts".
initial_max_data        = "10MiB" # initial connection-level flow control window
initial_max_stream_data = "1MiB"  # per-stream flow control window
max_streams_bidi        = 16      # cap on concurrent bidi streams per session
max_streams_uni         = 16      # cap on concurrent uni streams (rarely used)
enable_datagrams        = true    # MUST be true; required for video
fragment_reassembly_ms  = 17      # drop deadline at 60 fps; use 34 at 30 fps
datagram_send_queue_frames = 8    # per-session out-queue depth in WHOLE frames
                                  # (drop-oldest). Frame-granular, never per-fragment.
auth_deadline           = "5s"    # close unauthed sessions (CloseAuthTimeout 4408)

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

[addons]
# Directory the host scans at startup for add-on shared libraries
# (featherdesk-addon-<id>.{so,dylib,dll}). Each is dlopen'd, its ABIVersion
# checked, and its capability descriptor registered. Default is per-OS:
#   Linux:   $XDG_DATA_HOME/featherdesk/addons  (or ~/.local/share/featherdesk/addons)
#   macOS:   ~/Library/Application Support/FeatherDesk/addons
#   Windows: %PROGRAMDATA%\FeatherDesk\addons
dir          = ""                 # "" = per-OS default above (restart required)
abi_strict   = false              # true = a single ABI-mismatched library aborts startup
                                  # false = skip incompatible libraries with a warning

[capture]
# Mode controls how the runtime picks among loaded capture add-ons.
#   auto    = probe in default order
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "kms_egl", "nvfbc", "sck", "dxgi_dd" (restart required)

[encode]
# Mode controls how the runtime picks among loaded encoder add-ons.
#   auto    = HW HEVC → HW H.264 → SW H.264 probe order
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "libva", "nvenc", "vt_hw", "openh264", "x264" (restart required)

[encode.cursor]
mode = "separate"                 # "separate" (client renders) | "embedded" (server blends)

# ─────────────────────────────────────────────────────────────────────────
# STREAM PARAMETERS (initial defaults — runtime values may diverge)
# See specs/core/MODULE_STREAM_PARAMS.md for the full contract.
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

# Keyframe behavior: 0 = on-demand only (client requests via control-stream JSON)
keyframe_interval = 0

# Color depth / HDR
# Setting hdr = true forces bit_depth = 10, color_space = "bt2020", and
# switches encoder selection to HEVC Main10 (rejects H.264-only encoders).
bit_depth   = 8                  # 8 or 10
hdr         = false
color_space = "bt709"            # "bt709" (SDR) | "bt2020" (HDR)

# Chroma subsampling. "420" = universal (default). "422"/"444" sharpen text/detail
# but are capability-negotiated: used only if the encoder AND client support them,
# otherwise transparently falls back to "420" (reliable on native client,
# best-effort in browser). See MODULE_STREAM_PARAMS "Chroma Subsampling".
chroma      = "420"              # "420" | "422" | "444"

[stream.adaptive]
# Bandwidth adaptation policy. Two-tier: fast (send-side, per-frame) +
# slow (client feedback, 100ms windows). See MODULE_STREAM_PARAMS.md.
enabled               = true
interval_ms           = 100          # telemetry window (100ms, NOT 500ms)
min_bitrate_bps       = 1_000_000    # 1 Mbps floor
max_bitrate_bps       = 25_000_000   # 25 Mbps ceiling
loss_threshold_pct    = 5.0          # trigger bitrate reduction (slow path)
recovery_threshold_pct = 1.0         # allow bitrate increase
fast_reduction_factor = 0.5          # immediate reduction on send-side detection
adjustment_factor     = 0.7          # multiply on slow-path degradation
recovery_factor       = 1.1          # multiply on recovery

# ─────────────────────────────────────────────────────────────────────────
# AUTHENTICATION (see specs/core/MODULE_AUTH.md for full details)
# ─────────────────────────────────────────────────────────────────────────

[auth]
# Mode: "none" (dev only, prints warning) | "token" | "password" | "pin"
mode = "token"                       # SECURE DEFAULT. "none" only for local dev.

# Token mode
token              = ""              # explicit token; "" = auto-generate 32-byte
                                     # crypto/rand token at startup (CSPRNG mandatory).
                                     # Validation: if set explicitly, must be ≥ 32 chars.
token_file         = ""              # write generated token here (mode 0600) for ops tooling
session_ttl_minutes = 60             # auth session lifetime

# Password mode (requires "featherdesk hash-password" to generate)
password_hash      = ""              # argon2id hash

# PIN mode (first-launch pairing)
pin_length             = 8           # 8-digit PIN (100M possibilities). Min 6, max 12.
pairing_window_minutes = 5
max_pin_attempts       = 10          # global limit per window (not per-IP). Exponential
                                     # backoff: 1s, 2s, 4s, 8s... after 3rd failure.
paired_devices_file    = ""          # e.g. "/var/lib/featherdesk/paired.json" (mode 0600)

# Authorization
require_auth_for_view = true         # SECURE DEFAULT. Viewers must also authenticate.
                                     # Set false only for trusted LANs / demos.
allow_takeover        = false        # SECURE DEFAULT. Controller slot is locked.
                                     # When takeover occurs, displaced controller gets
                                     # QUIC close code 4410 with reason "controller_takeover".

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
require_same_auth     = true         # don't allow resume with different credentials
# NOTE: max connections is server.max_clients (not here)

# ─────────────────────────────────────────────────────────────────────────
# INPUT (injection — requires a loaded input add-on; see MODULE_INPUT.md)
# ─────────────────────────────────────────────────────────────────────────

[input]
enabled        = true     # master switch. false = view-only even if an add-on is loaded.
relative_mouse = true     # honor pointer-lock relative-mode frames (FPS gaming)

# ─────────────────────────────────────────────────────────────────────────
# CLIPBOARD (core; text + rich HTML; see MODULE_CLIPBOARD.md)
# ─────────────────────────────────────────────────────────────────────────

[clipboard]
enabled    = false              # opt-in (clipboard carries secrets)
direction  = "bidirectional"    # "bidirectional" | "client_to_host" | "host_to_client" | "disabled"
max_bytes  = 1048576            # 1 MiB cap per payload
formats    = ["text", "html"]   # supported: "text", "html" (image/file NOT supported)

# ─────────────────────────────────────────────────────────────────────────
# FILE TRANSFER (core; drag-drop, fixed folder, QUIC stream on the main WebTransport session)
# ─────────────────────────────────────────────────────────────────────────

[filetransfer]
enabled        = false          # opt-in
incoming_dir   = ""             # "" = <Downloads>/FeatherDesk/Incoming
outgoing_dir   = ""             # "" = <Downloads>/FeatherDesk/Outgoing
max_file_bytes = 0              # 0 = unlimited; else per-file cap
max_concurrent = 4              # simultaneous transfers
rate_limit_bps = 0              # 0 = unlimited; else throttle to protect video

# ─────────────────────────────────────────────────────────────────────────
# GAMEPAD (browser-driven gamepad redirection; MODULE_GAMEPAD.md)
# ─────────────────────────────────────────────────────────────────────────

[gamepad]
enabled         = false        # opt-in. Requires a gamepad-capable input add-on (vigem, uinput, gcvirtual).
max_controllers = 4            # 1..4 — XInput cap; also the co-op player cap
allow_rumble    = true         # forward host game vibration requests to the client
allow_coop      = false        # opt-in: let "role":"player" clients each claim a pad slot
                               # (local co-op over the network). See MODULE_GAMEPAD "Co-op".

# ═════════════════════════════════════════════════════════════════════════
# ADD-ON MODULE CONFIGS
# ═════════════════════════════════════════════════════════════════════════
#
# Each loaded add-on may have its own [addon_module_<id>] section.
# Section name MUST match the add-on ID exactly (e.g. the
# `featherdesk-addon-openh264` library pairs with `[addon_module_openh264]`).
#
# Rules:
#   - Sections for add-ons NOT loaded are SILENTLY IGNORED
#     (not strict-rejected). This lets a single config file work for any
#     set of loaded add-ons.
#   - Sections for add-ons that ARE loaded undergo strict validation —
#     unknown keys fail parsing.
#   - The active encoder reads ONLY its own [addon_module_*] section. The
#     main [encode] section provides selection (mode, force_addon, cursor);
#     dynamic per-frame parameters (width, height, fps, qp, bitrate, hdr)
#     live in [stream] and are passed via stream.Params; the add-on section
#     provides STATIC tuning (read once at startup) that doesn't change at runtime.
#   - If an add-on section is absent, the add-on uses its built-in defaults.
#
# What lives where:
#   [stream]              — dynamic, runtime-mutable (width, fps, bitrate, qp, hdr)
#   [encode]              — codec-agnostic selection (mode, force_addon, cursor)
#   [addon_module_*]      — static (startup-read) tuning specific to one add-on
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
profile          = "h264_high"    # "h264_baseline" | "h264_main" | "h264_high" | "hevc_main" | "hevc_main10"
                                  # codec-prefixed to disambiguate multi-codec add-ons
multipass        = "disabled"     # "disabled" | "qres" | "fullres"

[addon_module_amf]
# AMD AMF SDK on Windows. (Linux uses [addon_module_amf_rocm] — same keys, different add-on ID.)
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
output_index     = 0             # Which connected CRTC/connector to capture (0 = first active).
                                  # The per-capturer display selector (cf. dxgi_dd/nvfbc output_index,
                                  # sck display_id). v1 captures exactly one display; see MODULE_CAPTURE
                                  # "Display selection". Multi-monitor / runtime switch is v2.
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

# ─────────────────────────────────────────────────────────────────────────
# Input add-ons (injection backends; see MODULE_INPUT.md)
# ─────────────────────────────────────────────────────────────────────────

[addon_module_interception]
# Windows Interception filter driver + SendSAS for Ctrl+Alt+Del.
keyboard_device = 0    # 0 = first available keyboard (1..10 to pin a specific device)
mouse_device    = 0    # 0 = first available mouse (11..20 to pin)
enable_sas      = true # allow Ctrl+Alt+Del via SendSAS (requires SYSTEM service)

[addon_module_uinput]
# Linux kernel /dev/uinput (X11 + Wayland). Keyboard, mouse, scroll.
device_name   = "FeatherDesk Virtual Input"
hi_res_scroll = true   # use REL_WHEEL_HI_RES if the kernel supports it

[input.macos]
# macOS kb/mouse is the in-core `enigo` default (CGEventPost) — NOT a separate
# add-on, so this is a core [input] subsection, not [addon_module_*].
# Requires Accessibility permission.
prompt_accessibility = true   # auto-open the Accessibility pane if not trusted

[addon_module_win_touch]
# Windows Touch Injection API. Separate from interception.
max_contacts = 10      # 1..256 simultaneous touch points
feedback     = "none"  # "none" | "default" | "indirect" — system touch visual

[addon_module_vigem]
# Windows ViGEmBus virtual gamepad (Xbox 360 emulation).
driver_check    = true     # verify the ViGEmBus driver is installed at startup
controller_type = "x360"   # v1: "x360" only (DS4 deferred)

[addon_module_gcvirtual]
# macOS GCVirtualController (Game Controller framework, macOS 14+).
layout          = "standard"  # v1: "standard" Standard Gamepad layout only

# ═════════════════════════════════════════════════════════════════════════

# ─────────────────────────────────────────────────────────────────────────
# AUDIO (host→client system audio; DESIGN LOCKED, implementation deferred behind
# the video trigger — see MODULE_AUDIO.md). Schema below IS enforced once the
# audio add-ons land; the [audio] section + struct field exist now so a config
# carrying them parses. Codec is chosen by the loaded audio codec add-on (`opus` ⇒ Opus, else PCM).
# ─────────────────────────────────────────────────────────────────────────

[audio]
enabled  = false    # opt-in. Requires a loaded audio capture add-on.
frame_ms = 20       # 10 or 20 (lower = less latency, ~2× packet rate)
channels = "auto"   # "auto" = follow the host output layout (stereo / 5.1 / 7.1, ≤7.1);
                    # "stereo" = force a host-side downmix to 2.0

[addon_module_wasapi]      # Windows — WASAPI loopback
device = ""                # "" = default render endpoint; or a specific endpoint id

[addon_module_sck_audio]   # macOS — rides the sck screen-capture session
exclude_current_process = true  # don't capture FeatherDesk's own output

[addon_module_pipewire]    # Linux — PipeWire monitor source
target = ""                # "" = auto-detect the default sink's .monitor
```

---

## Validation rules

| Section | Rule | Failure mode |
|---------|------|--------------|
| `server.bind` | `host:port` form; port 1–65535 | startup error |
| `server.bind` | parseable as IP or hostname | startup error |
| `server.tls.cert` / `server.tls.key` | both empty OR both set + readable | startup error |
| `transport.enable_datagrams` | MUST be `true` (video requires datagrams) | startup error |
| `transport.fragment_reassembly_ms` | 1–1000 | startup error |
| `transport.datagram_send_queue_frames` | 1–64 (whole-frame out-queue depth) | startup error |
| `transport.max_streams_bidi` | ≥ 4 (control + input + clipboard + ≥1 file) | startup error |
| `transport.keepalive_period` / `max_idle_timeout` / `auth_deadline` | parseable duration; `keepalive_period < max_idle_timeout` | startup error |
| `transport.initial_max_data` / `initial_max_stream_data` | parseable byte size; `initial_max_data ≥ initial_max_stream_data` | startup error |
| `log.format` | one of `auto`/`json`/`text` | startup error |
| `log.level` | one of `debug`/`info`/`warn`/`error` | startup error |
| `log.output` | `stderr` / `stdout` / writable file path | startup error |
| `metrics.port` | 1–65535, must differ from the port in `server.bind` | startup error |
| `capture.mode` | `auto` or `forced` | startup error |
| `capture.force_addon` | required if `mode = "forced"` (phase A); must name a loaded add-on ID (phase B, in `pipeline::new` after the add-ons dir is scanned) | startup error |
| `encode.mode` | `auto` or `forced` | startup error |
| `encode.force_addon` | required if `mode = "forced"` (phase A); must name a loaded add-on ID (phase B, in `pipeline::new` after the add-ons dir is scanned) | startup error |
| `encode.cursor.mode` | `separate` or `embedded` | startup error |
| `stream.fps` | 1–240 | startup error |
| `stream.bitrate_bps` | 0 (QP mode) or ≥ 100000 (100 kbps minimum) | startup error |
| `stream.qp` | 0–51 | startup error |
| `stream.chroma` | `420`/`422`/`444` (422/444 auto-fall-back to 420 if unsupported) | startup error |
| `stream.width` / `stream.height` | 0 (native) or ≥ 320 | startup error |
| `auth.mode` | one of `none`/`token`/`password`/`pin`; `none` prints security warning | startup error |
| `auth.password_hash` | required if `mode = "password"` | startup error |
| `auth.token` | if `mode = "token"`: empty = auto-generate (CSPRNG); if set, must be ≥ 32 chars | startup error |
| `auth.pin_length` | 6–12 (default 8) | startup error |
| `auth.max_pin_attempts` | 1–100 (default 10) | startup error |
| `reconnect.cache_ttl_seconds` | 0–3600 | startup error |
| `server.max_clients` | 1–100 | startup error |
| `clipboard.direction` | one of `bidirectional`/`client_to_host`/`host_to_client`/`disabled` | startup error |
| `clipboard.max_bytes` | ≥ 1024 | startup error |
| `clipboard.formats` | subset of `["text","html"]` | startup error |
| `filetransfer.incoming_dir` / `outgoing_dir` | empty (default) or writable directory | startup error |
| `filetransfer.max_concurrent` | 1–16 | startup error |
| `gamepad.max_controllers` | 1–4 | startup error |
| `gamepad.enabled` requires a gamepad-capable add-on (`vigem`/`uinput`/`gcvirtual`) | else warn, gamepad records dropped | startup warning |
| `input.enabled` requires an input add-on loaded | else view-only (warn, not error) | startup warning |
| `audio.frame_ms` | 10 or 20 | startup error |
| `audio.channels` | `auto` or `stereo` | startup error |
| `audio.enabled` requires an audio capture add-on (`wasapi`/`sck_audio`/`pipewire`) | else warn, audio disabled | startup warning |
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
| `[transport]` flow-control / stream caps / `enable_datagrams` | ❌ | restart required (set on the QUIC listener at bind) |
| `[transport]` `fragment_reassembly_ms` / `datagram_send_queue_frames` / `auth_deadline` | ✅ | applied to new frames / sessions |
| `[log]` | ✅ | new handler created, in-flight writes complete on old handler |
| `[metrics]` `enabled` | ✅ | start/stop the listener |
| `[metrics]` `port`/`bind` | ❌ | restart required |
| `[capture]` `mode` | ✅ | active capture restarts |
| `[capture]` `force_addon` | ❌ | restart required |
| `[encode]` non-`force_addon` | ✅ | active encoder reconfigured |
| `[encode]` `force_addon` | ❌ | restart required (swapping encoder binary at runtime is unsafe) |
| `[encode.cursor.mode]` | ✅ | cursor pipeline rewired; cached IDR invalidated |

Reload always **revalidates the entire file** before applying anything.
Partial application is never observable — either the new config is fully
valid and applied atomically, or the reload is rejected with the previous
config still in effect.

---

## Implementation outline

```rust
// crate: featherdesk-config
use serde::Deserialize;
use std::{collections::HashMap, path::Path};

#[derive(Deserialize)]
pub struct Config {
    pub server: ServerSection,
    pub transport: TransportSection, // QUIC tunables — MUST exist as a struct
                                     // field, else (with the per-section
                                     // deny_unknown_fields below) every config
                                     // carrying a [transport] section is rejected
                                     // (the schema ships one by default).
    pub log: LogSection,
    pub metrics: MetricsSection,
    pub capture: CaptureSection,
    pub encode: EncodeSection,
    pub stream: StreamSection,        // dynamic params: width/height/fps/bitrate/qp/hdr
    pub auth: AuthSection,            // mode, password_hash, token, pin_*
    pub reconnect: ReconnectSection,  // cache_ttl_seconds, require_same_auth
    pub input: InputSection,          // enabled, relative_mouse
    pub clipboard: ClipboardSection,  // enabled, direction, max_bytes, formats
    pub filetransfer: FileTransferSection, // enabled, dirs, caps
    pub gamepad: GamepadSection,      // enabled, max_controllers, allow_rumble
    pub audio: AudioSection,          // enabled, frame_ms (design locked; impl deferred)
    // Per-addon sections ([addon_module_*]) are captured RAW here as toml::Value
    // (serde flatten collects every remaining table) and strict-decoded
    // per-add-on in phase B — they do not appear as static struct fields.
    #[serde(flatten)]
    pub addon_modules: HashMap<String, toml::Value>,
}

/// TransportSection maps the [transport] schema. Duration + byte-size values are
/// written as TOML strings ("15s", "10MiB"), so the fields use small wrapper
/// types whose Deserialize impl parses the string — serde/`toml` will not decode
/// "15s" into a bare std::time::Duration or "10MiB" into a u64 on its own.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TransportSection {
    pub keepalive_period: Duration,
    pub max_idle_timeout: Duration,
    pub initial_max_data: ByteSize,
    pub initial_max_stream_data: ByteSize,
    pub max_streams_bidi: u32,
    pub max_streams_uni: u32,
    pub enable_datagrams: bool,
    pub fragment_reassembly_ms: u32,
    pub datagram_send_queue_frames: u32,
    pub auth_deadline: Duration,
}

/// Duration and ByteSize wrap their underlying values and implement Deserialize
/// via a string parse ("15s" → std::time::Duration; "10MiB" → bytes).
pub struct Duration(pub std::time::Duration);
pub struct ByteSize(pub u64);

/// load does PHASE-A validation only (syntax, defaults, intra-section rules). It
/// captures unknown [addon_module_*] sections RAW (as toml::Value) instead of
/// failing on them — their strict decode is deferred to phase B, when the loaded
/// add-on set is known. Errors are clear and actionable (file + line + key).
pub fn load(path: &Path) -> Result<Config, ConfigError> { /* … */ }

/// validate does PHASE-B (load-aware) validation: force_addon must name a loaded
/// add-on ID, and each captured [addon_module_<id>] value is strict-decoded iff
/// its add-on is loaded (else silently ignored). Called from pipeline::new after
/// the add-ons directory has been scanned.
pub fn validate(cfg: &Config, loaded: &AddonSet) -> Result<(), ConfigError> { /* … */ }

/// watch sets up signal handling for hot reload. Calls `f` on every successful
/// reload. `f` must not block — apply changes asynchronously.
pub fn watch(cancel: CancellationToken, path: &Path, f: impl Fn(&Config) + Send + 'static) -> Result<(), ConfigError> { /* … */ }
```

The TOML parser is the `toml` crate (+ `serde`) with strict mode
(`#[serde(deny_unknown_fields)]`) on each known section; `[addon_module_*]`
sections are captured as `toml::Value` (via `#[serde(flatten)]`) and
strict-validated per-add-on in phase B (so a misspelled add-on id in a section
name is silently ignored, not flagged — verify the add-on logged its loaded config).

---

## Status

📋 **Specced — not yet implemented.** Existing code reads CLI flags in
`src/main.rs`. Migration:
1. Add the `featherdesk-config` crate with the types above.
2. Replace flag parsing in `main.rs` with `config::load`.
3. Update probe + selection logic to honor `capture.force_addon` / `encode.force_addon` when set.
4. Wire `SIGHUP` handler (Linux/macOS) and Service Control (Windows).
5. Delete legacy flags from `main.rs`.

Implementation lands in the same sprint that wires the first cross-platform
binary (after the Foundation stage in the implementation plan).
