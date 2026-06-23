# Module Spec: Audio

> # ⏸️ DEFERRED
>
> **Status:** Deferred until video capture+encode is stable across all three OSes
> (Linux, macOS, Windows).
>
> **Why:** Audio is currently a Linux-only PipeWire subprocess implementation.
> Properly cross-platform audio (WASAPI on Windows, CoreAudio on macOS,
> PipeWire/ALSA on Linux) needs its own design pass — and the current spec
> contradicts several confirmed architectural decisions (no subprocess
> backends, `*slog.Logger` instead of custom Logger). Rather than refactor
> twice, the module is paused until video work proves the cross-platform
> pluggable add-on pattern, at which point audio gets the same treatment.
>
> **Not core.** This module has been removed from the CENTRAL_SPEC module map.
> The content below is preserved for reference but should not be treated as
> current architecture.
>
> **Trigger to un-defer:** Video capture+encode add-ons working end-to-end on
> Linux + macOS + Windows with the bench harness producing comparable numbers.

---

## Overview

The Audio module captures system audio output and delivers it as raw PCM chunks for streaming to connected clients. It uses PipeWire (via the `pw-cat` command-line tool) for audio capture and PulseAudio (`pactl`) for source discovery.

---

## Public Interface

```go
package audio

// AudioCapturer captures system audio output.
type AudioCapturer interface {
    // Chunks returns a read-only channel delivering timestamped PCM chunks.
    // Each chunk's Data is exactly ChunkBytes (3840) bytes: 20ms of 48kHz stereo S16LE.
    // The channel is closed when the capturer is stopped.
    Chunks() <-chan AudioChunk

    // Close stops capture and releases all resources.
    Close()
}

// AudioChunk is one PCM chunk stamped at CAPTURE time.
type AudioChunk struct {
    Data      []byte // 3840 bytes, S16LE interleaved stereo. OWNED by receiver.
    Timestamp uint64 // CLOCK_MONOTONIC ns, sampled in the read loop when the chunk
                     // is read from pw-cat — NOT when the pipeline consumes it.
}

// AudioConfig configures the audio capture subsystem.
type AudioConfig struct {
    SampleRate int    // Default: 48000
    Channels   int    // Default: 2 (stereo)
    BitDepth   int    // Default: 16 (S16LE)
    FrameSize  int    // Samples per chunk. Default: 960 (20ms at 48kHz)
    Target     string // PipeWire target node (empty = auto-detect)
    Logger     Logger
}

// Constants
const (
    DefaultSampleRate = 48000
    DefaultChannels   = 2
    DefaultBitDepth   = 16
    DefaultFrameSize  = 960                                    // 20ms at 48kHz
    ChunkBytes        = DefaultFrameSize * DefaultChannels * 2 // 3840 bytes
)
```

---

## Internal Architecture

### Source Discovery

```
pactl list short sources
    → parse output lines
    → filter: contains ".monitor" AND NOT contains "hdmi" (case-insensitive)
    → select first matching source
    → extract numeric ID
```

**Fallback:** If no monitor source found, `pw-cat` captures from default without `--target`.

### Capture Pipeline

```
pw-cat --record --rate 48000 --channels 2 --format s16 --latency 20ms [--target <id>] -
    → stdout pipe
    → readLoop goroutine:
         io.ReadFull(3840 bytes)
         ts = clock.Now()                       // CLOCK_MONOTONIC ns, AT CAPTURE
         ch <- AudioChunk{Data: copy, Timestamp: ts}
    → buffered channel (cap 32 = ~640ms buffer)
    → consumer reads via Chunks()
```

**Timestamp placement is load-bearing:** stamping in the read loop (above) keeps audio on the same monotonic timeline as video. Stamping at consumption time (the original bug, `BroadcastAudio(chunk, time.Now().UnixMilli())`) adds up to 640 ms of buffer skew and uses the wrong clock — A/V sync becomes impossible.

### Back-Pressure Handling

When the output channel is full (consumer is slow):
```go
select {
case c.ch <- chunk:
    // delivered
default:
    // channel full, drop OLDEST chunk
    <-c.ch      // discard oldest
    c.ch <- chunk // push newest
}
```

This ensures the consumer always gets the most recent audio, at the cost of skipped chunks (acceptable for real-time streaming).

### Error Recovery

```
readLoop detects EOF/error on stdout pipe
    → kill pw-cat process (SIGKILL)
    → wait for process exit
    → sleep 2 seconds
    → restart pw-cat (call start() again)
    → resume reading
```

---

## Wire Format

Audio is sent over the WebSocket using the standard 22-byte protocol framing. The **server** assigns the audio `Sequence` (independent from video) in `BroadcastAudio`.

```
Header (22 bytes):
    Version    = 1
    Type       = FrameTypeAudioPCM (4)
    Sequence   = server audio counter (independent of video)
    Timestamp  = AudioChunk.Timestamp (CLOCK_MONOTONIC ns, from capture)
    Width      = SampleRate (48000)   // overloaded field (documented)
    Height     = Channels (2)         // overloaded field (documented)
    PayloadSize = 3840

Payload (3840 bytes):
    Raw S16LE PCM (signed 16-bit little-endian)
    Interleaved stereo: [L0][R0][L1][R1]...[L959][R959]
```

---

## Browser Playback

The client plays audio using the Web Audio API:

1. `AudioContext` created (48kHz sample rate)
2. `AudioWorkletProcessor` registered (inline code via Blob URL)
3. On each PCM chunk received:
   - Convert S16LE bytes to Float32 samples (divide by 32768)
   - Post to AudioWorklet via `port.postMessage()`
4. Worklet fills output buffer from a ring buffer

**User Gesture Requirement:** AudioContext must be created after a user interaction (keydown/pointerdown). First input event triggers audio initialization.

---

## Refactoring Directives

### R-AUD-01: Fix Race Condition
`reconnect()` mutates `c.cmd` and `c.stdout` from the `readLoop` goroutine while `Close()` may be called concurrently. Add a `sync.Mutex` to protect these fields, or use an internal channel for lifecycle commands.

### R-AUD-02: Use SIGTERM Before SIGKILL
Replace `os.Kill` (SIGKILL) with `SIGTERM` + 1-second grace period, falling back to SIGKILL. This allows `pw-cat` to cleanly release PipeWire resources.

### R-AUD-03: Add Exponential Backoff
Replace fixed 2-second reconnect delay with exponential backoff (1s → 2s → 4s → 8s → max 30s). Reset backoff on successful capture.

### R-AUD-04: Extract Interface to `pkg/audio`
Move `AudioCapturer` and `AudioConfig` to a public package. Keep PipeWire implementation in `internal/audio/pipewire/`.

### R-AUD-05: Accept Logger Interface
Use `*slog.Logger` (stdlib `log/slog`) — no custom logger interface. Tests can pass a `slog.New(slog.NewTextHandler(io.Discard, nil))` for quiet runs or a JSON handler to a buffer for assertions.

### R-AUD-06: Add Opus Compression (Future)
For WAN deployments, add optional Opus encoding:
```
PCM chunk → Opus encoder → compressed frame → FrameTypeAudioOpus
```
This would reduce bandwidth from ~768 kbps to ~64-128 kbps.

### R-AUD-07: Configurable Channel Buffer Size
Make the channel capacity configurable via `AudioConfig` rather than hardcoded at 32.

### R-AUD-08: Add Capture Metrics
Expose metrics: chunks delivered, chunks dropped, reconnect count, latency estimate.

### R-AUD-09: Support ALSA/PulseAudio Fallback
If PipeWire is unavailable, fall back to:
1. `parec` (PulseAudio recorder)
2. `arecord` (ALSA, with monitor source via loopback module)

### R-AUD-10: Separate Protocol Field Semantics
Instead of overloading `Width`/`Height` fields for sample rate and channels, define a dedicated `AudioHeader` or use reserved bits in the frame type byte to indicate audio parameters.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Source name parsing and filtering | No |
| Unit | Back-pressure drop behavior (channel full) | No |
| Unit | S16LE → Float32 conversion correctness | No |
| Integration | Full capture pipeline (5 seconds of audio) | Yes (PipeWire) |
| Integration | Reconnect recovery after process kill | Yes (PipeWire) |
| Mock | Fake AudioCapturer (generates silence or tone) | No |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Capture latency | <25ms (20ms frame + 5ms pipeline) |
| CPU usage | <1% (pw-cat does the heavy lifting) |
| Memory | <2MB (channel buffer + process overhead) |
| Bandwidth (uncompressed) | 768 kbps (48kHz × 2ch × 16bit) |
| Bandwidth (with Opus, future) | 64-128 kbps |
| Max jitter tolerance | 640ms (32 chunks buffered) |
