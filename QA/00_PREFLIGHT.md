# QA 00 — Preflight: build, install, config, add-on loading

Everything that must be true before a single frame is worth looking at. This
area has **no user-story coverage** — the 90 stories start at "a client
connects" and assume a running host — so these checks exist only here.

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-PRE-1 | Build the workspace from a clean checkout on a machine with no prior Rust artifacts | Builds with the documented toolchain and no manual steps beyond installed system deps. Record the exact toolchain version used | CENTRAL_SPEC "Implementation Language & Conventions" | ☐ |
| QA-PRE-2 | Build each add-on separately (`cargo build --release -p featherdesk-addon-<id>`) | Each produces `featherdesk-addon-<id>.{so,dylib,dll}`. **No add-on's native dependency is linked into the host binary** — check with `ldd`/`otool -L`/`dumpbin` that the host does not pull in libva, NVENC, PipeWire, etc. | CENTRAL_SPEC "Add-on loading model" | ☐ |
| QA-PRE-3 | Run the host with an **empty** add-ons directory | Starts, warns, serves the page, and reports no capture backend rather than crashing | CENTRAL_SPEC "Add-on directory" | ☐ |
| QA-PRE-4 | Run the host with a **missing** add-ons directory | Treated as empty with a warning — not a startup failure | `MODULE_CONFIG` `[addons] dir` | ☐ |
| QA-PRE-5 | Default `[addons] dir` resolution: move the binary + its `addons/` folder to an arbitrary path and run | Add-ons load from `addons/` next to the **executable**, not the working directory. Confirm by running from a different cwd | CENTRAL_SPEC "Add-on directory (portable)" | ☐ |
| QA-PRE-6 | Drop in an add-on built against a **different** `ABI_VERSION` | Skipped with a warning; the host still starts. With `[addons] abi_strict = true`, startup aborts instead | `MODULE_ABI` "ABI versioning" | ☐ |
| QA-PRE-7 | Drop in an add-on built for the **wrong OS or arch** | Rejected on `descriptor().os`/`.arch` mismatch, with a log line naming which | `MODULE_ABI`; US-XP-12 | ☐ |
| QA-PRE-8 | Drop in a deliberately corrupt/truncated `.so` | Load failure is caught and named; the host continues with the remaining add-ons | `MODULE_ABI` "load-failure taxonomy" | ☐ |
| QA-PRE-9 | An add-on's `tracing` output appears in the host's log | Proves `init(host)` called `install_log_sink` **first**. An add-on whose logs are silent is a conformance failure even if it works | CENTRAL_SPEC "Where to register a new add-on" step 4 | ☐ |
| QA-PRE-10 | Start with an **invalid** config (unknown key in a known section) | Startup fails with a message naming the key and section. **A misconfigured server must not start** | `MODULE_CONFIG` "Validation" | ☐ |
| QA-PRE-11 | Config contains an `[addon_module_x]` section for an add-on that is **not loaded** | Silently ignored — not a startup error. This is what lets one config file serve many add-on sets | `MODULE_CONFIG` "Section naming convention" | ☐ |
| QA-PRE-12 | Config contains an unknown key in a section for an add-on that **is** loaded | `AbiErr::BadConfig` (10) — the **add-on** validates its own section, the host has no schema for it | `MODULE_ABI` "Add-on configuration" | ☐ |
| QA-PRE-13 | `SIGHUP` with a changed reloadable key (e.g. `[clipboard] direction`) | Applied live, logged, no session dropped | `MODULE_CONFIG` "Hot reload behavior" | ☐ |
| QA-PRE-14 | `SIGHUP` with a changed **restart-required** key (e.g. `server.bind`, `force_addon`) | Ignored for this process with a clear log line saying a restart is required — never silently half-applied | `MODULE_CONFIG` reload table | ☐ |
| QA-PRE-15 | `SIGHUP` with a **broken** config file | Previous config stays in force, error logged, process keeps running. A bad edit must never take the host down | `MODULE_CONFIG` "Reload" | ☐ |
| QA-PRE-16 | Subcommands dispatch before config load: `hash-password`, `revoke-device`, `list-devices` | Each works without a valid `[server]`/`[capture]` config present | `MODULE_CONFIG` "CLI surface" | ☐ |
| QA-PRE-17 | `--config <path>` is the only flag | Any other flag is rejected with usage; config is read from the given path | `MODULE_CONFIG` "CLI surface" | ☐ |
| QA-PRE-18 | Linux: `setcap cap_sys_admin+p` then run **unprivileged** with `kms_egl` | Capture works without full root | `LINUX_SPEC.md` | ☐ |
| QA-PRE-19 | Linux **without** the capability, `kms_egl` loaded | `probe()` reports `available:false` with a reason; selection falls through to a no-root add-on if one is loaded, else a clean fatal "no capture" | `LINUX_SPEC.md` "Runtime probe order" | ☐ |
| QA-PRE-20 | **Linux headless (L4):** forced connector (`video=HDMI-A-1:1920x1080e`), compositor running, no monitor attached | `kms_egl` captures normally. This is the corrected headless story — confirm it works as documented | `PLATFORM_COMPAT` "Headless on Linux" | ☐ |
| QA-PRE-21 | **Linux with Xvfb only**, `kms_egl` loaded | `probe()` reports `available:false` (no CRTC with a mode) and selection falls through. It must **not** select and then return empty frames | `KMS_EGL_LINUX_SPEC` "Requires a real KMS CRTC" | ☐ |
| QA-PRE-22 | **L3 no-root:** wlroots compositor, `wl_screencopy` only, no `CAP_SYS_ADMIN` | Capture works with zero privilege; session resolves `cursorMode = "embedded"` | `WL_SCREENCOPY_LINUX_SPEC` | ☐ |
| QA-PRE-23 | `pw_portal` first launch with `allow_prompt = true` | Consent dialog appears; after approval a restore token is written **mode 0600** | `PW_PORTAL_LINUX_SPEC` | ☐ |
| QA-PRE-24 | `pw_portal` second launch | Starts with **no prompt** using the stored token, and writes back the rotated replacement | `PW_PORTAL_LINUX_SPEC` | ☐ |
| QA-PRE-25 | `pw_portal` with `allow_prompt = false` and no token | `probe()` reports `available:false`, reason `consent_required`. **No dialog appears** — critical on an unattended host | `PW_PORTAL_LINUX_SPEC` | ☐ |
| QA-PRE-26 | **W2 headless Windows:** first launch with no physical display | IddCx virtual display auto-installs with exactly **one** UAC prompt; subsequent launches prompt none | `DXGI_DD_WINDOWS_SPEC` | ☐ |
| QA-PRE-27 | Windows: host process is `PER_MONITOR_AWARE_V2` | Asserted at startup; `dxgi_dd.probe()` reports unavailable if not. Verify advertised dims match panel pixels on a scaled display | CENTRAL_SPEC Windows capture note | ☐ |
| QA-PRE-28 | macOS: unsigned add-on in the add-ons directory | Rejected/blocked as documented rather than crashing the host | `MACOS_SPEC` "Add-ons and the hardened runtime" | ☐ |
| QA-PRE-29 | macOS: Accessibility permission not yet granted | Prompt appears at startup; input is refused until granted, with a clear message | `CGEVENT_MACOS_SPEC` | ☐ |
| QA-PRE-30 | Metrics endpoint reachable on its configured bind; a **non-loopback** `metrics.bind` | Startup prints the documented warning | `MODULE_CONFIG` `[metrics]` | ☐ |
| QA-PRE-31 | Graceful shutdown (SIGTERM / Ctrl-C) with clients attached | `{"type":"server_shutdown"}` reaches every session, grace period observed, then close `4503`. No panic, no orphaned add-on process (check for a stray `ffmpeg` if `x264` was in use) | `MODULE_SERVER` R-SRV-05 | ☐ |
| QA-PRE-32 | RSS after 10 minutes streaming to one client | Within the stated `<50 MB` target, and **not growing** across the window | CENTRAL_SPEC "Product Goals" | ☐ |
