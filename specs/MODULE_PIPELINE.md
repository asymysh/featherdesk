# Module Spec: Pipeline (Orchestrator)

## Overview

The Pipeline module is the runtime wiring layer that connects all other modules into a functioning streaming system. It handles lifecycle management, capability probing, backend selection, frame pacing, frame drop decisions, and graceful shutdown. This is the "main loop" extracted into a testable, configurable struct.

---

## Public Interface

```go
package pipeline

// Pipeline connects capture, encode, server, audio, and input into a streaming system.
type Pipeline struct {
    cfg       *config.Config           // parsed TOML config (owned by caller, read-only)
    capturer  capture.Capturer
    surfCap   capture.SurfaceCapturer  // nil if capturer doesn't implement SurfaceCapturer
    encoder   encode.Encoder           // nil if hardware path
    hwEncoder hwencode.HardwareEncoder // nil if software path
    server    server.Server
    params    stream.Params            // current dynamic stream parameters
    logger    *slog.Logger
    stats     *Stats
    // (audio + input deferred — see MODULE_AUDIO / MODULE_INPUT deferred banners)
}

// New creates a Pipeline from a parsed TOML configuration. Does NOT start anything.
// The TOML config struct is defined in specs/MODULE_CONFIG.md — pipeline does not
// own configuration parsing.
func New(cfg *config.Config, logger *slog.Logger) (*Pipeline, error)

// Start probes compiled-in add-ons, initializes the chosen capture + encoder
// add-ons, and begins streaming. Blocks until ctx is cancelled. Returns after
// graceful shutdown completes.
func (p *Pipeline) Start(ctx context.Context) error

// Stats returns a snapshot of pipeline performance metrics.
// Returns StatsSnapshot (no sync.Mutex — safe to copy/serialize).
func (p *Pipeline) Stats() StatsSnapshot
```

The pipeline takes `*config.Config` directly (the parsed TOML struct from
[`MODULE_CONFIG.md`](./MODULE_CONFIG.md)). There is no separate `PipelineConfig`
struct. The pipeline reads `[capture]`, `[encode]`, `[stream]`,
`[stream.adaptive]` sections plus per-add-on `[addon_module_<tag>]` sections.

---

## Internal Architecture

### Startup Sequence

```
1. Caller (cmd/server/main.go) loads TOML via config.Load(--config path)
2. Caller creates *slog.Logger per [log] section (text in TTY, JSON otherwise)
3. pipeline.New(cfg, logger) builds the Pipeline:
   a. Probe each compiled-in capture add-on (registered at init() per build tag):
      - Linux:   nvfbc → kms_egl
      - macOS:   sck
      - Windows: dxgi_dd (with optional IddCx VDD auto-install if no display)
   b. Probe each compiled-in encoder add-on:
      - HW: nvenc → amf/amf_rocm → libva → mf_hw → qsv → vt_hw
      - SW: x264 (if ffmpeg present) → vt_sw → openh264
   c. Honor [capture] force_addon / [encode] force_addon overrides:
      - If forced and add-on not compiled in: startup error
      - If forced and probe fails: startup error
      - If auto: pick first available per the order above
4. Match capture surface format to encoder input:
    - HW encoder + SurfaceCapturer with compatible FBInfo → zero-copy path
   - SW encoder + any Capturer → CPU readback + I420 conversion path
5. If hardware path errors with ErrFallbackToSoftware mid-session: degrade
   to software path permanently for the rest of the session
6. Derive stream dims from the capturer's actual resolution (NOT hardcoded).
7. Create server (embedded client FS, session token).
8. Probe + create input dispatcher if an input add-on is compiled in AND
   `[input] enabled`: build `KeyMouseInjector` (interception/uinput/cgevent) and
   optional `TouchInjector` (win_touch), sized to the SAME stream dims. If no
   input add-on is compiled in, the binary is **view-only** (log it; not an error).
9. Probe + create webcam Receiver + Sink if a webcam add-on is compiled in AND
   `[webcam] enabled`. Otherwise no webcam capability.
10. Create clipboard Monitor + file-transfer Service if their `[*] enabled`.
11. Create audio capturer (if `[audio] enabled = true` and PipeWire available). [deferred]
12. Wire callbacks:
   - server.ConfigProvider      → returns current ConfigPayload (codec, dims, fps, hdr, cursorMode, session_token)
   - server.OnNewClient         → p.forceKeyframe() ONLY (server already gates on cached keyframe)
   - server.SetInputCallback    → input.Dispatcher.Dispatch (binary; nil if view-only)
   - server.SetWebcamCallback   → webcam.Receiver.HandleFrame (nil if no webcam add-on)
   - server.SetClipboardCallback→ clipboard.Monitor.Set (direction-gated)
   - server.OnKeyframeRequest   → p.forceKeyframe() (server rate-limits before calling)
13. Start goroutines (frame loop, clipboard monitor, audio loop, server).
14. Enter main frame loop.
```

> `forceKeyframe()` dispatches to the active encoder (hw or sw) — never to a nil one. This fixes the round-1 bug where `encoder.ForceKeyframe()` would nil-panic on the hardware path.

### Main Frame Loop

Key fixes vs round 1: skip is decided BEFORE capture; `EncodedFrame.Data` is contiguous Annex B (no per-NAL split/rejoin); the server owns the sequence; pacing is capture-to-capture (NOT delivery-to-delivery).

**Allocation strategy:** NAL output buffers and WS message buffers use `sync.Pool` (sized to typical access-unit ~50KB). Steady-state frame loop is zero-alloc after warmup. The `Converter.Convert()` reuses I420 buffers (already documented); the encoder and server must follow the same pattern.

```go
func (p *Pipeline) runFrameLoop(ctx context.Context) {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()

    targetInterval := time.Second / time.Duration(p.params.FPS)
    maxSkip := (p.params.FPS / 5) - 1
    if maxSkip < 0 { maxSkip = 0 }

    var (
        skipBudget int
        lastFrameT time.Time
    )

    for {
        if ctx.Err() != nil {
            return
        }

        // (1) Skip owed frames from a previous overrun.
        if skipBudget > 0 {
            skipBudget--
            p.stats.RecordDrop()
            sleepToInterval(&lastFrameT, targetInterval)
            continue
        }

        // (2) Pacing: wait until the next frame is due.
        sleepToInterval(&lastFrameT, targetInterval)
        lastFrameT = time.Now() // SET BEFORE capture, not after broadcast.
                                // This makes pacing capture-to-capture,
                                // decoupled from downstream processing time.

        // (3) Capture + encode.
        frameStart := time.Now()
        ef, ok, err := p.captureEncode()
        if err != nil {
            if errors.Is(err, stream.ErrFallbackToSoftware) {
                p.degradeToSoftware() // permanent for this session
                continue
            }
            p.recordCaptureError(err)
            continue
        }
        if !ok {
            continue // static screen, nothing to send
        }

        // (4) Broadcast. Keyframe detection is done once here; the server
        //     trusts ef.Keyframe (no redundant NAL scan).
        p.server.Broadcast(ef.CodecType, ef)
        p.stats.RecordFrame(time.Since(frameStart))

        // (5) Drop decision: if we overran, owe skips (capped at maxSkip → 5 FPS floor).
        if d := time.Since(frameStart); d > targetInterval {
            owe := int(d/targetInterval)
            if owe > maxSkip { owe = maxSkip }
            skipBudget = owe
        }
    }
}

// captureEncode runs the active path. Returns (frame, ok, err).
// ok == false means "no new frame this tick" (not an error).
func (p *Pipeline) captureEncode() (EncodedFrame, bool, error) {
    if p.hwEncoder != nil {
        return p.runHardwareFrame()
    }
    return p.runSoftwareFrame()
}

func (p *Pipeline) runHardwareFrame() (EncodedFrame, bool, error) {
    fb, err := p.surfCap.NextSurface()
    if err != nil {
        if errors.Is(err, hwencode.ErrFallbackToSoftware) {
            p.degradeToSoftware()
            return EncodedFrame{}, false, nil
        }
        return EncodedFrame{}, false, err
    }
    if fb == nil { return EncodedFrame{}, false, nil } // no new frame

    // HW encoder takes ownership of the FBInfo handle and releases it after encode.
    encoded, err := p.hwEncoder.EncodeSurface(fb)
    if err != nil {
        if errors.Is(err, hwencode.ErrFallbackToSoftware) {
            p.degradeToSoftware()
            return EncodedFrame{}, false, nil
        }
        return EncodedFrame{}, false, err
    }
    if encoded == nil { return EncodedFrame{}, false, nil } // skip frame
    return *encoded, true, nil
}

func (p *Pipeline) runSoftwareFrame() (stream.EncodedFrame, bool, error) {
    frame, err := p.capturer.NextFrame()
    if err != nil { return stream.EncodedFrame{}, false, err }
    if frame == nil { return stream.EncodedFrame{}, false, nil } // static screen

    i420 := p.converter.Convert(frame) // takes *capture.Frame, NOT frame.Data
    data, err := p.encoder.Encode(i420)
    if err != nil { return stream.EncodedFrame{}, false, err }
    if len(data) == 0 { return stream.EncodedFrame{}, false, nil }
    return stream.EncodedFrame{
        Data: data, Width: uint16(frame.Width), Height: uint16(frame.Height),
        Timestamp: frame.Timestamp,
        Keyframe:  containsKeyframe(data, p.codecType()),
        CodecType: protocol.FrameTypeVideoH264, // SW path is always H.264
    }, true, nil
}

// forceKeyframe dispatches to whichever encoder is active.
func (p *Pipeline) forceKeyframe() {
    if p.hwEncoder != nil { p.hwEncoder.ForceKeyframe() } else { p.encoder.ForceKeyframe() }
}
```

**`containsKeyframe`**: for H.264, scans NALs for type 5 (IDR). **`sleepToInterval(&lastFrameT, interval)`** sleeps until `lastFrameT + interval`, then sets `lastFrameT = now()`. Both are O(1)/cheap.

> **Frame-drop semantics: pull-latest source assumed.** The skip-a-capture strategy assumes the capturer is a **pull-latest** source: a call to `NextFrame` / `NextSurface` always returns the CURRENT framebuffer, so skipping cleanly drops stale frames. This is true for KMS+EGL (Linux), ScreenCaptureKit (macOS), and DXGI Desktop Duplication (Windows). Pipe-based subprocess capturers (X11grab, ffmpeg-based) were rejected from the architecture.

### Audio Loop (Separate Goroutine)

```go
func (p *Pipeline) runAudioLoop(ctx context.Context) {
    if p.audio == nil {
        return
    }
    chunks := p.audio.Chunks()
    for {
        select {
        case <-ctx.Done():
            return
        case chunk, ok := <-chunks:
            if !ok {
                return
            }
            // chunk.Timestamp was sampled at CAPTURE time in the audio read loop,
            // on the SAME CLOCK_MONOTONIC epoch as video. Do NOT re-stamp here.
            p.server.BroadcastAudio(chunk)
        }
    }
}
```

**Critical:** the audio timestamp comes from `chunk.Timestamp` (capture-time, monotonic), never from `time.Now()` at this point. Re-stamping here would add up to ~640 ms of channel-buffer skew and break A/V sync (this was the original bug).

### Resolution-Change Handling

The pipeline owns the resolution-change orchestration (no other module drives it):

```go
// Detected when capture returns a frame with different Width/Height than the
// current params. No sentinel error needed -- the pipeline compares dimensions.
func (p *Pipeline) handleResize(newW, newH int) {
    p.logger.Info("pipeline", fmt.Sprintf("resolution change → %dx%d", newW, newH))
    // 1. Rebuild software converter + encoder (or reconfigure hw encoder) for new dims.
    p.rebuildEncoder(newW, newH)
    // 2. Resize the uinput device so absolute coords still map 1:1 to the stream.
    if p.input != nil { p.input.Resize(newW, newH) }
    // 3. Push a fresh Config to all clients (new dims, same codec/cursorMode).
    p.server.SendConfig(p.currentConfig())
    // 4. Force a keyframe so clients can re-init their decoders immediately.
    p.forceKeyframe()
}
```

Invariant: **capture dims == encoder dims == Config dims == uinput range.** No hidden scaling anywhere; this keeps the client's absolute mouse mapping pixel-accurate.

### Shutdown Sequence

```
1. Context cancelled (signal handler or explicit cancel)
2. Frame loop exits (ctx.Done select case)
3. Audio loop exits (ctx.Done select case)
4. Close encoder (flushes pending frames)
5. Close capturer (releases DRM/EGL/subprocess)
6. Close audio (kills pw-cat)
7. Close input (destroys uinput device)
8. Server.Start returns (graceful HTTP shutdown with 5s timeout)
9. Print final statistics
10. Exit
```

Order matters: encoder before capturer (encoder may reference captured DMA-BUF), server last (clients get final frames).

---

## Capability Probing

The pipeline iterates over compiled-in add-ons (registered at startup via
Go build tags) and asks each one to probe its prerequisites. There is no
fixed `SystemCapabilities` struct — the set of probes is determined by which
add-ons were compiled in.

```go
// Each capture and encoder add-on registers itself at init() time.
// The registry holds only add-ons whose build tag was active at compile time.
type AddonRegistry struct {
    Captures []CaptureAddon
    Encoders []EncoderAddon
}

type CaptureAddon interface {
    Name() string  // build tag (e.g. "kms_egl", "dxgi_dd")
    Probe(*slog.Logger) (*ProbeResult, error)
    New(cfg capture.CaptureConfig) (capture.Capturer, error)
}

type EncoderAddon interface {
    Name() string  // build tag (e.g. "nvenc", "openh264", "x264")
    Kind() string  // "hw" or "sw"
    Probe(*slog.Logger) (*ProbeResult, error)
    NewSW(cfg encode.EncoderConfig) (encode.Encoder, error)         // SW add-ons only
    NewHW(cfg hwencode.HWEncoderConfig) (hwencode.HardwareEncoder, error) // HW add-ons only
}

type ProbeResult struct {
    Available    bool
    Reason       string   // human-readable explanation if !Available
    Capabilities []string // e.g. ["h264", "hevc"] for an encoder
    Details      map[string]any // per-add-on probe metadata
}

func (p *Pipeline) ProbeAddons() map[string]*ProbeResult {
    results := make(map[string]*ProbeResult)
    for _, c := range p.registry.Captures {
        results[c.Name()], _ = c.Probe(p.logger)
    }
    for _, e := range p.registry.Encoders {
        results[e.Name()], _ = e.Probe(p.logger)
    }
    return results
}
```

This is called once at startup. Results are logged at INFO level and used for
add-on selection. The selection logic respects `[capture] force_addon` and
`[encode] force_addon` from TOML config; if not forced, it uses the per-OS
probe order documented in each platform's `capture/README.md` and
`encoders/README.md`.

There are **no probes for removed/deferred subsystems** (ffmpeg, PipeWire,
Mutter, uinput) — those were rejected as add-on prerequisites along with
their associated paths.

---

## Statistics

```go
type Stats struct {
    // Counters are atomics — no mutex needed for the hot path.
    framesCaptured   atomic.Uint64
    framesEncoded    atomic.Uint64
    framesDropped    atomic.Uint64
    framesBroadcast  atomic.Uint64
    bytesBroadcast   atomic.Uint64
    clientCount      atomic.Int32
    audioChunks      atomic.Uint64
    audioDrops       atomic.Uint64

    // RollingStats is the only field needing a mutex (ring buffer).
    mu             sync.Mutex
    totalFrameTime RollingStats // 60-sample sliding window
}

// StatsSnapshot is the read-only copy returned by Snapshot().
// Contains NO sync.Mutex (safe to copy, return by value, serialize).
type StatsSnapshot struct {
    FramesCaptured  uint64
    FramesEncoded   uint64
    FramesDropped   uint64
    FramesBroadcast uint64
    BytesBroadcast  uint64
    ClientCount     int32
    AudioChunks     uint64
    AudioDrops      uint64
    FrameTime       RollingSnapshot // Min, Max, Avg, P99
}

// Methods used by the frame loop (all O(1)):
func (s *Stats) RecordFrame(d time.Duration) // atomic increment + mu-locked ring append
func (s *Stats) RecordDrop()                 // atomic increment only (no lock)
func (s *Stats) Snapshot() StatsSnapshot     // reads atomics + locks mu briefly for rolling stats

// RollingStats tracks min/max/avg/p99 over a sliding 60-sample window.
type RollingStats struct {
    values [60]time.Duration
    index  int
    count  int
}

func (r *RollingStats) Record(d time.Duration)
func (r *RollingStats) Avg() time.Duration
func (r *RollingStats) Min() time.Duration
func (r *RollingStats) Max() time.Duration
func (r *RollingStats) P99() time.Duration
```

**Key improvement:** Fixed-size rolling window (60 samples) instead of unbounded slice. Memory is O(1) regardless of runtime duration. `Stats` is mutex-guarded because the frame loop writes and `/status` reads concurrently.

---

## Error Recovery Strategy

| Error Source | Severity | Recovery Action |
|-------------|----------|-----------------|
| Capture returns error (transient) | Warn | Log, skip frame, continue |
| Capture returns error (3 consecutive) | Error | Attempt capturer restart |
| Capture returns error (10 consecutive) | Fatal | Shutdown pipeline |
| Encoder returns error | Warn | Log, skip frame, continue |
| Encoder returns ErrFallbackToSoftware | Info | Switch to software encoder permanently |
| Server broadcast fails | - | Per-client: drop frame (handled internally) |
| Audio chunk channel closed | Warn | Attempt audio reconnect |
| Input device error | Warn | Log, disable input (viewers still work) |
| Context cancelled | - | Graceful shutdown |

---

## Refactoring Directives

### R-PIP-01: Extract from main.go
Move all logic from `cmd/server/main.go` into this module. `main.go` should only:
1. Parse `--config <path>` → `config.Load(path)` → `*config.Config`
2. Call `pipeline.New(cfg, logger)`
3. Call `pipeline.Start(ctx)`
4. Print final stats
5. Exit

### R-PIP-02: Make Stats Observable
Expose stats via the server's `/status` endpoint (already exists) and optionally via Prometheus metrics endpoint (`/metrics`).

### R-PIP-03: Hot-Reload Encoder
If hardware encoder becomes unavailable mid-stream (GPU reset, driver crash), fall back to software encoder without dropping the connection:
1. Detect encode error
2. Create software encoder with same config
3. Force keyframe on new encoder
4. Swap atomically

### R-PIP-04: Configuration Validation
Validate `*config.Config` at `New()` time (in addition to MODULE_CONFIG validation):
- `stream.fps` must be 1-240
- `stream.qp` must be 0-51 (H.264 range)
- `stream.bitrate_bps` must be 0 (QP mode) or >= 100000 (100kbps minimum)
- At least one capture add-on compiled in
- At least one encoder add-on compiled in

### R-PIP-05: Structured Shutdown Logging
On shutdown, log a summary:
```
INFO [pipeline] Shutdown complete: 18432 frames captured, 147 dropped (0.8%), 2h14m uptime
```

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Config validation at New() | No |
| Unit | Frame drop calculation logic | No |
| Unit | Stats rolling window (min/max/avg/p99) | No |
| Unit | Capability probe result parsing | No |
| Integration | Full pipeline with mock capturer + mock encoder | No |
| Integration | Graceful shutdown order verification | No |
| Integration | Hardware → software fallback transition | Partially |
| System | End-to-end: capture → encode → broadcast → client decode | Yes |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Pipeline overhead (frame loop bookkeeping) | <0.1ms/frame |
| Shutdown time (graceful) | <2 seconds |
| Memory (stats + pipeline state) | <1MB |
| Frame pacing jitter | <2ms from target interval |
| Drop decision latency | O(1) — single comparison |
