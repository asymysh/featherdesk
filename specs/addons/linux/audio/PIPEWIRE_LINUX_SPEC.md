# Linux Audio Add-On: PipeWire Monitor Capture

> 🔒 Design locked · ⏸️ Implementation deferred behind the video trigger (see
> [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).

## Purpose

The `pipewire` add-on is the Linux **system-audio capture** backend for the core
Audio module ([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).
It implements `AudioCapturer` by capturing the default sink's **monitor**
via **native libpipewire** — no `pw-cat`/`parec` subprocess, no driver.

> This replaces the old Linux-only `pw-cat` subprocess design. libpipewire is
> linked directly (Rust FFI); capture runs in-process on the PipeWire thread loop.

---

## How It Works

A PipeWire **sink** exposes a `.monitor` that carries whatever is played to it.
The add-on connects an input `pw_stream` to the default sink's monitor.

```c
pw_init(NULL, NULL);
struct pw_thread_loop *loop = pw_thread_loop_new("featherdesk-audio", NULL);
struct pw_stream *stream = pw_stream_new_simple(
    pw_thread_loop_get_loop(loop), "featherdesk-capture",
    pw_properties_new(
        PW_KEY_MEDIA_TYPE,    "Audio",
        PW_KEY_MEDIA_CATEGORY,"Capture",
        PW_KEY_MEDIA_ROLE,    "Music",
        PW_KEY_STREAM_CAPTURE_SINK, "true",   // capture the sink's monitor
        NULL),
    &stream_events, userdata);

// Request the canonical format via an SPA pod (F32 or S16, 48k, host layout).
pw_stream_connect(stream, PW_DIRECTION_INPUT, PW_ID_ANY,
    PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_RT_PROCESS,
    params, n_params);

// on_process callback (PipeWire RT thread):
static void on_process(void *data) {
    struct pw_buffer *b = pw_stream_dequeue_buffer(stream);
    // read interleaved samples → normalize to 48k/S16LE/host layout (Vorbis order)
    ts = clockMonotonicNs();         // stamp AT CAPTURE
    emit(samples, ts);
    pw_stream_queue_buffer(stream, b);
}
```

`PW_KEY_STREAM_CAPTURE_SINK = "true"` makes PipeWire auto-route the input stream
to the **default sink's monitor**, so it follows the user's default-output
changes without re-discovery. A specific `target` (from config) pins a sink.

### Format & normalization

The add-on negotiates F32 or S16 at 48 kHz via the SPA pod, **following the host
layout** (stereo / 5.1 / 7.1, capped at 7.1), and converts to the canonical format
MODULE_AUDIO defines — **48 kHz, S16LE, interleaved, Vorbis channel order**:

- float32 → S16LE (clamp + scale),
- reorder the graph's `SPA_AUDIO_CHANNEL_*` positions (`FL FR FC LFE …`, the
  WAV/SMPTE order) into the **Vorbis** order (`L C R Ls Rs LFE`) MODULE_AUDIO
  defines — the two differ by a C/R transposition and by LFE moving from fourth
  to last. Downmix to stereo only if `[audio] channels = "stereo"`,
- resample if the graph rate ≠ 48 kHz (`rubato`, sinc interpolation, MIT —
  declared as a dependency of this add-on; bypassed entirely when the graph
  already runs at 48 kHz, which PipeWire usually does).

The channel count is **not** fixed at 2: `format()` reports what the sink's layout
actually is, and `config.audioLayout` tells the client which layout to expect.

### Timestamp

Stamped on `CLOCK_MONOTONIC` in `on_process` (same epoch as video). PipeWire's
buffer `pts`/`now` is available for finer alignment but the process-callback
stamp suffices for the ~40 ms sync window.

---

## Fallbacks (inside this add-on, no subprocess)

PipeWire is default on modern distros, but the add-on degrades natively:

1. **PipeWire** (preferred) — native libpipewire as above.
2. **PulseAudio** — if only Pulse is present, capture the default sink's
   `.monitor` via **libpulse** (`pa_simple` / async API), still in-process. (NOT
   the `parec` subprocess.)
3. **ALSA** — last resort via the `snd-aloop` loopback device, if configured.
   Documented as best-effort; many systems lack a monitor without Pulse/PW.

`Probe` reports which backend is available; selection is automatic.

---

## License

| Component | License |
|-----------|---------|
| libpipewire-0.3 | MIT |
| libpulse (fallback) | LGPL-2.1 (dynamically linked) |
| `rubato` (sample-rate conversion, sinc interpolation) | MIT — pure Rust, no C dependency |
| Our Rust FFI binding | MIT |

No driver. `snd-aloop` (ALSA fallback) is a stock kernel module.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-pipewire   # cdylib → featherdesk-addon-pipewire.so
```

Links `libpipewire-0.3` into the add-on library (plus `libpulse` when the Pulse
fallback is built in; the Opus codec is the separate `opus` add-on, not bundled here). Runs the PipeWire thread
loop in-process; no external tools.

---

## Constructor & Probe

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-pipewire   (cfg(target_os = "linux"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// `descriptor().kind` is `AudioCapture` (0x04). Connects the capture stream at
/// [audio] frame_ms and starts the thread loop. Honors
/// [addon_module_pipewire] target (default = the default sink's monitor).
/// Called on the audio thread; `new_codec` returns `PipelineError::AddonBackend`
/// here — the Opus codec is the separate `opus` add-on.
fn new_capturer(&self, cfg: audio::AudioConfig)
    -> Result<Box<dyn audio::AudioCapturer>, PipelineError>;
```

`probe` initializes libpipewire and checks that a default sink monitor (or a
Pulse/ALSA fallback) is reachable; it is side-effect-free and releases what it
opens. It reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(), codecs: RVec::new(),
    caps: AddonCaps(0),          // AudioCapturer has no optional methods
    displays: RVec::new(),
})
```

**Availability is not an error.** No PipeWire, Pulse or ALSA monitor is
`ROk(ProbeReport { available: false, reason: "no system-audio monitor source" })`,
never an `RErr`. **Set every capability bit this add-on actually serves** —
`AddonCaps(0)` is correct and complete here, because `AudioCapturer` has no
optional methods. **Only claim what this call can prove:** a bit claimed here and
refused later is a capability lie (MODULE_ABI "Misbehaving add-ons").

---

## Error Handling

| Failure | Returned as | Behavior |
|---------|-------------|----------|
| No PipeWire/Pulse/ALSA monitor available | `ProbeReport { available: false, reason }` | Add-on not selected; log a clear "no system-audio monitor" message |
| Default sink changes mid-session | — | `STREAM_CAPTURE_SINK` reroutes to the new default. The add-on re-reads the new sink's channel positions, re-derives the Vorbis permutation, and **synthesizes silence across the gap**, so the capture-stamped timeline stays continuous and the master clock never stalls. If the new sink's layout differs (a 5.1 receiver replacing stereo headphones), that is a format change: `format()` reports the new channel count and the pipeline pushes a fresh `config` with the new `audioChannels`/`audioLayout`/`audioDescription` |
| Stream error / disconnect | `audio::AudioError::DeviceLost` if reconnection fails | libpipewire reconnect on the thread loop with bounded backoff, synthesizing silence across the gap; the same layout-change rule applies |
| Reconnection exhausts its retry budget | `audio::AudioError::Unrecoverable(detail)` | The pipeline drops the audio path for the session and pushes a fresh `config` with `audio: false`; video is untouched |
| Xrun / buffer underrun | — | Treated as silence for that interval; continue |

---

## File Structure

```
addons/audio/pipewire/
├── pipewire.rs           // AudioCapturer impl, Rust FFI (built into the add-on cdylib)
├── pulse_fallback.rs     // libpulse monitor capture (no subprocess)
├── resample.rs           // graph format → 48k/S16LE/host layout (rubato)
└── tests.rs
```

---

## Configuration

```toml
[addon_module_pipewire]
target = ""   # "" = auto-detect the default sink's .monitor; or a specific node/sink
```

---

## Status

📋 Specced — **in v1**. Order: libpipewire bindings + probe →
capture-sink stream + SPA format → on_process normalize → monotonic stamp →
Pulse/ALSA fallbacks.
