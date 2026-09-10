# macOS Audio Add-On: ScreenCaptureKit Audio

> 🔒 Design locked · ⏸️ Implementation deferred behind the video trigger (see
> [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).

## Purpose

The `sck_audio` add-on is the macOS **system-audio capture** backend for the core
Audio module ([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).
It implements the `AudioCapturer` trait using **ScreenCaptureKit's** built-in audio
(`SCStreamConfiguration.capturesAudio`, macOS 13+) — no driver, no virtual device.

Its key advantage: the `sck` **capture** add-on is already running an `SCStream`
for screen video, so audio is captured on the **same stream and the same clock**.

---

## Relationship to the `sck` capture add-on

`sck_audio` does **not** open its own `SCStream`. It attaches an **audio output**
to the single `SCStream` the `sck` capture add-on owns:

- The `sck` add-on creates the `SCStream`; `sck_audio` registers an
  `SCStreamOutput` for `SCStreamOutputType.audio` on it (video uses
  `.screen`).
- Therefore `sck_audio` **requires** `sck` to be loaded and active. If
  `[audio] enabled` but the capture add-on isn't `sck`, `probe` fails with a
  clear message (system audio on macOS is only wired through SCK here).
- One stream, one permission prompt (Screen Recording), one clock.

```objc
// In the shared SCStreamConfiguration (set by sck, audio fields added here):
cfg.capturesAudio              = YES;
cfg.sampleRate                 = 48000;
cfg.channelCount               = <host layout channel count, 1..8; 2 when [audio] channels = "stereo">;
                               // read from the default output device via
                               // AudioObjectGetPropertyData(kAudioDevicePropertyPreferredChannelLayout)
                               // (AVAudioSession on the iOS-family equivalent) — NOT a constant
cfg.excludesCurrentProcessAudio = YES;   // don't capture FeatherDesk's own output

// Audio output delegate:
- (void)stream:(SCStream *)s didOutputSampleBuffer:(CMSampleBufferRef)sb
                                         ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeAudio) return;
    // CMSampleBuffer → AudioBufferList (Float32) → normalize → emit with ts
}
```

### Format & normalization

SCK delivers Float32 PCM at the configured rate/channels. The add-on converts to
the canonical format MODULE_AUDIO defines — **48 kHz, S16LE, interleaved, Vorbis
channel order** — with the channel count **following the host output layout** up
to 7.1:

- float32 → S16LE (clamp + scale),
- reorder CoreAudio's `AudioChannelLayout` (`L R C LFE …`) into the **Vorbis**
  order (`L C R Ls Rs LFE`) MODULE_AUDIO defines — the two differ by a C/R
  transposition and by LFE moving from fourth to last. Downmix to stereo only if
  `[audio] channels = "stereo"`,
- resample if the device rate ≠ 48 kHz (`rubato`, sinc interpolation, MIT —
  declared as a dependency of this add-on; bypassed entirely when the device
  already runs at 48 kHz).

The channel count is **not** fixed at 2: `format()` reports what the host layout
actually is, and `config.audioLayout` tells the client which layout to expect.

### Timestamp & clock mapping

The `CMSampleBuffer` presentation timestamp is host-time based
(`mach_absolute_time` units). The add-on maps it into the process's
`CLOCK_MONOTONIC` epoch (the same mapping the `sck` video add-on uses for frame
timestamps) so audio and video share one timeline — this is what makes
audio-master A/V sync exact on macOS.

---

## License

| Component | License |
|-----------|---------|
| ScreenCaptureKit / CoreMedia / CoreAudio | Apple system frameworks — linked, not redistributed |
| `rubato` (sample-rate conversion) | MIT — the only third-party dependency; pure Rust, no C library |
| Our Rust FFI / Obj-C++ binding | MIT |

No driver. Requires the **Screen Recording** permission (already needed for `sck`
video — audio adds no new prompt). App must be signed + notarized.

---

## Build & Distribution

Build each add-on as its own shared library and drop the set into the add-ons directory:
```bash
cargo build --release -p featherdesk-addon-sck   # cdylib  featherdesk-addon-sck.dylib
cargo build --release -p featherdesk-addon-sck_audio   # cdylib  featherdesk-addon-sck_audio.dylib
cargo build --release -p featherdesk-addon-vt_hw   # cdylib  featherdesk-addon-vt_hw.dylib
cargo build --release -p featherdesk-addon-opus   # cdylib  featherdesk-addon-opus.dylib
```

`sck_audio` is meaningless without `sck`; the probe enforces the pairing.

---

## Constructor & Probe

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-sck_audio   (cfg(target_os = "macos"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// `descriptor().kind` is `AudioCapture` (0x04). Attaches the audio output to
/// the shared `SCStream` and starts emitting PcmChunks; it receives a handle to
/// the sck stream through the pipeline wiring. Called on the audio thread;
/// `new_codec` returns `PipelineError::AddonBackend` here.
fn new_capturer(&self, cfg: audio::AudioConfig)
    -> Result<Box<dyn audio::AudioCapturer>, PipelineError>;
```

`probe` checks the OS version and that `sck` is the selected capturer, and
resolves the host output layout. It is side-effect-free — no audio output is
attached until `new_capturer`. On success it reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(), codecs: RVec::new(),
    caps: AddonCaps(0),          // AudioCapturer has no optional methods
    displays: RVec::new(),
})
```

**Availability is not an error.** macOS 12 or a capturer that is not `sck` is
`ROk(ProbeReport { available: false, reason: "sck_audio requires macOS 13+ and
the sck capture add-on" })`, never an `RErr`. **Set every capability bit this
add-on actually serves** — `AddonCaps(0)` is correct and complete here, because
`AudioCapturer` has no optional methods. **Only claim what this call can prove:**
a bit claimed here and refused later is a capability lie (MODULE_ABI
"Misbehaving add-ons").

---

## Error Handling

| Failure | Returned as | Behavior |
|---------|-------------|----------|
| Pre-macOS 13 | `ProbeReport { available: false, reason }` | Add-on not selected; the host streams video only. Log "SCK audio requires macOS 13+" |
| `sck` not the active capturer | `ProbeReport { available: false, reason }` | Same; log "sck_audio requires the sck capture add-on" |
| Screen-Recording permission denied | `ProbeReport { available: false, reason }` | Surfaces via the shared `sck` permission flow; audio disabled with the same notice |
| Stream stops / reconfigures (display change) | — | The audio output is re-attached when `sck` rebuilds the stream, and **silence is synthesized across the gap** so the capture-stamped timeline stays continuous |
| Default output device changes mid-session | — | The SCK audio tap follows the system default. The add-on registers an `AudioObjectAddPropertyListener` for `kAudioHardwarePropertyDefaultOutputDevice`, and on notification re-attaches the audio output to the shared `SCStream` (re-applying `capturesAudio`, the rate and the resolved channel count). Silence is synthesized for the gap so the capture-stamped timeline stays continuous and the master clock never stalls |
| Host channel layout changes (stereo ↔ 5.1) | — | Treated as a format change: the add-on re-resolves the layout, re-derives the Vorbis permutation, `format()` reports the new channel count, and the pipeline pushes a fresh `config` with the new `audioChannels`/`audioLayout`/`audioDescription` |
| CoreMedia / SCK call fails mid-session | `audio::AudioError::Backend(detail)` | Log and continue on the next sample buffer; `detail` crosses the ABI in `AbiError.detail` |
| Re-attach exhausts its retry budget | `audio::AudioError::Unrecoverable(detail)` | The pipeline drops the audio path for the session and pushes a fresh `config` with `audio: false`; video is untouched |

---

## File Structure

```
featherdesk-addon-sck_audio/   (its own cdylib crate)
├── src/lib.rs              // AudioCapturer impl + abi_stable root module, Rust FFI
├── src/sckaudio_bridge.mm  // Obj-C++ shim: attach audio output, CMSampleBuffer → PCM
├── src/sckaudio_bridge.h   // plain C signatures for the Rust extern "C" block
└── tests/sckaudio.rs
```

> No build-tag stub file is needed — the add-on is its own cdylib crate.

---

## Configuration

```toml
[addon_module_sck_audio]
exclude_current_process = true   # don't capture FeatherDesk's own output
```

---

## Status

📋 Specced — implementation deferred. Order: confirm shared-SCStream wiring with
`sck` → audio output delegate → CMSampleBuffer → Float32 → S16LE → host-time→
monotonic mapping → silence across stream rebuilds.
