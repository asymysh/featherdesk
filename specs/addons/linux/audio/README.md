# Linux Audio Add-Ons

## Default binary audio: NONE

Like capture/encode/input, the default binary ships with **zero audio backends**.
With no audio add-on compiled in (and/or `[audio] enabled = false`), the binary
streams video only. Audio is an opt-in build-tagged add-on. Audio is
**host→client only** (the remote machine's system output) — there is no mic.

> Audio design is **locked** but **implementation is deferred** behind the video
> trigger. See [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md).

---

## Available add-ons

| Build tag | Mechanism | Driver? | Spec |
|-----------|-----------|---------|------|
| `pipewire` | PipeWire monitor (native libpipewire; Pulse/ALSA fallback) | none | [`PIPEWIRE_LINUX_SPEC.md`](./PIPEWIRE_LINUX_SPEC.md) |

## Codec (separate build tag)

| Build tag | Effect |
|-----------|--------|
| `opus` | Opus codec (BSD libopus, in-process). FEC/PLC, ~96–128 kbps. **Recommended.** |
| _(none)_ | Raw S16LE PCM passthrough (1.536 Mbps, no loss concealment). |

```bash
go build -tags "kms_egl,openh264,pipewire,opus" -o featherdesk-linux ./cmd/server
```
