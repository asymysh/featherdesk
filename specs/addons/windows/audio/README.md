# Windows Audio Add-Ons

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
| `wasapi` | WASAPI loopback on the default render endpoint | none | [`WASAPI_WINDOWS_SPEC.md`](./WASAPI_WINDOWS_SPEC.md) |

## Codec (separate add-on)

| Add-on ID | Effect |
|-----------|--------|
| `opus` | Opus codec (BSD libopus, in-process). ~96–128 kbps VBR, in-band FEC always on. **Recommended.** See [`OPUS_AUDIO_CODEC_SPEC.md`](../../OPUS_AUDIO_CODEC_SPEC.md). |
| _(none)_ | Raw S16LE PCM passthrough (1.536 Mbps at stereo, 4.6 at 5.1, 6.1 at 7.1). |

Which loss concealment a listener actually gets depends on the client path, not
on this table: full Opus FEC + PLC on the wasm-libopus and native paths,
worklet-side concealment in the WebCodecs `AudioDecoder` path, which exposes
neither. See [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)
"Loss concealment, by path".

Build each add-on (capture, encoder, `wasapi`, `opus`) separately as a cdylib
and drop the resulting `.dll` files into the add-ons directory, e.g.:

```bash
cargo build --release -p featherdesk-addon-wasapi   # cdylib  featherdesk-addon-wasapi.dll
```
