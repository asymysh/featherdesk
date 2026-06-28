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
arch** from a single crate and dropped into the add-ons directory on any OS.

## What this spec owns vs defers

This spec owns only the **packaging + ABI surface** of the codec add-on. All
**Opus parameters and wire behavior** (application mode, 20 ms framing, VBR,
in-band FEC/PLC, multistream channel mapping for 5.1/7.1, single-datagram sizing,
`config`-advertised `audioCodec`/`audioSampleRate`/`audioChannels`/`audioLayout`,
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
    /// Encode one canonical PCM chunk (48 kHz, S16LE, interleaved) into one wire
    /// payload. Ownership of the returned buffer transfers to the host (RVec<u8>
    /// across the ABI). Matches `AudioEncoder::encode` in MODULE_AUDIO (always a
    /// payload; the error crosses the ABI as a u32 the host maps to AudioError).
    fn encode(&mut self, chunk: &PcmChunk) -> Result<RVec<u8>, u32>;
    /// "opus" — advertised by the host in the `config` message as audioCodec.
    fn codec(&self) -> &str;
}
```

- Mono/stereo use `opus_encoder`; 5.1/7.1 use `opus_multistream_encoder`
  (channel mapping family 1). The number of channels comes from the capturer's
  `Format`/`ChannelLayout` (MODULE_AUDIO).
- The encoder is driven by the **host audio task** (the channel never crosses the
  ABI): the task pulls `PcmChunk`s from the capturer, calls `encode`, and hands
  the payload to `server.broadcast_audio(AUDIO_OPUS, payload, capture_ts_ns)`.
- Errors cross the ABI as `u32` (mapped host-side to `AudioError`), per the
  add-on ABI contract in CENTRAL_SPEC.

## Configuration (`[addon_module_opus]`)

The tuning surface is small; defaults match MODULE_AUDIO's locked settings.

```toml
[addon_module_opus]
bitrate_kbps = 0      # 0 = auto by channel count (~96 stereo, scaled for 5.1/7.1); else fixed VBR target
fec          = true   # in-band FEC (loss concealment) — keep on for non-LAN
complexity   = 8      # 0..10 libopus complexity; lower = less CPU, slightly lower quality
# application mode, frame size (20 ms), VBR, and channel mapping are fixed by
# MODULE_AUDIO and are NOT knobs here.
```

## Build

```bash
cargo build --release -p featherdesk-addon-opus   # → featherdesk-addon-opus.{so,dylib,dll}
```

Drop the artifact into the add-ons directory alongside one per-OS audio capture
add-on. Recommended combinations are listed in each OS audio `README.md`.

## What this add-on does NOT do

- **No capture.** It never touches a microphone or a system-audio device — that
  is the per-OS `AudioCapturer` add-on's job.
- **No resampling.** The capturer delivers the canonical 48 kHz S16LE format
  (MODULE_AUDIO); the codec assumes it.
- **No client→host audio.** Audio is host→client only; mic is out of scope.
- **No decoding.** The browser decodes Opus via WebAudio/WebCodecs
  (see MODULE_WEB_CLIENT "Audio Playback").

## Implementation Status

📋 Specced; design locked, implementation deferred with the rest of audio. libopus
Rust FFI bindings pending.
