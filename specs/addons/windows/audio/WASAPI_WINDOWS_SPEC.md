# Windows Audio Add-On: WASAPI Loopback

> 🔒 Design locked · ⏸️ Implementation deferred behind the video trigger (see
> [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).

## Purpose

The `wasapi` add-on is the Windows **system-audio capture** backend for the core
Audio module ([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).
It implements the `audio::AudioCapturer` trait by capturing the default render
endpoint's output via **WASAPI loopback** — no driver, no subprocess, no virtual
cable.

---

## How It Works

WASAPI exposes a **loopback** capture mode on a *render* endpoint: you open the
speakers as if to play, but with `AUDCLNT_STREAMFLAGS_LOOPBACK` you instead
receive whatever the system is mixing to them.

```rust
// WASAPI / Core Audio via the `windows` crate (windows-rs).
// 1. Default render endpoint (the speakers/headphones the user hears).
let en: IMMDeviceEnumerator = unsafe { CoCreateInstance(&MMDeviceEnumerator, None, CLSCTX_ALL)? };
let dev: IMMDevice = unsafe { en.GetDefaultAudioEndpoint(eRender, eConsole)? };

// 2. Activate an IAudioClient in SHARED mode + LOOPBACK + event-driven.
let ac: IAudioClient = unsafe { dev.Activate(CLSCTX_ALL, None)? };
let mix: *mut WAVEFORMATEX = unsafe { ac.GetMixFormat()? }; // usually 32-bit float, 48k, 2ch
unsafe {
    ac.Initialize(
        AUDCLNT_SHAREMODE_SHARED,
        AUDCLNT_STREAMFLAGS_LOOPBACK | AUDCLNT_STREAMFLAGS_EVENTCALLBACK,
        buf_duration, 0, mix, None,
    )?;
    ac.SetEventHandle(h_event)?;
}

// 3. Capture loop on a dedicated, COM-initialized OS thread.
let cap: IAudioCaptureClient = unsafe { ac.GetService()? };
unsafe { ac.Start()?; }
loop {
    unsafe { WaitForSingleObject(h_event, INFINITE); }
    let (mut data, mut frames, mut flags) = (std::ptr::null_mut::<u8>(), 0u32, 0u32);
    unsafe { cap.GetBuffer(&mut data, &mut frames, &mut flags, None, None)?; }
    let ts = clock_monotonic_ns();            // stamp AT CAPTURE
    emit(data, frames, flags);                // → normalize → PcmChunk
    unsafe { cap.ReleaseBuffer(frames)?; }
}
```

### Normalization to the canonical format

The mix format is whatever the endpoint runs (commonly 32-bit float at 48 kHz
stereo, but it can be 44.1 kHz, 24-bit, 5.1/7.1, etc.). The add-on converts to the
canonical format MODULE_AUDIO defines — **48 kHz, S16LE, interleaved, Vorbis
channel order** — with the channel count **following the host output layout** up
to 7.1:
- float32 → S16LE (clamp + scale),
- reorder the endpoint's `WAVEFORMATEXTENSIBLE` channel mask (`L R C LFE …`) into
  the **Vorbis** order (`L C R Ls Rs LFE`) MODULE_AUDIO defines — the two differ
  by a C/R transposition and by LFE moving from fourth to last. Downmix to stereo
  only if `[audio] channels = "stereo"`,
- resample if the device rate ≠ 48 kHz (`rubato`, sinc interpolation, MIT —
  declared as a dependency of this add-on; bypassed entirely when the device
  already runs at 48 kHz).

The channel count is **not** fixed at 2: `format()` reports what the endpoint's
layout actually is, and `config.audioLayout` tells the client which layout to
expect.

### Capture ring

The capture thread writes into a ring the add-on owns, and `next_chunk()` pops
it. The ring holds **4 chunks** (≈80 ms at the default 20 ms `frame_ms`) and is
**drop-oldest**: if the host's audio pump stalls, the oldest chunk is discarded
rather than letting the device callback block or the buffer grow. The add-on
counts the drops and the host surfaces them as
`featherdesk_audio_drops_total`.

### The silence gotcha (load-bearing)

WASAPI loopback delivers **no packets while nothing is playing**. If you simply
stop emitting, the audio timeline stalls and A/V sync drifts on the next sound.
So when `GetBuffer` reports `AUDCLNT_BUFFERFLAGS_SILENT` (or no event fires for a
frame interval), the add-on **synthesizes silence** chunks to keep a continuous,
capture-stamped timeline. (Opus encodes silence to a few bytes; DTX may be used.)

### Timestamp

Stamped on `CLOCK_MONOTONIC` at `GetBuffer` time in the read loop (same epoch as
video). WASAPI's device-position/QPC timestamps are available for finer accuracy
but the read-loop stamp is sufficient for the ~40 ms sync window.

---

## License

| Component | License |
|-----------|---------|
| WASAPI / Core Audio (`mmdeviceapi`, `audioclient`) | Windows system API — no third-party license |
| `rubato` (sample-rate conversion, sinc interpolation) | MIT — pure Rust, no C dependency |
| Our Rust FFI / COM binding | MIT |

No driver, no redistributable.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-wasapi   # cdylib  featherdesk-addon-wasapi.dll
```

- COM must be initialized (`CoInitializeEx`, MTA) on the capture thread (a
  dedicated `std::thread` owned by the add-on). Uninitialize in `Drop`.
- Pairs with the `opus` codec add-on for compressed audio; without it, raw PCM.

---

## Constructor & Probe

```rust
// crate: featherdesk-addon-wasapi (the add-on's cdylib)

// Layer 1 — what the host actually calls (MODULE_ABI "Root module surface"):
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// `descriptor().kind` is `AudioCapture` (0x04). Opens the loopback client at
/// [audio] frame_ms and starts the capture thread. Honors
/// [addon_module_wasapi] device (default = default endpoint). Called on the
/// audio thread; `new_codec` returns `PipelineError::AddonBackend` here.
fn new_capturer(&self, cfg: audio::AudioConfig)
    -> Result<Box<dyn audio::AudioCapturer>, PipelineError>;
```

`probe` checks that a default render endpoint exists and that `IAudioClient`
activates with the loopback flag; it is side-effect-free and releases what it
opens. It reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(), codecs: RVec::new(),
    caps: AddonCaps(0),          // AudioCapturer has no optional methods
    displays: RVec::new(),
})
```

**Availability is not an error.** No render endpoint is
`ROk(ProbeReport { available: false, reason: "no default render endpoint" })`,
never an `RErr`. **Set every capability bit this add-on actually serves** —
`AddonCaps(0)` is correct and complete here, because `AudioCapturer` has no
optional methods. **Only claim what this call can prove:** a bit claimed here and
refused later is a capability lie (MODULE_ABI "Misbehaving add-ons").

---

## Error Handling

| Failure | Returned as | Behavior |
|---------|-------------|----------|
| No render endpoint (headless / no audio device) | `ProbeReport { available: false, reason }` | Add-on not selected; the host streams video only. Log "no audio output device" |
| Default device changes mid-session (`IMMNotificationClient`) | — | Re-open on the new default endpoint and **synthesize silence across the gap**, so the capture-stamped timeline stays continuous and the master clock never stalls. If the new endpoint's layout differs (a 5.1 receiver replacing stereo headphones), that is a format change: the add-on re-resolves the layout and re-derives the Vorbis permutation, `format()` reports the new channel count, and the pipeline pushes a fresh `config` with the new `audioChannels`/`audioLayout`/`audioDescription` |
| `GetBuffer` glitch / `AUDCLNT_S_BUFFER_EMPTY` | — | Treat as silence for that interval; continue |
| `AUDCLNT_E_DEVICE_INVALIDATED` (device unplugged) | `audio::AudioError::DeviceLost` if reconnection fails | Reconnect to the new default with bounded retry/backoff, synthesizing silence across the gap; the same layout-change rule applies |
| Reconnection exhausts its retry budget | `audio::AudioError::Unrecoverable(detail)` | The pipeline drops the audio path for the session and pushes a fresh `config` with `audio: false`; video is untouched |
| COM / `IAudioClient` call fails mid-session | `audio::AudioError::Backend(detail)` | Log and continue on the next event; `detail` crosses the ABI in `AbiError.detail` |

---

## File Structure

```
addons/wasapi/
├── src/
│   ├── lib.rs        // AudioCapturer impl (COM via the windows crate)
│   └── resample.rs   // device mix-format → 48 kHz / S16LE / Vorbis order (rubato)
└── tests.rs
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## Configuration

```toml
[addon_module_wasapi]
device = ""   # "" = default render endpoint (loopback). Or a specific endpoint id.
```

---

## Future

- **Per-process loopback** (Windows 10 2004+) via `ActivateAudioInterfaceAsync` +
  `AUDIOCLIENT_ACTIVATION_PARAMS` (`PROCESS_LOOPBACK`) to capture one app's audio.
  Out of scope for v1 (system mix only).

---

## Status

📋 Specced — **in v1**. Order: endpoint enumerate + probe → loopback
client + event loop → mix-format normalize → silence synthesis → `format()`/`Drop`
lifecycle.
