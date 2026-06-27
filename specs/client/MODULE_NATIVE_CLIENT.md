# Module Spec: Native Client (v2)

> # 🔒 SPLIT FINAL · ⏸️ v2 — IMPLEMENTATION DEFERRED
>
> The **v1 = browser client / v2 = native client** split is a **final** product
> decision. v1 ships the zero-install browser client ([`MODULE_WEB_CLIENT.md`](./MODULE_WEB_CLIENT.md));
> v2 adds a native desktop client for full-fidelity input, reliable 4:4:4, and
> lowest latency. The **wire protocol is identical** — the native client is a
> different *front end* on the same QUIC protocol, not a different system.
>
> This doc captures the v2 plan + the contracts that differ from the browser.
> Implementation is deferred until v1 ships. **Connectivity (P2P/NAT traversal)
> is the one open item** — Tailscale/`tsnet` is the leading candidate but is
> still under evaluation (see "Connectivity").

---

## The v1 / v2 client split (final)

| | **Browser client (v1)** | **Native client (v2)** |
|---|---|---|
| Install | none (open a URL) | per-OS app (signed/notarized) |
| Transport | WebTransport (QUIC) | **`quic-go` directly** (no HTTP/3/WebTransport layer) |
| Input latency | ~15 ms (DOM → WebTransport) | **sub-ms** (raw HID → QUIC datagram) |
| Decode | WebCodecs → canvas | direct platform decoder → surface (~10 ms lower) |
| Gamepad | casual (Gamepad API, dual-rumble) | **full HID** — gyro/accel, touchpad, adaptive triggers, LED, battery |
| Chroma | 4:2:0 (4:4:4 best-effort) | **4:4:4 reliable** |
| Audio | WebCodecs/AudioWorklet | direct CoreAudio/WASAPI/ALSA (lower latency) |
| Multi-monitor | single (v1) | native multi-display (future) |
| Connectivity | LAN / port-forward / Cloudflare Tunnel / Tailscale Funnel | **P2P/NAT traversal — under evaluation** (see below) |
| Protocol opacity | visible in DevTools | opaque compiled binary |

The browser stays the **default, casual, frictionless** client. The native client
is the **power-user** path. Neither replaces the other.

---

## Same wire protocol, leaner transport

The native client speaks the **exact** protocol in [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md)
(22-byte media `FrameHeader`, 8-byte `DatagramHeader`, the StreamType-tagged
streams, binary input records, the `config` handshake). It just uses **`quic-go`
directly** instead of the browser's HTTP/3 + WebTransport convenience layer —
leaner stack, same channels (datagrams for media, reliable streams for
control/input/clipboard/file, bootstrap stream for the join IDR).

> This is exactly the "transport addition, not a rewrite" the architecture was
> built for: the server already serves QUIC; a native client is another QUIC
> peer. Nothing in `pkg/protocol` is browser-specific.

---

## What v2 unlocks (the deltas worth building for)

### Full-fidelity gamepad (raw HID)
The browser Gamepad API caps you at dual-rumble + rAF-polled buttons/axes. The
native client reads **raw HID / platform APIs** (XInput, DualSense HID, Linux
evdev, macOS GameController) for **gyro/accelerometer, touchpad, adaptive
triggers, LED, battery**, at sub-ms polling. The protocol's `0x40–0x4F` gamepad
type space has room for **richer record types** (motion, touchpad, trigger-effect
acks) that extend the wire format **without breaking the browser** — a browser
client simply never sends them. Co-op player slots ([`MODULE_GAMEPAD.md`](../interaction/MODULE_GAMEPAD.md))
work identically.

### Reliable 4:4:4
Browser `VideoDecoder` 4:4:4 support is inconsistent; a native decoder (platform
or FFmpeg) decodes 4:4:4 reliably, so the chroma negotiation
([`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)) doesn't fall back. This is
the "crisp remote-desktop text" win, dependable.

### Lower latency end-to-end
Direct surface attach (no canvas), raw-HID input, and direct audio output shave
~10 ms decode + ~15 ms input + ~80 ms audio vs the browser stack — the difference
between "remote desktop" and "feels local."

### Platform extras (future)
Direct HDR surfaces, native multi-monitor, codec flexibility beyond the WebCodecs
set. Captured here as direction, not scoped for the first v2.

---

## Connectivity (the open question)

The native client must reach the host across NATs. Options:

1. **Same as the browser** — LAN / port-forward / Cloudflare Tunnel / Tailscale
   Funnel. Zero new code; user provides reachability.
2. **Embedded overlay (leading candidate, NOT locked): Tailscale `tsnet`.** The
   client embeds `tsnet`, joins the user's tailnet, and connects to the host as a
   tailnet node — WireGuard P2P hole-punching with DERP relay fallback, i.e.
   Parsec-grade connectivity without us operating STUN/TURN. The planned
   **auth-key paste flow** bundles a pre-auth key + session token + host address
   into one paste.
3. **Custom P2P** — QUIC hole-punching + our own rendezvous + TURN relays. Gives
   full control but means **we operate signaling + relay infrastructure** (cost +
   ops). Rejected for the browser anyway (browser P2P ⇒ WebRTC, which we declined).

> **Status: under evaluation.** Whether to embed Tailscale, use an alternative
> open-source overlay/NAT-traversal library, or stay with option 1 is **not yet
> decided** — see the open discussion. This section will be finalized before v2
> implementation starts; nothing else in this spec depends on the outcome.

---

## Architecture rules this preserves (already satisfied)

The browser-first work was built so the native client is additive:

- **Wire protocol is transport-agnostic** — `pkg/protocol` is bytes-in/bytes-out;
  the protocol header carries its own length; no HTTP/WS framing leaks in.
- **Input encoding is platform-neutral** — HID usages + stream-pixel coordinates;
  a native client producing input from raw HID emits **byte-identical** records to
  the browser for the same logical input.
- **Capabilities live in the `config` handshake** — codec string (incl. chroma),
  audio codec/channels, cursor mode — so both clients decide from the same payload.

---

## Costs (acknowledged, deferred)

Per-OS builds + **code signing** (Apple Developer ID + notarization; Windows EV
cert + SmartScreen; Linux AppImage signature), **auto-update** (Sparkle / Squirrel
/ custom), 3× platform maintenance for decode/input/audio backends, and the GUI
framework choice (Tauri / Qt / SDL3 / native) — all v2-project concerns, specced
when v2 opens.

---

## Triggers to open the v2 project

- v1 (browser) is shipped and stable end-to-end on all three host OSes, **and**
- a concrete need lands for: gaming-grade sub-10 ms input, reliable 4:4:4,
  full-HID gamepad fidelity, or friction-free NAT traversal.

Until then v1's browser client is the right client, and this spec is the contract
the native client will honor.
