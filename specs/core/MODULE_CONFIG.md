# Module Spec: Config

## Overview

All runtime configuration lives in a **single TOML file**. The binary's command
line is deliberately tiny: one option that selects that file, and three
subcommands that exist because they must run *without* a server.

This is a deliberate inversion of the original CLI-flag-first design — see
the conversation history (round-1 spec called for "single-binary philosophy,
no config file") for context.

### CLI surface

```
featherdesk [--config <path> | -c <path>]        # run the server (the default)
featherdesk hash-password [--config <path>]      # print an argon2id PHC hash, exit
featherdesk revoke-device <device-id> [--config <path>]
featherdesk list-devices [--config <path>]
featherdesk --version | -V
featherdesk --help | -h
```

Argv is hand-parsed — the binary reads fewer than ten tokens and a `clap`
dependency is not warranted.

- Subcommand dispatch happens on `argv[1]` **before** the config file is read, so
  `hash-password` runs on a machine that has no config file yet. `--config` is
  still accepted by `revoke-device` / `list-devices`, which need
  `[auth] paired_devices_file`.
- `hash-password` prompts twice on the TTY with echo disabled (`rpassword`),
  refuses a mismatch and refuses an empty password, and prints the PHC string
  `$argon2id$v=19$m=65536,t=3,p=4$…` on stdout and nothing else, so it can be
  redirected into a config-management pipeline. It never writes the config file.
  If stdin is not a TTY it reads one line from stdin instead of prompting.
- `revoke-device <id>` rewrites `paired_devices_file` (same 0600 permissions,
  written to a temporary file in the same directory and `rename`d) and prints the
  number of devices removed. It does not signal a running server — the operator
  sends `SIGHUP` (see MODULE_AUTH "Token rotation and device revocation").
- `list-devices` prints `device_id`, first-paired timestamp and last-seen
  timestamp, one per line, tab-separated.
- **Exit codes:** `0` success; `1` runtime failure (unreadable file, bad password
  confirmation, unknown device id); `2` usage error (unknown subcommand, unknown
  flag, missing operand). An unknown `argv[1]` that starts with `-` and is not
  `--config`/`-c`/`--version`/`-V`/`--help`/`-h` is a usage error — it is
  **never** ignored, because silently ignoring an unrecognised argument is how
  `hash-password` came to boot a streaming server.
- There are **no environment variables**. See "Precedence" below.

### Precedence

There is exactly one value source: the TOML file. The command line **selects**
that file and nothing else; no flag overrides a key, and no environment variable
is read. When a key is absent the documented default applies. The two apparent
exceptions are not overrides: `[auth] token_file`, which is a place a token may
be *supplied* when `token = ""` (see MODULE_AUTH "Mode: `token`"), and
`[addons] dir`'s `""`, which resolves relative to the running executable.
Ordering is therefore trivial and total: **file → default**, with the file always
winning where it speaks.

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

0. Dispatch `argv[1]` per "CLI surface". `hash-password`, `revoke-device`,
   `list-devices`, `--version` and `--help` run their tool and exit here, before
   any config file is read. Only the no-subcommand case continues.
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
per-section below with a `(restart required)` marker, and every key's
reloadability is stated in the "Hot" column of the Key reference.

### Validation

Every section has a strict schema. Any unknown key fails parsing with a
clear error pointing at the offending line. This catches typos that would
otherwise silently fall back to defaults.

**Exception: add-on module sections.**

Sections matching `[addon_module_<id>]` are validated only when the matching
add-on **library is loaded** (i.e. `<id>` matches a `featherdesk-addon-<id>.*`
library found in the add-ons directory; see `[addons] dir`).

- Add-on section present + add-on loaded → strict validation (unknown keys fail) — performed by the add-on, not the host
- Add-on section present + add-on NOT loaded → silently ignored
- Add-on section absent + add-on loaded → add-on uses built-in defaults
- Add-on section absent + add-on NOT loaded → no effect

**Who validates.** The host has no schema for `[addon_module_<id>]` — the schema
lives in the add-on, behind `dlopen`. Phase B therefore does not decode these
sections; it re-serializes each one whose add-on is loaded into
`RAddonConfig.section_toml` (MODULE_ABI "Add-on configuration") and the add-on
strict-decodes it in `construct()` against its own `#[serde(deny_unknown_fields)]`
struct. An unknown key comes back as `AbiErr::BadConfig` and fails startup with
the add-on id, the key and the config file path.

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
| `opus` | `[addon_module_opus]` |
| `wasapi` | `[addon_module_wasapi]` |
| `sck_audio` | `[addon_module_sck_audio]` |
| `pipewire` | `[addon_module_pipewire]` |

---

## Schema

```toml
# featherdesk.toml — example with every supported key shown at its default.

[server]
base_path    = "/"               # URL path prefix every route is served under, for a
                                 # reverse proxy or tunnel that terminates on a subpath
                                 # rather than a hostname root. "/" (default) = no prefix.
                                 # MUST begin and end with "/" when not "/" (e.g. "/desk/").
                                 # Applies uniformly to every route on BOTH listeners —
                                 # /, /cert-hashes, /healthz, /wt, /ws, /auth, /pair,
                                 # /logout — because a route that keeps the bare path while
                                 # its siblings move is a 404 that only appears in
                                 # production. Sourced into transport::Config, which STRIPS
                                 # it before routing and before the /wt and /ws upgrade
                                 # match, so the prefix has exactly one implementation and
                                 # the two carrier-specific endpoints cannot be left behind
                                 # (MODULE_TRANSPORT "Base path"). The SPA derives its own
                                 # prefix from location.pathname and never hardcodes "/", so
                                 # the same embedded bundle serves any prefix.
                                 # NOT applied to the metrics listener, which is a separate
                                 # bind and is not proxied.
bind         = "0.0.0.0:30084"    # TCP *and* UDP listen address: HTTPS bootstrap +
                                  # HTTP/3 + WebTransport, one port (restart required)
allow_origin = ""                 # empty = same-origin only (SECURE DEFAULT).
                                  # Set to "*" only for trusted LANs. Server rejects
                                  # WebTransport upgrades where the Origin header doesn't match.
max_clients  = 25                 # max concurrent WebTransport sessions (reject with close::SERVER_FULL 4429)
max_message_bytes = 4096          # max control-stream JSON message size (bytes). Rejects larger.
input_rate_limit  = 1000          # max input events/sec per client (mousemove coalesced)
keyframe_min_interval_ms   = 500  # global coalescer: at most one forced IDR per this
                                  # window, across all clients and all reasons
keyframe_request_burst     = 3    # per-session token bucket for {"type":"keyframe"}
keyframe_request_refill_ms = 2000 # one token per this interval
shutdown_grace_ms          = 250  # flush window for {"type":"server_shutdown"} before
                                  # closing sessions with close::SERVER_SHUTDOWN 4503

[server.tls]
# Two trust modes (see MODULE_SERVER "TLS Configuration" + "Browser certificate
# trust"):
#   CA-trusted  — set cert + key to absolute paths of a PEM chain + private key
#                 (real domain, directly or behind an ACME reverse proxy).
#   self-signed — BOTH empty (the self-hosted/LAN default): the server manages a
#                 short-lived (≤14d) auto-rotated ECDSA P-256 cert. The browser
#                 reaches WebTransport via serverCertificateHashes (NOT a TLS
#                 click-through); the SPA reads the hash list from /cert-hashes.
# Self-signed mode retains its KEY PAIR across rotations, so the SPKI fingerprint
# is a stable pin; deleting the cached key file breaks every client pin.
# TLS 1.3 is MANDATORY under QUIC; no version knob.
cert = ""                         # path (restart required — the path selects the trust
                                  # mode); the FILE at it is re-read on SIGHUP and
                                  # swapped into the live listener
key  = ""                         # path (restart required); same SIGHUP re-read
extra_sans    = []                # self-signed mode: extra SANs, e.g. ["host.lan","10.0.0.5"]
rotate_before = "3d"              # self-signed mode: regenerate when < this validity remains

[transport]
# Tunables for the transport (see MODULE_TRANSPORT.md). Defaults are good; expose
# for ops debugging. The two liveness keys are CARRIER-GENERIC — same semantics on
# WebTransport and WebSocket, one implementation each. There are deliberately no
# ws_* twins: two knobs for one question is how a deployment ends up reaped on one
# carrier and immortal on the other.
keepalive_period        = "15s"   # keepalive cadence (transport-level liveness).
                                  #   WebTransport: QUIC keepalive PINGs.
                                  #   WebSocket:    a WebSocket Ping control frame.
max_idle_timeout        = "30s"   # close after this much silence (the real dead-peer
                                  # mechanism). Must be > keepalive_period.
                                  #   WebTransport: QUIC's own idle timeout.
                                  #   WebSocket:    an app-side timer in the session task —
                                  #                 TCP keepalives are measured in HOURS, so
                                  #                 without it a half-open connection through a
                                  #                 tunnel holds a max_clients slot, and the
                                  #                 controller slot, until the OS gives up.
ping_interval           = "2s"    # app-level Ping DATAGRAM cadence (RTT sampling, NOT liveness,
                                  # and NOT the WebSocket Ping control frame above).
                                  # 0 disables app pings (transport RTT only). See MODULE_SERVER
                                  # "Keepalive, liveness & timeouts".
initial_max_data        = "10MiB" # initial connection-level flow control window
initial_max_stream_data = "1MiB"  # per-stream flow control window
max_streams_bidi        = 16      # cap on concurrent bidi streams per session.
                                  # VALIDATED: must be >= 3 + filetransfer.max_concurrent
                                  # + filetransfer.queue_depth (3 + 4 + 8 = 15 at defaults)
max_streams_uni         = 16      # cap on concurrent uni streams (bootstrap + cursor)
enable_datagrams        = true    # MUST be true; required for video
fragment_reassembly_ms  = 17      # drop deadline at 60 fps; use 34 at 30 fps. A CLIENT-side
                                  # deadline, advertised at connect (restart required)
datagram_send_queue_frames = 8    # per-session video out-queue depth in WHOLE frames
                                  # (`FrameOut`, drop-oldest). Frame-granular, never
                                  # per-fragment.
audio_send_queue_chunks = 25      # per-session audio out-queue depth in WHOLE chunks
                                  # (~500 ms at frame_ms = 20, drop-oldest). A SEPARATE
                                  # ring from the video one, so a video burst can never
                                  # evict audio. Same ring on both carriers.
auth_deadline           = "5s"    # close unauthed sessions (CloseAuthTimeout 4408)
websocket_fallback      = true    # serve the degraded WebSocket carrier on /ws
                                  # (restart required — the route is installed on the
                                  # router at bind). false → /ws returns 501 and
                                  # UDP-blocked clients cannot connect at all. See
                                  # MODULE_TRANSPORT "Carrier selection".
ws_max_message_bytes    = "16MiB" # inbound cap on the fallback carrier; must be >= the
                                  # 16 MiB per-frame reassembly cap
ws_send_queue_bytes     = "8MiB"  # fallback carrier out-queue byte ceiling; exceeding it
                                  # after coalescing closes the session (4400)
reassembly_max_bytes    = "4MiB"  # per-session in-progress reassembly bytes; oldest-
                                  # FrameID-first eviction above it
join_idr_timeout        = "1s"    # how long a join waits for a fresh IDR before serving
                                  # the stale one with the pump gated

[log]
format = "auto"     # "auto" | "json" | "text"
                    #   auto = text when stdout is a TTY, JSON otherwise
level  = "info"     # "debug" | "info" | "warn" | "error"
output = "stderr"   # "stderr" | "stdout" | "/path/to/logfile"

[metrics]
enabled = true
bind    = "127.0.0.1"             # private by default; plain HTTP, no auth, no TLS.
                                  # A non-loopback value prints a startup warning.
port    = 9090                    # Prometheus scrape port  (restart required)
path    = "/metrics"

[addons]
# Directory the host scans at startup for add-on shared libraries
# (featherdesk-addon-<id>.{so,dylib,dll}). Each is dlopen'd, its ABIVersion
# checked, and its capability descriptor registered.
# Portable / drop-anywhere: point this at ANY absolute or relative path. The
# default ("") resolves to an `addons/` folder next to the running core (the host
# executable's own directory), so the binary + its `addons/` travel together in
# whatever folder you keep the project. A missing dir is treated as empty (warn) —
# the host still runs, just with no backends loaded.
# Advisory (not enforced): loading a library runs its native code in the host;
# place only add-ons you trust here, and secure the folder yourself if you run
# the host elevated. FeatherDesk does NOT police this directory's ownership or
# permissions. See CENTRAL_SPEC "Add-on directory (portable, user-controlled)".
dir          = ""                 # "" = `<core's own dir>/addons`; or any abs/rel path (restart required)
abi_strict   = false              # true = a single ABI-mismatched library aborts startup
                                  # false = skip incompatible libraries with a warning

[capture]
# Mode controls how the runtime picks among loaded capture add-ons.
#   auto    = probe in default order
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "kms_egl", "nvfbc", "sck", "dxgi_dd" (restart required)
# How the pointer reaches the client.
#   auto      = "separate" when the selected add-on reports AddonCaps::CURSOR,
#               else "embedded" (which requires AddonCaps::EMBED_CURSOR)
#   separate  = require a separate cursor; add-ons without CURSOR are not eligible
#   embedded  = require the cursor burned into the frame; add-ons without
#               EMBED_CURSOR are not eligible
# The resolved value is reported to the client as `config.cursorMode`.
# See MODULE_CAPTURE "Cursor delivery and the separate / embedded decision".
cursor_mode  = "auto"             # "auto" | "separate" | "embedded"

[encode]
# Mode controls how the runtime picks among loaded encoder add-ons.
#   auto    = probe order per OS (see MODULE_PIPELINE step 3e); the chosen
#             encoder emits H.264 unless the session is HDR (MODULE_STREAM_PARAMS).
#             SW auto order is openh264 → vt_sw (macOS only); x264 is NOT in it.
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "libva", "nvenc", "vt_hw", "openh264" (restart required).
                                  # Also the ONLY way to reach "x264" — see
                                  # MODULE_ENCODE "Software encoder order".

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
fps         = 60                 # target capture+encode rate; the pipeline lowers the
                                 # ADVERTISED rate to what the host sustains
                                 # (MODULE_PIPELINE "Sustainable-rate control"). 60 is a
                                 # hardware-path figure; the software fallback typically
                                 # settles at 30.

# Quality mode (mutually exclusive):
#   bitrate_bps > 0 → bandwidth-target mode (variable QP)
#   bitrate_bps = 0 → constant-QP mode using qp
# With [stream.adaptive] enabled = true this is the *initial* mode; the first
# congestion signal converts the session to bitrate mode, one way — see
# MODULE_STREAM_PARAMS "Cold start".
bitrate_bps = 0
qp          = 26                 # 0..51 for H.264 (lower = higher quality)

# Keyframe behavior: 0 = on-demand only (client requests via control-stream JSON)
keyframe_interval = 0
idle_keyframe_ms  = 100          # on a static screen, re-encode the last captured frame
                                 # as an IDR this long after a force-keyframe is raised,
                                 # so a join never waits out join_idr_timeout

# Color depth / HDR
# Setting hdr = true forces bit_depth = 10, color_space = "bt2020", and switches the
# encoder to HEVC Main10 — WebCodecs has no H.264 HDR profile. Every SDR session is
# H.264 regardless of what HEVC hardware is present. Entering HDR is refused while any
# attached session reported decode.hevc10 == false, and degrade_to_software forces the
# session back out of it. See MODULE_STREAM_PARAMS "HDR Pipeline".
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
token              = ""              # explicit token; "" = read token_file, else auto-generate a
                                     # 32-byte CSPRNG token at startup. If set, must be ≥ 32 chars.
token_file         = ""              # supply a token here, or receive the generated one (mode 0600).
session_ttl_minutes = 60             # auth session lifetime

# Password mode (generate with "featherdesk hash-password" — see CLI surface)
password_hash      = ""              # argon2id hash

# PIN mode (first-launch pairing)
pin_length             = 8           # 8-digit PIN (100M possibilities). Min 6, max 12.
pairing_window_minutes = 5
max_pin_attempts       = 10          # process-global limit per window (not per-IP, not
                                     # per-connection). Exponential backoff: 1s, 2s, 4s,
                                     # 8s... after 3rd failure.
paired_devices_file    = ""          # e.g. "/var/lib/featherdesk/paired.json" (mode 0600)

# Authorization
require_auth_for_view = true         # SECURE DEFAULT. Viewers must also authenticate. When false, an
                                     # unauthenticated client is admitted with max_role = view and can never
                                     # hold the controller slot (MODULE_AUTH "Identity ceilings per mode").
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
max_bytes  = 1048576            # 1 MiB cap per payload (1024 … 4194304). It is an
                                # allocation size a client controls, so it is bounded
                                # above as well as below.
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
queue_depth    = 8              # accepted-but-waiting transfers beyond max_concurrent.
                                # max_concurrent + queue_depth is advertised to the
                                # client as config.fileStreamBudget
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
#     unknown keys fail parsing. The ADD-ON performs it, against its own
#     #[serde(deny_unknown_fields)] struct; the host has no schema for the
#     section and forwards it verbatim (MODULE_ABI "Add-on configuration").
#   - The active encoder reads ONLY its own [addon_module_*] section. The
#     main [encode] section provides selection (mode, force_addon);
#     dynamic per-frame parameters (width, height, fps, qp, bitrate, hdr)
#     live in [stream] and are passed via stream.Params; the add-on section
#     provides STATIC tuning (read once at startup) that doesn't change at runtime.
#   - If an add-on section is absent, the add-on uses its built-in defaults.
#
# What lives where:
#   [stream]              — dynamic, runtime-mutable (width, fps, bitrate, qp, hdr)
#   [encode]              — codec-agnostic selection (mode, force_addon)
#   [capture]             — capture-side selection and cursor policy
#   [addon_module_*]      — static (startup-read) tuning specific to one add-on
#
# ─────────────────────────────────────────────────────────────────────────
# SW H.264 add-ons
# ─────────────────────────────────────────────────────────────────────────

[addon_module_openh264]
# Cisco OpenH264 — BSD licensed, Rust FFI in-process. The software DEFAULT on
# every OS: no external binary to find or lose, and it forces an IDR in place for
# the cost of one flag on the next frame. Emits Constrained Baseline only.
threads      = 0                  # 0 = auto (min(cpu_count, 4) — saturates at 4)
                                  # Range: 1–16. Above 4 has diminishing returns.
slice_mode   = "fixed"            # "single" (1 slice) | "fixed" (N slices = N threads)

[addon_module_x264]
# libx264 via ffmpeg subprocess — GPL isolated. OPT-IN: never auto-selected,
# reached only by [encode] force_addon = "x264". It is ~2x faster than OpenH264 on
# a CPU-bound host and is worth forcing where that has been MEASURED; it is not the
# default, because its only mechanism for an on-demand IDR is killing and
# respawning the ffmpeg child. See MODULE_ENCODE "Software encoder order".
# Requires ffmpeg in PATH or bundled.
ffmpeg_path  = ""                 # "" = auto-discover (see X264_SUBPROCESS spec)
threads      = 0                  # 0 = auto (cpu_count, capped at 12). Sweet spot is 8.
preset       = "ultrafast"        # "ultrafast" | "superfast" | "veryfast" | "faster" | "fast" | "medium"
                                  # ultrafast is mandatory for sub-5ms encode.
# tune is hardcoded to "zerolatency"; NOT a config key — writing it fails startup with AbiErr::BadConfig (see X264_SUBPROCESS_*_SPEC "Configuration")
profile      = "high"             # "high" | "high422" | "high444" — the chroma negotiation
                                  # selects it; this is the ceiling

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
profile          = "h264_high"    # "h264_baseline" | "h264_main" | "h264_high" | "hevc_main" | "hevc_main10"
allow_frame_reordering = false    # false = lower latency (no B-frames)

[addon_module_vt_sw]
# VideoToolbox software fallback — second in the macOS SW auto order, after openh264.
# Uses Apple's tuned H.264 SW encoder. All keys are the same as [addon_module_vt_hw]
# — the parser treats vt_sw and vt_hw as schema-aliases. (Listed here for
# strict-validator clarity.)
realtime         = true
profile          = "h264_high"
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

[addon_module_nvfbc]
# NVIDIA NvFBC capture. Linux only (Windows uses DXGI DD).
output_index     = 0              # Which NVIDIA output to capture
capture_type     = "to_cuda"      # "to_cuda" | "to_sys" | "to_gl" — CUDA for direct NVENC pairing

[addon_module_wl_screencopy]
# Linux wlroots screencopy capture — NO ROOT. Headless / nested Wayland, or a host
# that cannot grant CAP_SYS_ADMIN. wlroots-family compositors only (sway, Hyprland,
# river, labwc, Wayfire); GNOME/KDE need pw_portal instead. Never selected ahead of
# kms_egl. Cannot deliver a separate cursor, so a session on this add-on always
# resolves cursorMode = "embedded". See WL_SCREENCOPY_LINUX_SPEC.md.
output_name      = ""             # wl_output name, e.g. "HEADLESS-1", "DP-1". "" = first output.
                                  # The per-capturer display selector; v1 captures exactly one
                                  # display; see MODULE_CAPTURE "Display selection".
protocol         = "auto"         # "auto" | "image_copy" | "export_dmabuf" | "screencopy".
                                  # Forcing one the compositor does not advertise is a probe
                                  # failure, never a silent fallback.
prefer_dmabuf    = true           # false forces the shm path (diagnostic only; costs zero-copy)

[addon_module_pw_portal]
# Linux xdg-desktop-portal ScreenCast capture — NO ROOT. The only no-root path on
# GNOME/KDE Wayland. Costs an INTERACTIVE CONSENT PROMPT on first launch; later
# launches reuse a restore token. Never selected ahead of kms_egl or wl_screencopy.
# See PW_PORTAL_LINUX_SPEC.md.
restore_token_file = ""           # "" = <state dir>/featherdesk/pw_portal.token (mode 0600).
                                  # Holds the portal restore token, which is a capability to
                                  # capture this user's screen — it is deliberately NOT stored
                                  # in this file, which is operator-authored and hot-reloaded.
allow_prompt       = true         # false = never open a consent dialog; with no valid token,
                                  # probe() reports available:false reason "consent_required"
                                  # so selection falls through. Set false on unattended hosts.
output_name        = ""           # Monitor-name hint where the backend honours one; the portal
                                  # remains the authority over source choice.

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
display_id       = 0              # 0 = main display (sentinel); else a CGDirectDisplayID from ProbeReport.displays[].id

# Whether the pointer is composited into the frame is NEVER a per-add-on key: it is
# CaptureConfig.embed_cursor, derived once from [capture] cursor_mode and the add-on's
# capability bits. Two switches for one question is how a stream ends up with a
# compositor cursor burned in AND a client overlay on top. See MODULE_CAPTURE
# "Cursor delivery and the separate / embedded decision".

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

# macOS kb/mouse is the in-core `enigo` default (CGEventPost) — NOT a separate
# add-on, and it has NO config section and NO keys: prompting for Accessibility
# permission is unconditional, not a knob (see CGEVENT_MACOS_SPEC).

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

[addon_module_opus]        # the audio codec add-on (`opus` ⇒ Opus, else PCM)
bitrate_kbps = 0      # 0 = auto by channel count (~96 stereo, scaled for 5.1/7.1); else fixed VBR target
complexity   = 8      # 0..10 libopus complexity; lower = less CPU, slightly lower quality

[addon_module_wasapi]      # Windows — WASAPI loopback
device = ""                # "" = default render endpoint; or a specific endpoint id

[addon_module_sck_audio]   # macOS — rides the sck screen-capture session
exclude_current_process = true  # don't capture FeatherDesk's own output

[addon_module_pipewire]    # Linux — PipeWire monitor source
target = ""                # "" = auto-detect the default sink's .monitor
```

---

## Key reference (types, defaults, ranges, reload)

One row per declared key. A key in the Schema with no row here is a spec defect. "Hot"
means the value takes effect on `SIGHUP` without a restart; every other key is
`(restart required)` and the reload rejects a file that changes it, leaving the previous
config fully in effect (see "Hot reload behavior"). "On invalid" is what happens when the
value is present but fails its rule; an **absent** key always takes its default silently.

| Key | Type | Default | Valid range / domain | Hot | On invalid |
|-----|------|---------|----------------------|-----|-----------|
| `server.base_path` | string | `"/"` | `"/"`, or a path beginning **and** ending with `/` (e.g. `"/desk/"`); no `..`, no query, no scheme. Sourced into `transport::Config`, not `server::Config` — the transport strips it (MODULE_TRANSPORT "Base path") | ❌ | startup error |
| `server.bind` | string | `"0.0.0.0:30084"` | `host:port`; host parses as an IP literal or a hostname; port 1–65535. One address, two listeners (TCP + UDP) | ❌ | startup error |
| `server.allow_origin` | string | `""` | `""` (same-origin) or `"*"` or an exact origin `scheme://host[:port]` | ✅ | startup error |
| `server.max_clients` | u32 | `25` | 1–100 | ✅ (new sessions) | startup error |
| `server.max_message_bytes` | u32 | `4096` | 256–65536 | ✅ (new lines) | startup error |
| `server.input_rate_limit` | u32 | `1000` | 1–100000 | ✅ (new sessions) | startup error |
| `server.keyframe_min_interval_ms` | u32 | `500` | 100–5000 | ✅ (next request) | startup error |
| `server.keyframe_request_burst` | u32 | `3` | 1–32 | ✅ (new sessions) | startup error |
| `server.keyframe_request_refill_ms` | u32 | `2000` | 100–60000 | ✅ (new sessions) | startup error |
| `server.shutdown_grace_ms` | u32 | `250` | 0–5000 | ✅ (read at shutdown) | startup error |
| `server.tls.cert` | path | `""` | `""` (self-signed) or a readable PEM chain; `cert` and `key` both empty or both set | ❌ (the path; the **file** at it is re-read) | startup error |
| `server.tls.key` | path | `""` | as `cert`; the key file must be mode 0600 (Unix) / owner-only ACL (Windows) | ❌ (the path; the **file** at it is re-read) | startup error |
| `server.tls.extra_sans` | array of string | `[]` | each entry a hostname or an IP literal; self-signed mode only | ❌ | startup error |
| `server.tls.rotate_before` | duration | `"3d"` | `"1h"`–`"7d"`, strictly less than the certificate's validity (≤ 14 d) | ❌ | startup error |
| `transport.keepalive_period` | duration | `"15s"` | parseable duration; strictly `< max_idle_timeout` | ❌ | startup error |
| `transport.max_idle_timeout` | duration | `"30s"` | parseable duration; strictly `> keepalive_period` | ❌ | startup error |
| `transport.ping_interval` | duration | `"2s"` | `"0s"` (app pings off; QUIC RTT only) or `"100ms"`–`max_idle_timeout` | ✅ (new sessions) | startup error |
| `transport.initial_max_data` | byte size | `"10MiB"` | parseable byte size; `≥ initial_max_stream_data` | ❌ | startup error |
| `transport.initial_max_stream_data` | byte size | `"1MiB"` | parseable byte size; `≤ initial_max_data` | ❌ | startup error |
| `transport.max_streams_bidi` | u32 | `16` | `≥ 3 + filetransfer.max_concurrent + filetransfer.queue_depth` (15 at the defaults) | ❌ | startup error |
| `transport.max_streams_uni` | u32 | `16` | ≥ 2 (bootstrap + cursor) | ❌ | startup error |
| `transport.enable_datagrams` | bool | `true` | MUST be `true` — video rides datagrams on the QUIC carrier | ❌ | startup error |
| `transport.fragment_reassembly_ms` | u32 | `17` | 1–1000 (17 ≈ one 60 fps frame; use 34 at 30 fps) | ❌ | startup error |
| `transport.datagram_send_queue_frames` | u32 | `8` | 2–64 whole access units | ✅ (new sessions) | startup error |
| `transport.audio_send_queue_chunks` | u32 | `25` | 2–64 whole chunks | ✅ (new sessions) | startup error |
| `transport.auth_deadline` | duration | `"5s"` | `"1s"`–`"60s"` | ✅ (new sessions) | startup error |
| `transport.websocket_fallback` | bool | `true` | — ; `false` makes `/ws` return `501` and UDP-blocked clients cannot connect at all | ❌ | startup error |
| `transport.ws_max_message_bytes` | byte size | `"16MiB"` | `≥ 16MiB`, the per-frame reassembly cap | ✅ (new sessions) | startup error |
| `transport.ws_send_queue_bytes` | byte size | `"8MiB"` | `"1MiB"`–`"64MiB"` | ✅ (new sessions) | startup error |
| `transport.reassembly_max_bytes` | byte size | `"4MiB"` | `"1MiB"`–`"64MiB"` | ✅ (new sessions) | startup error |
| `transport.join_idr_timeout` | duration | `"1s"` | `"100ms"`–`"10s"` | ✅ (next join) | startup error |
| `log.format` | enum | `"auto"` | `auto` \| `json` \| `text` | ✅ | startup error |
| `log.level` | enum | `"info"` | `debug` \| `info` \| `warn` \| `error` | ✅ | startup error |
| `log.output` | string | `"stderr"` | `stderr` \| `stdout` \| a writable file path | ✅ | startup error |
| `metrics.enabled` | bool | `true` | — | ❌ | startup error |
| `metrics.bind` | string | `"127.0.0.1"` | an IP literal; a non-loopback value logs a startup warning (the endpoint is plain HTTP with no auth) | ❌ | startup error |
| `metrics.port` | u16 | `9090` | 1–65535, must differ from the port in `server.bind` | ❌ | startup error |
| `metrics.path` | string | `"/metrics"` | begins with `/`, no query, no fragment | ❌ | startup error |
| `addons.dir` | string | `""` | `""` (the running executable's own `addons/`) or any absolute or relative path; a missing directory warns and yields no backends | ❌ | startup warning |
| `addons.abi_strict` | bool | `false` | — | ❌ | startup error |
| `capture.mode` | enum | `"auto"` | `auto` \| `forced` | ❌ | startup error |
| `capture.force_addon` | string | `""` | required if `mode = "forced"` (phase A); must name a loaded add-on ID (phase B, in `pipeline::new` after the add-ons dir is scanned) | ❌ | startup error |
| `capture.cursor_mode` | enum | `"auto"` | one of `auto`/`separate`/`embedded` (phase A). Phase B: `separate` requires a loaded, probing capture add-on reporting `AddonCaps::CURSOR`; `embedded` requires one reporting `AddonCaps::EMBED_CURSOR`; `auto` requires one reporting either | ✅ | startup error |
| `encode.mode` | enum | `"auto"` | `auto` \| `forced` | ❌ | startup error |
| `encode.force_addon` | string | `""` | required if `mode = "forced"` (phase A); must name a loaded add-on ID (phase B). The only way to reach `x264` | ❌ | startup error |
| `stream.width` / `stream.height` | u32 | `0` / `0` | `0` (display native) or ≥ 320 | ✅ | startup error |
| `stream.fps` | u32 | `60` | 1–240 | ✅ | startup error |
| `stream.bitrate_bps` | u32 | `0` | `0` (constant-QP mode) or ≥ 100000 (100 kbps minimum) | ✅ | startup error |
| `stream.qp` | u8 | `26` | 0–51 | ✅ | startup error |
| `stream.keyframe_interval` | u32 | `0` | `0` (on-demand only) or 1–600 frames | ✅ | startup error |
| `stream.idle_keyframe_ms` | u32 | `100` | 10–2000 | ✅ | startup error |
| `stream.bit_depth` | u8 | `8` | `8`, or `10` only together with `hdr = true` | ✅ | startup error |
| `stream.hdr` | bool | `false` | `true` forces `bit_depth = 10` and `color_space = "bt2020"` | ✅ | startup error |
| `stream.color_space` | enum | `"bt709"` | `bt709` (SDR) \| `bt2020` (HDR — requires `hdr = true`) | ✅ | startup error |
| `stream.chroma` | enum | `"420"` | `420` \| `422` \| `444` (422/444 auto-fall-back to 420 if unsupported) | ✅ | startup error |
| `stream.adaptive.enabled` | bool | `true` | — | ✅ | startup error |
| `stream.adaptive.interval_ms` | u32 | `100` | 20–1000 | ✅ | startup error |
| `stream.adaptive.min_bitrate_bps` / `max_bitrate_bps` | u32 | `1_000_000` / `25_000_000` | each ≥ 100000, and `min ≤ max` | ✅ | startup error |
| `stream.adaptive.loss_threshold_pct` / `recovery_threshold_pct` | **f64** | `5.0` / `1.0` | 0.0–100.0, `recovery ≤ loss` | ✅ | startup error |
| `stream.adaptive.fast_reduction_factor` / `adjustment_factor` | **f64** | `0.5` / `0.7` | 0.1–0.99 | ✅ | startup error |
| `stream.adaptive.recovery_factor` | **f64** | `1.1` | 1.01–2.0 | ✅ | startup error |
| `auth.mode` | enum | `"token"` | `none` \| `token` \| `password` \| `pin`; `none` prints the security warning | ✅ | startup error |
| `auth.token` | string | `""` | `""` (read `token_file`, else generate) or ≥ 32 characters | ✅ (new connections only) | startup error |
| `auth.token_file` | path | `""` | `""` or a path; read when `token = ""`, written mode 0600 when generating | ✅ (re-read) | startup error |
| `auth.session_ttl_minutes` | u32 | `60` | 1–1440 | ✅ | startup error |
| `auth.password_hash` | string | `""` | required when `mode = "password"`; a well-formed `$argon2id$` PHC string | ✅ | startup error |
| `auth.pin_length` | u8 | `8` | 6–12 | ✅ | startup error |
| `auth.pairing_window_minutes` | u32 | `5` | 1–60 | ✅ | startup error |
| `auth.max_pin_attempts` | u32 | `10` | 1–100, counted process-globally per pairing window | ✅ | startup error |
| `auth.paired_devices_file` | path | `""` | `""` or a writable path; created mode 0600 | ✅ (contents re-read) | startup error |
| `auth.require_auth_for_view` | bool | `true` | — ; `false` admits an unauthenticated client with `max_role = view`, never more | ✅ | startup error |
| `auth.allow_takeover` | bool | `false` | — | ✅ | startup error |
| `reconnect.enabled` | bool | `true` | — | ❌ | startup error |
| `reconnect.cache_ttl_seconds` | u32 | `300` | 0–3600 (`0` disables resume) | ❌ | startup error |
| `reconnect.require_same_auth` | bool | `true` | — | ❌ | startup error |
| `input.enabled` | bool | `true` | — ; with no input add-on loaded the session is view-only | ❌ | startup warning |
| `input.relative_mouse` | bool | `true` | — | ❌ | startup error |
| `clipboard.enabled` | bool | `false` | — | ❌ | startup error |
| `clipboard.direction` | enum | `"bidirectional"` | `bidirectional` \| `client_to_host` \| `host_to_client` \| `disabled` | ✅ | startup error |
| `clipboard.max_bytes` | usize | `1048576` | 1024 – 4194304 (4 MiB), **and** `6 × max_bytes + 1024 ≤ min(transport.ws_send_queue_bytes, transport.ws_max_message_bytes)` — the clipboard wire cap is `6 ×` the content cap (JSON escapes a control byte as `\u00XX`), so a legal payload at the content cap must still fit the fallback carrier. 6 MiB + 1 KiB at the defaults, inside the 8 MiB out-queue; raising this to 4 MiB requires raising both transport keys to ≥ 24 MiB + 1 KiB (`25166848` bytes) — the `+ 1024` is part of the bound, so `24MiB` exactly still fails validation | ✅ | startup error |
| `clipboard.formats` | array of string | `["text","html"]` | a non-empty subset of `["text","html"]` | ✅ | startup error |
| `filetransfer.enabled` | bool | `false` | — | ❌ | startup error |
| `filetransfer.incoming_dir` / `outgoing_dir` | path | `""` / `""` | empty (default) or an existing directory openable as a `cap_std::fs::Dir` at startup; on Windows resolvable with the `\\?\` prefix | ❌ | startup error |
| `filetransfer.max_file_bytes` | u64 | `0` | `0` (unlimited) or ≥ 1024 | ✅ (new transfers) | startup error |
| `filetransfer.max_concurrent` | u32 | `4` | 1–16 | ✅ (new transfers) | startup error |
| `filetransfer.queue_depth` | u32 | `8` | 0–64; `max_concurrent + queue_depth` is advertised as `config.fileStreamBudget` | ❌ | startup error |
| `filetransfer.rate_limit_bps` | u64 | `0` | `0` (unlimited) or ≥ 8192 | ✅ | startup error |
| `gamepad.enabled` | bool | `false` | — ; with no gamepad-capable add-on (`vigem`/`uinput`/`gcvirtual`) loaded, gamepad records are dropped | ❌ | startup warning |
| `gamepad.max_controllers` | u32 | `4` | 1–4 (the XInput cap, and the co-op player cap) | ❌ | startup error |
| `gamepad.allow_rumble` | bool | `true` | — ; inert unless the injector reports `AddonCaps::RUMBLE` | ❌ | startup error |
| `gamepad.allow_coop` | bool | `false` | — | ❌ | startup error |
| `audio.enabled` | bool | `false` | — ; with no audio capture add-on (`wasapi`/`sck_audio`/`pipewire`) loaded, audio stays off | ❌ | startup warning |
| `audio.frame_ms` | u32 | `20` | `10` or `20` | ❌ | startup error |
| `audio.channels` | enum | `"auto"` | `auto` (follow the host output layout, ≤ 7.1) \| `stereo` (force a 2.0 downmix) | ❌ | startup error |
| `[addon_module_<id>]`, any key | add-on-owned | add-on-owned | the add-on owns the *decoding*; the host forwards the table verbatim, never decodes it, and caps the re-serialized fragment at 64 KiB. **Key names, defaults and domains are normative in the Schema block above**; an add-on spec may restate them for readability but may not diverge, and where it does, this file wins | ❌ (read once, at `construct()`) | startup error — `AbiErr::BadConfig` from the add-on, naming the id, the key and the config file path |
| `addon_module_x264.profile` | enum | `"high"` | `high` \| `high422` \| `high444` — the chroma negotiation selects it; this is the ceiling | ❌ | startup error (`AbiErr::BadConfig`, raised by the add-on) |
| Unknown key anywhere | — | — | — | — | startup error |

---

## Default config

If the binary is started with `--config` pointing at a file that exists but
is empty, every section above defaults as shown in the schema example. If
the file does not exist, the binary writes a fully-defaulted config to that
path and continues startup (development convenience; for production deploy,
provision the file via configuration management before starting).

---

## Hot reload behavior

On SIGHUP (or the Windows service equivalent) `config::watch` re-reads and
**revalidates the entire file**, then publishes it on a `watch` channel. Partial
application is never observable — either the new config is fully valid and every
applier below sees it, or the reload is rejected with the previous config still in
effect.

Every ✅ row names the code that applies it and which of the two applier sites
runs it. There are exactly **two** receiving halves of the reload watch, and no row
of this table may name a third:

1. the pipeline's **`fd-config` task** (`pipeline::config_applier`, MODULE_PIPELINE
   startup step 12b), which holds a `cfg_tx.subscribe()` taken BEFORE the sender is
   moved into the `config::watch` callback; and
2. the **frame loop's step-(0d) applier**, which holds `FrameLoop.cfg_rx`.

The producer for both is the single `config::watch` registration in
`Pipeline::start`. The `fd-config` task runs its ✅ rows in the order they appear
below, diffing the new `Arc<Config>` against the config it last applied, so an
unchanged section costs nothing; an applier error is logged at `error` and never
propagated, leaving the previous value in force. A key with no applier is `❌`, not
`✅`.

| Section / key | Hot | Applier — and where it runs |
|---|---|---|
| `[server]` `bind` | ❌ | restart required (both listeners are bound once, at startup) |
| `[server.tls]` `cert` / `key` **paths** | ❌ | restart required — the paths select the trust mode (CA-trusted vs self-signed) |
| `[server.tls]` certificate + key **file contents** | ✅ | `Server::reload_tls`, on the `fd-config` task. The Server is the sole owner of the Transport, so it is the only object that can forward the call; it re-reads the files at the startup paths and performs the same in-place `ServerConfig` key swap the self-signed auto-rotation already does. On error the previous key stays in place and the error is logged. In-flight sessions are untouched; new handshakes present the new chain |
| `[server.tls]` `extra_sans` / `rotate_before` | ❌ | restart required |
| `[auth]` (every key) | ✅ | `Server::set_auth_policy(auth::reload(&cfg.auth)?, cfg.auth.allow_takeover)`, on the `fd-config` task. `require_auth_for_view` is NOT an argument — it is evaluated inside the Authenticator, so the replacement already carries it. That one call also clears the `SessionCache` internally; `SessionCache::clear` is Server-private on `Config.session_cache`, which has no accessor, so there is no separate `SessionCache::clear()` call site anywhere. Existing sessions survive and are not re-validated; they cannot **resume** afterwards without the new credential. A reloaded `token = ""` keeps the token generated at startup — it never mints a new one, because the generated token is retained in the `auth` crate's `OnceLock<String>` |
| `[server]` `allow_origin` | ✅ | a field of `server::SessionDefaults`, filled at MODULE_PIPELINE step 7 and replaced wholesale by `Server::set_session_defaults` (an `ArcSwap`) on the `fd-config` task. It is loaded from the `ArcSwap` on every WebTransport or WebSocket upgrade — i.e. before a session exists — so the origin check is armed from the first connection, not from the first reload. There is no separate `allow_origin` setter |
| `[server]` `max_clients` / `input_rate_limit` / `max_message_bytes` / `keyframe_min_interval_ms` / `keyframe_request_burst` / `keyframe_request_refill_ms` / `shutdown_grace_ms` | ✅ | the same `Server::set_session_defaults` call, read when the next session is accepted; never disconnects an existing session |
| `[transport]` flow control, stream caps, `enable_datagrams`, `keepalive_period`, `max_idle_timeout`, `websocket_fallback` | ❌ | restart required (set on the QUIC endpoint and the HTTP router at bind) |
| `[transport]` `datagram_send_queue_frames` / `audio_send_queue_chunks` / `ws_max_message_bytes` / `ws_send_queue_bytes` / `reassembly_max_bytes` / `auth_deadline` / `ping_interval` / `join_idr_timeout` | ✅ | the same `Server::set_session_defaults` call; applies to sessions accepted after the reload — a running session's ring capacity is fixed at accept |
| `[transport]` `fragment_reassembly_ms` | ❌ | restart required — it is a CLIENT-side deadline and v1 has no message that re-advertises it mid-session |
| `[clipboard]` `direction` / `formats` / `max_bytes` | ✅ | the same `Server::set_session_defaults` call (the gating), then `ClipboardHandle::set_config(cfg)`, both on the `fd-config` task. `cfg` is a `clipboard::Config` that task builds from the reloaded `direction` / `formats` / `max_bytes` — `set_config` takes a `clipboard::Config`, not a config section |
| `[clipboard]` `enabled` | ❌ | restart required (the driver task and its OS connection are created once). It IS a field of `clipboard::Config`, but it is not part of the *reload*. The owner of the startup value is `pipeline::config_applier`, which captures the `clipboard::Config` built at startup step 9 as a baseline and, on every reload, CLONES that baseline and overwrites only `direction` / `formats` / `max_bytes` from the reloaded section. `enabled` therefore reaches `set_config` unchanged from startup by construction, and there is no path by which a reload can flip it |
| `[filetransfer]` `max_concurrent` / `rate_limit_bps` / `max_file_bytes` | ✅ | `filetransfer::Service::set_limits(max_concurrent, rate_limit_bps, max_file_bytes)`, on the `fd-config` task — exactly those three keys, and they bound only what the Service will SERVE at any instant |
| `[filetransfer]` `queue_depth` | ❌ | restart required — it sizes the accepted-but-waiting FIFO the Service allocates once, at construction, and it is the term of `config.fileStreamBudget` that tells an already-connected client how many transfer streams it may leave OUTSTANDING; changing it mid-session desyncs a number the client is holding |
| `[filetransfer]` `enabled` / `incoming_dir` / `outgoing_dir` | ❌ | restart required (the sandbox directory handles are opened once) |
| `[stream.adaptive]` | ✅ | `stream::Manager::set_policy`, on the `fd-config` task, under the Manager's mutex — reached through `Pipeline.manager`, the same `Arc<Mutex<Box<dyn stream::Manager>>>` step 12 hands to `set_stream_params_callback` |
| `[stream]` `width` / `height` / `fps` / `bitrate_bps` / `qp` / `hdr` / `bit_depth` / `color_space` / `chroma` / `keyframe_interval` | ✅ | the `fd-config` task turns the diff into `stream::ParamDelta`s and pushes them through `stream::Manager::apply` — the **same funnel** a client `resize` uses, so they are applied on the frame-loop thread with the same clamping, IDR and config gating. `hdr` / `bit_depth` / `color_space` are not independently settable at runtime (validation pins the triple), so the whole triple travels as ONE `ParamDelta::Hdr { on }`; there is no `ParamDelta::BitDepth` or `ParamDelta::ColorSpace` |
| `[stream]` `idle_keyframe_ms` | ✅ | the frame loop's step-(0d) applier, which assigns it to `FrameLoop.idle_keyframe_ms`. It is this loop's own keepalive TIMER, not a `stream::Params` field: it never travels as a `ParamDelta` and never goes through the Manager |
| `[capture]` `cursor_mode` | ✅ | the frame loop's step-(0d) applier: `FrameLoop::resolve_cursor_mode` re-runs `CursorMode::resolve` against the CONSTRUCTED capture handle's `caps()`, the capturer is rebuilt only if `CaptureConfig.embed_cursor` changes value, the cached IDR is invalidated, and a fresh `config` is pushed when the RESOLVED `cursorMode` changes |
| `[capture]` `mode` / `force_addon`, `[encode]` `mode` / `force_addon` | ❌ | restart required — swapping the capture or encode add-on means re-probing and rebuilding the whole frame path, and Refactoring Principle 4 forbids it |
| `[log]` | ✅ | `config::LogReload` — the boxed applier `main.rs` builds over the `tracing_subscriber::reload::Layer` it installed at R-PIP-01 step 4 and hands to `pipeline::new`; the `fd-config` task calls it LAST, with the reloaded `[log]` section. It is the only route from the reload watch to the subscriber. In-flight writes complete on the old handler |
| `[metrics]` (every key) | ❌ | restart required — no component in this tree constructs or owns the Prometheus exporter listener, so a reload has nothing to start, stop or re-point |
| `[addons]` | ❌ | restart required — the add-on set is resolved once (CENTRAL_SPEC "Hot-swap = drop a library + restart") |
| `[input]`, `[gamepad]`, `[audio]`, `[reconnect]` | ❌ | restart required — they select which add-ons are loaded and constructed |
| `[addon_module_*]` | ❌ | restart required — the section is handed to the add-on once, at `construct()` |

---

## Implementation outline

```rust
// crate: featherdesk-config
use serde::Deserialize;
use std::{collections::HashMap, path::Path};

#[derive(Clone, Deserialize)]
pub struct Config {
    pub server: ServerSection,
    pub transport: TransportSection,
    pub log: LogSection,
    pub metrics: MetricsSection,
    pub addons: AddonsSection,        // [addons] dir / abi_strict
    pub capture: CaptureSection,      // mode, force_addon, cursor_mode
    pub encode: EncodeSection,
    pub stream: StreamSection,        // dynamic params: width/height/fps/bitrate/qp/hdr
    pub auth: AuthSection,            // mode, password_hash, token, pin_*
    pub reconnect: ReconnectSection,  // cache_ttl_seconds, require_same_auth
    pub input: InputSection,          // enabled, relative_mouse
    pub clipboard: ClipboardSection,  // enabled, direction, max_bytes, formats
    pub filetransfer: FileTransferSection, // enabled, dirs, caps
    pub gamepad: GamepadSection,      // enabled, max_controllers, allow_rumble, allow_coop
    pub audio: AudioSection,          // enabled, frame_ms (design locked; impl deferred)
    // EVERY core section above MUST have a field here. The `#[serde(flatten)]` below is a
    // catch-all, and serde forbids `deny_unknown_fields` alongside `flatten`, so there is
    // no strictness at the TOP level: a core section with no field is NOT rejected — it is
    // silently swallowed into `addon_modules` and then dropped by phase B, because its
    // name matches no add-on id. Strictness exists only INSIDE each section. Adding a
    // section to the schema without adding it here is therefore a silent data loss, not a
    // parse error, and no test catches it. (This is what happened to `[addons]`.)
    #[serde(flatten)]
    pub addon_modules: HashMap<String, toml::Value>,
}

/// AddonsSection maps the [addons] schema.
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AddonsSection {
    /// "" = `<the running executable's own directory>/addons`; or any absolute or
    /// relative path. Resolved once at startup (restart required).
    pub dir: String,
    /// true = a single ABI-mismatched library aborts startup; false = skip it with a warning.
    pub abi_strict: bool,
}

/// TransportSection maps the [transport] schema. Duration + byte-size values are
/// written as TOML strings ("15s", "10MiB"), so the fields use small wrapper
/// types whose Deserialize impl parses the string — serde/`toml` will not decode
/// "15s" into a bare std::time::Duration or "10MiB" into a u64 on its own.
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TransportSection {
    pub keepalive_period: Duration,
    pub max_idle_timeout: Duration,
    pub ping_interval: Duration,
    pub initial_max_data: ByteSize,
    pub initial_max_stream_data: ByteSize,
    pub max_streams_bidi: u32,
    pub max_streams_uni: u32,
    pub enable_datagrams: bool,
    pub fragment_reassembly_ms: u32,
    pub datagram_send_queue_frames: u32,
    pub audio_send_queue_chunks: u32,
    pub auth_deadline: Duration,
    pub websocket_fallback: bool,
    pub ws_max_message_bytes: ByteSize,
    pub ws_send_queue_bytes: ByteSize,
    pub reassembly_max_bytes: ByteSize,
    pub join_idr_timeout: Duration,
}

/// AuthSection maps the `[auth]` schema. Every field below is one row of the
/// `[auth]` block in "Configuration Schema" — that table remains the sole owner of
/// defaults, ranges and hot/cold status; this struct only gives them a Rust name.
/// `mode` is carried as a `String` on purpose: `featherdesk-config` does not depend
/// on `featherdesk-auth`, so `auth::new` maps it to `auth::Mode` and an unrecognised
/// value is the startup error the schema table already specifies.
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AuthSection {
    pub mode: String,                 // "none" | "token" | "password" | "pin"
    pub token: String,
    pub token_file: String,
    pub session_ttl_minutes: u32,
    pub password_hash: String,
    pub pin_length: u8,
    pub pairing_window_minutes: u32,
    pub max_pin_attempts: u32,
    pub paired_devices_file: String,
    pub require_auth_for_view: bool,
    pub allow_takeover: bool,
}

/// Duration and ByteSize wrap their underlying values and implement Deserialize
/// via a string parse ("15s" → std::time::Duration; "10MiB" → bytes).
#[derive(Clone, Copy, Deserialize)]
pub struct Duration(pub std::time::Duration);
#[derive(Clone, Copy, Deserialize)]
pub struct ByteSize(pub u64);

// `Clone` is NOT optional anywhere in this schema. `Pipeline::start`'s watch
// callback is `move |c| { let _ = cfg_tx.send(Arc::new(c.clone())); }` over a
// `&Config`, and that one line is what feeds both appliers — without `Clone` it
// does not compile. So `Config` and EVERY section struct in the public schema
// derive it: `ServerSection`, `TlsSection`, `TransportSection`, `LogSection`,
// `MetricsSection`, `AddonsSection`, `CaptureSection`, `EncodeSection`,
// `StreamSection`, `AdaptiveSection`, `AuthSection`, `ReconnectSection`,
// `InputSection`, `ClipboardSection`, `FileTransferSection`, `GamepadSection`
// and `AudioSection`, plus `Clone + Copy` on the `Duration` and `ByteSize`
// newtypes.

/// load does PHASE-A validation only (syntax, defaults, intra-section rules). It
/// captures unknown [addon_module_*] sections RAW (as toml::Value) instead of
/// failing on them — their strict decode is deferred to phase B, when the loaded
/// add-on set is known. Errors are clear and actionable (file + line + key).
pub fn load(path: &Path) -> Result<Config, ConfigError> { /* … */ }

/// validate does PHASE-B (load-aware) validation: force_addon must name a loaded
/// add-on ID, cursor_mode must be satisfiable by a probing capture add-on's caps,
/// and each captured [addon_module_<id>] table whose add-on is loaded is
/// re-serialized into the `RAddonConfig.section_toml` the pipeline passes to that
/// add-on's `construct()`; tables whose add-on is not loaded are dropped. The host
/// does not decode them — the schema is inside the add-on (see MODULE_ABI "Add-on
/// configuration"). Called from pipeline::new after the add-ons directory has been
/// scanned.
pub fn validate(cfg: &Config, loaded: &AddonSet) -> Result<(), ConfigError> { /* … */ }

/// watch installs the SIGHUP handler (Windows: the service control equivalent)
/// and calls `f` on every successful, fully-revalidated reload. `f` must not
/// block: the only thing the pipeline's `f` does is publish the new `Arc<Config>`
/// on a `tokio::sync::watch` channel, read by the pipeline's `fd-config` applier
/// task and by the frame loop's step-(0d) applier — the only two subscribers in
/// the system.
///
/// Called exactly once, from `Pipeline::start` (MODULE_PIPELINE "Public
/// Interface"). A reload that fails validation is logged at `error` and dropped;
/// the previous config stays in effect and `f` is not called, so a partially-valid
/// file never reaches an applier.
pub fn watch(cancel: CancellationToken, path: &Path, f: impl Fn(&Config) + Send + 'static) -> Result<(), ConfigError> { /* … */ }

/// LogReload is the `[log]` hot-reload applier, boxed so the
/// `tracing_subscriber` generic parameters stay inside `main.rs`. `main.rs`
/// installs the `tracing_subscriber::reload::Layer` at R-PIP-01 step 4 and
/// builds this over its `reload::Handle`; `pipeline::new` takes it, `Pipeline`
/// holds it, and the `fd-config` task calls it with the reloaded `[log]`
/// section. It is the ONLY route from the reload watch to the subscriber, and
/// the `[log]` row of "Hot reload behavior" names it.
pub struct LogReload(
    pub Box<dyn Fn(&LogSection) -> Result<(), ConfigError> + Send + Sync>,
);
```

The TOML parser is the `toml` crate (+ `serde`) with strict mode
(`#[serde(deny_unknown_fields)]`) on each known section; `[addon_module_*]`
sections are captured as `toml::Value` (via `#[serde(flatten)]`) and, in phase B,
re-serialized as a TOML fragment and handed to the owning add-on, which
strict-decodes them against its own schema (so a misspelled add-on id in a section
name is silently ignored, not flagged — verify the add-on logged its loaded config).

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | argv dispatch: `hash-password` exits before any config read; an unknown subcommand and an unknown flag both exit 2; a bare `featherdesk` uses the OS-conventional config path | No |
| Unit | `Duration`/`ByteSize` string-form parsing (`"15s"`, `"10MiB"`) succeeds and rejects a bare unitless number | No |
| Unit | Every rule in the Key reference table independently: e.g. `stream.fps` outside 1–240, `transport.enable_datagrams = false`, `metrics.port == server.bind` port | No |
| Unit | Every top-level table in the schema example has a matching field on `struct Config` — a section with no field is silently swallowed, so this is asserted mechanically, not by review | No |
| Unit | Unknown key anywhere in a known section fails parsing with a file+line+key error, never a silent default | No |
| Unit | `[addon_module_<id>]` phase-A capture as raw `toml::Value` never fails parsing regardless of its contents (deferred to phase B) | No |
| Integration | Phase-B validation: an `[addon_module_x264]` section is forwarded verbatim as `section_toml` when `x264` is loaded, is dropped when it is not, and an unknown key inside it fails startup via `AbiErr::BadConfig` naming the key; `encode.force_addon` naming an unloaded add-on ID fails startup | No |
| Integration | Phase-B capability cross-check: `capture.cursor_mode = "separate"` with no loaded add-on reporting `AddonCaps::CURSOR` fails startup, naming each add-on it rejected and why | No |
| Integration | Missing config file at the resolved default path: binary writes a fully-defaulted config and continues; empty-but-existing file also defaults every section | No |
| Integration | Hot reload (SIGHUP) atomicity: an invalid reloaded file leaves the previous config fully in effect (no partial application observable) | No |
| Integration | Hot reload applies `[log]`, `[stream]`, `[auth]` and `[clipboard]` gating changes live, and re-reads the `[server.tls]` certificate and key **files** without a rebind; confirms `[server] bind`, `[capture]`/`[encode]` `mode` and `force_addon`, `[metrics]` and `[filetransfer] queue_depth`, and the `[transport]` flow-control keys are rejected without a restart | No |
| Integration | Every ✅ row of "Hot reload behavior" has a call site: the `fd-config` task (MODULE_PIPELINE step 12b) or the frame loop's step-(0d) applier. Asserted mechanically against the table, not by review | No |
| Integration | A SIGHUP whose `[auth] token` is still `""` keeps the token generated at startup and never mints a new one | No |
| Integration | `[gamepad].enabled` / `[audio].enabled` / `[input].enabled` with no matching add-on loaded degrades to a startup warning, not a hard error | No |

---

## Status

📋 **Specced — not yet implemented.** Existing code reads CLI flags in
`src/main.rs`. Migration:
1. Add the `featherdesk-config` crate with the types above.
2. Replace flag parsing in `main.rs` with `config::load`, behind the `argv[1]`
   subcommand dispatch in "CLI surface".
3. Update probe + selection logic to honor `capture.force_addon` / `encode.force_addon` when set.
4. Wire `SIGHUP` (Linux/macOS) and Service Control (Windows) inside `config::watch`;
   the pipeline registers it in `start()`.
5. Delete legacy flags from `main.rs`.

Implementation lands in the same sprint that wires the first cross-platform
binary (after the Foundation stage in the implementation plan).
