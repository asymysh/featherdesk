# QA 11 — Platform sign-off grid

Matching user stories: `PROJECT_ARTIFACTS/user_stories/08_cross_platform.md`.

One grid per release. **An empty cell is not a pass** — fill every cell,
including `N/A`, with a reason where it is not obvious. A checklist that is
mostly blank reads as "mostly working" and means "mostly untested".

## Release header

| Field | Value |
|-------|-------|
| Release / commit | |
| Date | |
| Tester(s) | |
| Platforms **claimed** by this release | |
| Known exceptions shipping anyway | |

## Environment classes (from `README.md`)

| Class | Host configuration | Available? | Tested? |
|-------|--------------------|-----------|---------|
| L1 | Linux X11, AMD/Intel, root — `kms_egl` + `libva` | ☐ | ☐ |
| L2 | Linux Wayland, NVIDIA proprietary — `kms_egl` + `nvenc` | ☐ | ☐ |
| L3 | Linux wlroots, **no root** — `wl_screencopy` + SW encode | ☐ | ☐ |
| L4 | Linux headless, forced connector, no monitor | ☐ | ☐ |
| W1 | Windows 11, NVIDIA — `dxgi_dd` + `nvenc` | ☐ | ☐ |
| W2 | Windows 11, **no display** — `dxgi_dd` + IddCx | ☐ | ☐ |
| M1 | macOS Apple Silicon — `sck` + `vt_hw` | ☐ | ☐ |

## Area sign-off

Enter `PASS` / `FAIL` / `BLOCKED` / `N/A` / `DEFERRED` per cell.

| Area | L1 | L2 | L3 | L4 | W1 | W2 | M1 |
|------|----|----|----|----|----|----|----|
| 00 Preflight | | | | | | | |
| 01 Connect & auth | | | | | | | |
| 02 Video | | | | | | | |
| 03 Input | | | | | | | |
| 04 Multi-client | | | | | | | |
| 05 Clipboard & files | | | | | | | |
| 06 Resilience | | | | | | | |
| 07 Audio | | | | | | | |
| 08 Performance | | | | | | | |
| 09 Security | | | | | | | |
| **10 Regression (TD)** | | | | | | | |

## Browser sign-off

| Area | Chrome (floor) | Chrome (current) | Edge | Firefox | Safari (16.4) | Safari (26.4+) |
|------|----------------|------------------|------|---------|---------------|----------------|
| Connect (carrier used) | | | | | | |
| Video decode | | | | | | |
| Input | | | | | | |
| Clipboard | | | | | | |
| File transfer | | | | | | |
| Gamepad | | | | | | |
| Audio | | | | | | |

Expected, per `PLATFORM_COMPAT.md` — a result matching these is a `PASS`, not a
`FAIL`:

- **Firefox:** no HEVC in WebCodecs, so an HDR session must be **refused** with
  `hdr_unavailable`, not served black.
- **Safari below 26.4:** no WebTransport → the WebSocket carrier, expected and
  correct.
- **Safari, self-signed mode:** incomplete `serverCertificateHashes` support may
  require a CA-trusted cert. Record which was needed.

## Network sign-off

| Class | Carrier obtained | Video | Input latency | Notes |
|-------|------------------|-------|---------------|-------|
| N1 LAN | | | | |
| N2 Overlay/VPN (UDP forwarded) | should be **WebTransport** | | | |
| N3 Cloudflare Tunnel | should be **WebSocket** | | | |
| N4 UDP blocked | should fall back < 3 s | | | |
| N5 1 % loss | | | | |
| N5 2 % loss | | | | |

## Deferred features (expected `DEFERRED`, must appear in the release note)

| Feature | State | Note |
|---------|-------|------|
| Audio | | Trigger moved to Linux-video-working under OQ-02 |
| Client→host microphone | | OQ-02b, Linux-first, post-v1 |
| Webcam redirection | | Not in plan; wire type `0x50` reserved |
| Multi-monitor | | v2 / native client |
| AV1 encode | | Wire type reserved; no add-on implements it |
| Native client | | v2 |
| NAT traversal / relay | | v1 is LAN + operator-supplied reachability |
| Image clipboard | | Deferred in v1 |

## Exit statement

Written by the tester, not generated:

> *This build was tested on the classes marked above. The following areas were
> not covered, and why: … The following checks FAILed and are shipping anyway,
> with sign-off from …: … Every row of `10_REGRESSION_TD.md` is PASS: yes / no.*

A release where the last sentence is "no" does not ship.
