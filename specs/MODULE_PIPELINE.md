# Module Spec: Pipeline (Orchestrator)

## Overview

The Pipeline module is the runtime wiring layer that connects all other modules into a functioning streaming system. It handles lifecycle management, capability probing, backend selection, frame pacing, frame drop decisions, and graceful shutdown. This is the "main loop" extracted into a testable, configurable struct.

---

## Public Interface

```go
package pipeline

// Pipeline connects capture, encode, server, input, clipboard, file transfer,
// and audio (deferred) into a streaming system.
type Pipeline struct {
    cfg       *config.Config           // parsed TOML config (owned by caller, read-only)
    registry  *AddonRegistry           // compiled-in capture/encoder add-ons (init()-registered)
    transport transport.Transport      // QUIC/WebTransport listener; built here, handed to server
    capturer  capture.Capturer
    surfCap   capture.SurfaceCapturer  // nil if capturer doesn't implement SurfaceCapturer
    converter *encode.Converter        // BGRA/RGBA→I420 (+ scale to output dims); nil on HW path
    encoder   encode.Encoder           // nil if hardware path
    hwEncoder hwencode.HardwareEncoder // nil if software path
    server    server.Server
    input     input.Dispatcher         // nil → view-only (no input add-on compiled in)
    clipboard clipboard.Monitor        // nil if [clipboard] disabled or Probe failed
    files     filetransfer.Service     // nil if [filetransfer] disabled
    audio     audio.AudioCapturer      // per-OS capture add-on; nil if no audio add-on / [audio] disabled
    audioEnc  audio.AudioEncoder       // Opus (build tag) or PCM passthrough; nil if audio off
                                       // (audio design LOCKED; impl deferred — MODULE_AUDIO)
    params    stream.Params            // current dynamic stream parameters (output dims)
    paramCh   chan stream.Params       // adaptive/control param changes, applied ON the frame loop
    logger    *slog.Logger
    stats     *Stats
    // (audio + webcam deferred; input/clipboard/filetransfer are live modules)
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
   - ALWAYS set `p.capturer` (a `SurfaceCapturer` also satisfies `Capturer`),
     and set `p.surfCap` additionally when the chosen capturer implements it.
     `p.capturer` must be non-nil even on the zero-copy path so that
     `degradeToSoftware` can fall back to `p.capturer.NextFrame()` on the SAME
     add-on without re-probing.
5. If hardware path errors with ErrFallbackToSoftware mid-session: degrade
   to software path permanently for the rest of the session (builds the
   Converter + SW encoder via `buildSoftwarePath`; `p.capturer` is already set)
6. Derive stream dims from the capturer's actual resolution (NOT hardcoded).
6b. Instantiate the QUIC transport: `transport.New(transport.Config{…})` from
    the `[server]`, `[server.tls]`, and `[transport]` sections (binds the UDP
    port + TLS). The pipeline owns the transport and hands it to the server.
7. Create server (embedded client FS, session token), passing it the transport
    via `server.Config.Transport`.
8. Probe + create input dispatcher if an input add-on is compiled in AND
   `[input] enabled`: build `KeyMouseInjector` (interception/uinput/cgevent) and
   optional `TouchInjector` (win_touch), sized to the SAME stream dims. If no
   input add-on is compiled in, the binary is **view-only** (log it; not an error).
9. (Webcam was here — deferred to a future version, see CENTRAL_SPEC "Deferred".)
10. Create clipboard Monitor + file-transfer Service if their `[*] enabled`.
11. If `[audio] enabled` AND an audio capture add-on is compiled in: create the
    capturer + the encoder (`opus` build tag → Opus, else PCM passthrough). The
    `[audio]` section + struct field exist now; the per-OS add-ons themselves are
    **implementation-deferred** (MODULE_AUDIO), so this step is wired but inert
    until they land. No add-on / disabled → `p.audio = nil` (video-only).
12. Wire callbacks:
   - server.ConfigProvider          → returns current ConfigPayload (codec, dims, fps, hdr, cursorMode, session_token)
   - server.OnNewClient             → p.forceKeyframe() ONLY (server already gates on cached keyframe)
   - server.SetInputCallback        → input.Dispatcher.Dispatch (binary; nil if view-only)
   - server.SetClipboardCallback    → clipboard.Monitor.Set (direction + role gated by server)
   - server.SetFileTransferService  → filetransfer.Service (nil if [filetransfer] disabled → server rejects new file-transfer streams with CloseProtocolError)
   - server.OnKeyframeRequest       → p.forceKeyframe() (server rate-limits before calling)
   - input gamepad rumble emitter   → server.SendGamepadRumble (nil if no gamepad add-on)
13. Start goroutines (frame loop, clipboard monitor, audio loop, server).
14. Enter main frame loop.
```

> `forceKeyframe()` dispatches to the active encoder (hw or sw) — never to a nil one. This fixes the round-1 bug where `encoder.ForceKeyframe()` would nil-panic on the hardware path.

### Main Frame Loop

Key fixes vs round 1: skip is decided BEFORE capture; `EncodedFrame.Data` is contiguous Annex B (no per-NAL split/rejoin); the server owns the sequence; pacing is capture-to-capture (NOT delivery-to-delivery).

**Allocation strategy:** the encoder's Annex B output buffer and the server's broadcast access-unit buffers use `sync.Pool` (sized to a typical access unit ~50KB). Steady-state frame loop is zero-alloc after warmup. The `Converter.Convert()` reuses I420 buffers (already documented); the encoder and server must follow the same pattern.

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

        // (0) Apply any pending parameter changes ON THIS GOROUTINE (M-6).
        //     stream.Manager / control handlers never touch the encoder directly;
        //     they send stream.Params into p.paramCh and the frame loop drains it
        //     here, so Encode and UpdateStreamParams are never concurrent.
        select {
        case np := <-p.paramCh:
            p.applyParams(np)
        default:
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
        if errors.Is(err, stream.ErrFallbackToSoftware) {
            p.degradeToSoftware()
            return EncodedFrame{}, false, nil
        }
        return EncodedFrame{}, false, err
    }
    if fb == nil { return EncodedFrame{}, false, nil } // no new surface this tick

    // EncodeSurface owns fb: it calls fb.Release() exactly once on EVERY path
    // (success, error, ErrFallbackToSoftware). The pipeline never releases it.
    encoded, err := p.hwEncoder.EncodeSurface(fb)
    if err != nil {
        if errors.Is(err, stream.ErrFallbackToSoftware) {
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

    i420 := p.converter.Convert(frame) // *capture.Frame → I420, scaled to output dims
    data, keyframe, err := p.encoder.Encode(i420)
    if err != nil { return stream.EncodedFrame{}, false, err }
    if len(data) == 0 { return stream.EncodedFrame{}, false, nil } // skipped (rate control)
    return stream.EncodedFrame{
        // Dims are the ENCODED (output) dims = i420 dims, NOT the native capture
        // dims — the converter already scaled to the stream resolution.
        Data: data, Width: uint16(i420.Width), Height: uint16(i420.Height),
        Timestamp: frame.Timestamp,
        Keyframe:  keyframe,                     // from the encoder (M-2); no NAL re-scan
        CodecType: protocol.FrameTypeVideoH264,  // SW path is always H.264
    }, true, nil
}

// forceKeyframe dispatches to whichever encoder is active.
func (p *Pipeline) forceKeyframe() {
    if p.hwEncoder != nil { p.hwEncoder.ForceKeyframe() } else { p.encoder.ForceKeyframe() }
}

// applyParams applies a stream.Params change ON THE FRAME-LOOP GOROUTINE (M-6).
// One unified path for BOTH client-requested changes (resize/set_bitrate/set_fps/
// set_hdr arriving via p.paramCh) and capture-detected resolution changes.
func (p *Pipeline) applyParams(np stream.Params) {
    dimsChanged := np.Width != p.params.Width || np.Height != p.params.Height
    // Try in-place reconfigure on the active capturer + encoder; on
    // stream.ErrRequiresRestart, tear down and rebuild for the new params.
    if err := p.reconfigureOrRebuild(np); err != nil {
        p.logger.Error("pipeline", "param change failed", "err", err)
        return
    }
    p.params = np
    if dimsChanged && p.input != nil {
        p.input.Resize(np.Width, np.Height) // keep absolute mouse mapping 1:1
    }
    p.server.SendConfig(p.currentConfig()) // push fresh {"type":"config"} line
    p.forceKeyframe()                       // let clients re-init their decoders
}

// degradeToSoftware permanently swaps the HW path for the SW path mid-session
// (GPU reset / driver constraint). Builds the Converter + SW encoder, nils
// hwEncoder/surfCap, forces a keyframe. Called only from the frame-loop goroutine.
func (p *Pipeline) degradeToSoftware() {
    p.logger.Warn("pipeline", "hardware encoder unavailable; degrading to software")
    p.buildSoftwarePath(p.params) // sets p.converter + p.encoder
    p.hwEncoder, p.surfCap = nil, nil
    p.forceKeyframe()
}
```

**`sleepToInterval(&lastFrameT, interval)`** sleeps until `lastFrameT + interval`, then sets `lastFrameT = now()`. O(1)/cheap. There is no `containsKeyframe` — keyframe status comes from the encoder (`EncodedFrame.Keyframe` on the HW path; the `keyframe` return on the SW path), never a pipeline-side NAL scan.

**Two internal helpers** referenced above: `buildSoftwarePath(p stream.Params)` constructs `p.converter` (BGRA/RGBA→I420 + scale-to-output) and a SW `encode.Encoder` for the given params; `reconfigureOrRebuild(np stream.Params)` calls `UpdateStreamParams(np)` on the active capturer + encoder and, on `stream.ErrRequiresRestart`, tears them down and rebuilds for `np`. Both run only on the frame-loop goroutine.

> **Frame-drop semantics: pull-latest source assumed.** The skip-a-capture strategy assumes the capturer is a **pull-latest** source: a call to `NextFrame` / `NextSurface` always returns the CURRENT framebuffer, so skipping cleanly drops stale frames. This is true for KMS+EGL (Linux), ScreenCaptureKit (macOS), and DXGI Desktop Duplication (Windows). Pipe-based subprocess capturers (X11grab, ffmpeg-based) were rejected from the architecture.

### Audio Loop (Separate Goroutine)

```go
func (p *Pipeline) runAudioLoop(ctx context.Context) {
    if p.audio == nil || p.audioEnc == nil {
        return // no audio add-on / [audio] disabled → video-only
    }
    chunks := p.audio.Chunks()
    codecType := audioCodecType(p.audioEnc.Codec()) // "opus"→0x08, "pcm/s16le"→0x04
    for {
        select {
        case <-ctx.Done():
            return
        case chunk, ok := <-chunks:
            if !ok {
                return
            }
            payload, err := p.audioEnc.Encode(chunk) // Opus packet OR PCM passthrough
            if err != nil { p.recordAudioError(err); continue }
            // chunk.Timestamp was sampled at CAPTURE time in the add-on read loop,
            // on the SAME CLOCK_MONOTONIC epoch as video. Do NOT re-stamp here.
            p.server.BroadcastAudio(codecType, payload, chunk.Timestamp)
        }
    }
}
```

**Critical:** the audio timestamp comes from `chunk.Timestamp` (capture-time, monotonic), never from `time.Now()` at this point. Re-stamping here would add up to ~640 ms of channel-buffer skew and break A/V sync (this was the original bug).

### Resolution-Change Handling

The pipeline owns the resolution-change orchestration (no other module drives it).
Both triggers funnel through the **single** `applyParams` path (defined above),
so there is exactly one place that mutates the encoder/capturer:

```go
// Capture-detected change: capture returns a frame whose native dims differ
// from the current OUTPUT dims AND no explicit downscale is configured. The
// pipeline builds a Params with the new dims and reuses applyParams — it does
// NOT have a second, separate resize routine.
func (p *Pipeline) onCaptureDimsChanged(newW, newH int) {
    np := p.params
    np.Width, np.Height = newW, newH
    p.paramCh <- np   // applied on the frame-loop goroutine (M-6)
}

// Client-requested changes (resize / set_bitrate / set_fps / set_hdr) arrive via
// stream.Manager, which likewise sends a stream.Params into p.paramCh.
```

`applyParams` resizes input, sends a fresh `{"type":"config"}` line, and forces a
keyframe (see its definition under "Main Frame Loop"). HDR requested with no
HEVC/10-bit encoder is rejected there: the server sends `{"type":"hdr_unavailable"}`
and the stream stays SDR (see [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)).

Invariant: **encoder-output dims == config dims == input-coordinate range.** The
encoder (HW in-encoder, SW via libyuv `I420Scale`) absorbs any native→output
scaling; this keeps the client's absolute mouse mapping pixel-accurate.

### Shutdown Sequence

```
1. Context cancelled (signal handler or explicit cancel)
2. Frame loop exits (ctx.Done select case)
3. Audio loop exits (ctx.Done select case)        [audio deferred — placeholder]
4. Close encoder (flushes pending frames)
5. Close capturer (releases DRM/EGL/subprocess)
6. Close audio encoder + capturer add-on (no subprocess) [audio impl deferred]
7. Close input Dispatcher (closes active KeyMouse / Touch / Gamepad injectors,
   releasing all held keys + buttons on the way out)
8. Close clipboard Monitor (stops the message pump / X event loop)
9. Close filetransfer Service (drains in-flight, fsyncs, removes orphan .part files)
10. Server.Start returns (graceful HTTP shutdown with 5s timeout)
11. Print final statistics
12. Exit
```

Order matters: encoder before capturer (encoder may reference captured DMA-BUF),
server last (clients get final frames + an orderly close).

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

**Key improvement:** Fixed-size rolling window (60 samples) instead of unbounded slice. Memory is O(1) regardless of runtime duration. `Stats` is mutex-guarded because the frame loop writes and the Prometheus metrics exporter reads concurrently.

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
| Clipboard Monitor error (Wayland unsupported, X display drop) | Warn | Log, disable clipboard sync, leave video unaffected |
| File-transfer write error | Warn | Send `ERROR` to client for that transfer; drop only that transfer |
| Gamepad add-on connect failure | Warn | Drop subsequent gamepad records; log once per index |
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
Expose stats via the Prometheus metrics endpoint (`/metrics` on the separate
metrics port — see MODULE_CONFIG `[metrics]`). The legacy `/status` JSON endpoint
on the main port is removed; liveness is `/healthz`, detailed counters are
Prometheus. No unauthenticated observability surface on the main TLS server.

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
