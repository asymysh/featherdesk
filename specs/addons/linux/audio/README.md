# Linux Audio Add-Ons

## Default binary audio: NONE

Like capture/encode/input, the default binary ships with **zero audio backends**.
With no audio add-on loaded (and/or `[audio] enabled = false`), the binary
streams video only. Audio is an opt-in add-on shared library. Audio is
**host→client only** (the remote machine's system output) — there is no mic.

> Audio design is **locked** but **implementation is deferred** behind the video
> trigger. See [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md).

---

## Available add-ons

| Add-on ID | Mechanism | Driver? | Spec |
|-----------|-----------|---------|------|
| `pipewire` | PipeWire monitor (native libpipewire; Pulse/ALSA fallback) | none | [`PIPEWIRE_LINUX_SPEC.md`](./PIPEWIRE_LINUX_SPEC.md) |

## Codec (separate add-on)

| Add-on ID | Effect |
|-----------|--------|
| `opus` | Opus codec (BSD libopus, in-process). FEC/PLC, ~96–128 kbps. **Recommended.** |
| _(none)_ | Raw S16LE PCM passthrough (1.536 Mbps, no loss concealment). |

Each add-on — capture, encoder, the `pipewire` audio backend, and the `opus`
codec — is a standalone shared library you drop into the add-ons directory. Build
one with, e.g.:
```bash
go build -buildmode=c-shared -o featherdesk-addon-pipewire.so ./internal/audio/pipewire
```
