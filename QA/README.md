# FeatherDesk QA — Manual Verification Checklists

Hands-on checklists for verifying a built FeatherDesk behaves the way `specs/`
says it should. **Nothing here is runnable today** — no code exists on this
branch (`BRANCH.md` "Current Status"). These are written now, against the specs,
so that the acceptance bar is fixed *before* the implementation can argue with
it.

## How this differs from the two artifacts that already exist

Three layers, three audiences. Do not merge them.

| Layer | Where | Who runs it | What it proves |
|-------|-------|-------------|----------------|
| **Testing Strategy tables** | each `specs/**/MODULE_*.md` | CI, automatically | A module honours its own contract |
| **User stories** | `PROJECT_ARTIFACTS/user_stories/` (90, across 8 areas) | whoever writes acceptance tests | A described behaviour has a witness |
| **These checklists** | `QA/` | a human, on real hardware, before a release | The assembled product works, on machines CI does not have, in failure modes CI does not stage |

A checklist item that is fully covered by an automated test does not belong
here. What belongs here is what a human must see: a real GPU, a real second
browser, a pulled network cable, a driver install prompt, a picture that looks
right.

## Files

| File | Area | Matching user stories |
|------|------|----------------------|
| [`00_PREFLIGHT.md`](./00_PREFLIGHT.md) | Build, install, config, add-on loading, first run | — (no story area covers install) |
| [`01_CONNECT_AUTH.md`](./01_CONNECT_AUTH.md) | Carriers, TLS, auth modes, pairing, resume, `base_path` | `01_connection_and_auth.md` |
| [`02_VIDEO.md`](./02_VIDEO.md) | Capture, encode, cursor, keyframes, HDR, chroma, resolution | `02_video_streaming.md` |
| [`03_INPUT.md`](./03_INPUT.md) | Keyboard, mouse, touch, gamepad, coordinate accuracy | `04_input_control.md` |
| [`04_MULTICLIENT.md`](./04_MULTICLIENT.md) | Roles, takeover, limits, per-user caps, egress | `05_multi_client.md` |
| [`05_CLIPBOARD_FILES.md`](./05_CLIPBOARD_FILES.md) | Clipboard both directions, file transfer both directions | `06_clipboard_and_filetransfer.md` |
| [`06_RESILIENCE.md`](./06_RESILIENCE.md) | Loss, reconnect, crash recovery, idle suspension, failover | `07_resilience_and_reconnection.md` |
| [`07_AUDIO.md`](./07_AUDIO.md) | Host audio, A/V sync, teardown | `03_audio.md` |
| [`08_PERFORMANCE.md`](./08_PERFORMANCE.md) | Latency budget, parity gate, bandwidth, resource use | — |
| [`09_SECURITY.md`](./09_SECURITY.md) | Auth boundaries, add-on trust, metrics exposure, sanitisation | — |
| [`10_REGRESSION_TD.md`](./10_REGRESSION_TD.md) | **The 40 catalogued TD bugs** — proof each is actually gone | — |
| [`11_PLATFORM_SIGNOFF.md`](./11_PLATFORM_SIGNOFF.md) | Per-OS sign-off grid and release gate | `08_cross_platform.md` |

[`10_REGRESSION_TD.md`](./10_REGRESSION_TD.md) is the highest-value file here.
Every row in it is a bug that **actually shipped** in the Go implementation. A
rewrite is the most likely moment to reintroduce them, and "the file that held
the bug was deleted" is not evidence the bug is gone.

## Conventions

Every checklist row carries a stable ID (`QA-<AREA>-<n>`), so a result can be
cited in a release note or a bug report without quoting the whole row.

Status values — use exactly these:

| Value | Meaning |
|-------|---------|
| `PASS` | Observed the expected result, on the stated platform, this build |
| `FAIL` | Observed something else. **Must** link an issue |
| `BLOCKED` | Could not run it — missing hardware, an earlier check failed |
| `N/A` | Not applicable to this platform or configuration (say why) |
| `DEFERRED` | The feature is specced but intentionally not implemented in this release |

Rules that keep the results honest:

- **`PASS` means observed, not inferred.** "The code looks right" is not a pass.
- **A check is per platform.** A `PASS` on Linux says nothing about Windows.
  [`11_PLATFORM_SIGNOFF.md`](./11_PLATFORM_SIGNOFF.md) is where that is tracked.
- **Record the build.** Every run records the commit, the host OS and version,
  the GPU, the loaded add-on set, and the browser and version.
- **`N/A` needs a reason.** An unexplained `N/A` is how a whole platform quietly
  stops being tested.
- **Deferred is not a pass.** Audio, AV1 and multi-monitor are specced and
  unimplemented; they are `DEFERRED`, and a release note says so.

## Environment matrix

The minimum set of environments a full pass covers. Anything narrower is a
partial pass and must be stated as one.

| Class | Host | Notes |
|-------|------|-------|
| L1 | Linux, X11, AMD or Intel GPU, root available | `kms_egl` + `libva` — the reference path |
| L2 | Linux, Wayland (GNOME or KDE), NVIDIA proprietary | `kms_egl` + `nvenc`; the historically finicky combination |
| L3 | Linux, wlroots compositor, **no root** | `wl_screencopy` + software encode — the no-root path |
| L4 | Linux, headless (forced connector, no monitor) | Proves the corrected headless story actually works |
| W1 | Windows 11, NVIDIA | `dxgi_dd` + `nvenc` |
| W2 | Windows 11, **no physical display** | `dxgi_dd` + IddCx auto-install, one UAC prompt |
| M1 | macOS, Apple Silicon | `sck` + `vt_hw` |
| B1..B4 | Chrome, Edge, Firefox, Safari — each at its floor version and at current | Firefox has no HEVC decode; Safari below 26.4 has no WebTransport |
| N1 | LAN | The performance baseline |
| N2 | Overlay/VPN forwarding UDP (WireGuard/Tailscale) | Must keep the **WebTransport** carrier |
| N3 | HTTP-proxying tunnel (Cloudflare Tunnel) | Must work, on the **WebSocket** carrier, permanently |
| N4 | UDP blocked at the firewall | Must fall back within the 3 s deadline |
| N5 | Lossy link (1 % and 2 % induced loss) | Where the carrier difference becomes visible |

## Release gate

A build ships when all of the following hold. Anything less ships with a stated
exception, not silently.

1. Every `00`–`07` check is `PASS`, `N/A` with a reason, or `DEFERRED` on the
   platforms that release claims to support.
2. **Every row of [`10_REGRESSION_TD.md`](./10_REGRESSION_TD.md) is `PASS`.**
   There is no acceptable exception here: each is a bug this project has already
   shipped once.
3. [`08_PERFORMANCE.md`](./08_PERFORMANCE.md)'s parity gate passes
   (`BRANCH.md` "Migration Strategy" step 3).
4. [`09_SECURITY.md`](./09_SECURITY.md) has no `FAIL`.
5. [`11_PLATFORM_SIGNOFF.md`](./11_PLATFORM_SIGNOFF.md) is filled in, including
   the columns that are `N/A` — an empty cell is not a pass.
