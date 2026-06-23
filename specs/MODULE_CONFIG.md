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
#   auto    = probe in default order (see MODULE_CAPABILITIES.md when written)
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "kms_egl", "nvfbc", "sck" (restart required)

[encode]
# Mode controls how the runtime picks among compiled-in encoder add-ons.
#   auto    = HW HEVC → HW H.264 → SW H.264 probe order
#   forced  = use force_addon only, fail at startup if unavailable
mode         = "auto"
force_addon  = ""                 # e.g. "libva", "nvenc", "vt_hw", "openh264"
fps          = 60
# Rate control: if bitrate_bps > 0 use bitrate target; else use fixed QP.
bitrate_bps  = 0
qp           = 23                 # H.264 range 0–51, lower = higher quality

[encode.cursor]
mode = "separate"                 # "separate" (client renders) | "embedded" (server blends)

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
