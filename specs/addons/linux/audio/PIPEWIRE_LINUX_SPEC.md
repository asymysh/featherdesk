# Linux Audio Add-On: PipeWire Monitor Capture

> 🔒 Design locked · ⏸️ Implementation deferred behind the video trigger (see
> [`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).

## Purpose

The `pipewire` add-on is the Linux **system-audio capture** backend for the core
Audio module ([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)).
It implements `audio.AudioCapturer` by capturing the default sink's **monitor**
via **native libpipewire** — no `pw-cat`/`parec` subprocess, no driver.

> This replaces the old Linux-only `pw-cat` subprocess design. libpipewire is
> linked directly (CGo); capture runs in-process on the PipeWire thread loop.

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

// Request the canonical format via an SPA pod (F32 or S16, 48k, 2ch).
pw_stream_connect(stream, PW_DIRECTION_INPUT, PW_ID_ANY,
    PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS | PW_STREAM_FLAG_RT_PROCESS,
    params, n_params);

// on_process callback (PipeWire RT thread):
static void on_process(void *data) {
    struct pw_buffer *b = pw_stream_dequeue_buffer(stream);
    // read interleaved samples → normalize to 48k/stereo/S16LE
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
layout** (stereo / 5.1 / 7.1, capped at 7.1); it reorders the graph's channel
positions into the canonical Vorbis order (`config.audioLayout`) and downmixes to
stereo only when `[audio] channels = "stereo"`. PipeWire usually runs at 48 kHz.

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
| Our CGo binding | MIT |

No driver. `snd-aloop` (ALSA fallback) is a stock kernel module.

---

## Build & Distribution

```bash
go build -tags "pipewire,opus" -o featherdesk-linux ./cmd/server
```

Links `libpipewire-0.3` (and `libpulse` when the Pulse fallback is compiled).
Runs the PipeWire thread loop in-process; no external tools.

---

## Constructor & Probe

```go
// internal/audio/pipewire/pipewire_linux.go  (build tag: pipewire)

// Probe returns true if libpipewire initializes and a default sink monitor (or a
// Pulse/ALSA fallback) is reachable. Side-effect-free.
func Probe() bool

// New connects the capture stream at [audio] frame_ms and starts the thread
// loop. Honors [addon_module_pipewire] target (default = default sink monitor).
func New(cfg audio.AudioConfig) (audio.AudioCapturer, error)
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| No PipeWire/Pulse/ALSA monitor available | `Probe` false → add-on not selected; log a clear "no system-audio monitor" message |
| Default sink changes mid-session | `STREAM_CAPTURE_SINK` auto-reroutes; emit silence across any gap |
| Stream error / disconnect | libpipewire reconnect on the thread loop; bounded backoff |
| Xrun / buffer underrun | Treated as silence for that interval; continue |

---

## File Structure

```
internal/audio/pipewire/
├── pipewire_linux.go     // build tag: pipewire (AudioCapturer impl, CGo)
├── pulse_fallback.go     // libpulse monitor capture (no subprocess)
├── resample.go           // graph format → 48k/stereo/S16LE
├── stub.go               // build tag: !pipewire (no-op, never registers)
└── pipewire_test.go
```

---

## Configuration

```toml
[addon_module_pipewire]
target = ""   # "" = auto-detect the default sink's .monitor; or a specific node/sink
```

---

## Status

📋 Specced — implementation deferred. Order: libpipewire bindings + probe →
capture-sink stream + SPA format → on_process normalize → monotonic stamp →
Pulse/ALSA fallbacks.
