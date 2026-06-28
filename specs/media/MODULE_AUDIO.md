# Module Spec: Audio

> # 🔒 DESIGN LOCKED · ⏸️ IMPLEMENTATION DEFERRED
>
> **Design status:** Current. This spec reflects the locked QUIC / pluggable
> add-on architecture (host→client system audio, per-OS capture add-ons,
> pluggable Opus/PCM codec, realtime **audio-master** A/V sync). It supersedes
> the old Linux-only `pw-cat` subprocess design.
>
> **Implementation status:** Deferred behind the same trigger as before — video
> capture+encode working end-to-end on Linux + macOS + Windows. Locking the
> design now stops it contradicting the rest of the spec base; no code is pulled
> forward.
>
> **Scope:** Audio is **host→client only** (the remote machine's system audio
> output, streamed to the viewer). Client→host **microphone is out of scope**
> (dropped — it would need per-OS virtual-input drivers, the same class of work
> as the deferred webcam virtual-device).

---

## Overview

The Audio module captures the host's **system audio output** and streams it to
connected clients, tightly synchronized to the video. It follows the same
**pluggable, zero-by-default** pattern as Capture/Encode:

- The **core** module (this spec) defines the `AudioCapturer` + `AudioEncoder`
  interface contracts, the wire framing, and the A/V-sync model.
- **Capture** is a per-OS add-on shared library (WASAPI loopback / ScreenCaptureKit
  audio / PipeWire). The default binary ships **none** — no audio add-on means no
  audio, exactly like "no input add-on means view-only".
- **Codec** is pluggable and **advertised in the `config` message** (like the
  video codec): **Opus** when the `opus` add-on is loaded (compressed,
  loss-resilient), otherwise **raw PCM** passthrough (zero dependency).

There is **no subprocess** (`pw-cat`/`parec` are gone) and **no custom logger**
(logging is via the `tracing` crate).

---

## Public Interface

```rust
// crate: featherdesk-audio

// AudioCapturer captures the host system-audio output. Per-OS add-ons implement
// it (wasapi / sck_audio / pipewire). Zero-by-default: absent ⇒ no audio.
// Cleanup is RAII (Drop) — no Close().
//
// The channel does NOT cross the add-on ABI: the add-on exposes next_chunk()
// and the host's audio task pumps it into a tokio::sync::mpsc channel (see
// CENTRAL "Pluggable Architecture"). Each chunk is exactly one frame
// (frame_samples * channels * 2 bytes, S16LE interleaved), stamped AT CAPTURE
// (see "Realtime").
pub trait AudioCapturer {
    /// next_chunk delivers the next timestamped PCM chunk. Ok(Some(chunk)) = a
    /// chunk; Ok(None) = the capturer has stopped (end of stream); Err = failure.
    fn next_chunk(&mut self) -> Result<Option<PcmChunk>, AudioError>;

    /// format reports the negotiated capture format (rate/channels). The add-on
    /// resamples/mixes the OS device format to this canonical format so the
    /// encoder + client see one shape regardless of OS.
    fn format(&self) -> Format;
}

// AudioEncoder turns PCM chunks into wire payloads. Selected by loaded add-on:
//   opus  → addons/audio/opus/  (libopus, BSD, in-process Rust FFI)
//   <none>→ PCM passthrough (built-in; copies the S16LE bytes through)
// Cleanup is RAII (Drop) — no Close().
pub trait AudioEncoder {
    /// encode encodes one PCM chunk into one wire payload (Opus packet or raw
    /// PCM). The returned RVec<u8> is owned (ownership transfers — deterministic
    /// drop across the add-on ABI). The codec string for the config handshake is
    /// reported by codec().
    fn encode(&mut self, chunk: &PcmChunk) -> Result<RVec<u8>, AudioError>;

    /// codec returns the codec advertised in the config message, e.g.
    /// "opus" or "pcm/s16le". The client configures its decoder from this.
    fn codec(&self) -> &str;
}

// PcmChunk is one fixed-size PCM frame stamped at CAPTURE time.
pub struct PcmChunk {
    pub data: RVec<u8>,    // frame_samples*channels*2 bytes, S16LE interleaved, channel
                           // order per Format.layout (stereo / 5.1 / 7.1)
    pub timestamp_ns: u64, // CLOCK_MONOTONIC ns, sampled in the capture read loop —
                           // NOT when the pipeline consumes it (see "Realtime").
}

// Format is the canonical capture/transport format.
pub struct Format {
    pub sample_rate: u32,       // 48000 (fixed for v1)
    pub channels: u8,           // 1..8 — follows the host output (stereo, 5.1=6, 7.1=8)
    pub layout: ChannelLayout,  // channel order/positions (see "Surround")
}

// ChannelLayout names the speaker mapping so the client renders/downmixes
// correctly. Order is the standard Vorbis/Opus mapping-family-1 order.
#[repr(u8)]
pub enum ChannelLayout {
    Mono = 0,    // 1ch: M
    Stereo,      // 2ch: L R
    Layout5_1,   // 6ch: L R C LFE Ls Rs
    Layout7_1,   // 8ch: L R C LFE Rls Rrs Ls Rs
}

// AudioConfig is the core config (from [audio] TOML). Per-add-on device
// selection lives in [addon_module_<id>]. Logging is via the `tracing` crate.
pub struct AudioConfig {
    pub frame_ms: u32,    // 10 or 20 (default 20). Drives PcmChunk size + Opus frame.
    pub channels: String, // "auto" (follow host, ≤7.1) | "stereo" (force downmix at host)
}

pub const DEFAULT_SAMPLE_RATE: u32 = 48000;
pub const DEFAULT_CHANNELS: u8 = 2;   // stereo when the host is stereo (the common case)
pub const MAX_CHANNELS: u8 = 8;       // 7.1
pub const DEFAULT_FRAME_MS: u32 = 20; // 20 ms @ 48 kHz = 960 samples/chan
// chunk_bytes(ch) for a 20 ms frame = 960 * ch * 2B (e.g. stereo 3840, 5.1 11520).
```

> **Why the encoder is in-process (not a subprocess):** unlike x264 (GPL, isolated
> in ffmpeg), libopus is **BSD-licensed**, so it links directly via Rust FFI with no
> license concern and no subprocess latency. PCM passthrough needs no library.

---

## Pluggable Capture Add-Ons (one per OS)

| OS | Add-on ID | Mechanism | Driver? | Spec |
|----|-----------|-----------|---------|------|
| **Windows** | `wasapi` | WASAPI **loopback** on the default render endpoint (`AUDCLNT_STREAMFLAGS_LOOPBACK`) | none | [`../addons/windows/audio/WASAPI_WINDOWS_SPEC.md`](../addons/windows/audio/WASAPI_WINDOWS_SPEC.md) |
| **macOS** | `sck_audio` | ScreenCaptureKit `SCStreamConfiguration.capturesAudio` (macOS 13+) | none | [`../addons/macos/audio/SCK_AUDIO_MACOS_SPEC.md`](../addons/macos/audio/SCK_AUDIO_MACOS_SPEC.md) |
| **Linux** | `pipewire` | PipeWire monitor source (native libpipewire); Pulse `.monitor` / ALSA `snd-aloop` fallback | none | [`../addons/linux/audio/PIPEWIRE_LINUX_SPEC.md`](../addons/linux/audio/PIPEWIRE_LINUX_SPEC.md) |

None requires a driver. The add-on **normalizes** its native device format (the
mix is often 32-bit float at the device rate) to the canonical 48 kHz / stereo /
S16LE before handing chunks to the core, so the encoder and client are
OS-agnostic. On macOS the audio rides the SCK session already opened for screen
capture — same process, same clock.

---

## Codec (pluggable, advertised in `config`)

| Codec | Add-on ID | Library | Bandwidth | On loss | Wire type |
|-------|-----------|---------|-----------|---------|-----------|
| **Opus** (default when loaded) | [`opus`](../addons/OPUS_AUDIO_CODEC_SPEC.md) | libopus (BSD, Rust FFI) | ~96–128 kbps VBR | **FEC + PLC conceals** dropped packets | `frame_type::AUDIO_OPUS` (0x08) |
| **Raw PCM** (built-in fallback) | — | none | 1.536 Mbps | a lost packet = a ~20 ms gap (no concealment) | `frame_type::AUDIO_PCM` (0x04) |

- The server advertises the codec in the **`config`** control-stream message
  (`audioCodec`, `audioSampleRate`, `audioChannels`, `audioLayout`) — the client
  never hardcodes them. This kills the old `Width`/`Height` overload (those fields
  are unused/zero for audio now).
- **Opus settings:** application = `OPUS_APPLICATION_AUDIO` (general system audio,
  not just voice), 20 ms frames, VBR, **in-band FEC enabled**, complexity tuned
  for low latency. A stereo 20 ms Opus packet is ~100–300 bytes → **one datagram**,
  no fragmentation. 5.1/7.1 packets are larger but still typically one datagram
  (and fragment cleanly as a media type if not).
- **Multichannel** uses **Opus multistream** (`opus_multistream_encoder`, channel
  mapping family 1) for 5.1 (6ch) / 7.1 (8ch). PCM passthrough simply carries the
  interleaved N-channel S16LE.
- Opus is strongly preferred for any non-LAN use: its FEC/PLC is what keeps audio
  clean under datagram loss, which is the premise of audio-master sync (below).

---

## Surround (5.1 / 7.1)

Audio follows the **host's output layout**: if the remote machine is set to 5.1
or 7.1, the capture add-on delivers 6/8 channels and they stream through
end-to-end; if the host is stereo, you get stereo. There is **no upmixing**.

- `[audio] channels = "auto"` (default) — follow the host layout, capped at 7.1.
  `"stereo"` forces a host-side downmix (useful to save bandwidth or when the
  client is known to be stereo).
- **Channel order** is the standard Vorbis/Opus mapping-family-1 order
  (`L R C LFE Ls Rs` for 5.1; `L R C LFE Rls Rrs Ls Rs` for 7.1). The capture
  add-on reorders the OS device layout into this canonical order; `config.audioLayout`
  tells the client which layout to expect.
- **Client downmix.** Most viewers are stereo. If the decoded channel count
  exceeds the client's output device channels, the client **downmixes to stereo**
  (standard ITU-R coefficients: `C` and `LFE` folded in, surrounds attenuated).
  A surround-capable client plays all channels.

---

## Wire Format

Audio is a **media** datagram type (S→C, unreliable). Like video it carries the
22-byte `FrameHeader` (it needs a wire **Timestamp** for A/V sync and a
**Sequence** for gap/PLC) — but unlike video it is **single-datagram** for Opus
(no fragmentation). See [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) "media vs
control" datagram classes.

```
DatagramHeader (8 bytes): Version=1, Type=AUDIO_OPUS(0x08)|AUDIO_PCM(0x04),
                          FrameID = audio Sequence, FragIndex = 0 | LAST
FrameHeader (22 bytes):   Version=1, Type=<same>,
                          Sequence  = server audio counter (independent of video),
                          Timestamp = PcmChunk.timestamp_ns (CLOCK_MONOTONIC ns, at capture),
                          Width=0, Height=0 (UNUSED — params are in `config`),
                          PayloadSize = payload.len()
Payload:                  Opus packet (≈100–300 B) OR raw S16LE PCM (3840 B @ 20 ms)
```

- The **server** assigns the audio `Sequence` (independent counter from video) in
  `broadcast_audio`. PCM at 20 ms is ~3840 B → fragmented (~4 datagrams) like video;
  Opus is one datagram.
- Audio params (codec, sample rate, channels) are in the `config` message, not the
  per-frame header (resolves old R-AUD-10).

---

## A/V Sync — render video to the audio clock (audio is master)

The realtime rule: **audio must never glitch; video bends to match it.** Human
hearing detects a 20 ms audio dropout instantly, while a frame of video judder is
invisible. So audio is the **master clock** and video slaves to it — the reverse
of the old spec (which held/dropped *audio* to match video and is wrong for
realtime).

- **One shared clock:** `CLOCK_MONOTONIC` ns, single process epoch, both audio and
  video stamped **at capture**.
- **Audio playout is the presentation clock.** The client plays audio **gaplessly**
  from a small (~40 ms) jitter buffer; the `AudioContext` playout position defines
  "now". Audio is never held or dropped for sync (only Opus PLC fills genuine loss).
- **Video slaves to the audio clock.** At draw time the renderer presents the
  decoded frame whose capture `Timestamp` is nearest the current audio playout
  time:
  - video running **ahead** → hold the current frame a beat;
  - video **behind** by more than ~1 frame interval → drop frame(s) to catch up.
- Desync is therefore **self-correcting and bounded by the audio buffer (~40 ms)**.

If audio is disabled or no audio add-on is loaded, video presents on its own
capture clock (the pre-audio behavior) — there is no master to slave to.

---

## Realtime Pipeline

```
[capture add-on read loop]
    → read one frame of PCM from the OS device (WASAPI/SCK/PipeWire)
    → normalize to 48k/stereo/S16LE
    → ts = clock.now()                 // CLOCK_MONOTONIC ns, AT CAPTURE
    → PcmChunk { data, timestamp_ns: ts }
    → host pumps next_chunk() into mpsc channel  (SMALL: ~3–4 frames ≈ 60–80 ms, drop-OLDEST on overflow)
[pipeline audio loop]  (separate task, see MODULE_PIPELINE)
    → enc.encode(chunk)  → payload   (Opus packet or PCM passthrough)
    → server.broadcast_audio(codec_type, payload, chunk.timestamp_ns)   // assigns audio Sequence
[server] → fragment if needed → datagrams (sent IMMEDIATELY, not batched)
```

- **Timestamp placement is load-bearing.** Stamping at capture (above) keeps audio
  on the same monotonic timeline as video. Stamping at consumption time (the
  original bug) added up to 640 ms of buffer skew and broke sync.
- **Small buffers everywhere.** The old 640 ms (cap-32) back-pressure buffer is
  replaced by a ~60–80 ms capture channel and a ~40 ms client jitter buffer.
  Realtime > completeness: on overflow, **drop the oldest** chunk so the consumer
  always gets the freshest audio.

---

## Browser Playback

1. `AudioContext` at 48 kHz, created on the first user gesture (autoplay policy).
2. Read the `audioCodec` from the `config` message and pick the decode path:
   - **Opus:** WebCodecs `AudioDecoder({codec:"opus", …})` → `AudioData`; fall back
     to a small wasm libopus decoder where `AudioDecoder` lacks Opus (older Safari).
   - **PCM:** convert S16LE → Float32 (÷32768) directly, no decoder.
3. If `config.audioChannels` exceeds the output device's channel count, **downmix**
   to the device layout (e.g. 5.1 → stereo) before buffering; else keep all
   channels. (`AudioContext.destination.maxChannelCount` reports the device.)
4. Push decoded Float32 (interleaved per `config.audioLayout`) into an
   `AudioWorklet` ring buffer (~40 ms), configured for the playout channel count.
5. The worklet plays gaplessly; its playout position drives the video sync clock
   (see "A/V Sync"). On a genuine gap with no Opus FEC recovery, the worklet
   outputs PLC/silence for that 20 ms rather than stalling.

---

## Configuration

```toml
[audio]
enabled    = false   # opt-in. Requires a loaded audio capture add-on.
frame_ms   = 20      # 10 or 20 (lower = less latency, ~2× packet rate)
channels   = "auto"  # "auto" = follow the host output layout (≤7.1); "stereo" =
                     # force a host-side downmix to 2.0
# codec is chosen by which add-on is loaded: `opus` ⇒ Opus (multistream for 5.1/7.1), else PCM.

[addon_module_wasapi]   # Windows
device = ""             # "" = default render endpoint (loopback). Or an endpoint id.

[addon_module_sck_audio] # macOS — rides the sck screen-capture session
exclude_current_process = true  # don't capture FeatherDesk's own output

[addon_module_pipewire]  # Linux
target = ""             # "" = auto-detect the default sink's .monitor
```

`[audio] enabled = true` with no audio add-on loaded → startup **warning**,
audio silently disabled (matches the gamepad/input "needs an add-on" pattern).

---

## Refactoring Directives (status)

| ID | Was | Now |
|----|-----|-----|
| R-AUD-01 | race in `pw-cat reconnect()` | N/A — no subprocess; native capture add-ons own their lifecycle |
| R-AUD-02/03 | SIGTERM/backoff for `pw-cat` | N/A — no subprocess |
| R-AUD-04 | defined in `featherdesk-audio` | Core `AudioCapturer`/`AudioEncoder` traits in `featherdesk-audio`; add-on impls in `addons/audio/<id>/` |
| R-AUD-05 | custom logger | **Done** — `tracing` everywhere |
| R-AUD-06 | Opus "future" | **Now the default codec** (`opus` add-on); PCM is the fallback |
| R-AUD-07 | configurable buffer | Buffers are small + fixed for realtime; `frame_ms` is the only knob |
| R-AUD-09 | ALSA/Pulse fallback | Lives inside the `pipewire` Linux add-on |
| R-AUD-10 | overloaded Width/Height | **Done** — audio params moved to the `config` message |

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | PCM normalization (device format → 48k/stereo/S16LE) | No |
| Unit | S16LE ↔ Float32 conversion | No |
| Unit | Back-pressure drop-oldest behavior | No |
| Unit | Opus encode/decode round-trip + FEC framing | No (libopus) |
| Unit | A/V sync: video frame selection against a synthetic audio clock | No |
| Integration | Full capture→encode→broadcast (5 s) per OS add-on | Yes (per OS) |
| Mock | Fake AudioCapturer (tone/silence generator) for sync + server tests | No |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Capture latency | <25 ms (20 ms frame + pipeline) |
| Opus encode latency | <5 ms / frame |
| End-to-end audio added latency | <60 ms (capture + ~40 ms client buffer) |
| Bandwidth (Opus, stereo) | 96–128 kbps (5.1 ≈ 256–384, 7.1 ≈ 384–512 kbps) |
| Bandwidth (PCM, stereo) | 1.536 Mbps (768 kbps/ch → 5.1 ≈ 4.6, 7.1 ≈ 6.1 Mbps) |
| A/V desync bound | ≤ ~40 ms (audio jitter buffer) |
| CPU (Opus encode) | <1% |
