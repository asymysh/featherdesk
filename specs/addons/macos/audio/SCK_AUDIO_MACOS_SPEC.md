# macOS Audio Add-On: ScreenCaptureKit Audio

> 🔒 Design locked · ⏸️ Implementation deferred behind the video trigger (see
> [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).

## Purpose

The `sck_audio` add-on is the macOS **system-audio capture** backend for the core
Audio module ([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).
It implements `audio.AudioCapturer` using **ScreenCaptureKit's** built-in audio
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
  `[audio] enabled` but the capture add-on isn't `sck`, `Probe` fails with a
  clear message (system audio on macOS is only wired through SCK here).
- One stream, one permission prompt (Screen Recording), one clock.

```objc
// In the shared SCStreamConfiguration (set by sck, audio fields added here):
cfg.capturesAudio              = YES;
cfg.sampleRate                 = 48000;
cfg.channelCount               = 2;
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
the canonical **48 kHz / S16LE**, following the host layout (stereo / 5.1 / 7.1).
SCK can be asked for the host's channel count; the add-on reorders to canonical
Vorbis order (`config.audioLayout`) and downmixes to stereo only when
`[audio] channels = "stereo"`.

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
| ScreenCaptureKit / CoreMedia | Apple system frameworks — linked, not redistributed |
| Our CGo / Obj-C++ binding | MIT |

No driver. Requires the **Screen Recording** permission (already needed for `sck`
video — audio adds no new prompt). App must be signed + notarized.

---

## Build & Distribution

Build each add-on as its own shared library and drop the set into the add-ons directory:
```bash
go build -buildmode=c-shared -o featherdesk-addon-sck.dylib       ./internal/capture/sck
go build -buildmode=c-shared -o featherdesk-addon-sck_audio.dylib ./internal/audio/sckaudio
go build -buildmode=c-shared -tags vt_hw -o featherdesk-addon-vt_hw.dylib ./internal/encode/vt
go build -buildmode=c-shared -o featherdesk-addon-opus.dylib      ./internal/audio/opus
```

`sck_audio` is meaningless without `sck`; the probe enforces the pairing.

---

## Constructor & Probe

```go
// internal/audio/sckaudio/sckaudio_darwin.go  (built into the add-on's shared library)

// Probe returns true on macOS 13+ AND when the sck capture add-on is the active
// capturer (so an SCStream exists to attach the audio output to).
func Probe() bool

// New attaches the audio output to the shared SCStream and starts emitting
// PCMChunks. It receives a handle to the sck stream via the pipeline wiring.
func New(cfg audio.AudioConfig) (audio.AudioCapturer, error)
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| Pre-macOS 13 | `Probe` false → add-on not selected; log "SCK audio requires macOS 13+" |
| `sck` not the active capturer | `Probe` false → log "sck_audio requires the sck capture add-on" |
| Screen-Recording permission denied | Surfaces via the shared `sck` permission flow; audio disabled with the same notice |
| Stream stops / reconfigures (display change) | Audio output is re-attached when `sck` rebuilds the stream; emit silence across the gap |

---

## File Structure

```
internal/audio/sckaudio/
├── sckaudio_darwin.go      // AudioCapturer impl, CGo (built into the add-on's shared library)
├── sckaudio_bridge.mm      // Obj-C++ shim: attach audio output, CMSampleBuffer → PCM
├── sckaudio_bridge.h       // plain C signatures for CGo
└── sckaudio_test.go
```

> No `!sck_audio` stub file is needed — the add-on is its own shared library.

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
