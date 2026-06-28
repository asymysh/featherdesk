# Module Spec: Pipeline (Orchestrator)

## Overview

The Pipeline module is the runtime wiring layer that connects all other modules into a functioning streaming system. It handles lifecycle management, capability probing, backend selection, frame pacing, frame drop decisions, and graceful shutdown. This is the "main loop" extracted into a testable, configurable struct.

---

## Public Interface

```rust
// module: featherdesk-host::pipeline  (NOT a separate crate — the orchestrator
// lives in the host binary; see CENTRAL_SPEC "File Structure")

/// Pipeline connects capture, encode, server, input, clipboard, file transfer,
/// and audio (deferred) into a streaming system.
pub struct Pipeline {
    cfg: std::sync::Arc<config::Config>,              // parsed TOML config (read-only)
    registry: AddonRegistry,                          // capture/encoder/input/audio add-ons loaded from the add-ons directory (dlopen)
    transport: Box<dyn transport::Transport>,         // QUIC/WebTransport listener; built here, handed to server
    capturer: Box<dyn capture::Capturer>,
    surf_cap: Option<Box<dyn capture::SurfaceCapturer>>, // None if capturer doesn't implement SurfaceCapturer
    converter: Option<encode::Converter>,             // BGRA/RGBA→I420 (+ scale to output dims); None on HW path
    encoder: Option<Box<dyn encode::Encoder>>,        // None if hardware path
    hw_encoder: Option<Box<dyn hwencode::HardwareEncoder>>, // None if software path
    server: Box<dyn server::Server>,
    input: Option<input::Dispatcher>,                 // None → view-only (no input add-on loaded)
    clipboard: Option<clipboard::Monitor>,            // None if [clipboard] disabled or probe failed
    files: Option<filetransfer::Service>,             // None if [filetransfer] disabled
    audio: Option<Box<dyn audio::AudioCapturer>>,     // per-OS capture add-on; None if no audio add-on / [audio] disabled
    audio_enc: Option<Box<dyn audio::AudioEncoder>>,  // Opus (add-on) or PCM passthrough; None if audio off
                                                      // (audio design LOCKED; impl deferred — MODULE_AUDIO)
    params: stream::Params,                           // current dynamic stream parameters (output dims)
    // adaptive/control param changes, applied ON the frame loop (tokio::sync::mpsc;
    // senders held by stream::Manager + on_capture_dims_changed, drained by the loop).
    param_tx: tokio::sync::mpsc::Sender<stream::Params>,
    param_rx: tokio::sync::mpsc::Receiver<stream::Params>,
    stats: std::sync::Arc<Stats>,
    // Logging is via the `tracing` crate (replaces the old *slog.Logger field).
    // (audio + webcam deferred; input/clipboard/filetransfer are live modules)
}

/// Creates a Pipeline from a parsed TOML configuration. Does NOT start anything.
/// The TOML config struct is defined in specs/core/MODULE_CONFIG.md — pipeline
/// does not own configuration parsing.
pub fn new(cfg: std::sync::Arc<config::Config>) -> Result<Pipeline, PipelineError> { /* … */ }

impl Pipeline {
    /// Probes loaded add-ons, initializes the chosen capture + encoder add-ons,
    /// and begins streaming. Blocks (awaits) until `cancel` fires. Returns after
    /// graceful shutdown completes.
    pub async fn start(&mut self, cancel: CancellationToken) -> Result<(), PipelineError> { /* … */ }

    /// Returns a snapshot of pipeline performance metrics.
    /// Returns StatsSnapshot (no Mutex — safe to copy/serialize).
    pub fn stats(&self) -> StatsSnapshot { /* … */ }
}

/// One thiserror-derived error enum for the crate.
#[derive(Debug, thiserror::Error)]
pub enum PipelineError {
    #[error("config: {0}")] Config(String),
    #[error("no capture add-on loaded")] NoCapture,
    #[error("no encoder add-on loaded")] NoEncoder,
    #[error(transparent)] Stream(#[from] stream::StreamError),
}
```

The pipeline takes `Arc<config::Config>` directly (the parsed TOML struct from
[`MODULE_CONFIG.md`](./MODULE_CONFIG.md)). There is no separate `PipelineConfig`
struct. The pipeline reads `[capture]`, `[encode]`, `[stream]`,
`[stream.adaptive]` sections plus per-add-on `[addon_module_<id>]` sections.

---

## Internal Architecture

### Startup Sequence

```
1. Caller (src/main.rs) loads TOML via config::load(--config path)
   — phase-A validation only: syntax + intra-section rules. [addon_module_*]
   sections are captured raw (undecoded); force_addon is checked non-empty when
   mode="forced" but NOT yet checked against the loaded set.
2. Caller initializes `tracing-subscriber` per [log] section (text in TTY, JSON otherwise)
3. pipeline::new(cfg) builds the Pipeline:
   a. **Load add-ons**: scan [addons] dir, dlopen/LoadLibraryW each
      featherdesk-addon-*.{so,dylib,dll}, check ABIVersion, read the capability
      descriptor, and register it by kind (Captures/Encoders/Inputs/Audio).
   b. **Phase-B (load-aware) config validation**: force_addon must name a LOADED
      add-on ID (else startup error); each [addon_module_<id>] section is now
      strict-decoded iff its add-on is loaded, else silently ignored.
   d. Probe each loaded capture add-on (one shared library per add-on):
      - Linux:   nvfbc → kms_egl
      - macOS:   sck
      - Windows: dxgi_dd (with optional IddCx VDD auto-install if no display)
   e. Probe each loaded encoder add-on:
      - HW: nvenc → amf/amf_rocm → libva → qsv → mf_hw → vt_hw
      - SW: x264 (if ffmpeg present) → vt_sw → openh264
   f. Honor [capture] force_addon / [encode] force_addon overrides:
      - If forced and add-on not loaded: startup error
      - If forced and probe fails: startup error
      - If auto: pick first available per the order above
      - If the chosen path is HW, require at least one loaded SW encoder add-on
        as a fallback; if none is loaded, fail fast ("HW encoder X has no SW
        fallback add-on loaded") rather than risk an unrecoverable mid-session
        StreamError::FallbackToSoftware.
4. Match capture surface format to encoder input:
    - HW encoder + SurfaceCapturer with compatible FBInfo → zero-copy path
   - SW encoder + any Capturer → CPU readback + I420 conversion path
   - ALWAYS set `self.capturer` (a `SurfaceCapturer` also satisfies `Capturer`),
     and set `self.surf_cap` additionally when the chosen capturer implements it.
     `self.capturer` must be `Some(..)` even on the zero-copy path so that
     `degrade_to_software` can fall back to `self.capturer.next_frame()` on the SAME
     add-on without re-probing.
5. If hardware path errors with StreamError::FallbackToSoftware mid-session: degrade
   to software path permanently for the rest of the session (builds the
   Converter + SW encoder via `build_software_path`; `self.capturer` is already set)
6. Derive stream dims from the capturer's actual resolution (NOT hardcoded).
6b. Instantiate the QUIC transport: `transport.New(transport.Config{…})` from
    the `[server]`, `[server.tls]`, and `[transport]` sections (binds the UDP
    port + TLS). The pipeline owns the transport and hands it to the server.
7. Create server (embedded client FS, session token), passing it the transport
    via `server.Config.Transport`.
8. Create the input dispatcher if `[input] enabled` (default true): the
   `KeyMouseInjector` is the **in-core `enigo` default** unless an override add-on
   is loaded (Interception on Windows, uinput on Linux); plus an optional
   `TouchInjector` (win_touch) and gamepad injector (vigem/gcvirtual/uinput),
   sized to the SAME stream dims. Only `[input] enabled = false` makes the binary
   **view-only** (log it; not an error).
9. (Webcam was here — deferred to a future version, see CENTRAL_SPEC "Deferred".)
10. Create clipboard Monitor + file-transfer Service if their `[*] enabled`.
11. If `[audio] enabled` AND an audio capture add-on is loaded: create the
    capturer + the encoder (`opus` add-on → Opus, else PCM passthrough). The
    `[audio]` section + struct field exist now; the per-OS add-ons themselves are
    **implementation-deferred** (MODULE_AUDIO), so this step is wired but inert
    until they land. No add-on / disabled → `self.audio = None` (video-only).
12. Wire callbacks:
   - server.set_config_provider       → returns current ConfigPayload (codec, dims, fps, hdr, cursorMode, session_token)
   - server.set_new_client_callback   → self.force_keyframe() ONLY (server already gates on cached keyframe)
   - server.set_input_callback        → input::Dispatcher::dispatch (binary; None only if [input] enabled=false)
   - server.set_clipboard_callback    → clipboard::Monitor::set (direction + role gated by server)
   - server.set_file_transfer_service → filetransfer::Service (None if [filetransfer] disabled → server rejects new file-transfer streams with close::PROTOCOL_ERROR)
   - server.set_keyframe_request_callback → self.force_keyframe() (server rate-limits before calling)
   - input gamepad rumble emitter     → server.send_gamepad_rumble (None if no gamepad add-on)
13. Start the frame loop (dedicated std::thread) + tokio tasks (clipboard monitor, audio loop, server).
14. Enter main frame loop.
```

> `force_keyframe()` dispatches to the active encoder (hw or sw) — never to a `None` one. This fixes the round-1 bug where `encoder.force_keyframe()` would panic on the hardware path.

### Main Frame Loop

Key fixes vs round 1: skip is decided BEFORE capture; `EncodedFrame.data` is contiguous Annex B (no per-NAL split/rejoin); the server owns the sequence; pacing is capture-to-capture (NOT delivery-to-delivery).

**Allocation strategy:** the encoder's Annex B output buffer and the server's broadcast access-unit buffers reuse pooled buffers (e.g. a `bytes::BytesMut` pool sized to a typical access unit ~50KB). Steady-state frame loop is zero-alloc after warmup. The `Converter::convert()` reuses I420 buffers (already documented); the encoder and server must follow the same pattern.

```rust
fn run_frame_loop(&mut self, cancel: CancellationToken) {
    // Runs on a dedicated OS thread (std::thread), not a tokio task — the
    // capture/encode hot loop pins a thread (replaces runtime.LockOSThread).

    let target_interval = Duration::from_secs(1) / self.params.fps;
    let max_skip = (self.params.fps / 5).saturating_sub(1);

    let mut skip_budget: u32 = 0;
    let mut last_frame_t = Instant::now();

    loop {
        if cancel.is_cancelled() {
            return;
        }

        // (0) Apply any pending parameter changes ON THIS THREAD (M-6).
        //     stream::Manager / control handlers never touch the encoder directly;
        //     they send stream::Params into self.param_rx and the frame loop drains
        //     it here, so encode and update_stream_params are never concurrent.
        if let Ok(np) = self.param_rx.try_recv() {
            self.apply_params(np);
        }

        // (1) Skip owed frames from a previous overrun.
        if skip_budget > 0 {
            skip_budget -= 1;
            self.stats.record_drop();
            sleep_to_interval(&mut last_frame_t, target_interval);
            continue;
        }

        // (2) Pacing: wait until the next frame is due.
        sleep_to_interval(&mut last_frame_t, target_interval);
        last_frame_t = Instant::now(); // SET BEFORE capture, not after broadcast.
                                       // This makes pacing capture-to-capture,
                                       // decoupled from downstream processing time.

        // (3) Capture + encode.
        let frame_start = Instant::now();
        let ef = match self.capture_encode() {
            Ok(Some(ef)) => ef,
            Ok(None) => continue, // static screen, nothing to send
            Err(stream::StreamError::FallbackToSoftware) => {
                self.degrade_to_software(); // permanent for this session
                continue;
            }
            Err(e) => {
                self.record_capture_error(e);
                continue;
            }
        };

        // (4) Broadcast. Keyframe detection is done once here; the server
        //     trusts ef.keyframe (no redundant NAL scan).
        self.server.broadcast(ef.codec_type, ef);
        self.stats.record_frame(frame_start.elapsed());

        // (5) Drop decision: if we overran, owe skips (capped at max_skip → 5 FPS floor).
        let d = frame_start.elapsed();
        if d > target_interval {
            let owe = (d.as_nanos() / target_interval.as_nanos()) as u32;
            skip_budget = owe.min(max_skip);
        }
    }
}

// capture_encode runs the active path. Ok(None) means "no new frame this tick"
// (not an error).
fn capture_encode(&mut self) -> Result<Option<stream::EncodedFrame>, stream::StreamError> {
    if self.hw_encoder.is_some() {
        self.run_hardware_frame()
    } else {
        self.run_software_frame()
    }
}

fn run_hardware_frame(&mut self) -> Result<Option<stream::EncodedFrame>, stream::StreamError> {
    let fb = match self.surf_cap.as_mut().unwrap().next_surface() {
        Ok(Some(fb)) => fb,
        Ok(None) => return Ok(None), // no new surface this tick
        Err(stream::StreamError::FallbackToSoftware) => {
            self.degrade_to_software();
            return Ok(None);
        }
        Err(e) => return Err(e),
    };

    // Capture metadata BEFORE the move (encode_surface consumes fb). Output dims
    // come from the active params (the HW encoder scaled to them per the scaling
    // invariant), NOT fb's native capture dims — mirrors the SW path.
    let (out_w, out_h) = (self.params.width as u16, self.params.height as u16);
    let ts_ns = fb.timestamp_ns;
    let codec_type = video_codec_type(self.hw_encoder.as_ref().unwrap().codec()); // "avc1.*"→H264, "hvc1.*"→HEVC

    // encode_surface CONSUMES fb (moved in → FbInfo's Drop releases it exactly
    // once on EVERY path: success, error, FallbackToSoftware). The pipeline never
    // releases it — RAII replaces the Go `fb.Release()` discipline. It returns ONE
    // EncodedUnit (no Option — the HW path never "skips"; the only skip is
    // next_surface() → Ok(None) above), or StreamError::FallbackToSoftware.
    match self.hw_encoder.as_mut().unwrap().encode_surface(fb) {
        Ok(unit) => Ok(Some(stream::EncodedFrame {
            data: unit.data.into_vec().into(), // owned RVec<u8> → bytes::Bytes
            width: out_w,
            height: out_h,
            timestamp_ns: ts_ns,
            keyframe: unit.keyframe,           // encoder-set (M-2); no NAL re-scan
            codec_type,                        // VIDEO_H264 or VIDEO_HEVC
        })),
        Err(stream::StreamError::FallbackToSoftware) => {
            self.degrade_to_software();
            Ok(None)
        }
        Err(e) => Err(e),
    }
}

fn run_software_frame(&mut self) -> Result<Option<stream::EncodedFrame>, stream::StreamError> {
    let frame = match self.capturer.next_frame()? {
        Some(f) => f,
        None => return Ok(None), // static screen
    };

    let i420 = self.converter.as_mut().unwrap().convert(&frame); // capture::Frame → I420, scaled to output dims
    match self.encoder.as_mut().unwrap().encode(&i420)? {
        None => Ok(None), // skipped (rate control)
        Some(unit) => Ok(Some(stream::EncodedFrame {
            // Dims are the ENCODED (output) dims = i420 dims, NOT the native
            // capture dims — the converter already scaled to the stream resolution.
            data: unit.data.into_vec().into(), // owned RVec<u8> → bytes::Bytes
            width: i420.width as u16,
            height: i420.height as u16,
            timestamp_ns: frame.timestamp_ns,
            keyframe: unit.keyframe,                     // from the encoder (M-2); no NAL re-scan
            codec_type: protocol::frame_type::VIDEO_H264, // SW path is always H.264
        })),
    }
}

// force_keyframe dispatches to whichever encoder is active.
fn force_keyframe(&mut self) {
    if let Some(hw) = self.hw_encoder.as_mut() {
        hw.force_keyframe();
    } else if let Some(sw) = self.encoder.as_mut() {
        sw.force_keyframe();
    }
}

// apply_params applies a stream::Params change ON THE FRAME-LOOP THREAD (M-6).
// One unified path for BOTH client-requested changes (resize/set_bitrate/set_fps/
// set_hdr arriving via self.param_rx) and capture-detected resolution changes.
fn apply_params(&mut self, np: stream::Params) {
    let dims_changed = np.width != self.params.width || np.height != self.params.height;
    // Try in-place reconfigure on the active capturer + encoder; on
    // StreamError::RequiresRestart, tear down and rebuild for the new params.
    if let Err(e) = self.reconfigure_or_rebuild(&np) {
        tracing::error!(target: "pipeline", err = %e, "param change failed");
        return;
    }
    if dims_changed {
        if let Some(input) = self.input.as_mut() {
            input.resize(np.width, np.height); // keep absolute mouse mapping 1:1
        }
    }
    self.params = np;
    self.server.send_config(self.current_config()); // push fresh {"type":"config"} line
    self.force_keyframe();                           // let clients re-init their decoders
}

// degrade_to_software permanently swaps the HW path for the SW path mid-session
// (GPU reset / driver constraint). Builds the Converter + SW encoder, clears
// hw_encoder/surf_cap, forces a keyframe. Called only from the frame-loop thread.
fn degrade_to_software(&mut self) {
    tracing::warn!(target: "pipeline", "hardware encoder unavailable; degrading to software");
    let params = self.params.clone();
    self.build_software_path(&params); // sets self.converter + self.encoder
    self.hw_encoder = None;
    self.surf_cap = None;
    self.force_keyframe();
}
```

**`sleep_to_interval(&mut last_frame_t, interval)`** sleeps until `last_frame_t + interval`, then sets `last_frame_t = Instant::now()`. O(1)/cheap. There is no `contains_keyframe` — keyframe status comes from the encoder (`EncodedFrame.keyframe` on the HW path; the `keyframe` field of the returned unit on the SW path), never a pipeline-side NAL scan.

**Two internal helpers** referenced above: `build_software_path(&stream::Params)` constructs `self.converter` (BGRA/RGBA→I420 + scale-to-output) and a SW `encode::Encoder` for the given params; `reconfigure_or_rebuild(&stream::Params)` calls `update_stream_params(np)` on the active capturer + encoder and, on `StreamError::RequiresRestart`, tears them down and rebuilds for `np`. Both run only on the frame-loop thread.

> **Frame-drop semantics: pull-latest source assumed.** The skip-a-capture strategy assumes the capturer is a **pull-latest** source: a call to `next_frame` / `next_surface` always returns the CURRENT framebuffer, so skipping cleanly drops stale frames. This is true for KMS+EGL (Linux), ScreenCaptureKit (macOS), and DXGI Desktop Duplication (Windows). Pipe-based subprocess capturers (X11grab, ffmpeg-based) were rejected from the architecture.

### Audio Loop (Separate Task)

```rust
async fn run_audio_loop(&mut self, cancel: CancellationToken) {
    // no audio add-on / [audio] disabled → video-only
    let (Some(audio), Some(audio_enc)) = (self.audio.as_mut(), self.audio_enc.as_mut()) else {
        return;
    };
    let mut chunks = audio.chunks(); // tokio::sync::mpsc::Receiver<PcmChunk>
    let codec_type = audio_codec_type(audio_enc.codec()); // "opus"→0x08, "pcm/s16le"→0x04
    loop {
        tokio::select! {
            _ = cancel.cancelled() => return,
            maybe = chunks.recv() => {
                let Some(chunk) = maybe else { return }; // channel closed
                match audio_enc.encode(&chunk) { // Opus packet OR PCM passthrough
                    Err(e) => { self.stats.record_audio_drop(); tracing::warn!(target: "pipeline", err = %e, "audio encode"); continue }
                    Ok(payload) => {
                        // chunk.timestamp_ns was sampled at CAPTURE time in the add-on read
                        // loop, on the SAME CLOCK_MONOTONIC epoch as video. Do NOT re-stamp here.
                        self.server.broadcast_audio(codec_type, payload.into_vec().into(), chunk.timestamp_ns); // RVec -> Bytes (host-side)
                    }
                }
            }
        }
    }
}
```

**Critical:** the audio timestamp comes from `chunk.timestamp_ns` (capture-time, monotonic), never from `Instant::now()` at this point. Re-stamping here would add up to ~640 ms of channel-buffer skew and break A/V sync (this was the original bug).

### Resolution-Change Handling

The pipeline owns the resolution-change orchestration (no other module drives it).
Both triggers funnel through the **single** `applyParams` path (defined above),
so there is exactly one place that mutates the encoder/capturer:

```rust
// Capture-detected change: capture returns a frame whose native dims differ
// from the current OUTPUT dims AND no explicit downscale is configured. The
// pipeline builds a Params with the new dims and reuses apply_params — it does
// NOT have a second, separate resize routine.
fn on_capture_dims_changed(&self, new_w: u32, new_h: u32) {
    let mut np = self.params.clone();
    np.width = new_w;
    np.height = new_h;
    let _ = self.param_tx.try_send(np); // applied on the frame-loop thread (M-6)
}

// Client-requested changes (resize / set_bitrate / set_fps / set_hdr) arrive via
// stream::Manager, which likewise sends a stream::Params into self.param_tx.
```

`apply_params` resizes input, sends a fresh `{"type":"config"}` line, and forces a
keyframe (see its definition under "Main Frame Loop"). HDR requested with no
HEVC/10-bit encoder is rejected there: the server sends `{"type":"hdr_unavailable"}`
and the stream stays SDR (see [`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)).

Invariant: **encoder-output dims == config dims == input-coordinate range.** The
encoder (HW in-encoder, SW via libyuv `I420Scale`) absorbs any native→output
scaling; this keeps the client's absolute mouse mapping pixel-accurate.

### Shutdown Sequence

```
1. CancellationToken fired (signal handler or explicit cancel)
2. Frame loop exits (checks cancel.is_cancelled())
3. Audio loop exits (cancel.cancelled() select branch)  [audio deferred — placeholder]
4. Drop encoder (flushes pending frames in its Drop impl)
5. Drop capturer (releases DRM/EGL/subprocess on Drop)
6. Drop audio encoder + capturer add-on (no subprocess) [audio impl deferred]
7. Drop input Dispatcher (its Drop releases active KeyMouse / Touch / Gamepad
   injectors, releasing all held keys + buttons on the way out)
8. Drop clipboard Monitor (Drop stops the message pump / X event loop)
9. Drop filetransfer Service (Drop drains in-flight, fsyncs, removes orphan .part files)
10. server.start() returns (graceful HTTP shutdown with 5s timeout)
11. Print final statistics
12. Exit
```

> Cleanup is RAII: each component releases its resources in its `Drop` impl
> (there is no explicit `Close()`). Drop **order** still matters: encoder before
> capturer (encoder may reference a captured DMA-BUF), server last (clients get
> final frames + an orderly close). Enforce the order with explicit `drop(...)`
> calls (or field/declaration order) rather than relying on incidental scope exit.

---

## Capability Probing

The pipeline iterates over the add-ons the host loaded from the add-ons
directory (each a shared library `dlopen`'d at startup; see CENTRAL_SPEC
"Add-on loading model") and asks each one to probe its prerequisites. There is no
fixed `SystemCapabilities` struct — the set of probes is determined by which
add-on libraries are present in the directory.

```rust
// The dlopen loader populates the registry at startup: it scans the add-ons
// directory, loads each library, checks its abi_stable version + layout, and
// adapts its exported #[sabi_trait] object into the per-kind traits below (keyed
// off the capability descriptor's `kind`). The registry holds only add-ons whose
// library was present AND ABI-compatible. All five descriptor kinds have a home:
// capture, encode/hwencode (both EncoderAddon, distinguished by kind()),
// input, and audio (capture + the opus codec).
pub struct AddonRegistry {
    pub captures: Vec<Box<dyn CaptureAddon>>,
    pub encoders: Vec<Box<dyn EncoderAddon>>,
    pub inputs: Vec<Box<dyn InputAddon>>, // injection OVERRIDE add-ons (uinput, interception, win_touch, vigem, gcvirtual); kb/mouse default is in-core enigo
    pub audio: Vec<Box<dyn AudioAddon>>,  // audio capture add-ons + the opus codec add-on
}

pub trait CaptureAddon {
    fn name(&self) -> &str; // add-on ID (e.g. "kms_egl", "dxgi_dd")
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    fn new(&self, cfg: capture::CaptureConfig) -> Result<Box<dyn capture::Capturer>, PipelineError>;
}

pub trait EncoderAddon {
    fn name(&self) -> &str; // add-on ID (e.g. "nvenc", "openh264", "x264")
    fn kind(&self) -> &str; // "hw" or "sw"
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    fn new_sw(&self, cfg: encode::EncoderConfig) -> Result<Box<dyn encode::Encoder>, PipelineError>;          // SW add-ons only
    fn new_hw(&self, cfg: hwencode::HWEncoderConfig) -> Result<Box<dyn hwencode::HardwareEncoder>, PipelineError>; // HW add-ons only
}

pub trait InputAddon {
    fn name(&self) -> &str; // add-on ID (e.g. "uinput", "interception", "vigem")
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    fn new(&self, cfg: input::InjectorConfig) -> Result<Box<dyn input::Injector>, PipelineError>;
}

pub trait AudioAddon {
    fn name(&self) -> &str; // add-on ID (e.g. "pipewire", "wasapi", "sck_audio", "opus")
    fn kind(&self) -> &str; // "capture" or "codec"
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
}

pub struct ProbeResult {
    pub available: bool,
    pub reason: String,           // human-readable explanation if !available
    pub capabilities: Vec<String>, // e.g. ["h264", "hevc"] for an encoder
    pub details: HashMap<String, serde_json::Value>, // per-add-on probe metadata
}

impl Pipeline {
    fn probe_addons(&self) -> HashMap<String, ProbeResult> {
        let mut results = HashMap::new();
        for c in &self.registry.captures { if let Ok(r) = c.probe() { results.insert(c.name().to_string(), r); } }
        for e in &self.registry.encoders { if let Ok(r) = e.probe() { results.insert(e.name().to_string(), r); } }
        for i in &self.registry.inputs   { if let Ok(r) = i.probe() { results.insert(i.name().to_string(), r); } }
        for a in &self.registry.audio    { if let Ok(r) = a.probe() { results.insert(a.name().to_string(), r); } }
        results
    }
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

```rust
pub struct Stats {
    // Counters are atomics — no lock needed for the hot path.
    frames_captured: AtomicU64,
    frames_encoded: AtomicU64,
    frames_dropped: AtomicU64,
    frames_broadcast: AtomicU64,
    bytes_broadcast: AtomicU64,
    client_count: AtomicI32,
    audio_chunks: AtomicU64,
    audio_drops: AtomicU64,

    // RollingStats is the only field needing a lock (ring buffer).
    total_frame_time: Mutex<RollingStats>, // 60-sample sliding window
}

/// StatsSnapshot is the read-only copy returned by snapshot().
/// Contains NO Mutex (safe to copy, return by value, serialize).
#[derive(Clone)]
pub struct StatsSnapshot {
    pub frames_captured: u64,
    pub frames_encoded: u64,
    pub frames_dropped: u64,
    pub frames_broadcast: u64,
    pub bytes_broadcast: u64,
    pub client_count: i32,
    pub audio_chunks: u64,
    pub audio_drops: u64,
    pub frame_time: RollingSnapshot, // min, max, avg, p99
}

// Methods used by the frame loop (all O(1)):
impl Stats {
    pub fn record_frame(&self, d: Duration) { /* atomic increment + lock-guarded ring append */ }
    pub fn record_drop(&self) { /* atomic increment only (no lock) */ }
    pub fn snapshot(&self) -> StatsSnapshot { /* reads atomics + briefly locks for rolling stats */ }
}

/// RollingStats tracks min/max/avg/p99 over a sliding 60-sample window.
pub struct RollingStats {
    values: [Duration; 60],
    index: usize,
    count: usize,
}

impl RollingStats {
    pub fn record(&mut self, d: Duration) { /* … */ }
    pub fn avg(&self) -> Duration { /* … */ }
    pub fn min(&self) -> Duration { /* … */ }
    pub fn max(&self) -> Duration { /* … */ }
    pub fn p99(&self) -> Duration { /* … */ }
}
```

**Key improvement:** Fixed-size rolling window (60 samples) instead of an unbounded `Vec`. Memory is O(1) regardless of runtime duration. `Stats` is lock-guarded on its ring buffer because the frame loop writes and the Prometheus metrics exporter reads concurrently.

---

## Error Recovery Strategy

| Error Source | Severity | Recovery Action |
|-------------|----------|-----------------|
| Capture returns error (transient) | Warn | Log, skip frame, continue |
| Capture returns error (3 consecutive) | Error | Attempt capturer restart |
| Capture returns error (10 consecutive) | Fatal | Shutdown pipeline |
| Encoder returns error | Warn | Log, skip frame, continue |
| Encoder returns StreamError::FallbackToSoftware | Info | Switch to software encoder permanently |
| Server broadcast fails | - | Per-client: drop frame (handled internally) |
| Audio chunk channel closed | Warn | Attempt audio reconnect |
| Input device error | Warn | Log, disable input (viewers still work) |
| Clipboard Monitor error (Wayland unsupported, X display drop) | Warn | Log, disable clipboard sync, leave video unaffected |
| File-transfer write error | Warn | Send `ERROR` to client for that transfer; drop only that transfer |
| Gamepad add-on connect failure | Warn | Drop subsequent gamepad records; log once per index |
| Context cancelled | - | Graceful shutdown |

---

## Refactoring Directives

### R-PIP-01: Extract from main.rs
Move all logic from `src/main.rs` into this module. `main.rs` should only:
1. Parse `--config <path>` → `config::load(path)` → `Arc<config::Config>`
2. Call `pipeline::new(cfg)`
3. Call `pipeline.start(cancel).await`
4. Print final stats
5. Exit

### R-PIP-02: Make Stats Observable
Expose stats via the Prometheus metrics endpoint (`/metrics` on the separate
metrics port — see MODULE_CONFIG `[metrics]`). The legacy `/status` JSON endpoint
on the main port is removed; liveness is `/healthz`, detailed counters are
Prometheus. No unauthenticated observability surface on the main TLS server.

**Exported metrics catalog.** The exporter maps `StatsSnapshot` (+ a few
server/transport fields) onto these series. Names use the `featherdesk_` prefix
and Prometheus type conventions (`_total` for counters); all are process-global
unless noted.

| Metric | Type | Source | Meaning |
|--------|------|--------|---------|
| `featherdesk_frames_captured_total` | counter | `frames_captured` | frames pulled from the capturer |
| `featherdesk_frames_encoded_total` | counter | `frames_encoded` | access units produced by the encoder |
| `featherdesk_frames_dropped_total` | counter | `frames_dropped` | frames skipped at capture (pacing/overload) |
| `featherdesk_frames_broadcast_total` | counter | `frames_broadcast` | frames fanned out to ≥1 session |
| `featherdesk_bytes_broadcast_total` | counter | `bytes_broadcast` | encoded video bytes sent (pre-fragmentation) |
| `featherdesk_clients` | gauge | `client_count` | currently connected sessions |
| `featherdesk_audio_chunks_total` | counter | `audio_chunks` | PCM chunks encoded (0 while audio deferred) |
| `featherdesk_audio_drops_total` | counter | `audio_drops` | audio chunks dropped |
| `featherdesk_frame_time_seconds` | summary | `frame_time` (60-sample window) | capture→broadcast latency; exports min/max/avg/p99 |
| `featherdesk_datagram_send_drops_total` | counter | server `frame_out` overflow | per-session out-queue drop-oldest events (the fast-path congestion signal) |
| `featherdesk_rtt_seconds` | gauge | QUIC `smoothed_rtt` (+ app ping/pong) | per-client RTT (labeled `client`); also the adaptive input |
| `featherdesk_effective_bitrate_kbps` | gauge | `stream::Manager` | current adaptive target bitrate |
| `featherdesk_effective_fps` | gauge | pipeline pacing | current target fps |
| `featherdesk_keyframes_forced_total` | counter | server keyframe path | IDRs forced (join/gap), useful for storm detection |
| `featherdesk_build_info` | gauge=1 | host | version/codec/OS as labels |

Add-on selection (which capture/encoder add-on won the probe) is emitted once at
startup as a `tracing` event and as labels on `featherdesk_build_info`, not as a
time series. The exporter reads `snapshot()` (atomics + a brief ring-buffer lock)
on each scrape — scraping never blocks the frame loop.

### R-PIP-03: Hot-Reload Encoder
If hardware encoder becomes unavailable mid-stream (GPU reset, driver crash), fall back to software encoder without dropping the connection:
1. Detect encode error
2. Create software encoder with same config
3. Force keyframe on new encoder
4. Swap atomically

### R-PIP-04: Configuration Validation
Validate `config::Config` at `new()` time (in addition to MODULE_CONFIG validation):
- `stream.fps` must be 1-240
- `stream.qp` must be 0-51 (H.264 range)
- `stream.bitrate_bps` must be 0 (QP mode) or >= 100000 (100kbps minimum)
- At least one capture add-on loaded
- At least one encoder add-on loaded

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
