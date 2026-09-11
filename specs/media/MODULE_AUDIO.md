# Module Spec: Audio

> # 🔒 DESIGN LOCKED · ⏸️ IMPLEMENTATION DEFERRED
>
> **Design status:** Current. This spec reflects the locked QUIC / pluggable
> add-on architecture (host→client system audio, per-OS capture add-ons,
> pluggable Opus/PCM codec, realtime **audio-master** A/V sync). It supersedes
> the old Linux-only `pw-cat` subprocess design.
>
> **Implementation status:** Deferred behind **video capture+encode working
> end-to-end on Linux** — not on all three OSes. The earlier all-three trigger
> was unsatisfiable: `BRANCH.md` "Migration Strategy" step 4 states that Windows
> and macOS are specced, not built, and gate no cutover, so a trigger requiring
> them deferred audio indefinitely while reading as "coming later" (GAP_TRIAGE
> OQ-02). Linux is the platform with a working Go reference implementation, so
> the trigger now names the only platform that can actually fire it. Un-deferring
> lands two add-ons — `pipewire` capture and the `opus` codec — and nothing else.
>
> **`[audio] enabled` stays `false` by default.** Audio-master sync moves
> motion-to-photon from ~20 ms to ~65 ms (~45 ms at `frame_ms = 10`), so the
> latency argument is an argument about the default, not about shipping the
> feature. Locking the design now stops it contradicting the rest of the spec
> base; no code is pulled forward.
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
// NO CHANNEL CROSSES THE ADD-ON ABI, and there is no host-side pump either: the
// host's audio THREAD calls next_chunk() directly (MODULE_PIPELINE, the audio
// loop). The ~60–80 ms decoupling buffer lives INSIDE the add-on — its OS device
// callback fills a 4-frame drop-oldest ring that next_chunk() pops. Each chunk is
// exactly one frame (frame_samples * channels * 2 bytes, S16LE interleaved),
// stamped AT CAPTURE (see "Realtime").
//
// Thread-affine, but declared `Send`. The host constructs, uses and drops this
// object on a single worker thread (the audio thread — CENTRAL_SPEC "Concurrency
// Model") and it never crosses a thread boundary once built — `Send` is required
// only because the empty `FrameLoop` / `AudioLoop` struct that will build it is
// moved onto that thread by `Pipeline::start`. The object may hold a bound COM
// apartment or CoreAudio context for its whole life (MODULE_ABI "Thread
// requirements").
pub trait AudioCapturer: Send {
    /// next_chunk delivers the next timestamped PCM chunk. Ok(Some(chunk)) = a
    /// chunk; Ok(None) = the capturer has stopped (end of stream); Err = failure.
    /// The deadline is **≤ 200 ms**; `AudioLoop::run` checks `cancel` between
    /// calls, so a cancelled session leaves within one deadline plus one encode —
    /// which is what keeps the audio join inside `Pipeline::start`'s 2 s budget
    /// (MODULE_PIPELINE shutdown step 5).
    fn next_chunk(&mut self) -> Result<Option<PcmChunk>, AudioError>;

    /// format reports the negotiated capture format (rate/channels/layout). The
    /// add-on resamples and reorders the OS device format to this canonical
    /// format — 48 kHz, S16LE, interleaved, Vorbis channel order (see
    /// "Surround") — so the encoder and client see one shape regardless of OS.
    /// The CHANNEL COUNT is not fixed: it follows the host output layout up to
    /// 7.1, or is 2 when [audio] channels = "stereo".
    fn format(&self) -> Format;
}

// AudioEncoder turns PCM chunks into wire payloads. Selected by loaded add-on:
//   opus  → addons/audio/opus/  (libopus, BSD, in-process Rust FFI)
//   <none>→ PCM passthrough (built-in; copies the S16LE bytes through)
// Cleanup is RAII (Drop) — no Close(). Thread-affine on the audio thread, like
// the capturer, and declared `Send` for the same reason.
pub trait AudioEncoder: Send {
    /// encode encodes one PCM chunk into one wire payload (Opus packet or raw
    /// PCM). The returned RVec<u8> is owned (ownership transfers — deterministic
    /// drop across the add-on ABI). The codec string for the config handshake is
    /// reported by codec().
    fn encode(&mut self, chunk: &PcmChunk) -> Result<RVec<u8>, AudioError>;

    /// codec returns the codec advertised in the config message, e.g.
    /// "opus" or "pcm/s16le". The client configures its decoder from this.
    fn codec(&self) -> &str;

    /// description returns the codec-specific decoder-init bytes the client needs
    /// (Opus: the OpusHead of RFC 7845 5.1; PCM: empty). Carried on the wire as
    /// config.audioDescription, base64.
    fn description(&self) -> RVec<u8>;
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
// correctly. THE ORDER ON THE WIRE IS THE VORBIS I ORDER, which is what Opus
// channel mapping family 1 defers to (RFC 7845 section 5.1.1.2). It is NOT the
// WAV/SMPTE order: C and R are transposed and LFE is LAST, not fourth. The PCM
// passthrough codec uses the same order, so the client's deinterleave does not
// depend on which codec is active.
//
// Declared in `featherdesk-abi` (MODULE_ABI), next to `ChannelMode`, because it is
// a field of `RAudioFormat` and every field of a `#[derive(StableAbi)]` struct
// must itself be `StableAbi` — `#[repr(u8)]` alone provides nothing. This crate
// re-exports it, so every `audio::ChannelLayout` path is unchanged; the
// discriminants are the values this declaration already had implicitly.
pub use abi::ChannelLayout;

// AudioConfig is the core config (from [audio] TOML). Per-add-on device
// selection lives in [addon_module_<id>]. It is forwarded verbatim onto the audio
// thread as `pipeline::AudioPlan.cfg` (MODULE_PIPELINE), alongside the ids of the
// add-ons startup selection chose. Logging is via the `tracing` crate.
// An add-on's events reach the operator only if it installed the host's sink —
// `featherdesk_abi::install_log_sink(host.log)` in `init()` (MODULE_ABI "Root
// module surface"); each `cdylib` otherwise has its own uninitialised dispatcher
// and its output is discarded.
pub struct AudioConfig {
    pub frame_ms: u32,    // 10 or 20 (default 20). Drives PcmChunk size + Opus frame.
    pub channels: String, // "auto" (follow host, ≤7.1) | "stereo" (force downmix at host)
                          // (boundary form: ChannelMode)
}

pub const DEFAULT_SAMPLE_RATE: u32 = 48000;
pub const DEFAULT_CHANNELS: u8 = 2;   // the fallback when the host layout cannot be read; NOT a cap
pub const MAX_CHANNELS: u8 = 8;       // 7.1
pub const DEFAULT_FRAME_MS: u32 = 20; // 20 ms @ 48 kHz = 960 samples/chan
// chunk_bytes(ch) for a 20 ms frame = 960 * ch * 2B (e.g. stereo 3840, 5.1 11520).

// AudioError — the audio crate's error enum. Audio is a SEPARATE crate with its
// own loop, so it is NOT folded into StreamError. Across the add-on ABI it crosses
// as the AbiErr code the host maps back (see MODULE_ABI).
#[derive(Debug, thiserror::Error)]
pub enum AudioError {
    #[error("audio: capture device lost / invalidated")] DeviceLost,        // AbiErr::DeviceLost (6)
    #[error("audio: add-on unrecoverable, do not retry: {0}")] Unrecoverable(String), // AbiErr::Unrecoverable (7)
    #[error("audio: add-on does not serve this optional method")] Unsupported,        // AbiErr::Unsupported (8)
    #[error("audio: backend failure: {0}")] Backend(String),                // AbiErr::Generic (1)
}
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
mix is often 32-bit float at the device rate) to the canonical **48 kHz / S16LE /
interleaved / Vorbis channel order**, with the channel count following the host
output layout up to 7.1 (or forced to 2 by `[audio] channels = "stereo"`), before
handing chunks to the core, so the encoder and client are OS-agnostic. On macOS
the audio rides the SCK session already opened for screen capture — same process,
same clock.

### Device changes

All three add-ons handle a mid-session **default-output change** the same way:
re-attach to the new default endpoint and **synthesize silence across the gap**,
so the capture-stamped timeline stays continuous and the master clock never
stalls (a stalled clock would otherwise trip the watchdog in "Leaving
audio-master" and drop the whole session out of audio-master for a device
switch). If the new endpoint's layout differs from the old one (stereo ↔ 5.1),
the add-on re-resolves the layout and the pipeline pushes a fresh `config` with
the new `audioChannels` / `audioLayout` / `audioDescription`. Each add-on names
the OS mechanism it listens on — `IMMNotificationClient`,
`kAudioHardwarePropertyDefaultOutputDevice`, `STREAM_CAPTURE_SINK` — in its own
spec; the rule they implement is this one.

---

## Codec (pluggable, advertised in `config`)

| Codec | Add-on ID | Library | Bandwidth | On loss | Wire type |
|-------|-----------|---------|-----------|---------|-----------|
| **Opus** (default when loaded) | [`opus`](../addons/OPUS_AUDIO_CODEC_SPEC.md) | libopus (BSD, Rust FFI) | ~96–128 kbps VBR | **FEC + PLC** where the decoder can reach them (wasm/native); **worklet-side concealment** in the WebCodecs path — see "Loss concealment, by path" | `frame_type::AUDIO_OPUS` (0x08) |
| **Raw PCM** (built-in fallback) | — | none | 1.536 Mbps | a lost packet = a ~20 ms gap (no concealment) | `frame_type::AUDIO_PCM` (0x04) |

- The server advertises the codec in the **`config`** control-stream message
  (`audioCodec`, `audioSampleRate`, `audioChannels`, `audioLayout`,
  `audioDescription`) — the client never hardcodes them. This kills the old
  `Width`/`Height` overload (those fields are unused/zero for audio now).
- **Opus settings:** application = `OPUS_APPLICATION_AUDIO` (general system audio,
  not just voice), 20 ms frames, VBR, **in-band FEC enabled**, complexity tuned
  for low latency. A stereo 20 ms Opus packet is ~100–300 bytes → **one datagram**,
  no fragmentation. 5.1/7.1 packets are larger but still typically one datagram
  (and fragment cleanly as a media type if not).
- **Multichannel** uses **Opus multistream** (`opus_multistream_encoder`, channel
  mapping family 1) for 5.1 (6ch) / 7.1 (8ch), and the OpusHead that makes it
  decodable travels in `config.audioDescription` (see below). PCM passthrough
  simply carries the interleaved N-channel S16LE.
- **Loss concealment, by path.** The encoder always runs with
  `OPUS_SET_INBAND_FEC(1)` and `OPUS_SET_PACKET_LOSS_PERC(10)` — FEC emits
  redundancy only when the encoder expects loss, so a zero here silently disables
  the feature. What the receiver can do with it differs:

  | Client path | Gap detection | Concealment |
  |-------------|---------------|-------------|
  | Browser, WebCodecs `AudioDecoder` | audio `Sequence` gap at the decode site | **Worklet-side only.** WebCodecs exposes no FEC entry point and no null-packet decode, so the client synthesizes `frame_ms` of concealment: repeat the last decoded frame with a linear fade to silence across it, and advance `audioPlayoutTs` by exactly `frame_ms` so the master clock never stalls. |
  | Browser, wasm libopus fallback | same | **Full Opus FEC + PLC.** `opus_decode(next_packet, …, decode_fec=1)` reconstructs the lost frame from the following packet; a second consecutive loss falls back to `opus_decode(NULL, 0, …)` PLC. |
  | Native client (v2) | same | Full Opus FEC + PLC, as above. |

  A browser therefore gets *bounded, clock-preserving* concealment rather than
  Opus's own — which is a real improvement over a hard hole, and is not the same
  claim. Do not describe browser playback as FEC-protected.
- **Gap detection has a consumer.** `audio.js` keeps `lastAudioSeq`. At the
  decode site (never in the worklet, which sees only post-decode Float32 and can
  therefore never issue an FEC or PLC decode), `gap = seq - lastAudioSeq - 1`;
  for each missing frame it runs the concealment for the active path, advances
  `audioPlayoutTs` by `frame_ms`, and increments an `audioGaps` counter reported
  in the 1 Hz `{"type":"stats"}` line.

### The Opus identification header (`config.audioDescription`)

Multichannel Opus is only decodable if the decoder is told the stream/coupled
counts and the channel mapping. Those live exclusively in the OpusHead. The
`opus` add-on builds it once, at construction, exposes it through
`AudioEncoder::description()`, and the server carries it in
`config.audioDescription` as base64.

```
offset  size  field                value
------  ----  -------------------  --------------------------------------------
  0      8    Magic Signature      "OpusHead" (ASCII)
  8      1    Version              1
  9      1    Output Channel Count N (1..8)
 10      2    Pre-skip (u16 LE)    OPUS_GET_LOOKAHEAD from the encoder
                                   (312 at 48 kHz with default complexity)
 12      4    Input Sample Rate    48000 (u32 LE; informational)
 16      2    Output Gain (i16 LE) 0
 18      1    Mapping Family       0 for N <= 2, 1 for N >= 3
--- the next three fields are present ONLY for Mapping Family 1 ---
 19      1    Stream Count         see table
 20      1    Coupled Count        see table
 21      N    Channel Mapping      see table
```

Total length: 19 bytes for family 0, `21 + N` bytes for family 1 (25 for 4ch,
27 for 6ch, 29 for 8ch).

Stream/coupled counts and mapping are the RFC 7845 §5.1.1.2 defaults for family
1, which are also what `opus_multistream_surround_encoder_create` produces:

| N | Family | Streams | Coupled | Channel Mapping |
|---|--------|---------|---------|-----------------|
| 1 | 0 | 1 | 0 | (absent) |
| 2 | 0 | 1 | 1 | (absent) |
| 6 (5.1) | 1 | 4 | 2 | `0 4 1 2 3 5` |
| 8 (7.1) | 1 | 5 | 3 | `0 6 1 2 3 4 5 7` |

The client passes it straight through:

```javascript
const audioCfg = {
  codec: cfg.audioCodec,               // "opus"
  sampleRate: cfg.audioSampleRate,     // 48000
  numberOfChannels: cfg.audioChannels, // 1..8
};
if (cfg.audioDescription) {
  audioCfg.description = base64ToBytes(cfg.audioDescription);
}
if (!(await AudioDecoder.isConfigSupported(audioCfg)).supported) {
  // fall back to the wasm libopus decoder (see "Browser Playback"), which takes
  // the same OpusHead bytes
}
audioDecoder.configure(audioCfg);
```

`audioDescription` is the empty string for the PCM codec and for mono/stereo
Opus (mapping family 0 needs no description), and is REQUIRED and non-empty
whenever `audioCodec == "opus"` and `audioChannels > 2`.

---

## Surround (5.1 / 7.1)

Audio follows the **host's output layout**: if the remote machine is set to 5.1
or 7.1, the capture add-on delivers 6/8 channels and they stream through
end-to-end; if the host is stereo, you get stereo. There is **no upmixing**.

- `[audio] channels = "auto"` (default) — follow the host layout, capped at 7.1.
  `"stereo"` forces a host-side downmix (useful to save bandwidth or when the
  client is known to be stereo).
- **Channel order on the wire is the Vorbis I order** — `L C R Ls Rs LFE` for
  5.1 and `L C R Ls Rs Rls Rrs LFE` for 7.1 — because that is the order Opus
  channel mapping family 1 defers to (RFC 7845 §5.1.1.2). It is **not** the
  WAV/SMPTE/`WAVEFORMATEXTENSIBLE` order (`L R C LFE …`) the OS device layouts
  use. The capture add-on reorders the OS device layout into the Vorbis order;
  `config.audioLayout` tells the client which layout to expect.
- **The client permutes back at the sink.** The Web Audio API's `"speakers"`
  channel interpretation defines 6 channels as `L R C LFE SL SR` (the WAV order)
  and defines no layout above 6, so `audio.js` applies a fixed permutation
  between the decoded Vorbis-order frames and the `AudioWorklet` ring buffer.
  `dst[i] = src[PERM[i]]`:

  | Layout | `PERM` (destination index → source index) | Destination order |
  |--------|-------------------------------------------|-------------------|
  | 5.1 | `[0, 2, 1, 5, 3, 4]` | `L R C LFE SL SR` (Web Audio "speakers") |
  | 7.1 | `[0, 2, 1, 7, 5, 6, 3, 4]` | `L R C LFE BL BR SL SR` (WAV 7.1; Web Audio treats >6 as "discrete") |

- **Client downmix.** Most viewers are stereo. If the decoded channel count
  exceeds `AudioContext.destination.maxChannelCount`, the client downmixes from
  the **Vorbis-order** frame before the permutation, with these coefficients, then
  clamps each sample to `[-1.0, 1.0]`:

  ```
  5.1 -> 2.0:  L' = L + 0.7071*C + 0.7071*Ls
               R' = R + 0.7071*C + 0.7071*Rs
  7.1 -> 2.0:  L' = L + 0.7071*C + 0.5*Ls + 0.5*Rls
               R' = R + 0.7071*C + 0.5*Rs + 0.5*Rrs
  ```

  **LFE is discarded**, per ITU-R BS.775-3 — folding it into a stereo mix at any
  gain produces bass build-up and clipping with no benefit on the small speakers
  a viewer is most likely using.

---

## Wire Format

Audio is a **media** datagram type (S→C, unreliable). Like video it carries the
22-byte `FrameHeader` (it needs a wire **Timestamp** for A/V sync and a
**Sequence** the client's decode site uses for loss detection and concealment —
see [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) "Audio Playback
Pipeline") — but unlike video it is **single-datagram** for Opus (no
fragmentation). See [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) "media vs
control" datagram classes. On the WebSocket fallback carrier the same payload
travels as a reliable tagged message with the same headers — only the carrier
differs (see [`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Carrier
selection").

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
invisible — but the cost is not judder alone: slaving video to the audio clock
adds the whole jitter buffer to video motion-to-photon (CENTRAL_SPEC
"Motion-to-photon budget"). So audio is the **master clock** and video slaves to
it — the reverse of the old spec (which held/dropped *audio* to match video and
is wrong for realtime).

- **One shared clock:** `CLOCK_MONOTONIC` ns, single process epoch, both audio and
  video stamped **at capture**.
- **Audio playout is the presentation clock.** The client plays audio **gaplessly**
  from a small (~40 ms) jitter buffer; the `AudioContext` playout position defines
  "now". Audio is never held or dropped for sync (only the concealment above fills
  genuine loss).
- **Video slaves to the audio clock.** At draw time the renderer presents the
  decoded frame whose capture `Timestamp` is nearest the current audio playout
  time:
  - video running **ahead** → hold the current frame a beat;
  - video **behind** by more than ~1 frame interval → drop frame(s) to catch up.
- Desync is therefore **self-correcting and bounded by the audio buffer (~40 ms)**.

**Leaving audio-master.** If audio is disabled or no audio add-on is loaded,
video presents on its own capture clock from the start — there is no master to
slave to. Mid-session, the client falls back to that same local clock on either
of two triggers, whichever fires first: a `config` whose `audio` is `false` after
one whose `audio` was `true` (the host-side signal, below), or a **watchdog** —
`audioPlayoutTs` has not advanced for **200 ms** while video frames are still
arriving. Either alone would leave a hole: the signal can be lost with the
connection that carries it, and the watchdog alone would be guessing. Both legs
are specified in [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md)
"Leaving audio-master".

---

## Realtime Pipeline

```
[capture add-on read loop]
    → read one frame of PCM from the OS device (WASAPI/SCK/PipeWire)
    → normalize to 48k / S16LE / host layout (Vorbis order)
    → ts = clock.now()                 // CLOCK_MONOTONIC ns, AT CAPTURE
    → PcmChunk { data, timestamp_ns: ts }
    → into the add-on's OWN ring (4 frames ≈ 60–80 ms, drop-OLDEST on overflow)
[pipeline audio loop]  (dedicated audio thread, see MODULE_PIPELINE)
    → next_chunk() is called directly by the host's audio thread — no host-side
      channel sits between capture and encode
    → enc.encode(chunk)  → payload   (Opus packet or PCM passthrough)
    → server.broadcast_audio(codec_type, payload, chunk.timestamp_ns)   // assigns audio Sequence
[server] → fragment if needed → datagrams (sent IMMEDIATELY, not batched)
```

- **Timestamp placement is load-bearing.** Stamping at capture (above) keeps audio
  on the same monotonic timeline as video. Stamping at consumption time (the
  original bug) added up to 640 ms of buffer skew and broke sync.
- **Small buffers everywhere.** The old 640 ms (cap-32) back-pressure buffer is
  replaced by exactly two: the add-on's own 4-frame capture ring (~60–80 ms) and
  the per-session `audio_out` ring the server's datagram pump drains, plus a
  ~40 ms client jitter buffer. There is no third queue between capture and
  encode. Realtime > completeness: on overflow, **drop the oldest** chunk so the
  consumer always gets the freshest audio.
- **When the audio path ends, the session says so.** If `next_chunk()` returns
  `Ok(None)` (end of stream) or the audio error ladder gives up, the audio thread
  drops the encoder and the capturer and the pipeline pushes a fresh `config` with
  `audio: false` (and `audioCodec: ""`, `audioChannels: 0`, `audioDescription:
  ""`), logging once at `warn` with the reason. Audio does not come back within
  the session; re-enabling it requires a restart, exactly like a capture add-on
  that has been poisoned. Video is untouched.

---

## Browser Playback

1. `AudioContext` at 48 kHz, created on the first user gesture (autoplay policy).
2. Read the `audioCodec` from the `config` message and pick the decode path:
   - **Opus:** WebCodecs `AudioDecoder({codec:"opus", …})` → `AudioData`; fall back
     to a small wasm libopus decoder where `AudioDecoder` lacks Opus (older Safari).
     Both take the `config.audioDescription` OpusHead bytes when present.
   - **PCM:** convert S16LE → Float32 (÷32768) directly, no decoder.
3. If `config.audioChannels` exceeds the output device's channel count, **downmix**
   to the device layout (e.g. 5.1 → stereo) before buffering; else keep all
   channels. (`AudioContext.destination.maxChannelCount` reports the device.)
4. Push decoded Float32 (permuted to the Web Audio order per "Surround") into an
   `AudioWorklet` ring buffer (~40 ms), configured for the playout channel count.
5. The worklet plays gaplessly; its playout position drives the video sync clock
   (see "A/V Sync"). Loss is detected and concealed at the **decode site** (see
   "Loss concealment, by path"), which advances `audioPlayoutTs` across the gap —
   the worklet only plays what it is handed and never stalls waiting for a packet
   that was lost.
6. **Teardown and backgrounding** are specified in
   [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) **R-CLI-14**. In
   short: the worklet node is disconnected *before* `audioContext.close()` (never
   the reverse — a closing context can still run `process()`), teardown is
   idempotent and hangs off `pagehide` rather than `unload` (so the page stays
   bfcache-eligible and it fires reliably on mobile), and a **backgrounded tab
   keeps playing audio gaplessly** while video decoding stops. That asymmetry is
   deliberate: audio is the master clock here, so pausing it would desync the
   stream on return, and browsers throttle timers but not `AudioWorklet`.

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
| Unit | PCM normalization (device format → 48 kHz / S16LE / host layout in Vorbis order) | No |
| Unit | S16LE ↔ Float32 conversion | No |
| Unit | Back-pressure drop-oldest behavior | No |
| Unit | Channel order: a synthetic 5.1 source with a distinct tone per speaker survives capture → encode → decode → permute → downmix with each tone in the right destination channel. The regression this guards is the WAV-vs-Vorbis transposition (C/R swapped, LFE moved) | No |
| Unit | **Buffer conservation (TD-40 regression guard)** — feed N seconds of synthetic samples through the capture→normalize→frame-assembly path with back-pressure disabled; assert `samples_out == samples_in` exactly, with zero silent drop. The historical `PCMProcessor.process()` bug discarded ~83 % of samples (kept 128 of every 960) and shipped undetected because nothing counted. Any drop must be *explicit* (a back-pressure event with a counter), never a consequence of a buffer-size mismatch | No |
| Unit | **Client-side ring-buffer conservation** — the browser `AudioWorklet` processor is the exact site of the original bug: push chunks of the server's `frame_ms` size into it and pull 128-sample render quanta out; assert the total pulled equals the total pushed (modulo the tail still resident in the ring) and that a chunk size that is *not* a multiple of 128 loses nothing at the boundary | No |
| Unit | Opus encode/decode round-trip + FEC framing | No (libopus) |
| Unit | `description()` produces a byte-exact OpusHead: 19 bytes for stereo (family 0), 27 for 5.1 with stream/coupled counts 4/2 and mapping `0 4 1 2 3 5`, 29 for 7.1 with 5/3 and `0 6 1 2 3 4 5 7`; PCM returns empty | No |
| Unit | Concealment: dropping every 10th audio datagram advances `audioPlayoutTs` by exactly `frame_ms` per loss and produces no clock discontinuity; `audioGaps` counts each one | No |
| Unit | A/V sync: video frame selection against a synthetic audio clock | No |
| Integration | Full capture→encode→broadcast (5 s) per OS add-on | Yes (per OS) |
| Integration | A mid-session default-output change re-attaches, synthesizes silence across the gap, and never stalls the master clock; a layout change with it pushes a fresh `config` | Yes (per OS) |
| Mock | Fake AudioCapturer (tone/silence generator) for sync + server tests | No |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Capture latency | <25 ms (20 ms frame + pipeline) |
| Opus encode latency | <5 ms / frame |
| End-to-end added latency, audio path | <60 ms (capture + ~40 ms client buffer) |
| Motion-to-photon penalty imposed on VIDEO | +40–45 ms (the jitter buffer the renderer slaves to) — see CENTRAL_SPEC "Motion-to-photon budget" |
| Bandwidth (Opus, stereo) | 96–128 kbps (5.1 ≈ 256–384, 7.1 ≈ 384–512 kbps) |
| Bandwidth (PCM, stereo) | 1.536 Mbps (768 kbps/ch → 5.1 ≈ 4.6, 7.1 ≈ 6.1 Mbps) |
| A/V desync bound | ≤ ~40 ms (audio jitter buffer) |
| CPU (Opus encode) | <1% |
