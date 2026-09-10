# FeatherDesk

Low-latency remote desktop streaming. Single binary, embedded web client, sub-20 ms motion-to-photon on LAN.

> **Branch status (`featherdesk-refactor`):** spec-first architectural redesign.
> All module contracts are written and audited; implementation hasn't started in
> this branch yet. See [`BRANCH.md`](./BRANCH.md) for the original refactor
> rationale and the [`specs/`](./specs/) directory for the authoritative module
> specs. Working implementation lives on `feature-libav-vp8s8`.

## What it is

- Captures the host's desktop (Linux KMS+EGL, Windows DXGI Desktop Duplication, macOS ScreenCaptureKit)
- Encodes with hardware acceleration where available (NVENC, AMF, VAAPI, QSV, MediaFoundation, VideoToolbox) or in-process software (OpenH264); an opt-in `x264` add-on trades an external ffmpeg dependency for more speed on CPU-bound hosts
- Streams to a browser client over HTTP/3 + WebTransport, with a WebSocket fallback carrier on the same port for UDP-blocked networks
- Injects input (keyboard, mouse, touch, gamepad) into the host via per-OS injectors (Interception, uinput, CGEventPost, ViGEmBus, GCVirtualController)
- Adds bidirectional clipboard sync + drag-and-drop file transfer
- **Requires the client to be able to reach the host.** v1 does no NAT traversal and runs no relay: the host listens on one address as both TCP and UDP, and the user supplies reachability — same LAN, a forwarded port, or a tunnel that carries QUIC end to end. Hosts behind CGNAT or a corporate firewall with no inbound path are not reachable in v1. P2P/NAT traversal is a v2 item ([`./specs/v2/MODULE_NETWORK.md`](./specs/v2/MODULE_NETWORK.md))

## Architecture in one paragraph

Zero-by-default plugin model. The default binary ships with no capture backend, no encoder, and no input injector — all of these are add-on shared libraries. Users compose the deployment they need by dropping the add-on shared libraries they want (one capture add-on, one or more encoder add-ons, and one or more input add-ons) into the add-ons directory; the host loads them at runtime via dlopen. The core modules (protocol, server, client, pipeline, config, auth, input dispatcher, clipboard, file transfer, gamepad) are always present and transport-agnostic.

## Core modules

| | |
|---|---|
| [`MODULE_CAPTURE`](./specs/media/MODULE_CAPTURE.md) | Cross-platform `Capturer` interface; concrete implementations are add-ons |
| [`MODULE_ENCODE`](./specs/media/MODULE_ENCODE.md) | Software encoder interface |
| [`MODULE_HARDWARE_ENCODE`](./specs/media/MODULE_HARDWARE_ENCODE.md) | Hardware encoder interface |
| [`MODULE_TRANSPORT`](./specs/core/MODULE_TRANSPORT.md) | TCP HTTPS bootstrap + HTTP/3 + WebTransport, and the WebSocket fallback carrier |
| [`MODULE_PROTOCOL`](./specs/core/MODULE_PROTOCOL.md) | Wire format: frame headers, channel model, framing |
| [`MODULE_SERVER`](./specs/core/MODULE_SERVER.md) | HTTPS + WebTransport server, session management |
| [`MODULE_CLIENT`](./specs/client/MODULE_WEB_CLIENT.md) | Browser client (WebCodecs + WebTransport) |
| [`MODULE_PIPELINE`](./specs/core/MODULE_PIPELINE.md) | Orchestrator — probe, select, wire, lifecycle |
| [`MODULE_ABI`](./specs/core/MODULE_ABI.md) | The `abi_stable` add-on boundary — Layer-1 types, `AbiErr`, capability bits |
| [`MODULE_CONFIG`](./specs/core/MODULE_CONFIG.md) | TOML config schema, hot reload |
| [`MODULE_INPUT`](./specs/interaction/MODULE_INPUT.md) | Binary input wire + cross-platform injector contract |
| [`MODULE_CLIPBOARD`](./specs/interaction/MODULE_CLIPBOARD.md) | Bidirectional text + rich-HTML clipboard sync |
| [`MODULE_FILETRANSFER`](./specs/interaction/MODULE_FILETRANSFER.md) | Drag-drop transfer to a fixed folder |
| [`MODULE_GAMEPAD`](./specs/interaction/MODULE_GAMEPAD.md) | Browser Gamepad-API redirection + rumble |
| [`MODULE_AUTH`](./specs/core/MODULE_AUTH.md) | Auth modes + session tokens |
| [`MODULE_STREAM_PARAMS`](./specs/core/MODULE_STREAM_PARAMS.md) | Dynamic stream params + adaptive bitrate |

Plus the [`specs/addons/`](./specs/addons/) tree for every add-on shared library. `MODULE_AUDIO` is fully specced; only its *implementation* is deferred behind the video milestone.

## Out of scope (permanently)

- **Generic USB redirection** — kernel drivers on both ends, no browser path, BadUSB-class attack surface
- **DRM-protected content** (Netflix L1, Disney+ L1) — bypasses the compositor at the OS level; no legal capture path
- **TUN / VPN tunnel** — browsers can't use TUN devices; recommend Tailscale alongside instead
- **Smart-card / FIDO2 redirection** — no demonstrated demand

## Future plan

| Phase | What lands |
|---|---|
| **v1** | Browser client over HTTP/3 + WebTransport (QUIC), with a WebSocket fallback carrier for UDP-blocked networks and Safari below 26.4 — degraded (TCP head-of-line blocking under loss), same stream tags and message framing, only the carrier differs. No Tailscale. Modern browsers only (Chrome 107+, Edge 98+, Firefox 130+, Safari 16.4+; the floor is set by WebCodecs availability — Chrome 107 / Firefox 130 / Safari 16.4 — while the WebTransport carrier additionally needs Safari 26.4+). Connectivity is the user's network problem: LAN, a forwarded port, or a tunnel. A tunnel must carry **QUIC end to end** for the WebTransport path — Cloudflare Tunnel does not (`cloudflared` proxies HTTP/1.1 and HTTP/2 to the origin), so behind it only the WebSocket fallback works. |
| **v2** | Native FeatherDesk client (Windows / macOS / Linux) — same QUIC wire protocol via `quinn`/`wtransport` directly. Adds **full-HID gamepad** (gyro/touchpad/triggers/LED), **reliable 4:4:4**, **sub-ms input**, and a **protobuf (`prost`) native-client protocol** (browser stays JSON; protobuf is v2-only where there's no browser-JS constraint). The browser stays the casual client; native is the power-user client (see [`./specs/client/MODULE_NATIVE_CLIENT.md`](./specs/client/MODULE_NATIVE_CLIENT.md)). **Connectivity (P2P/NAT traversal) is under evaluation** — Tailscale `tsnet` is the leading candidate (one-paste auth-key flow), pending the open discussion. |
| **v3+** | Mobile clients (iOS / Android) with the same auth-key paste flow. |

## Roadmap (next steps)

The spec phase is complete and audited. The path from here:

1. **Implementation — Linux-first vertical slice.** Build the minimal end-to-end
   path that puts one real captured frame on screen in a browser:
   `config → transport → server → kms_egl (capture) → openh264 (encode) →
   pipeline → browser`. Each stage is the smallest viable implementation of its
   module spec; once a single frame round-trips, the remaining add-ons and
   features layer onto a proven pipeline.

2. **`MODULE_NETWORK.md` — connectivity for v2 (requirements documented, mechanism
   OPEN).** The listener-provider contract and candidate mechanisms (tsnet+Headscale,
   pion+TURN, DIY, Nebula) are captured in [`./specs/v2/MODULE_NETWORK.md`](./specs/v2/MODULE_NETWORK.md).
   The mechanism is evaluated and locked when v2 native-client work begins.

3. ✅ **`MODULE_NATIVE_CLIENT.md` — done (split is final).** The v1=browser /
   v2=native split is locked and the native-client plan is written (full-HID
   gamepad, reliable 4:4:4, sub-ms input, same QUIC protocol). The auth-key paste
   flow and the connectivity mechanism are finalized once step 2 resolves.

## License

Per add-on shared library. Each encoder add-on declares its own license and ships as a separate shared library; the host binary never links add-on code, so its license is independent of which add-ons are dropped in (e.g. the GPL-2 `x264` ffmpeg-subprocess add-on — opt-in, never auto-selected — stays isolated in its own library; the BSD `openh264` add-on, which is the software default, stays permissive). See individual `specs/addons/` entries.

## Status

Spec phase: complete and audited. Implementation: not started in this branch. See [`BRANCH.md`](./BRANCH.md) for the original refactor plan; see [`specs/CENTRAL_SPEC.md`](./specs/CENTRAL_SPEC.md) for the live architecture overview.
