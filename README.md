# FeatherDesk

Low-latency remote desktop streaming. Single binary, embedded web client, sub-20 ms motion-to-photon on LAN.

> **Branch status (`featherdesk-refactor`):** spec-first architectural redesign.
> All module contracts are written and audited; implementation hasn't started in
> this branch yet. See [`BRANCH.md`](./BRANCH.md) for the original refactor
> rationale and the [`specs/`](./specs/) directory for the authoritative module
> specs. Working implementation lives on `feature-libav-vp8s8`.

## What it is

- Captures the host's desktop (Linux KMS+EGL, Windows DXGI Desktop Duplication, macOS ScreenCaptureKit)
- Encodes with hardware acceleration where available (NVENC, AMF, VAAPI, QSV, MediaFoundation, VideoToolbox) or software (OpenH264 / x264 subprocess)
- Streams to a browser client over HTTP/3 + WebTransport
- Injects input (keyboard, mouse, touch, gamepad) into the host via per-OS injectors (Interception, uinput, CGEventPost, ViGEmBus, GCVirtualController)
- Adds bidirectional clipboard sync + drag-and-drop file transfer

## Architecture in one paragraph

Zero-by-default plugin model. The default binary ships with no capture backend, no encoder, and no input injector — all of these are build-tagged add-ons. Users compose the binary they need by combining one capture add-on, one or more encoder add-ons, and one or more input add-ons via Go build tags. The core modules (protocol, server, client, pipeline, config, auth, input dispatcher, clipboard, file transfer, gamepad) are always present and transport-agnostic.

## Core modules

| | |
|---|---|
| [`MODULE_CAPTURE`](./specs/MODULE_CAPTURE.md) | Cross-platform `Capturer` interface; concrete implementations are add-ons |
| [`MODULE_ENCODE`](./specs/MODULE_ENCODE.md) | Software encoder interface |
| [`MODULE_HARDWARE_ENCODE`](./specs/MODULE_HARDWARE_ENCODE.md) | Hardware encoder interface |
| [`MODULE_TRANSPORT`](./specs/MODULE_TRANSPORT.md) | HTTP/3 + WebTransport transport layer |
| [`MODULE_PROTOCOL`](./specs/MODULE_PROTOCOL.md) | Wire format: frame headers, channel model, framing |
| [`MODULE_SERVER`](./specs/MODULE_SERVER.md) | HTTPS + WebTransport server, session management |
| [`MODULE_CLIENT`](./specs/MODULE_CLIENT.md) | Browser client (WebCodecs + WebTransport) |
| [`MODULE_PIPELINE`](./specs/MODULE_PIPELINE.md) | Orchestrator — probe, select, wire, lifecycle |
| [`MODULE_CONFIG`](./specs/MODULE_CONFIG.md) | TOML config schema, hot reload |
| [`MODULE_INPUT`](./specs/MODULE_INPUT.md) | Binary input wire + cross-platform injector contract |
| [`MODULE_CLIPBOARD`](./specs/MODULE_CLIPBOARD.md) | Bidirectional text + rich-HTML clipboard sync |
| [`MODULE_FILETRANSFER`](./specs/MODULE_FILETRANSFER.md) | Drag-drop transfer to a fixed folder |
| [`MODULE_GAMEPAD`](./specs/MODULE_GAMEPAD.md) | Browser Gamepad-API redirection + rumble |
| [`MODULE_AUTH`](./specs/MODULE_AUTH.md) | Auth modes + session tokens |
| [`MODULE_STREAM_PARAMS`](./specs/MODULE_STREAM_PARAMS.md) | Dynamic stream params + adaptive bitrate |

Plus deferred specs (`MODULE_AUDIO`) and the [`ADD-ON-SPECS/`](./ADD-ON-SPECS/) tree for every build-tagged backend.

## Out of scope (permanently)

- **Generic USB redirection** — kernel drivers on both ends, no browser path, BadUSB-class attack surface
- **DRM-protected content** (Netflix L1, Disney+ L1) — bypasses the compositor at the OS level; no legal capture path
- **TUN / VPN tunnel** — browsers can't use TUN devices; recommend Tailscale alongside instead
- **Smart-card / FIDO2 redirection** — no demonstrated demand

## Future plan

| Phase | What lands |
|---|---|
| **v1** | Browser client over HTTP/3 + WebTransport (QUIC). No Tailscale. WSS / fallbacks are NOT supported — modern browsers only (Chrome 107+, Edge 98+, Firefox 130+, Safari 18.2+; the
floor is set by WebCodecs availability — Chrome 107 / Firefox 130 — not just WebTransport). Connectivity is the user's network problem (LAN, port forward, or their own Tailscale / Cloudflare Tunnel). |
| **v2** | Native FeatherDesk client (Windows / macOS / Linux). Built around WebTransport (QUIC) for transport. Embeds `tsnet`. The auth-key paste flow ships here — host generates a single sharing key that bundles a Tailscale pre-auth key + FeatherDesk session token + host address; client pastes it once and is connected to the tailnet AND the FeatherDesk host in one step. |
| **v3+** | Mobile clients (iOS / Android) with the same auth-key paste flow. |

## Roadmap (next steps)

The spec phase is complete and audited. The path from here:

1. **Implementation — Linux-first vertical slice.** Build the minimal end-to-end
   path that puts one real captured frame on screen in a browser:
   `config → transport → server → kms_egl (capture) → openh264 (encode) →
   pipeline → browser`. Each stage is the smallest viable implementation of its
   module spec; once a single frame round-trips, the remaining add-ons and
   features layer onto a proven pipeline.

2. **`MODULE_NETWORK.md` — Tailscale add-on (v2 enabler).** Spec the
   listener-provider pattern: the server asks `network.GetListener()` and gets
   a plain TCP/UDP listener by default, or a Tailscale-backed (`tsnet`) listener
   when the `tailscale` add-on is compiled in. Includes the one-click auth flow,
   Funnel (public exposure) as a config flag, Headscale support, and the
   binary-size note. This is the connectivity half of the v2 story.

3. **`MODULE_NATIVE_CLIENT.md` (promote `FUTURE_NATIVE_CLIENT.md`).** When v2
   work begins, promote the historical doc to a live module spec and add the
   **auth-key paste flow**: host generates one sharing key (Tailscale pre-auth
   key + FeatherDesk session token + host address, base64-bundled); the native
   client decodes it, joins the tailnet via embedded `tsnet`, and connects to
   the host in a single paste. This is the transport + UX half of the v2 story.

## License

Per build variant. Each encoder add-on declares its own license; the main binary's license depends on which add-ons are compiled in (e.g. `x264` ffmpeg-subprocess builds are GPL-2 isolated; `openh264` builds stay permissive). See individual `ADD-ON-SPECS/` entries.

## Status

Spec phase: complete and audited. Implementation: not started in this branch. See [`BRANCH.md`](./BRANCH.md) for the original refactor plan; see [`specs/CENTRAL_SPEC.md`](./specs/CENTRAL_SPEC.md) for the live architecture overview.
