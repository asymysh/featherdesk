# Module Spec: Pipeline (Orchestrator)

## Overview

The Pipeline module is the runtime wiring layer that connects all other modules into a functioning streaming system. It handles lifecycle management, capability probing, backend selection, frame pacing, frame drop decisions, and graceful shutdown. This is the "main loop" extracted into a testable, configurable struct.

---

## Public Interface

```go
package pipeline

// Pipeline connects capture, encode, server, audio, and input into a streaming system.
type Pipeline struct {
    config    PipelineConfig
    capturer  capture.Capturer
    encoder   encode.Encoder
    hwEncoder hwencode.HardwareEncoder // nil if software path
    converter encode.Converter         // nil if hardware path
    server    server.Server
    audio     audio.AudioCapturer      // nil if --no-audio
    input     input.InputHandler       // nil if uinput unavailable
    logger    *slog.Logger
    stats     *Stats
}

// PipelineConfig holds all user-facing configuration.
type PipelineConfig struct {
    // Server
    Port int
    Bind string

    // Capture
    FPS            int
    CaptureBackend capture.CaptureBackend

    // Encode
    EncodeBackend  encode.EncoderBackend
    HardwareEncode bool // Force hardware path
    SoftwareEncode bool // Force software path
    QP             int
    BitrateBps     int

    // Audio
    NoAudio bool

    // Logging
    Verbose bool
    Quiet   bool
    LogFile string
}

// New creates a Pipeline from configuration. Does NOT start anything.
func New(cfg PipelineConfig) (*Pipeline, error)

// Start probes capabilities, initializes all modules, and begins streaming.
// Blocks until ctx is cancelled. Returns after graceful shutdown completes.
func (p *Pipeline) Start(ctx context.Context) error

// Stats returns a snapshot of pipeline performance metrics.
func (p *Pipeline) Stats() Stats
```

---

## Internal Architecture

### Startup Sequence

```
1. Parse config (already done by caller)
2. Create logger (file/stderr, level from verbose/quiet)
3. Probe system capabilities:
   a. KMS/DRM root access?
   b. VA-API hardware encode available?
   c. /dev/uinput accessible?
   d. PipeWire (pw-cat) in PATH?
   e. ffmpeg in PATH?
4. Select capture backend:
   - If KMS available AND (hardware OR auto): use KMS
   - Else if Mutter screencast available: use Screencast
   - Else if X11 + ffmpeg: use X11Grab
   - Else: fatal error
5. Select encode path (exactly two tiers — no ffmpeg-vaapi):
   - If NOT --software AND capturer implements DMABufCapturer AND hwencode.SupportsFormat:
       → HardwareEncoder (zero-copy VA-API); set cursorMode="separate"
   - Else: software in-process (OpenH264 for H.264); cursorMode per config
   - If --hardware was forced but unavailable → fatal error
6. Derive stream dims from the capturer's actual resolution (NOT hardcoded).
7. Create server (embedded client FS, session token).
8. Create input device sized to the SAME stream dims (best-effort; warn if unavailable).
9. Create audio capturer (if --no-audio not set and PipeWire available).
10. Wire callbacks:
   - server.ConfigProvider      → returns current ConfigPayload (codec, dims, fps, audio, cursorMode)
   - server.OnNewClient         → p.forceKeyframe() ONLY (server already gates on cached keyframe)
   - server.OnInput             → input.HandleRawMessage()
   - server.OnKeyframeRequest   → p.forceKeyframe() (server rate-limits before calling)
11. Start goroutines (frame loop, audio loop, server).
12. Enter main frame loop.
```

> `forceKeyframe()` dispatches to the active encoder (hw or sw) — never to a nil one. This fixes the round-1 bug where `encoder.ForceKeyframe()` would nil-panic on the hardware path.

### Main Frame Loop

Key fixes vs round 1: skip is decided BEFORE capture; each path returns a full `EncodedFrame` (carrying W/H/timestamp/keyframe); the server owns the sequence (no `frameSeq` here); `codecType` is passed; pacing and skip no longer fight.

```go
func (p *Pipeline) runFrameLoop(ctx context.Context) {
    runtime.LockOSThread()
    defer runtime.UnlockOSThread()

    targetInterval := time.Second / time.Duration(p.config.FPS)
    // Floor of 5 FPS: never skip so many frames that effective rate < 5.
    maxSkip := (p.config.FPS / 5) - 1
    if maxSkip < 0 { maxSkip = 0 }

    var (
        skipBudget int       // frames we still owe to "catch up" (bounded by maxSkip)
        lastFrameT time.Time
    )

    for {
        if ctx.Err() != nil {
            return
        }

        // (1) If we owe skips from a previous slow frame, skip a capture now.
        if skipBudget > 0 {
            skipBudget--
            p.stats.RecordDrop()
            // Still respect pacing so we don't busy-spin.
            sleepToInterval(&lastFrameT, targetInterval)
            continue
        }

        // (2) Pacing: wait until the next frame is due.
        sleepToInterval(&lastFrameT, targetInterval)

        // (3) Capture + encode (one of the two paths).
        frameStart := time.Now()
        ef, ok, err := p.captureEncode()
        if err != nil {
            if errors.Is(err, hwencode.ErrFallbackToSoftware) {
                p.switchToSoftware() // permanent for this session
                continue
            }
            p.recordCaptureError(err) // transient/3x/10x policy
            continue
        }
        if !ok {
            // No new frame (static screen) — nothing to send this tick.
            lastFrameT = time.Now()
            continue
        }

        // (4) Broadcast exactly one per-frame message.
        p.server.Broadcast(p.codecType(), ef)
        p.stats.RecordFrame(time.Since(frameStart))
        lastFrameT = time.Now()

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
    dmaCap := p.capturer.(capture.DMABufCapturer)
    fb, err := dmaCap.NextDMABuf()
    if err != nil { return EncodedFrame{}, false, err }
    if fb == nil { return EncodedFrame{}, false, nil } // no new frame
    defer unix.Close(fb.DMAFD)

    nals, err := p.hwEncoder.EncodeDMABuf(hwencode.DMABufParams{
        FD: fb.DMAFD, Width: int(fb.Width), Height: int(fb.Height),
        Stride: int(fb.Stride), Format: fb.Format, Modifier: fb.Modifier,
    })
    if err != nil { return EncodedFrame{}, false, err }
    if len(nals) == 0 { return EncodedFrame{}, false, nil } // skip frame
    return EncodedFrame{
        NALs: nals, Width: uint16(fb.Width), Height: uint16(fb.Height),
        Timestamp: fb.Timestamp, Keyframe: containsKeyframe(nals, p.codecType()),
    }, true, nil
}

func (p *Pipeline) runSoftwareFrame() (EncodedFrame, bool, error) {
    frame, err := p.capturer.NextFrame()
    if err != nil { return EncodedFrame{}, false, err }
    if frame == nil { return EncodedFrame{}, false, nil } // static screen

    i420 := p.converter.Convert(frame.Data)
    nals, err := p.encoder.Encode(i420)
    if err != nil { return EncodedFrame{}, false, err }
    if len(nals) == 0 { return EncodedFrame{}, false, nil } // encoder skip
    return EncodedFrame{
        NALs: nals, Width: uint16(frame.Width), Height: uint16(frame.Height),
        Timestamp: frame.Timestamp, Keyframe: containsKeyframe(nals, p.codecType()),
    }, true, nil
}

// forceKeyframe dispatches to whichever encoder is active.
func (p *Pipeline) forceKeyframe() {
    if p.hwEncoder != nil { p.hwEncoder.ForceKeyframe() } else { p.encoder.ForceKeyframe() }
}
```

**`containsKeyframe`**: for H.264, scans NALs for type 5 (IDR). **`sleepToInterval(&lastFrameT, interval)`** sleeps until `lastFrameT + interval`, then sets `lastFrameT = now()`. Both are O(1)/cheap.

> **Frame-drop semantics: pull-latest source assumed.** The skip-a-capture strategy assumes the capturer is a **pull-latest** source: a call to `NextFrame` / `NextDMABuf` / `NextIOSurface` always returns the CURRENT framebuffer, so skipping cleanly drops stale frames. This is true for KMS+EGL DMA-BUF (Linux), ScreenCaptureKit (macOS), and DXGI Desktop Duplication (Windows) — the supported capture add-ons. Pipe-based subprocess capturers (X11grab, ffmpeg-based) would behave as FIFO buffers and need a `DrainLatest()` extension — those backends were rejected from the architecture, so the pipeline never needs to handle them.

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
// Detected when capture returns new dims (or a sentinel capture.ErrResized).
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

```go
type SystemCapabilities struct {
    HasDRMCard       bool
    HasRootAccess    bool
    DRMCardPath      string
    DRMWidth         int
    DRMHeight        int
    DRMRefreshHz     int
    HasVAAPI         bool
    VAAPICodecs      []string
    HasUInput        bool
    HasPipeWire      bool
    HasFFmpeg        bool
    HasMutterScreencast bool
}

func ProbeCapabilities() *SystemCapabilities
```

This is called once at startup. Results are logged at INFO level and used for backend selection.

---

## Statistics

```go
type Stats struct {
    mu sync.Mutex // Stats is updated from the frame loop and read from /status

    // Frame pipeline counters
    FramesCaptured   uint64
    FramesEncoded    uint64
    FramesDropped    uint64
    FramesBroadcast  uint64

    // Timing (rolling window, last 60 frames)
    TotalFrameTime  RollingStats // end-to-end per processed frame

    // Network
    BytesBroadcast  uint64
    ClientCount     int32

    // Audio
    AudioChunks     uint64
    AudioDrops      uint64
}

// Methods used by the frame loop (all O(1), lock briefly):
func (s *Stats) RecordFrame(d time.Duration) // FramesCaptured++, FramesBroadcast++, TotalFrameTime.Record(d)
func (s *Stats) RecordDrop()                 // FramesDropped++
func (s *Stats) Snapshot() StatsSnapshot     // lock-free copy for /status

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
1. Parse flags into `PipelineConfig`
2. Call `pipeline.New(cfg)`
3. Call `pipeline.Start(ctx)`
4. Print final stats
5. Exit

### R-PIP-02: Make Stats Observable
Expose stats via the server's `/status` endpoint (already exists) and optionally via Prometheus metrics endpoint (`/metrics`).

### R-PIP-03: Hot-Reload Encoder
If hardware encoder becomes unavailable mid-stream (GPU reset, driver crash), seamlessly fall back to software encoder without dropping the connection:
1. Detect encode error
2. Create software encoder with same config
3. Force keyframe on new encoder
4. Swap atomically

### R-PIP-04: Configuration Validation
Validate PipelineConfig at `New()` time:
- FPS must be 1-240
- Port must be 1-65535
- QP must be 0-51 (H.264 range)
- BitrateBps must be 0 or >= 100000 (100kbps minimum)
- Bind must be valid IP or "0.0.0.0"

### R-PIP-05: Structured Shutdown Logging
On shutdown, log a summary:
```
INFO [pipeline] Shutdown complete: 18432 frames captured, 147 dropped (0.8%), 2h14m uptime
```

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | PipelineConfig validation | No |
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
