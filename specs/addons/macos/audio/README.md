# macOS Audio Add-Ons

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
| `sck_audio` | ScreenCaptureKit audio on the **shared `sck` stream** (macOS 13+) | none | [`SCK_AUDIO_MACOS_SPEC.md`](./SCK_AUDIO_MACOS_SPEC.md) |

> `sck_audio` requires the `sck` capture add-on — it attaches audio to the same
> `SCStream` used for screen capture (one stream, one permission, one clock).

## Codec (separate build tag)

| Build tag | Effect |
|-----------|--------|
| `opus` | Opus codec (BSD libopus, in-process). FEC/PLC, ~96–128 kbps. **Recommended.** |
| _(none)_ | Raw S16LE PCM passthrough (1.536 Mbps, no loss concealment). |

```bash
go build -tags "sck,vt_hw,sck_audio,opus" -o featherdesk-macos ./cmd/server
```
