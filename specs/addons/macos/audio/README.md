# macOS Audio Add-Ons

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
| `sck_audio` | ScreenCaptureKit audio on the **shared `sck` stream** (macOS 13+) | none | [`SCK_AUDIO_MACOS_SPEC.md`](./SCK_AUDIO_MACOS_SPEC.md) |

> `sck_audio` requires the `sck` capture add-on — it attaches audio to the same
> `SCStream` used for screen capture (one stream, one permission, one clock).

## Codec (separate add-on)

| Add-on ID | Effect |
|-----------|--------|
| `opus` | Opus codec (BSD libopus, in-process). FEC/PLC, ~96–128 kbps. **Recommended.** |
| _(none)_ | Raw S16LE PCM passthrough (1.536 Mbps, no loss concealment). |

Build each add-on as its own shared library and drop the set into the add-ons directory:
```bash
cargo build --release -p featherdesk-addon-sck   # cdylib  featherdesk-addon-sck.dylib
cargo build --release -p featherdesk-addon-vt_hw   # cdylib  featherdesk-addon-vt_hw.dylib
cargo build --release -p featherdesk-addon-sck_audio   # cdylib  featherdesk-addon-sck_audio.dylib
cargo build --release -p featherdesk-addon-opus   # cdylib  featherdesk-addon-opus.dylib
```
