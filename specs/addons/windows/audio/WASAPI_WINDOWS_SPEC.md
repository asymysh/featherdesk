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
stereo, but it can be 44.1 kHz, 24-bit, 5.1/7.1, etc.). The add-on converts to
the **canonical 48 kHz / S16LE**, **following the host layout** (stereo / 5.1 /
7.1, capped at 7.1):
- float32 → S16LE (clamp + scale),
- reorder the endpoint's channel mask into the canonical Vorbis order
  (`config.audioLayout`); downmix to stereo only if `[audio] channels = "stereo"`,
- resample if the device rate ≠ 48 kHz (linear/`soxr` — only when needed).

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

/// `probe` returns true if a default render endpoint exists and IAudioClient
/// activates with the loopback flag (side-effect-free; releases what it opens).
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// Open the loopback client at [audio] frame_ms and start the capture thread.
/// Honors [addon_module_wasapi] device (default = default endpoint).
fn new(&self, cfg: audio::AudioConfig) -> Result<Box<dyn audio::AudioCapturer>, audio::AudioError>;
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| No render endpoint (headless / no audio device) | `probe` false → add-on not selected; log "no audio output device" |
| Default device changes mid-session (`IMMNotificationClient`) | Re-open on the new default endpoint; emit silence across the gap |
| `GetBuffer` glitch / `AUDCLNT_S_BUFFER_EMPTY` | Treat as silence for that interval; continue |
| `AUDCLNT_E_DEVICE_INVALIDATED` (device unplugged) | Reconnect to the new default; bounded retry/backoff |

---

## File Structure

```
addons/wasapi/
├── src/
│   ├── lib.rs        // AudioCapturer impl (COM via the windows crate)
│   └── resample.rs   // device mix-format → 48k/stereo/S16LE
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

📋 Specced — implementation deferred. Order: endpoint enumerate + probe → loopback
client + event loop → mix-format normalize → silence synthesis → `format()`/`Drop`
lifecycle.
