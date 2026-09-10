# Opus Audio Codec Add-On (cross-platform)

> 🔒 **DESIGN LOCKED · ⏸️ IMPLEMENTATION DEFERRED** — behind the same trigger as
> the rest of audio (video capture+encode working end-to-end on all three OSes).
> See [`../media/MODULE_AUDIO.md`](../media/MODULE_AUDIO.md).

## Purpose

The `opus` add-on is the default **audio encoder**: it turns the canonical PCM
chunks produced by a per-OS audio **capture** add-on (`pipewire` / `wasapi` /
`sck_audio`) into Opus packets for the wire. It is the audio analogue of the
video encoder add-ons — pluggable, opt-in, zero-by-default. With **no** codec
add-on loaded, the host falls back to **raw S16LE PCM passthrough**
(`frame_type::AUDIO_PCM`), so `opus` is the recommended-but-optional quality
path, never a hard dependency.

Unlike capture/encode video add-ons, this one is **OS-independent**: libopus is
portable and the add-on touches no platform API. It is built **once per target
arch** from a single crate and dropped into the add-ons directory on any OS (on
macOS, ad-hoc signed at minimum — see
[`macos/MACOS_SPEC.md`](./macos/MACOS_SPEC.md) "Add-ons and the hardened
runtime").

## What this spec owns vs defers

This spec owns only the **packaging + ABI surface** of the codec add-on. All
**Opus parameters and wire behavior** (application mode, 20 ms framing, VBR,
in-band FEC and per-path loss concealment, multistream channel mapping for
5.1/7.1, the OpusHead byte layout, single-datagram sizing, `config`-advertised
`audioCodec`/`audioSampleRate`/`audioChannels`/`audioLayout`/`audioDescription`,
and the audio-master A/V sync contract) are defined **once** in
[`../media/MODULE_AUDIO.md`](../media/MODULE_AUDIO.md) "Codec" + "Surround" +
"Wire Format" + "A/V Sync". Where this file and MODULE_AUDIO disagree,
MODULE_AUDIO wins.

## Add-on identity

| Field | Value |
|-------|-------|
| Add-on ID | `opus` |
| Kind | `audio` codec (implements `AudioEncoder`, **not** `AudioCapturer`) |
| Crate | `featherdesk-addon-opus` (cdylib) |
| Artifact | `featherdesk-addon-opus.{so,dylib,dll}` |
| OS / arch | all OSes; one build per CPU arch (no platform FFI) |
| Library | **libopus** (BSD-3) via Rust FFI — `audiopus_sys` / `bindgen`, statically linked into the add-on |
| Config section | `[addon_module_opus]` |

## Trait implemented

Implements `AudioEncoder` from the audio contract (see CENTRAL_SPEC "Contract 5:
Audio → Server" and MODULE_AUDIO "Public Interface"):

```rust
// Exported via the abi_stable root module like every other add-on.
pub trait AudioEncoder {
    /// Encode one canonical PCM chunk (48 kHz, S16LE, interleaved, Vorbis
    /// channel order) into one wire payload. Ownership of the returned buffer
    /// transfers to the host (RVec<u8> across the ABI). Matches
    /// `AudioEncoder::encode` in MODULE_AUDIO (always a payload; the error
    /// crosses the boundary as an AbiError whose code the host maps to this
    /// AudioError and whose detail it logs).
    fn encode(&mut self, chunk: &PcmChunk) -> Result<RVec<u8>, AudioError>;
    /// "opus" — advertised by the host in the `config` message as audioCodec.
    fn codec(&self) -> &str;
    /// The codec-specific decoder-init bytes the client needs: the OpusHead of
    /// RFC 7845 section 5.1. Built once, at construction, and carried on the wire
    /// as `config.audioDescription`, base64. Empty for the PCM passthrough codec.
    fn description(&self) -> RVec<u8>;
}
```

- Mono/stereo use `opus_encoder`; 5.1/7.1 use `opus_multistream_encoder`
  (channel mapping family 1). The number of channels comes from the capturer's
  `Format`/`ChannelLayout` (MODULE_AUDIO).
- **The add-on does not reorder channels.** Mapping family 1 defers to Vorbis I
  channel ordering (RFC 7845 §5.1.1.2), and `PcmChunk.data` already arrives in
  that order — the per-OS capture add-on reordered the device layout on the way
  in. Re-permuting here would undo it.
- **`description()` builds the OpusHead**, which is the only place the stream
  count, coupled-stream count and channel mapping exist; without it a WebCodecs
  `AudioDecoder` cannot parse a mapping-family-1 stream at all. The byte layout,
  and the stream/coupled/mapping values per channel count, are in MODULE_AUDIO
  "The Opus identification header (`config.audioDescription`)". Two fields come
  from this add-on's own state: `Output Channel Count` from the negotiated
  layout, and `Pre-skip` from `OPUS_GET_LOOKAHEAD` on the constructed encoder
  (312 at 48 kHz with the default complexity) — never a hard-coded constant.
- The encoder is driven by the **host audio pump** (the channel never crosses the
  ABI): the pump pulls `PcmChunk`s from the capturer, calls `encode`, and hands
  the payload to `server.broadcast_audio(AUDIO_OPUS, payload, capture_ts_ns)`.
- Errors cross the ABI as `AbiError { code, detail }`, and the audio domain's
  codes map bijectively onto `AudioError` — `DeviceLost` (6), `Unrecoverable` (7),
  `Unsupported` (8), `Backend` (1); see
  [`../core/MODULE_ABI.md`](../core/MODULE_ABI.md) "AbiErr registry". `detail` is
  logged by the host and never put on the wire.
- `probe()` reports `available: true` unconditionally — libopus is statically
  linked, so there is no prerequisite to be missing — with
  `codecs: [CodecId::Opus]` and `caps: AddonCaps(0)`: `AudioEncoder` has no
  optional methods, so there is no bit to claim. `descriptor().kind` is
  `AudioCodec` (0x05), so `new_codec` is the valid constructor and
  `new_capturer` returns `PipelineError::AddonBackend`.

## Configuration (`[addon_module_opus]`)

The tuning surface is small; defaults match MODULE_AUDIO's locked settings.

```toml
[addon_module_opus]
bitrate_kbps = 0      # 0 = auto by channel count (~96 stereo, scaled for 5.1/7.1); else fixed VBR target
complexity   = 8      # 0..10 libopus complexity; lower = less CPU, slightly lower quality
# application mode, frame size (20 ms), VBR, channel mapping and in-band FEC are
# fixed by MODULE_AUDIO and are NOT knobs here.
```

**FEC is fixed on, and so is the loss estimate it keys off.** The encoder always
runs with `OPUS_SET_INBAND_FEC(1)` **and** `OPUS_SET_PACKET_LOSS_PERC(10)`. Both
are v1 constants, not knobs: FEC emits redundancy only when the encoder expects
loss, so a `0` packet-loss percentage silently disables the feature while leaving
the FEC flag looking enabled. It costs roughly **5–10 % bitrate**, which is the
price of the redundancy.

What a receiver can do with that redundancy differs by client path, and only the
wasm-libopus and native paths can reach it — a WebCodecs `AudioDecoder` exposes
neither an FEC entry point nor a null-packet decode, so a browser on that path
gets the worklet-side concealment MODULE_AUDIO "Loss concealment, by path"
specifies instead. Do not describe browser playback as FEC-protected.

## Build

```bash
cargo build --release -p featherdesk-addon-opus   # → featherdesk-addon-opus.{so,dylib,dll}
```

Drop the artifact into the add-ons directory alongside one per-OS audio capture
add-on. Recommended combinations are listed in each OS audio `README.md`.

## What this add-on does NOT do

- **No capture.** It never touches a microphone or a system-audio device — that
  is the per-OS `AudioCapturer` add-on's job.
- **No resampling and no channel reordering.** The capturer delivers the
  canonical format — 48 kHz, S16LE, interleaved, **Vorbis channel order**
  (MODULE_AUDIO); the codec assumes all three.
- **No client→host audio.** Audio is host→client only; mic is out of scope.
- **No decoding.** The browser decodes Opus with WebCodecs `AudioDecoder`, or
  with the wasm libopus fallback where that lacks Opus — both of which take the
  same `description()` bytes (see MODULE_WEB_CLIENT "Audio Playback Pipeline").

## Implementation Status

📋 Specced; design locked, implementation deferred with the rest of audio. libopus
Rust FFI bindings pending.
