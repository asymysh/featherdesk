# Module Spec: Pipeline (Orchestrator)

## Overview

The Pipeline module is the runtime wiring layer that connects all other modules into a functioning streaming system. It handles lifecycle management, capability probing, backend selection, frame pacing, frame drop decisions, and graceful shutdown. This is the "main loop" extracted into a testable, configurable struct.

---

## Public Interface

```rust
// module: featherdesk-host::pipeline  (NOT a separate crate — the orchestrator
// lives in the host binary; see CENTRAL_SPEC "File Structure")

/// Pipeline is what `new()` builds and `start()` consumes. It is a BUILDER for
/// three independently-owned runtime halves, not a shared runtime object: no
/// field is ever borrowed by two of them at once, and `start` takes `self` by
/// value so the halves are moved apart rather than aliased.
///
/// This is what makes M-6 (encode and reconfigure never alias) structural. The
/// frame loop OWNS the capture handle, the converter and the encoder; no handle
/// to any of them exists anywhere else in the process. There is no discipline to
/// follow and no lock to forget — a second mutator does not typecheck.
pub struct Pipeline {
    cfg: std::sync::Arc<config::Config>,        // parsed TOML config (read-only)
    cfg_path: std::path::PathBuf,               // for config::watch (SIGHUP; see R-PIP-01)
    cfg_tx: tokio::sync::watch::Sender<std::sync::Arc<config::Config>>,

    frame: FrameLoop,                           // moved onto the frame thread by start()
    audio: Option<AudioLoop>,                   // moved onto the audio thread by start()
    clipboard: Option<ClipboardTask>,           // moved onto a tokio task by start()

    server: std::sync::Arc<dyn server::Server>, // shared: &self only (Send + Sync)
    input: Option<std::sync::Arc<std::sync::Mutex<Box<dyn input::Dispatcher>>>>,
    // `Arc<Mutex<..>>`: `dispatch` takes `&mut self` and is invoked from N
    // session tasks through a `Fn + Send + Sync` callback, and the frame loop
    // calls `resize` on a dimension change. Serializing is not a workaround, it
    // is required — the pressed-set that `release_all` depends on is shared state
    // that two concurrent injections would corrupt. The guard covers exactly one
    // record and is never held across an await (`dispatch` is synchronous).
    clipboard_handle: Option<clipboard::ClipboardHandle>, // concrete Clone + Send + Sync handle
    files: Option<std::sync::Arc<dyn filetransfer::Service>>,
    // `Arc<dyn …>`: `serve_stream` takes `&self` and runs on N concurrent stream
    // tasks, while the pipeline keeps a reference to Drop it at shutdown.
    // The parameter funnel. `stream::Manager` is the SOLE producer; the frame
    // loop is the sole consumer, and holds the receiver. Bound 32: at the 100 ms
    // adaptive cadence plus human-rate client requests this is never approached,
    // so a full funnel means the frame loop is wedged and is reported as such
    // (`featherdesk_param_updates_dropped_total`) rather than silently dropped.
    // `try_send` only — a blocking send from a control-stream reader would put a
    // session task behind an encode. The frame loop's published RESULT travels
    // back on `FrameLoop.applied_tx`, a `tokio::sync::watch` whose receiver
    // `new()` clones into the config provider, `set_params_watch` and the
    // Manager: only the latest matters and `borrow()` never blocks the writer.
    param_tx: tokio::sync::mpsc::Sender<stream::ParamUpdate>, // capacity 32
    manager: std::sync::Arc<std::sync::Mutex<Box<dyn stream::Manager>>>,
    // The SAME `Arc<Mutex<..>>` step 12 hands to `set_stream_params_callback`; the
    // pipeline retains a clone so the `fd-config` task can reach `Manager::apply`
    // (the `[stream]` rows) and `Manager::set_policy` (the `[stream.adaptive]` row).
    // Type-erasing the only reference into the callback closure is what made both
    // rows uncallable. `Manager: Send`, so `Arc<Mutex<Box<dyn Manager>>>` is
    // `Send + Sync` — the same bound the callback already requires.
    log_reload: config::LogReload,   // the `[log]` applier, handed in by `main.rs`
    // (R-PIP-01 step 4) through `new()` and moved into the `fd-config` task at
    // step 12b. Without it the `[log]` row has no route from the watch channel.
    stats: std::sync::Arc<Stats>,

    // The add-on registry owns every dlopen'd library. It is the LAST field of
    // every struct that holds an object built from one, so on any drop path the
    // libraries outlive the objects whose vtables point into them.
    registry: std::sync::Arc<AddonRegistry>,
    // Logging is via the `tracing` crate (replaces the old *slog.Logger field).
    // The transport is NOT a field: it is a local of `new()`, moved into
    // `server::Config` at step 7, and has exactly one owner from there on.
}

/// FrameLoop is the frame thread's whole world. Fields are declared in DROP
/// ORDER (Rust drops struct fields in declaration order), and that order is the
/// normative teardown order — see "Shutdown Sequence".
///
/// The media objects are `Option` because they are NOT built by `new()`: they
/// are built by `FrameLoop::open()` as the first statement executed on the frame
/// thread, so a thread-affine EGL/D3D/COM context is created, used and dropped
/// on ONE thread (see "Thread and handle ownership"). `open()` fills them;
/// `run()` unwraps them; the Level-2 fall-through swaps them; the epilogue drops
/// them.
///
/// Auto traits are computed from the field TYPES, not from what the fields hold
/// at spawn time, so the Layer-2 add-on traits are declared `Send` even though
/// no object built from them ever crosses a thread: `Pipeline::start` moves this
/// EMPTY struct onto the frame thread, and `Option<Box<dyn CaptureHandle>>` being
/// `None` at that moment does not help. Same for `AudioLoop`.
pub struct FrameLoop {
    // ── built on THIS thread by open(); dropped on this thread, in this order ──
    encoder: Option<Box<dyn encode::EncoderHandle>>,          // None on the hardware path
    hw_encoder: Option<Box<dyn hwencode::HwEncoderHandle>>,   // None on the software path
    converter: Option<encode::Converter>,                     // rotate + BGRA/RGBA→YUV + scale to
                                                              //   output dims; None on the HW path
    // ── Idle suspension (GAP_TRIAGE OQ-04) ───────────────────────────────────
    wake: Arc<tokio::sync::Notify>,      // signalled by set_session_count_callback on 0→N
    authed_clients: Arc<AtomicU32>,      // AUTHENTICATED sessions; never `client_count()`
    idle_since: Option<Instant>,         // Some while parked; drives idle_release_after
    released: bool,                      // true when Stage 2 dropped capture+encoder
    cursor: Option<CursorPublisher>,                          // Some ONLY when `FrameLoop::open()`
                                                              //   resolved cursorMode="separate". Owns
                                                              //   no OS handle; the CursorCapturer
                                                              //   itself is reached through `capture`
                                                              //   (`as_cursor`), never held twice.
    capture: Option<Box<dyn capture::CaptureHandle>>,         // THE capture object — exactly one owner

    // ── selection state carried across a swap ──────────────────────────────
    plan: SelectionPlan,                                // which add-on ids won the probe, in probe order
    poisoned: std::collections::HashSet<String>,        // add-on ids dropped for the session
    capture_ladder: ErrorLadder,                        // consecutive-error counters
    encode_ladder: ErrorLadder,
    pending_terminal: Option<PipelineError>,            // set by an infallible call site that hit a
                                                        //   terminal condition; taken at the top of
                                                        //   the next iteration

    // ── plain frame-loop state: read and written on this thread only ───────
    params: stream::Params,     // AUTHORITATIVE effective params
    param_epoch: u64,           // last applied ParamUpdate.epoch
    fps_ceiling: u32,           // the rate the operator or the client asked for: [stream] fps at
                                //   startup, then whatever the last set_fps/Manager change carried.
                                //   Sustainable-rate control may lower `params.fps` below it, never above.
    cursor_mode: CursorMode,    // Separate | Embedded — resolved in `FrameLoop::open()`,
                                //   re-resolvable on a swap or SIGHUP
    capture_dims: (u32, u32),   // last observed UPRIGHT capture geometry — (width, height) as
                                //   delivered, transposed for R90/R270 per MODULE_CAPTURE "Rotation".
                                //   Seeded at startup step 6 by `open()` from the capturer's
                                //   reported resolution; the ONLY comparand for detecting a
                                //   capture-side resolution or rotation change thereafter. NOT
                                //   `params`, which is the STREAM geometry after downscale and would
                                //   compare equal across a capture change whenever a downscale absorbs it.
    last_applied_ok: bool,      // the `ok` of the most recent `Applied`. Republishers that are
                                //   not themselves a param outcome (the restart rung) carry this
                                //   forward rather than asserting success, because `applied_tx` is
                                //   a `watch` and an unpolled `ok: false` would be erased.
                                //   Written in exactly two places — `apply_params`'s success send
                                //   (`= true`) and `report_param_failure` (`= false`) — and
                                //   initialised `true` at step 6, matching `open()`'s first
                                //   `Applied`. `applied_now` NEVER writes it, so a republisher
                                //   cannot launder a `true` in.
    idr_pending: bool,          // latched keyframe request
    idr_pending_since: Option<Instant>, // when it was latched, for the idle-screen keepalive
    idle_keyframe_ms: u32,      // `[stream] idle_keyframe_ms` — the idle-screen
                                // keepalive deadline. Seeded in `FrameLoop::open()` from
                                // `cfg_rx` and refreshed at step (0d) on every reload. It is
                                // a frame-loop TIMER, not a `stream::Params` field, so it
                                // never travels as a `ParamDelta` and never goes through the
                                // Manager.
    last_codec: String,         // last codec string advertised, to detect a codec change
    last_capture: Option<capture::Frame>, // most recent CPU frame, for the idle-screen keepalive
                                          //   (software path only — a surface is released in the
                                          //   iteration that produced it)

    // ── inbound edges ──────────────────────────────────────────────────────
    param_rx: tokio::sync::mpsc::Receiver<stream::ParamUpdate>,
    kf_req: std::sync::Arc<std::sync::atomic::AtomicBool>,
    cfg_rx: tokio::sync::watch::Receiver<std::sync::Arc<config::Config>>,
    audio_desc_rx: tokio::sync::watch::Receiver<Option<AudioDescriptor>>,
                                // Set by the audio thread; read at step (0d) with `has_changed()` +
                                // `borrow_and_update()`, and by `current_config()`. `None` ⇒ the
                                // config advertises `audio: false` with the five audio fields blank.
                                // Replaces `audio_live: Arc<AtomicBool>` and `audio_live_seen: bool`.

    // ── outbound edges (all Send + Sync handles, never exclusive borrows) ───
    applied_tx: tokio::sync::watch::Sender<stream::Applied>,
    server: std::sync::Arc<dyn server::Server>,
    input: Option<std::sync::Arc<std::sync::Mutex<Box<dyn input::Dispatcher>>>>,
    stats: std::sync::Arc<Stats>,

    registry: std::sync::Arc<AddonRegistry>,            // LAST: libraries outlive their objects
}

/// Everything the frame loop can know about the live audio path, and the ONLY
/// thing the audio thread publishes. `Some` from the moment `AudioLoop::open()`
/// succeeds until the audio thread leaves for any reason; `None` before that and
/// after that. `None` IS "audio retired" — there is no separate liveness flag.
///
/// It exists because CUR-24 moved the audio capturer and encoder behind `&mut`
/// on the audio thread: `AudioEncoder::codec()`, `AudioEncoder::description()`
/// and `AudioCapturer::format()` are the only producers of five mandatory
/// `protocol::ConfigBase` fields, and the frame loop — which builds every
/// `config` — can no longer reach them.
#[derive(Clone, PartialEq, Eq)]
pub struct AudioDescriptor {
    codec: String,                // audio::AudioEncoder::codec()
    sample_rate: u32,             // audio::AudioCapturer::format().sample_rate
    channels: u8,                 // …format().channels
    layout: audio::ChannelLayout, // …format().layout
    description: Vec<u8>,         // audio::AudioEncoder::description(), RAW bytes;
                                  //   current_config() base64s it into
                                  //   config.audioDescription
}

/// AudioPlan is startup step 11's verdict, carried onto the audio thread so
/// `AudioLoop::open()` can construct without re-running selection. It names
/// add-ons; it owns no add-on object.
pub struct AudioPlan {
    capturer_id: String,        // the selected audio capture add-on id
    codec_id: Option<String>,   // the selected audio codec add-on id;
                                //   None = PCM passthrough (no codec add-on loaded)
    cfg: audio::AudioConfig,    // the [audio] section, forwarded verbatim
}

/// AudioLoop is the audio thread's whole world, on the same rules. It is a
/// dedicated OS thread rather than a tokio task because the add-on's capture
/// backend is thread-affine on two of three platforms (WASAPI needs COM on the
/// calling thread, CoreAudio prefers a stable thread) and because a blocking
/// `next_chunk()` must not occupy a Tokio worker.
pub struct AudioLoop {
    encoder: Option<Box<dyn audio::AudioEncoder>>,  // built by open(), on this thread
    capturer: Option<Box<dyn audio::AudioCapturer>>,
    plan: AudioPlan,
    ladder: ErrorLadder,   // the audio thread's consecutive-error counter, on the
                           // SAME rules as capture_ladder / encode_ladder
    audio_desc_tx: tokio::sync::watch::Sender<Option<AudioDescriptor>>,
                           // The audio thread's ONE outbound edge. `send(Some(d))` immediately
                           // after open() succeeds; `send(None)` on EVERY exit path, including
                           // the early return when open() fails. Replaces
                           // `audio_live: Arc<AtomicBool>`.
    server: std::sync::Arc<dyn server::Server>,
    stats: std::sync::Arc<Stats>,
    registry: std::sync::Arc<AddonRegistry>,        // LAST
}

/// ClipboardTask drives the clipboard monitor. It holds the DRIVER half of the
/// clipboard split; the HANDLE half lives on the Pipeline and in the server's
/// callback, so the session-long driver and a per-paste `set` never contend for
/// one `&mut`.
pub struct ClipboardTask {
    driver: Box<dyn clipboard::Monitor>,
    changes: tokio::sync::watch::Receiver<Option<clipboard::Content>>,
    server: std::sync::Arc<dyn server::Server>,
}

/// SelectionPlan is the probe's verdict, carried into the frame thread so a
/// mid-session swap walks the SAME order startup used without re-running the
/// whole selection. `active_id` names the add-on currently in use for a
/// component; `next_candidate` yields the next unpoisoned id in probe order.
pub struct SelectionPlan {
    capture_order: Vec<String>,   // step 3d's order for this OS, minus add-ons that probed unavailable
    encode_order: Vec<String>,    // step 3e's order for this OS, HW entries then SW entries
    active_capture: String,
    active_encode: String,
}

impl SelectionPlan {
    /// The add-on id currently in use for `who`. Never empty: startup step 3f
    /// fails the process if no candidate was available for a component.
    fn active_id(&self, who: Component) -> &str;

    /// The next id after `active_id(who)` in `who`'s probe order, skipping every
    /// id in `poisoned`, or `None` when the order is exhausted (the caller then
    /// raises `PipelineError::AddonsExhausted`). On `Some(id)` it has already
    /// advanced that component's `active_*` field to `id`, so a second call
    /// yields the one after.
    fn next_candidate(&mut self, who: Component, poisoned: &std::collections::HashSet<String>) -> Option<String>;
}

/// Creates a Pipeline from a parsed TOML configuration. Loads and probes
/// add-ons, decides the selection plan, builds the transport + server, and wires
/// every callback. It does NOT construct any capture, encode or audio object —
/// those are built on the thread that will use them — and it starts nothing.
/// The TOML config struct is defined in
/// [`MODULE_CONFIG.md`](./MODULE_CONFIG.md) — pipeline does not own
/// configuration parsing.
pub fn new(
    cfg: std::sync::Arc<config::Config>,
    cfg_path: std::path::PathBuf,
    log_reload: config::LogReload,
) -> Result<Pipeline, PipelineError> { /* … */ }

impl Pipeline {
    /// A cloneable handle to the live counters. Take it BEFORE `start` consumes
    /// the Pipeline; it stays valid for the process lifetime, and `snapshot()`
    /// never blocks the frame loop (atomics plus one brief ring-buffer lock).
    pub fn stats_handle(&self) -> std::sync::Arc<Stats> { self.stats.clone() }

    /// Starts the frame thread, the audio thread, the clipboard task and the
    /// server, and awaits graceful shutdown. Takes `self` BY VALUE: the three
    /// runtime halves are moved apart here, which is why there is never more
    /// than one exclusive borrow of any of them.
    pub async fn start(self, cancel: CancellationToken) -> Result<(), PipelineError> {
        let Pipeline {
            cfg, cfg_path, cfg_tx, frame, audio, clipboard,
            server, input, clipboard_handle, files, param_tx, manager, log_reload,
            stats, registry,
        } = self;

        // SIGHUP producer → the one watch every applier reads. `config::watch`
        // re-parses and revalidates the WHOLE file and calls back only on
        // success, so a partially-valid file never reaches an applier. The
        // callback must not block: publishing on a `watch` is its only job, and
        // a `watch` is depth-1 drop-oldest by construction, which is exactly the
        // semantics a config reload wants.
        // The `fd-config` task's subscription is taken BEFORE `cfg_tx` is moved
        // into the callback below — that is the only ordering in which a second
        // receiver can exist at all.
        let cfg_rx = cfg_tx.subscribe();
        config::watch(cancel.clone(), &cfg_path, move |c| {
            let _ = cfg_tx.send(std::sync::Arc::new(c.clone()));
        })?;

        // Step 12b. There are exactly TWO receiving halves of this watch: this
        // task and the frame loop's step-(0d) applier. No third exists.
        let config_task = tokio::spawn(config_applier(
            cancel.clone(),
            cfg_rx,
            server.clone(),
            manager,
            clipboard_handle.clone(),
            files.clone(),
            log_reload,
        ));

        // EXCLUSIVE borrow #1, moved not borrowed: the frame thread owns its data.
        let frame_thread = std::thread::Builder::new()
            .name("fd-frame".into())
            .spawn({ let cancel = cancel.clone(); move || frame.run(cancel) })
            .map_err(|e| PipelineError::Config(format!("spawn frame thread: {e}")))?;

        // EXCLUSIVE borrow #2, likewise moved.
        let audio_thread = match audio {
            None => None,
            Some(a) => Some(std::thread::Builder::new()
                .name("fd-audio".into())
                .spawn({ let cancel = cancel.clone(); move || a.run(cancel) })
                .map_err(|e| PipelineError::Config(format!("spawn audio thread: {e}")))?),
        };

        let clipboard_task = clipboard.map(|c| tokio::spawn(c.run(cancel.clone())));

        // SHARED borrow: `Server::start` takes `&self` and the Server is behind
        // an Arc, so this coexists with the two exclusive borrows above by
        // construction. Returns when `cancel` fires, after notifying every
        // session and closing it with close::SERVER_SHUTDOWN (4503).
        let server_res = server.start(cancel.clone()).await;

        // ── Shutdown. Order is normative; see "Shutdown Sequence". ──────────
        cancel.cancel(); // idempotent; also covers a frame-loop terminal error
        let frame_res = join_worker("frame", frame_thread).await;
        let audio_res = match audio_thread { Some(h) => join_worker("audio", h).await, None => Ok(()) };
        if let Some(t) = clipboard_task { let _ = t.await; }
        let _ = config_task.await;   // the fd-config applier; `cancel` already fired
        drop(input);             // refcount decrement only: the server still holds clones
                                 // Dispatcher::Drop (and release_all) runs at drop(server) below
        drop(clipboard_handle);
        drop(files);             // Service::Drop drains in-flight, fsyncs, removes .part
        drop(param_tx);
        drop(server);            // last Arc<dyn Server> → drops server::Config.transport
        drop(cfg);
        drop(registry);          // dlopen'd libraries last, unconditionally
        log_shutdown_summary(&stats);

        frame_res.and(audio_res).and(server_res.map_err(PipelineError::from))
    }
}

/// Waits for a worker thread with the graceful-shutdown budget (2 s, the
/// "Performance Targets" figure). `JoinHandle::join` blocks, so it runs on a
/// blocking pool thread rather than on a Tokio worker. A panicked worker is
/// reported, never swallowed.
async fn join_worker(
    name: &'static str,
    h: std::thread::JoinHandle<Result<(), PipelineError>>,
) -> Result<(), PipelineError> {
    match tokio::time::timeout(
        std::time::Duration::from_secs(2),
        tokio::task::spawn_blocking(move || h.join()),
    ).await {
        Ok(Ok(Ok(r))) => r,
        Ok(Ok(Err(_))) => { tracing::error!(target: "pipeline", worker = name, "worker thread panicked"); Err(PipelineError::WorkerPanic(name)) }
        Ok(Err(e)) => Err(PipelineError::Config(format!("join {name}: {e}"))),
        Err(_) => { tracing::error!(target: "pipeline", worker = name, "worker did not exit within 2s"); Err(PipelineError::ShutdownTimeout(name)) }
    }
}

/// Emits the single structured shutdown line R-PIP-05 "Structured Shutdown
/// Logging" specifies, from the final counter values. Infallible, never panics,
/// and takes `&Stats` rather than `Arc<Stats>` so it cannot extend a lifetime
/// past the drops above it. The last statement of `Pipeline::start`.
fn log_shutdown_summary(stats: &Stats);

/// Maps the audio encoder's `codec()` string onto the wire audio frame type byte
/// (`frame_type::` values are MODULE_PROTOCOL's): `"opus"` => `0x08`; anything
/// else, `"pcm/s16le"` included, => `0x04`. Called once per `AudioLoop::run`,
/// after `open()`.
fn audio_codec_type(codec: &str) -> u8;

/// The `fd-config` task: the ONE receiving half of the SIGHUP watch outside the
/// frame loop. Spawned by `start()` at step 12b, immediately after `config::watch`
/// is registered, and cancelled with `cancel`. It holds a subscription taken from
/// `cfg_tx` BEFORE the sender is moved into the watch callback, plus one cheap
/// handle per applier — every one of them already a field of `Pipeline`.
///
/// On each `changed()` it runs the ✅ rows of MODULE_CONFIG "Hot reload behavior"
/// in table order, diffing the new `Arc<Config>` against `prev` (the config it last
/// applied) so an unchanged section costs nothing. An applier error is LOGGED at
/// `error`, never propagated: a bad certificate file or a rejected Authenticator
/// leaves the previous value in force and must not take the process down.
///
/// Order (normative): `[server.tls]` → `[auth]`, as
/// `Server::set_auth_policy(auth::reload(&cfg.auth)?, cfg.auth.allow_takeover)` →
/// `[server]`/`[transport]`/`[clipboard]` gating via one `set_session_defaults` →
/// `ClipboardHandle::set_config(cfg)`, where `cfg` is a `clipboard::Config` this task
/// builds from the reloaded `[clipboard]` `direction` / `formats` / `max_bytes` (it
/// takes `clipboard::Config`, not a config section). `enabled` is restart-required:
/// this task holds the startup `clipboard::Config` as a baseline, clones it and
/// overwrites only those three fields, so `enabled` is carried through unchanged
/// rather than re-read — there is no reload path that can flip it
/// → `Service::set_limits` → `Manager::set_policy` → the
/// `[stream]` diff pushed as `ParamDelta`s through `Manager::apply` → `LogReload`.
async fn config_applier(
    cancel: CancellationToken,
    mut cfg_rx: tokio::sync::watch::Receiver<std::sync::Arc<config::Config>>,
    server: std::sync::Arc<dyn server::Server>,
    manager: std::sync::Arc<std::sync::Mutex<Box<dyn stream::Manager>>>,
    clipboard_handle: Option<clipboard::ClipboardHandle>,
    files: Option<std::sync::Arc<dyn filetransfer::Service>>,
    log_reload: config::LogReload,
);

/// One thiserror-derived error enum for the crate.
#[derive(Debug, thiserror::Error)]
pub enum PipelineError {
    #[error("config: {0}")] Config(String),
    #[error("no capture add-on loaded")] NoCapture,
    #[error("no encoder add-on loaded")] NoEncoder,
    /// Every candidate for `component` ("capture" | "encode") is poisoned. This
    /// is the Level-2 terminal case — the frame loop returns it, fires `cancel`,
    /// and `start` propagates it as the process exit status.
    #[error("all {component} add-ons exhausted; last: {reason}")]
    AddonsExhausted { component: &'static str, reason: String },
    #[error("{0} worker thread panicked")] WorkerPanic(&'static str),
    #[error("{0} worker did not exit within the 2s shutdown budget")] ShutdownTimeout(&'static str),
    // ── the add-on load domain, mapped from AbiErr (MODULE_ABI) ────────────
    #[error("addon {id}: {detail}")]
    AddonBackend { id: String, detail: String },                 // AbiErr::Generic (1)
    #[error("addon {id}: unrecoverable: {detail}")]
    AddonUnrecoverable { id: String, detail: String },           // AbiErr::Unrecoverable (7)
    #[error("addon {id}: bad [addon_module_{id}] config: {detail}")]
    AddonConfig { id: String, detail: String },                  // AbiErr::BadConfig (10)
    #[error(transparent)] Stream(#[from] stream::StreamError),
    #[error(transparent)] Server(#[from] server::ServerError),
}
```

The pipeline takes `Arc<config::Config>` directly (the parsed TOML struct from
[`MODULE_CONFIG.md`](./MODULE_CONFIG.md)). There is no separate `PipelineConfig`
struct. The pipeline reads `[capture]`, `[encode]`, `[stream]`,
`[stream.adaptive]` sections plus per-add-on `[addon_module_<id>]` sections —
which it forwards verbatim rather than decoding (step 3b).

### Add-on handles

Each constructed add-on object has exactly ONE owner: a handle built by the
Layer-2 adapter. The handle owns the sabi object, caches the authoritative
`caps()` read back from it, and exposes every optional capability as a
**borrow**. A borrow cannot be a second owner, so the aliasing that separate
`Box<dyn SurfaceCapturer>` / `Box<dyn CursorCapturer>` fields would require is
not representable: no constructor can produce two owners of one `#[sabi_trait]`
object, and dropping either one would free the display/duplication handle the
frame path is still using.

| Handle | Declared in | Owns | Optional capabilities, by bit |
|---|---|---|---|
| `capture::CaptureHandle` (`: Capturer`) | [`../media/MODULE_CAPTURE.md`](../media/MODULE_CAPTURE.md) | one `abi::CapturerBox` | `as_surface()` iff `SURFACE`; `as_cursor()` iff `CURSOR`; `as_configurable()` iff `CONFIGURABLE`; `clear_cap(bit)` retires any of them |
| `encode::EncoderHandle` (`: Encoder`) | [`../media/MODULE_ENCODE.md`](../media/MODULE_ENCODE.md) | one `abi::EncoderBox` | `as_configurable()` iff `ENC_CONFIGURABLE` |
| `hwencode::HwEncoderHandle` (`: HardwareEncoder`) | [`../media/MODULE_HARDWARE_ENCODE.md`](../media/MODULE_HARDWARE_ENCODE.md) | one `abi::HwEncoderBox` | `as_configurable()` iff `ENC_CONFIGURABLE` |

Every handle serves the same capability contract — `caps()`, `clear_cap(bit)`,
and one `as_*()` borrow per optional bit: `as_configurable()` on both encoder
handles, and `as_surface()` / `as_cursor()` / `as_configurable()` on the capture
handle.

```rust
    /// Authoritative capability set: read from the CONSTRUCTED object, masked to
    /// the probe's claim. `probe()` is config-blind — `dxgi_dd` only learns it
    /// lost SURFACE when construct() falls back to a WARP adapter — so caps()
    /// may be a strict subset of the probe's and is never a superset. The
    /// adapter masks any extra bit and logs that masking once at `warn`. It
    /// never widens after construction.
    fn caps(&self) -> abi::AddonCaps;

    /// Clears one bit for the rest of the session: the matching accessor returns
    /// None from here on. This is how the host disables a capability — on a
    /// capability lie (`StreamError::Unsupported`), on `degrade_to_software`
    /// (clears SURFACE), and on a `next_cursor` failure (clears CURSOR). It NEVER
    /// drops the object: on X11 and DXGI the cursor query and the surface path
    /// borrow the SAME display/duplication handle the frame path is still using.
    fn clear_cap(&mut self, bit: u32);

    /// `Some` exactly while the matching bit is set. The borrow ends at the end
    /// of the statement, so it can never outlive or alias the owning handle.
    /// The capture handle adds two more of these — `as_surface()` and
    /// `as_cursor()` — on the same rule.
    fn as_configurable(&mut self) -> Option<&mut dyn …>;
```

`bit` is a `u32` because every flag is declared `pub const SURFACE: u32 = 1 << 0;`
… in `impl AddonCaps` and the sibling accessor is already
`pub fn has(self, bit: u32) -> bool` (MODULE_ABI). There is no
`disable_cursor()`: cursor polling is turned off with the same call every other
bit uses, `clear_cap(abi::AddonCaps::CURSOR)`.

Input add-ons need no handle type: each of the three injector traits is served by
a **different** add-on object which the `Dispatcher` owns outright, so there is no
aliasing to prevent.

---
## Internal Architecture

### Startup Sequence

```
0. (Windows only) Establish process DPI awareness BEFORE any other call.
   PER_MONITOR_AWARE_V2 is process-global and may be set only once, and both the
   dxgi_dd capture add-on (physical surface dimensions) and the win_touch input
   add-on (physical injection coordinates) depend on it. Two declarations, both
   shipped:
     a. The application manifest embedded in featherdesk.exe carries
        <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
        — the robust route, effective before any code runs.
     b. main() additionally calls
        SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)
        as the first statement, for hosts started from a build that lost the
        manifest. It returns ERROR_ACCESS_DENIED when (a) already applied — that
        error is EXPECTED and ignored. Any other failure is logged at error and
        startup continues (capture will then report the mismatch at step 3d).
   Non-Windows: no-op.
1. Caller (src/main.rs) loads TOML via config::load(--config path)
   — phase-A validation only: syntax + intra-section rules. [addon_module_*]
   sections are captured raw (undecoded); force_addon is checked non-empty when
   mode="forced" but NOT yet checked against the loaded set.
2. Caller initializes `tracing-subscriber` per [log] section (text in TTY, JSON otherwise)
3. pipeline::new(cfg, cfg_path, log_reload) builds the Pipeline:
   a. **Load add-ons**: scan [addons] dir, dlopen/LoadLibraryW each
      featherdesk-addon-*.{so,dylib,dll}, call `init(HostServices)`, check
      ABIVersion, read the capability descriptor, and register it by kind
      (Captures/Encoders/Inputs/Audio) — the three `Input*` descriptor kinds all
      land in `inputs`, distinguished later by `InputAddon::kind()`, exactly as
      the two audio kinds are by `AudioAddon::kind()`.
   b. **Phase-B (load-aware) config validation**: force_addon must name a LOADED
      add-on ID (else startup error); each [addon_module_<id>] table whose add-on
      is loaded is re-serialized for that add-on's `construct()`
      (`RAddonConfig.section_toml`); the host does not decode it — the schema
      lives inside the cdylib (MODULE_ABI "Add-on configuration"). A table whose
      add-on is not loaded is dropped.
   c. Read each probe's `ProbeResult.caps` (`MODULE_ABI` "Optional-method
      capability flags") — this is the ONLY way the host learns whether a
      `CapturerBox` also serves `next_surface` / `next_cursor` /
      `update_stream_params`, since a sabi object cannot be downcast. The adapter
      copies `ProbeReport.caps` through verbatim. These are SELECTION caps; the
      authoritative set is re-read from the constructed object at step 4 and may
      be a strict subset.
   d. Probe each loaded capture add-on (one shared library per add-on):
      - Linux:   nvfbc → kms_egl → wl_screencopy → pw_portal
      - macOS:   sck
      - Windows: dxgi_dd (with optional IddCx VDD auto-install if no display)
      - Cursor eligibility: for every candidate the pipeline evaluates
        `CursorMode::resolve(&cfg.capture.cursor_mode, probe_result.caps)`
        against the cached `ProbeResult.caps`; a candidate for which it returns
        `None` is INELIGIBLE and is skipped here, exactly like one whose probe
        reported `available = false` (see MODULE_CAPTURE "Cursor delivery").
        This runs BEFORE selection so a capturer that can deliver no cursor is
        never chosen. If no candidate remains, startup fails naming each add-on
        it rejected and why — never a running stream with no visible pointer.
      - **Display provisioning (last resort).** If every capture candidate
        reported `available: false` **for want of a display** — and only for
        that reason, never for a missing capability or a cursor-mode
        ineligibility — and `[capture] provision_display` is `auto` (or
        `force`, which comes straight here), the pipeline provisions one and
        re-runs this probe step exactly once: IddCx on Windows, a spawned
        headless wlroots compositor captured by `wl_screencopy` on Linux
        (`PLATFORM_COMPAT.md` "Virtual display provisioning"). A spawned
        compositor is a CHILD PROCESS the pipeline owns: its handle sits beside
        the other media objects in drop order and is terminated in the shutdown
        sequence, so it cannot outlive the server. A second failure after
        provisioning is fatal, reporting the original rejection list plus the
        provisioning attempt.
        The mode resolved for the SELECTED candidate is RECORDED on the
        `SelectionPlan`; nothing is constructed here.
   e. Probe each loaded encoder add-on, in the order for THIS OS (an add-on for
      another OS cannot be loaded, so a single global order would list entries
      that can never fire):
      - Linux   HW: nvenc → amf_rocm → libva      SW: openh264 → (x264 by force_addon)
      - Windows HW: nvenc → amf → qsv → mf_hw     SW: openh264 → (x264 by force_addon)
      - macOS   HW: vt_hw                         SW: openh264 → vt_sw → (x264 by force_addon)
      Vendor-specific SDKs precede generic abstractions (nvenc/amf before libva
      on Linux; nvenc/amf/qsv before mf_hw on Windows). `x264` is NOT in the auto
      order — it is reachable only via [encode] force_addon = "x264" (see
      MODULE_ENCODE "Software encoder order").
   f. Honor [capture] force_addon / [encode] force_addon overrides:
      - If forced and add-on not loaded: startup error
      - If forced and probe fails: startup error
      - If auto: pick first available per the order above
      - If the chosen path is HW, require at least one loaded SW encoder add-on
        as a fallback; if none is loaded, fail fast ("HW encoder X has no SW
        fallback add-on loaded") rather than risk an unrecoverable mid-session
        StreamError::FallbackToSoftware.
3g. Record, for each selected add-on, whether its `ProbeResult.caps` sets
    `CONFIGURABLE` / `ENC_CONFIGURABLE`. The `StreamParamsCapability` itself is
    read by `FrameLoop::open()` at step 13, through the constructed handle's
    `as_configurable()` accessor, and reaches the Manager on the first `Applied`
    (see `stream::Applied.caps`) — the Manager does not exist until step 12, nine
    steps after this one. It is the source of two things the Manager cannot
    otherwise know:
      - **hot vs restart, per field** — `hot_changeable["bitrate_bps"] == true`
        means `apply` routes it to `update_stream_params`; `false` means the
        pipeline tears down + rebuilds that add-on. The `AddonCaps` bit only says
        "configurable at all"; this says *which fields*.
      - **clamp bounds** — `min_values`/`max_values` are what `Manager::apply`
        clamps client `resize`/`set_bitrate`/`set_fps` requests against, so an
        out-of-range request is silently clamped rather than failing the encoder.
    An add-on that does not implement it is treated as fully immutable: every
    parameter change is a restart, and requests are clamped to the `[stream]`
    config bounds only. `StreamParamsCapability` carries `HashMap`/`serde_json`
    and is therefore **Layer-2 only** — it is built host-side by the adapter from
    the add-on's boundary-safe reply, never passed across `dlopen` as-is (same
    split as `ProbeReport` → `ProbeResult`, see MODULE_ABI).
4. Match capture surface format to encoder input:
   - HW encoder + a capturer whose caps include `SURFACE`, with a compatible
     `FbInfo` → zero-copy path
   - SW encoder + any capturer → CPU readback + YUV conversion path
   - Decide the surface/encoder pairing from the cached `ProbeResult.caps` and
     record it on the `SelectionPlan`. The capture object is CONSTRUCTED by
     `FrameLoop::open()` at step 13, which then re-reads `handle.caps()`
     (authoritative — a WARP fallback drops `SURFACE`), masks it to the probe's
     claim, logs that masking once at `warn`, and fixes the zero-copy decision on
     the SAME object `degrade_to_software` will later fall back to — there is no
     second construct and no re-probe.
   - When the mode step 3d resolved is "embedded", the zero-copy pairing also
     requires caps & EMBED_CURSOR_SURF; an add-on that can only embed into a CPU
     frame is paired with the software path.
5. If hardware path errors with StreamError::FallbackToSoftware mid-session: degrade
   to software path permanently for the rest of the session (builds the
   Converter + SW encoder via `build_software_path`; the capture handle is
   untouched) — and resets `hdr`/`bit_depth`/`color_space`/`chroma_subsampling`
   to what the SW path can actually carry (see `degrade_to_software`).
6. Record that the stream dims are derived from the capturer's actual resolution
   (NOT hardcoded) and are **upright**: `FrameLoop::open()` transposes to
   `(height, width)` for a capturer reporting `Rotation::R90`/`R270` (see
   MODULE_CAPTURE "Display rotation") using `abi::DisplayInfo.rotation` from the
   selected output's probe entry — the only pre-first-frame source of either
   dimension or rotation, which is why it is a required `DisplayInfo` field. The
   same upright pair SEEDS `FrameLoop.capture_dims`, which is the comparand the
   frame path uses from then on — this is the "startup step 6, and `on_capture_dims_changed` thereafter"
   split MODULE_CAPTURE "Rotation" names. Without the seed the field would start
   at `(0, 0)` and the first frame would fire a spurious dimension change.
6a. Record the `[capture] cursor_mode` policy and the mode step 3d resolved for
    the selected candidate on the `SelectionPlan`. RECORD ONLY: this step
    constructs nothing, touches no `CaptureConfig` and clears no capability bit.
    The mode is RE-RESOLVED against the constructed handle's `caps()` inside
    `FrameLoop::open()` at step 13, which passes `CaptureConfig.embed_cursor =
    plan_resolved.embed_cursor()` to the constructor, then builds `self.cursor` on
    `"separate"` or sets `self.cursor = None` and calls
    `clear_cap(abi::AddonCaps::CURSOR)` on `"embedded"`. If that re-resolution
    returns `None`, or returns a mode whose `embed_cursor()` differs from the value
    the constructor was given, the add-on lied about a capability: fall through to
    the next capture candidate and, if none remains, fail startup with step 3d's
    rejection list.
6b. Instantiate the transport: `transport::new(transport::Config{…})` from the
    `[server]`, `[server.tls]`, and `[transport]` sections. `[server] bind`
    supplies a single `host:port` which is bound BOTH as a TCP listener (HTTPS
    bootstrap, `/cert-hashes`, `/auth`, the `/ws` fallback upgrade, and the
    `Alt-Svc: h3=":<port>"` header) and as a UDP socket (QUIC/WebTransport), under
    one TLS configuration and one router — see MODULE_TRANSPORT "Two listeners,
    one port". `[server] base_path` is also sourced here, into
    `transport::Config.base_path`: the transport strips the prefix from every
    request before routing and before its own `/wt` / `/ws` upgrade match, which
    is why it is a transport field and has no twin on `server::Config` (see
    MODULE_TRANSPORT "Base path"). `transport::Config` has no `http` field: the
    route table arrives after construction, when `Server::start` calls
    `Transport::set_http_router`. The pipeline owns the transport and hands it to
    the server.
7. Create the server, filling **every** `server::Config` field
    (MODULE_SERVER "Public Interface") — the pipeline is the sole constructor,
    so an unfilled field here is a compile error rather than a runtime surprise:
    - `transport`   → the `transport::Transport` built at 6b (moved in)
    - `client_fs`   → the embedded web-client filesystem (`rust-embed`), served from `/`
    - `authenticator` → `auth::new(&cfg.auth)` per the `[auth]` section
                        (MODULE_AUTH). This is where the mode's one-time startup
                        work happens (token generation + `token_file` write, the
                        `none`-mode warning, PIN pairing window); an
                        `AuthError::Config` here **fails startup** rather than
                        starting a server that cannot gate correctly.
    - `session_cache` → backing store for resume, TTL from `[reconnect] cache_ttl_seconds`
    - `allow_takeover` → `[auth] allow_takeover`
    - `session_defaults` → a `SessionDefaults` built from `[server]`
                        (`allow_origin`, `max_clients`, `max_message_bytes`,
                        `input_rate_limit`, `keyframe_min_interval_ms`,
                        `keyframe_request_burst`, `keyframe_request_refill_ms`,
                        `shutdown_grace_ms`), `[transport]`
                        (`datagram_send_queue_frames`, `audio_send_queue_chunks`,
                        `reassembly_max_bytes`, `auth_deadline`, `ping_interval`,
                        `join_idr_timeout`, `ws_max_message_bytes`,
                        `ws_send_queue_bytes`) and `[clipboard]` (`direction`,
                        `formats`, `max_bytes`) — the same value the
                        `[server]`/`[transport]`/`[clipboard]` reload applier later
                        re-installs through `Server::set_session_defaults`.
                        `max_clients` is a field of THIS struct, not of
                        `server::Config`.
    There is **no** `stream_mgr` field — the Manager arrives later via
    `set_stream_params_callback` at step 12, because it cannot be built until
    `param_tx` exists. See the note in `server::Config`.
8. Create the input dispatcher if `[input] enabled` (default true). Select from
   `registry.inputs` by `InputAddon::kind()`:
     - `KeyMouse`: at most one may load. If an add-on of this kind probes
       available it OVERRIDES the in-core `enigo` default (Interception on
       Windows, uinput on Linux); if two are loaded, the first in probe order
       wins and the other is skipped with a `warn`.
     - `Touch`:   at most one (win_touch). Absent → touch records are dropped.
     - `Gamepad`: at most one (vigem / gcvirtual / uinput). Absent → gamepad
       records are dropped.
   Build them with `new_key_mouse` / `new_touch` / `new_gamepad`, sized to the
   SAME stream dims, and pass them to `input::new_dispatcher(km, touch, gp, cfg)`.
   Only `[input] enabled = false` makes the binary view-only (log it; not an error).
9. (Webcam was here — deferred to a future version, see CENTRAL_SPEC "Deferred".)
10. Create the clipboard driver + handle with `clipboard::spawn` and the
    file-transfer Service if their `[*] enabled`.
10b. If `[audio] mic_enabled` AND an `AudioSink` add-on is loaded, construct it
    (`AddonKind::AudioSink`, `0x0A` — `pw_vmic` on Linux, `win_vmic` on Windows;
    no macOS backend exists in v1). Construction happens on the audio thread,
    which is the only thread that will touch it, and the handle sits in the
    audio loop's drop order so the virtual device is torn down on shutdown
    rather than left behind. A probe failure here is a **warning**, never a
    startup error: mic is an accessory, and losing it must not stop the desktop
    from streaming (MODULE_AUDIO "Failure behaviour").
11. If `[audio] enabled` AND an audio capture add-on is loaded: SELECT the
    capturer + the encoder (`opus` add-on → Opus, else PCM passthrough) into an
    `AudioPlan`. They are CONSTRUCTED on the audio thread at step 13, not here,
    so nothing about the audio format is known yet: create the
    `tokio::sync::watch` of `Option<AudioDescriptor>` with an initial `None` —
    the channel starts at `None` and every `config` advertises `audio: false`
    until `AudioLoop::open()` succeeds and publishes an `AudioDescriptor`.
    The `[audio]` section + struct field exist now; the per-OS add-ons themselves
    are **implementation-deferred** (MODULE_AUDIO), so this step is wired but
    inert until they land. No add-on / disabled → `self.audio = None` and the
    channel stays `None` for the process lifetime (video-only).
12. Wire callbacks. Every callback below is `Fn + Send + Sync`, so none of them
    captures the Pipeline or any `&mut`. Each captures exactly one cheap handle:
    an `Arc<AtomicBool>`, an `Arc<Mutex<…>>`, an `Arc<dyn …>`, or a channel
    sender. That is the whole reason M-6 holds without a lock on the hot path.
   - server.set_config_provider       → a closure over `applied_rx.clone()`; builds the
                                        current `ConfigBase` (codec, dims, fps, hdr,
                                        color_space, chroma, audio params, cursorMode,
                                        clipboard, fileStreamBudget) from the frame
                                        loop's published params, never from a stale
                                        cache. It carries **no** session credential —
                                        `session_token` / `session_ttl_sec` / `resumed`
                                        / `carrier` are added by the server per
                                        recipient (MODULE_SERVER `send_config`),
                                        because the pipeline holds no session state.
   - server.set_new_client_callback   → `{ let f = kf_req.clone(); move || f.store(true, Ordering::Release) }`
                                        (the server already gates on the cached keyframe)
   - server.set_session_count_callback → `{ let n = authed.clone(); let w = wake.clone();
                                        move |c: u32| { let prev = n.swap(c, Ordering::AcqRel);
                                                        if prev == 0 && c > 0 { w.notify_one(); } } }`
                                        — the frame loop's idle gate. One atomic
                                        swap plus, on 0→N only, one notify; the
                                        server invokes it outside every session
                                        lock (MODULE_SERVER "Public Interface").
   - server.set_mic_callback          → `Some` iff `[audio] mic_enabled` AND an
                                        `AudioSink` add-on probed available: a closure
                                        over the mic packet channel into the audio
                                        thread's mic half. `None` otherwise, which makes
                                        the server drop mic datagrams at the role gate
                                        (MODULE_AUDIO "Microphone (client→host)").
   - server.set_keyframe_request_callback → the SAME closure over another `kf_req` clone
                                        (the server applies all three rate-limiting
                                        stages before invoking)
   - server.set_input_callback        → a closure over `Arc<Mutex<Box<dyn input::Dispatcher>>>`
                                        calling `dispatch`; the guard is held for
                                        exactly one record and never across an await
                                        (binary; None only if [input] enabled=false)
   - server.set_controller_change_callback → a closure over the same
                                        `Arc<Mutex<Box<dyn input::Dispatcher>>>`
                                        calling `release_all`; fires when the
                                        controlling client changes or departs, so
                                        keys the departing controller held are not
                                        left down on the host (TD-35). Same guard
                                        discipline as `set_input_callback`: held for
                                        exactly one call, never across an await.
                                        (None only if [input] enabled=false)
   - server.set_clipboard_callback    → a closure over a `clipboard::ClipboardHandle`
                                        clone calling `set(&self, …)` (C→H; length cap,
                                        direction, sanitization and role gating are the
                                        server's)
   - clipboard H→C drain              → the clipboard task (step 13) takes
                                        `ClipboardHandle::changes()` ONCE and calls
                                        `server.send_clipboard(content)` per change;
                                        the server does the direction /
                                        controller-only / sanitization gating.
                                        This is the mirror of the callback above —
                                        MODULE_CLIPBOARD "Internal Architecture"
                                        and MODULE_SERVER `clipboardReader()` both
                                        assume this wire exists; the pipeline owns it.
   - server.set_file_transfer_service → `Option<Arc<dyn filetransfer::Service>>` (None if
                                        [filetransfer] disabled → server rejects new
                                        file-transfer streams with close::PROTOCOL_ERROR)
   - server.set_params_watch          → `applied_rx.clone()`: the server's ONLY source for
                                        "what is the stream actually doing", feeding the
                                        config provider, `SessionState.last_params` and the
                                        adaptive loop's current bitrate
   - gamepad rumble                   → construct the `RumbleSink` (64-slot mpsc), install
                                        it on the gamepad injector when `AddonCaps::RUMBLE`
                                        is set and `[gamepad] allow_rumble` is true, and
                                        drain the receiver into `server.send_gamepad_rumble`
                                        (clamping `duration_ms` to [0, 5000] at the drain)
   - Construct the `stream::Manager` impl with a **clone of `self.param_tx`**,
     `applied_rx` and `self.stats.clone()`, and hand it to
     `server.set_stream_params_callback` behind a `Mutex` (`apply` takes
     `&mut self`). The Manager holds the `Arc<Stats>` for one purpose: calling
     `Stats::record_param_update_dropped` when a `ParamUpdate` cannot be
     enqueued — without it `featherdesk_param_updates_dropped_total` has no
     writer. The `Arc<Mutex<Box<dyn stream::Manager>>>` is built ONCE and a clone
     is retained as `Pipeline.manager`, because the `fd-config` task reaches
     `Manager::apply` (the `[stream]` rows) and `Manager::set_policy` (the
     `[stream.adaptive]` row) through it; type-erasing the only reference into
     the callback closure is what made both rows uncallable. The Manager is the
     **sole producer** on
     the funnel: the server dispatches control-stream
     `resize`/`set_bitrate`/`set_fps`/`set_hdr` requests to `Manager::apply` as a
     `stream::ParamDelta`, and the bandwidth-adaptation telemetry loop
     (MODULE_SERVER.md "RTT + adaptive bitrate") feeds it signals every
     `[stream.adaptive] interval_ms`. This is the one piece of wiring
     MODULE_SERVER.md and MODULE_STREAM_PARAMS.md both assume exists but neither
     module constructs — the pipeline owns it, same as every other cross-module
     wire in this step.

    `kf_req: Arc<AtomicBool>` is created in `new()`. It is the ONLY path from a
    server task to the encoder: the callbacks SET it, and the frame loop is the
    only reader — it clears it at step (0c) and performs the actual
    `force_keyframe()` at step (3), on its own thread, between two `encode` calls.
    Nothing outside the frame thread ever touches an encoder.
13. Move the three runtime halves apart and start them. `start(self)` destructures
    the Pipeline, so nothing is borrowed twice:
      - `FrameLoop` → a dedicated OS thread (`fd-frame`). It calls
        `FrameLoop::open()` FIRST, on that thread, to construct the capture
        handle, the converter and the encoder (thread affinity — see "Thread and
        handle ownership"), then enters the frame loop.
      - `AudioLoop` → a dedicated OS thread (`fd-audio`), same construct-on-use
        rule. `[audio] disabled` or no audio add-on → the half is `None` and no
        thread is spawned.
      - `ClipboardTask` → a tokio task. It awaits `Monitor::run(cancel)` and, in
        the same `select!`, drains the `changes()` watch → `server.send_clipboard`.
        A `ClipboardError` from `run` disables clipboard sync for the session and
        leaves video untouched (see "Error Recovery Strategy").
      - `server.start(cancel).await` on the calling task. The Server is behind an
        `Arc` and `start` takes `&self`, so this is a shared borrow alongside the
        two exclusive ones the threads own.
14. Enter shutdown when `cancel` fires or any half returns an error.
```

> `force_keyframe()` dispatches to the active encoder (hw or sw) — never to a `None` one. This fixes the round-1 bug where `encoder.force_keyframe()` would panic on the hardware path.

### Main Frame Loop

Key fixes vs round 1: skip is decided BEFORE capture; `EncodedFrame.data` is contiguous Annex B (no per-NAL split/rejoin); the server owns the sequence; pacing is capture-to-capture (NOT delivery-to-delivery); and pacing, the keyframe latch and the parameter drain are all inside the loop, so a runtime `set_fps` or keyframe request actually changes what the loop does.

**Allocation strategy:** the encoder's Annex B output buffer and the server's broadcast access-unit buffers reuse pooled buffers (e.g. a `bytes::BytesMut` pool sized to a typical access unit ~50KB). Steady-state frame loop is zero-alloc after warmup. The `Converter::convert()` reuses YUV buffers (already documented); the encoder and server must follow the same pattern.

```rust
/// Which half of the hot path produced an error. `stream::StreamError` is
/// deliberately ONE shared enum (MODULE_STREAM_PARAMS: "there is NO separate
/// CaptureError"), so the source cannot be recovered from the value — it is
/// tagged at the call site instead, which is the only place that knows it.
#[derive(Copy, Clone, Debug, PartialEq, Eq)]
enum Component { Capture, Encode }

impl Component {
    fn as_str(self) -> &'static str { match self { Component::Capture => "capture", Component::Encode => "encode" } }
}

/// One consecutive-error ladder per component. Every field is read and written
/// ONLY on the frame-loop thread — that is why they are plain `u32`/`Instant`
/// fields rather than atomics, and why the poisoned set is a plain `HashSet`.
/// The clock is `std::time::Instant` (monotonic); no wall clock is involved.
struct ErrorLadder {
    consecutive: u32,          // reset to 0 by ANY successful frame from this component
    restarts: u32,             // successful restarts inside the current decay window
    last_error_at: Option<Instant>,
    window_started_at: Instant,
}

impl ErrorLadder {
    /// The SAME 60 s decay rule Level 1 uses inside add-ons, applied to the
    /// pipeline's own counters: a component that has run 60 s without an error
    /// starts its next ladder at attempt 1. A process that fails once an hour is
    /// healthy; one that fails five times a minute is broken.
    /// It resets ONLY `consecutive`. `restarts` and `window_started_at` are the
    /// restart cap's own 60 s window and are rolled over by `record_error`, so an
    /// error-free gap does not silently forgive a restart budget.
    fn decay(&mut self, now: Instant) {
        if self.last_error_at.map_or(true, |t| now.duration_since(t) >= Duration::from_secs(60)) {
            self.consecutive = 0;
        }
    }
    fn ok(&mut self) { self.consecutive = 0; }
    /// Starts both ladders: `consecutive = 0`, `restarts = 0`, and
    /// `window_started_at = now` — the restart window opens with the component.
    fn fresh(now: Instant) -> Self { /* … */ }
}

impl FrameLoop {
    /// Constructs every media object ON THIS THREAD, from `self.plan` and
    /// `self.params`. Called exactly once, as the first statement of `run`;
    /// `pipeline::new()` constructed none of them (see "Thread and handle
    /// ownership"). It performs, in this order, the work the old startup steps
    /// 3g, 4, 6 and 6a described:
    ///
    ///   1. `CaptureAddon::open(CaptureConfig)` for `self.plan.active_id(
    ///      Component::Capture)` → `self.capture`, with
    ///      `CaptureConfig.embed_cursor = plan_resolved.embed_cursor()` from the
    ///      mode step 3d recorded on the `SelectionPlan` — the only expression
    ///      that supplies it. This is the ONE construct; `degrade_to_software`
    ///      later reuses the same object.
    ///   2. Re-read `caps()` on the constructed handle, mask off any bit the
    ///      probe did not claim, log that masking once at `warn`, and fix the
    ///      path: zero-copy iff `caps().has(abi::AddonCaps::SURFACE)` AND the
    ///      plan selected a HW encoder (and, when the resolved mode is
    ///      "embedded", also `EMBED_CURSOR_SURF`).
    ///   3. Derive the UPRIGHT stream dims from the constructed capturer's
    ///      resolution and its reported `Rotation` — for R90/R270 the
    ///      transposed `(height, width)` — and store them in `self.params`.
    ///   4. Re-resolve the cursor mode on the CONSTRUCTED handle:
    ///      `CursorMode::resolve(policy, self.capture.caps())` against the
    ///      `[capture] cursor_mode` policy the plan recorded (MODULE_CAPTURE
    ///      "Cursor delivery"). `None`, or a mode whose `embed_cursor()`
    ///      differs from the value passed to the constructor in step 1, is a
    ///      capability lie: fall through to the next capture candidate and, if
    ///      none remains, fail with step 3d's rejection list. Otherwise store
    ///      `self.cursor_mode` and, on "separate", build `self.cursor`; on
    ///      "embedded", leave `self.cursor = None` and
    ///      `clear_cap(abi::AddonCaps::CURSOR)` so the poll is skipped by
    ///      construction.
    ///   5. Build the encode side for `self.plan.active_id(Component::Encode)`
    ///      via the selected `EncoderAddon`, calling the ONE constructor valid
    ///      for its `kind()`: `EncoderAddon::new_hw(HWEncoderConfig)` →
    ///      `self.hw_encoder` on the zero-copy path, else
    ///      `EncoderAddon::new_sw(EncoderConfig)` → `self.encoder`, paired with
    ///      `self.converter`, via `build_software_path`. Calling the wrong one
    ///      for the add-on's kind returns `PipelineError::AddonBackend`
    ///      ("Capability Probing"). Both MUST be called on this thread, which
    ///      this step is.
    ///   6. Read `params_capability()` through each handle's
    ///      `as_configurable()` accessor and publish the first `Applied` on
    ///      `applied_tx` with `caps: Some(..)` (see `stream::Applied.caps`), and
    ///      initialise `self.last_applied_ok = true` to match it.
    ///
    /// Returns `PipelineError` on any failure; `run` propagates it and the
    /// thread exits without entering the loop. All five media fields are `Some`
    /// (per path) on `Ok`.
    fn open(&mut self) -> Result<(), PipelineError>;

    /// Runs on a dedicated OS thread (`fd-frame`), NOT a tokio task — the
    /// capture/encode hot loop pins a thread (replaces runtime.LockOSThread), and
    /// EGL/X11/D3D contexts are thread-affine.
    ///
    /// It takes `self` BY VALUE. Nothing outside this thread holds a handle to
    /// the capture object, the converter or the encoder, so "encode and
    /// reconfigure never alias" (M-6) is a property of the type system here, not
    /// a rule to remember.
    fn run(mut self, cancel: CancellationToken) -> Result<(), PipelineError> {
        // Thread affinity: the media objects are CONSTRUCTED here, on the thread
        // that will use them and drop them.
        self.open()?;

        let mut skip_budget: u32 = 0;
        let mut last_frame_t = Instant::now();

        loop {
            if cancel.is_cancelled() { break; }
            if let Some(e) = self.pending_terminal.take() { self.terminate(&cancel, e.clone()); return Err(e); }

            // (0-) IDLE GATE (Stage 1, unconditional — GAP_TRIAGE OQ-04).
            //      With no authenticated session there is nobody to send to, and
            //      capturing / converting / encoding into empty rings is pure
            //      waste. Park on a Notify the server signals when a session
            //      COMPLETES AUTH (lifecycle step 12), not when one is accepted:
            //      waking on accept would make an unauthenticated connection a
            //      capture-start primitive.
            if self.authed_clients.load(Ordering::Acquire) == 0 {
                self.stats.set_idle(true);
                self.idle_since = Some(Instant::now());
                // Stage 2 (opt-in): after [capture] idle_release_after of zero
                // sessions, release the capturer + encoder entirely. Rebuilt by
                // FrameLoop::open() on wake. See "Idle suspension" below.
                self.maybe_release_idle();
                self.wake.notified().await;
                self.stats.set_idle(false);
                self.on_idle_wake(&mut last_frame_t);   // see "Idle suspension"
                continue;                                // re-enter with fresh params
            }

            // (0) Drain the funnel and fold EVERY queued delta into ONE
            //     reconfigure — this is what makes "concurrent resize requests are
            //     coalesced; the pipeline applies only the latest" true. The funnel
            //     is single-producer FIFO (stream::Manager), so deltas fold in send
            //     order: two deltas naming different fields never revert each
            //     other, and two naming the same field are last-write-wins.
            let mut next = self.params.clone();
            let mut folded = false;
            while let Ok(u) = self.param_rx.try_recv() {
                if u.epoch <= self.param_epoch { continue; } // stale; unreachable with one producer
                self.param_epoch = u.epoch;
                next.apply_delta(&u.delta);
                folded = true;
            }
            if folded { self.apply_params(next); }

            // (0a) Pacing is derived from the CURRENT params EVERY iteration, not
            //      once before the loop. `set_fps`, the adaptive loop, sustainable-
            //      rate control and a `[stream]` hot reload all change
            //      `self.params.fps` at step (0); fps is a frame-loop pacing
            //      parameter, not just an encoder hint, so the loop must actually
            //      capture at the new rate. `Manager::apply` clamps fps to 1..=240
            //      (`[stream]` validation); the clamp here is belt-and-braces
            //      against a division by zero.
            let target_interval = Duration::from_secs(1) / self.params.fps.clamp(1, 240);
            // max_skip bounds SKIP ACCUMULATION, not frame rate: after one overrun
            // the loop may skip at most this many consecutive ticks, so a single
            // slow frame can never stall the stream for more than ~200 ms. It is
            // NOT a "5 FPS floor" and it does not set a frame rate — the delivered
            // rate is governed by "Sustainable-rate control" below.
            let max_skip = (self.params.fps / 5).saturating_sub(1);

            // (0b) Cursor poll — BEFORE the skip check on purpose. The whole point
            //      of cursorMode "separate" is that the pointer keeps moving while
            //      video is skipped, dropped, or static (Key Design Decision:
            //      "cursor moves without waiting for a video frame"). Polling this
            //      after (1)/(3) would freeze the cursor exactly when the stream is
            //      already struggling — the worst moment for it. The capturer is
            //      required to latch its pointer source so this works without a
            //      frame having been taken (MODULE_CAPTURE "Cursor and frame
            //      acquisition order").
            //      `self.cursor` is Some only when `FrameLoop::open()` resolved
            //      "separate"; the CursorCapturer is reached through the ONE
            //      capture object.
            if self.cursor.is_some() {
                // The `&mut self.capture` borrow is BOUND and ENDED here, before the
                // match: a scrutinee temporary lives until the end of the `match`, so
                // calling `self.on_cursor_error(e)` from an arm of
                // `match self.capture.as_mut()…` is E0499.
                let cursor_res = self.capture.as_mut().unwrap().as_cursor().map(|cc| cc.next_cursor());
                let observed = match cursor_res {
                    Some(Ok(v)) => v,
                    Some(Err(e)) => {
                        // Non-fatal and NEVER escalated to the capture-error
                        // ladder: a dead cursor query must not take video down.
                        self.on_cursor_error(e);
                        continue;
                    }
                    None => None, // unreachable: `cursor` is Some only while caps & CURSOR
                };
                let server = self.server.clone();
                self.cursor.as_mut().unwrap().tick(observed, Instant::now(), server.as_ref());
            }

            // (0c) Latch any pending keyframe request. `kf_req` is set from server
            //      tasks (a new client with no fresh cached IDR; a client
            //      {"type":"keyframe"} after a gap) and is read and cleared HERE and
            //      nowhere else. Latching into a field rather than acting
            //      immediately means a request that lands during a skip run is not
            //      lost: the skip branch below `continue`s without clearing it.
            if self.kf_req.swap(false, std::sync::atomic::Ordering::AcqRel) {
                self.idr_pending = true;
                self.idr_pending_since.get_or_insert_with(Instant::now);
            }
            // A pending IDR outranks pacing: a client is waiting on it, so cancel any
            // owed skips rather than making it wait up to `max_skip` frames.
            if self.idr_pending { skip_budget = 0; }

            // (0d) Hot reload, frame-path half (see MODULE_CONFIG "Hot reload
            //      behavior" for who applies what). `cfg_rx` is a `watch`, so this
            //      is a flag check on the fast path. `[stream]` keys that are
            //      `stream::Params` fields are NOT applied here — they arrive through
            //      the funnel at (0) like any other parameter change, which is what
            //      M-6 requires. `idle_keyframe_ms` is the one exception, and it is
            //      not a `Params` field: it is this loop's own keepalive timer, so it
            //      is assigned directly.
            if self.cfg_rx.has_changed().unwrap_or(false) {
                let cfg = self.cfg_rx.borrow_and_update().clone();
                self.idle_keyframe_ms = cfg.stream.idle_keyframe_ms;
                // Compare RESOLVED modes, never a policy against a mode: a function
                // given only the config cannot resolve "auto", which is the default.
                let prev = self.cursor_mode;
                if self.resolve_cursor_mode(&cfg.capture.cursor_mode) != prev {
                    self.idr_pending = true;        // the cached IDR is invalidated
                    let c = self.current_config();
                    self.server.send_config(c);
                }
            }
            // …and the audio half. The audio thread publishes `Some(descriptor)`
            // when it opens and `None` when it leaves for any reason other than
            // shutdown — the client must be told `audio: false`, otherwise its
            // presentation clock waits forever on an `audioPlayoutTs` that has
            // stopped advancing while video datagrams keep arriving.
            if self.audio_desc_rx.has_changed().unwrap_or(false) {
                let _desc = self.audio_desc_rx.borrow_and_update().clone();
                let c = self.current_config();
                self.server.send_config(c);
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

            // (3) Capture + encode. The IDR is forced HERE, between two encodes —
            //     the only `&mut encoder` touch outside `capture_encode`, and what
            //     the encoder traits actually require. `capture_encode` tags which
            //     half failed, because the error type cannot: capture and both
            //     encoders share one StreamError by design.
            if self.idr_pending {
                self.force_keyframe();
                self.idr_pending = false;
                self.stats.record_forced_keyframe();
            }
            let frame_start = Instant::now();
            let ef = match self.capture_encode() {
                Ok(Some(ef)) => { self.capture_ladder.ok(); self.encode_ladder.ok(); ef }
                Ok(None) => {
                    self.capture_ladder.ok();
                    // Static screen, or a pipelined encoder holding this frame.
                    // If an IDR has been pending past the keepalive deadline,
                    // re-encode the retained frame rather than leave a joining
                    // client waiting for a repaint that may never come.
                    match self.idle_keyframe() { Some(ef) => ef, None => continue }
                }
                Err((_, stream::StreamError::FallbackToSoftware)) => {
                    self.degrade_to_software(); // permanent for this session; idempotent
                    continue;
                }
                Err((who, stream::StreamError::Unrecoverable(reason))) => {
                    // Level 2. The add-on has exhausted its OWN ladder and says so.
                    // It is never retried: poison it and walk to the next candidate.
                    match self.fall_through(who, &reason) {
                        Ok(()) => continue,
                        Err(e) => { self.terminate(&cancel, e.clone()); return Err(e); }
                    }
                }
                Err((who, e)) => {
                    // Transient. Per-component consecutive counter, decayed.
                    match self.record_error(who, e) {
                        Ok(()) => continue,
                        Err(e) => { self.terminate(&cancel, e.clone()); return Err(e); }
                    }
                }
            };

            // (4) Broadcast. Keyframe detection is done once by the encoder; the
            //     server trusts ef.keyframe (no redundant NAL scan). The byte
            //     count is taken BEFORE the move, because broadcast consumes `ef`.
            if ef.keyframe { self.idr_pending_since = None; }
            self.stats.record_broadcast(ef.data.len());
            self.server.broadcast(ef.codec_type, ef);
            self.stats.record_frame(frame_start.elapsed());

            // (5) Drop decision: if we overran, owe skips (capped at max_skip, i.e.
            //     at most ~200 ms of consecutive skipping).
            let d = frame_start.elapsed();
            if d > target_interval {
                let owe = (d.as_nanos() / target_interval.as_nanos()) as u32;
                skip_budget = owe.min(max_skip);
            }
        }
        Ok(())
        // Epilogue: fields drop in declaration order — encoder, hw_encoder,
        // converter, cursor publisher, capture handle, then the Arc<AddonRegistry>
        // clone. Surfaces are consumed per iteration, so none outlives the display
        // that made it, and the library outlives every object built from it.
    }

    /// Handles a `CursorCapturer::next_cursor` failure. Deterministic, never
    /// fatal, and never escalated to the capture-error ladder — a dead cursor
    /// query must not take video down.
    ///
    /// 1. Send the cached position with `visible = 0`, so the client HIDES a
    ///    pointer that can no longer be trusted rather than freezing one at a
    ///    stale place.
    /// 2. `self.cursor = None` and `capture.clear_cap(AddonCaps::CURSOR)`, so
    ///    `as_cursor()` returns `None` and the poll is skipped by construction
    ///    rather than by a flag the loop must remember to check. The capture
    ///    object itself is NOT dropped: on X11 and DXGI the cursor query borrows
    ///    the same display/duplication handle the frame path is still using.
    /// 3. `featherdesk_cursor_errors_total` increments; `warn!` fires once.
    /// 4. If the selected add-on's `ProbeReport.caps` sets `EMBED_CURSOR`,
    ///    rebuild the capturer through the Add-On Crash Recovery path with
    ///    `CaptureConfig.embed_cursor = true`; ONLY IF that rebuild succeeds, set
    ///    `cursor_mode = Embedded` and push a fresh `config`. If the add-on does
    ///    not declare `EMBED_CURSOR`, or the rebuild fails, `cursor_mode` stays
    ///    `Separate` and NO
    ///    `config` is pushed: the session runs without a client-side pointer, and
    ///    claiming "embedded" to a client whose capturer cannot embed would be a
    ///    lie that costs the user the pointer twice.
    fn on_cursor_error(&mut self, e: stream::StreamError);
}

// capture_encode runs the active path. Ok(None) means "no complete access unit
// this tick" — a static screen, or a PIPELINED encoder whose child is still
// holding this frame (MODULE_ENCODE) — not an error. The (Component, _) tag says
// which half failed; the error type deliberately does not carry it.
fn capture_encode(&mut self) -> Result<Option<stream::EncodedFrame>, (Component, stream::StreamError)> {
    if self.hw_encoder.is_some() {
        self.run_hardware_frame()
    } else {
        self.run_software_frame()
    }
}

fn run_hardware_frame(&mut self) -> Result<Option<stream::EncodedFrame>, (Component, stream::StreamError)> {
    // The `&mut self.capture` borrow is bound and ENDED before the match: a
    // scrutinee temporary lives until the end of the `match`, so calling
    // `self.degrade_to_software()` from an arm would be E0499.
    let fb_res = {
        let cap = self.capture.as_mut().unwrap();
        cap.as_surface().unwrap().next_surface()
    };
    let fb = match fb_res {
        Ok(Some(fb)) => fb,
        Ok(None) => return Ok(None), // no new surface this tick
        Err(stream::StreamError::FallbackToSoftware) => {
            self.degrade_to_software();
            return Ok(None);
        }
        Err(e) => return Err((Component::Capture, e)),
    };
    self.stats.record_capture();

    // THE dims-change detection site (HW half) — same rule as the SW path, and it
    // must run BEFORE `fb` is moved into the encoder.
    let up = match fb.rotation {
        capture::Rotation::R90 | capture::Rotation::R270 => (fb.height, fb.width),
        _ => (fb.width, fb.height),
    };
    if up != self.capture_dims {
        self.capture_dims = up;
        self.on_capture_dims_changed(up.0, up.1);
        return Ok(None);   // `fb` drops here, releasing the surface exactly once
    }

    // Output dims come from the active params (the HW encoder scaled to them per
    // the scaling invariant), NOT fb's native capture dims — mirrors the SW path.
    // The timestamp travels WITH the access unit (EncodedUnit.timestamp_ns), so
    // there is no metadata to rescue before the move.
    let (out_w, out_h) = (self.params.width as u16, self.params.height as u16);
    let codec_type = video_codec_type(self.hw_encoder.as_ref().unwrap().codec()); // "avc1.*"→H264, "hvc1.*"→HEVC

    // encode_surface CONSUMES fb (moved in → FbInfo's Drop releases it exactly
    // once on EVERY path: success, error, FallbackToSoftware). The pipeline never
    // releases it — RAII replaces the Go `fb.Release()` discipline. It returns ONE
    // EncodedUnit (no Option — the HW path never "skips"; the only skip is
    // next_surface() → Ok(None) above), or StreamError::FallbackToSoftware.
    // Bound first, for the same reason: the `Err(FallbackToSoftware)` arm assigns
    // `self.hw_encoder = None` inside `degrade_to_software`.
    let enc_res = self.hw_encoder.as_mut().unwrap().encode_surface(fb);
    match enc_res {
        Ok(unit) => Ok(Some(stream::EncodedFrame {
            data: unit.data.into_vec().into(), // owned RVec<u8> → bytes::Bytes
            width: out_w,
            height: out_h,
            timestamp_ns: unit.timestamp_ns,   // filled from the surface before it was consumed
            keyframe: unit.keyframe,           // encoder-set (M-2); no NAL re-scan
            codec_type,                        // VIDEO_H264 or VIDEO_HEVC
        })),
        Err(stream::StreamError::FallbackToSoftware) => {
            self.degrade_to_software();
            Ok(None)
        }
        Err(e) => Err((Component::Encode, e)),
    }
}

fn run_software_frame(&mut self) -> Result<Option<stream::EncodedFrame>, (Component, stream::StreamError)> {
    let frame = match self.capture.as_mut().unwrap().next_frame() {
        Ok(Some(f)) => f,
        Ok(None) => return Ok(None), // no new content this tick (see MODULE_CAPTURE)
        Err(e) => return Err((Component::Capture, e)),
    };
    self.stats.record_capture();

    // THE dims-change detection site (SW half). Upright geometry, so a rotation
    // change is caught as the resolution change it is (MODULE_CAPTURE "Rotation" 5).
    // This is what makes `on_capture_dims_changed` reachable — including after an
    // in-place add-on restart, which rebuilds the capturer without reconstructing
    // the CursorPublisher and so has no other route to refresh its scale.
    let up = match frame.rotation {
        capture::Rotation::R90 | capture::Rotation::R270 => (frame.height, frame.width),
        _ => (frame.width, frame.height),
    };
    if up != self.capture_dims {
        self.capture_dims = up;
        self.on_capture_dims_changed(up.0, up.1);
        return Ok(None);   // this tick's frame is at the OLD geometry; the reconfigure
                           // that apply_params triggers re-shapes the converter first
    }

    // rotate → convert → scale. The Converter is configured for the CURRENT
    // params by build_software_path / reconfigure_or_rebuild, so its output
    // geometry is the advertised geometry by construction.
    // Both of these come BEFORE the `convert` call: `convert` returns a `&YuvFrame`
    // borrowed from the converter and held until `encode(yuv)` consumes it, so an
    // immutable borrow of `self.converter` — or of `self.encoder` — after it is
    // E0502/E0499.
    debug_assert_eq!(self.converter.as_ref().unwrap().output_dims(),
                     (self.params.width, self.params.height));
    let codec_type = video_codec_type(self.encoder.as_ref().unwrap().codec());
    let yuv = self.converter.as_mut().unwrap().convert(&frame); // capture::Frame → rotated,
                                                                // converted, scaled YuvFrame at
                                                                // output dims
    let out = match self.encoder.as_mut().unwrap().encode(yuv) {
        Ok(None) => Ok(None), // skipped (rate control), or pipelined and not out yet
        Ok(Some(unit)) => Ok(Some(stream::EncodedFrame {
            // Dims are the ENCODED (output) dims, which the Converter is
            // configured to produce and `self.params` records — see the scaling
            // invariant under "Resolution-Change Handling".
            data: unit.data.into_vec().into(), // owned RVec<u8> → bytes::Bytes
            width:  self.params.width  as u16,
            height: self.params.height as u16,
            timestamp_ns: unit.timestamp_ns,   // from the AU, not the input frame: a pipelined
                                               // encoder returns frame N-2's unit from call N
            keyframe: unit.keyframe,           // from the encoder (M-2); no NAL re-scan
            codec_type,                        // computed above, before the `convert` borrow
        })),
        Err(e) => Err((Component::Encode, e)),
    };
    self.last_capture = Some(frame); // retained for the idle-screen keepalive
    out
}

// force_keyframe dispatches to whichever encoder is active. Called only from
// step (3) of the frame loop and from `idle_keyframe`, on the frame-loop thread,
// between two encode calls.
fn force_keyframe(&mut self) {
    if let Some(hw) = self.hw_encoder.as_mut() {
        hw.force_keyframe();
    } else if let Some(sw) = self.encoder.as_mut() {
        sw.force_keyframe();
    }
}

// apply_params applies one FOLDED parameter change ON THE FRAME-LOOP THREAD
// (M-6). It is the single place that mutates the encoder/capturer for a
// parameter change, and it serves every trigger: client resize/set_*, the
// adaptive loop, sustainable-rate control, a capture-detected resolution change,
// and a `[stream]` hot reload.
// It takes no `replies` argument: `stream::ParamUpdate` has no reply channel and
// neither half of one can exist (see "param_failure_feedback" — `Manager::apply`
// takes no sender, and `set_stream_params_callback` returns synchronously).
fn apply_params(&mut self, np: stream::Params) {
    let old = self.params.clone();

    // What this change invalidates, computed BEFORE the reconfigure so a rebuild
    // can only widen it, never narrow it.
    let dims_changed   = np.width != old.width || np.height != old.height;
    let fps_changed    = np.fps != old.fps;
    let chroma_changed = np.chroma_subsampling != old.chroma_subsampling;
    let depth_changed  = np.bit_depth != old.bit_depth || np.hdr != old.hdr;
    let color_changed  = np.color_space != old.color_space;

    let outcome = self.reconfigure_or_rebuild(&np);
    let rebuilt = matches!(outcome, Ok(Reconfigured::Rebuilt));
    if let Err(e) = outcome {
        // An HDR request that no loaded encoder can serve is the ONE param
        // failure that is a user-visible toggle rather than a log line: silence
        // leaves the toggle lit against a stream that will never be HDR. The
        // requester's identity is not available here — it was consumed by
        // set_stream_params_callback — so the notice goes to every session, and a
        // client that never asked for HDR ignores it.
        if matches!(e, stream::StreamError::HdrUnsupported) && np.hdr {
            self.server.send_control(server::Recipients::Every,
                server::ControlMessage::HdrUnavailable {
                    reason: server::HdrUnavailableReason::NoHevcEncoder,
                });
        }
        self.report_param_failure(e, old); // never a silent return
        return;
    }

    if dims_changed {
        if let Some(d) = self.input.as_ref() {
            // One record's worth of lock, on the frame thread, never across an await.
            if let Ok(mut d) = d.lock() { let _ = d.resize(np.width, np.height); } // keep absolute
                                                                                   // mouse mapping 1:1
        }
        if self.cursor.is_some() {
            // DrawW/DrawH/Hotspot are stream-space quantities, so a resolution
            // change invalidates them: re-emit the active shape under the SAME
            // ShapeID with recomputed draw dimensions, then re-emit the position.
            // This is the ONE caller of `on_stream_dims_changed`. The server's
            // dedupe key includes the draw geometry, so the re-emission is written
            // to every session still holding the stale geometry.
            let server = self.server.clone();
            self.cursor.as_mut().unwrap().on_stream_dims_changed(&np, server.as_ref());
        }
    }

    let codec = self.active_codec();
    let codec_changed = codec != self.last_codec;
    self.last_codec = codec.clone();
    self.params = np;
    self.stats.set_effective_fps(self.params.fps);
    self.stats.set_effective_bitrate_kbps(self.params.bitrate_bps / 1000);

    // Two independent gates. `config` answers "did anything the client can see
    // change?"; the IDR answers "did the reference chain become invalid?". A
    // bitrate- or qp-only change answers no to both — that is the 100 ms adaptive
    // tick, and firing either on it is an IDR storm plus a decoder reset ten
    // times a second (and, on the opt-in `x264` add-on, ten ffmpeg respawns).
    // ConfigPayload carries codec / dims / fps / hdr / color_space / chroma /
    // audio / cursorMode; it carries neither bitrate nor QP nor keyframe_interval
    // (MODULE_STREAM_PARAMS: "no config message — a bitrate change is transparent
    // to the client decoder").
    if dims_changed || fps_changed || chroma_changed || depth_changed || color_changed || codec_changed {
        let c = self.current_config();
        self.server.send_config(c);
    }

    // An IDR is required ONLY when the reference chain is invalid. A REBUILT
    // encoder already starts a fresh stream with new SPS/PPS on its first frame,
    // so forcing one on top would be a second restart — on the opt-in `x264`
    // add-on, literally a second ffmpeg kill + respawn.
    if !rebuilt && (dims_changed || chroma_changed || depth_changed || codec_changed) {
        self.idr_pending = true;
        self.idr_pending_since.get_or_insert_with(Instant::now);
    }

    let applied = self.params.clone();
    self.last_applied_ok = true;          // THE writer for the success outcome
    let _ = self.applied_tx.send(stream::Applied {
        epoch: self.param_epoch,
        params: applied,
        codec: self.last_codec.clone(),
        cursor_mode: self.cursor_mode.as_str().to_string(),
        ok: true,
        caps: None, // an ordinary parameter change swapped no backend, so the
                    // Manager's cached StreamParamsCapability is still current
    });
}

// report_param_failure is the ONLY exit from a failed parameter change. There is
// no silent `return`: a change that did not land must be visible to everything
// that caches "current", or the Manager keeps clamping against params the
// pipeline is not running and every later change is computed from a fiction.
fn report_param_failure(
    &mut self,
    e: stream::StreamError,
    unchanged: stream::Params,
) {
    tracing::error!(target: "pipeline", err = %e, "param change failed; keeping previous params");
    self.stats.record_param_change_failed();

    // The ONE report. There is no step answering a `oneshot`: the funnel carries
    // no reply channel. Republishing the UNCHANGED params on the `Applied` watch
    // with ok:false rolls the Manager's and the server's caches back to what the
    // pipeline actually runs, and MODULE_SERVER turns that edge into
    // `resize_suppressed`.
    self.last_applied_ok = false;         // THE writer for the failure outcome
    let _ = self.applied_tx.send(stream::Applied {
        epoch: self.param_epoch,
        params: unchanged,
        codec: self.last_codec.clone(),
        cursor_mode: self.cursor_mode.as_str().to_string(),
        ok: false,
        caps: None, // nothing was swapped; the bounds the Manager clamps against
                    // are unchanged
    });

    // `self.params` is deliberately NOT advanced. The next request is computed
    // from the params in force, not from the one that failed.
}

// degrade_to_software permanently swaps the HW path for the SW path for the rest
// of the session (GPU reset / driver constraint). Called only from the
// frame-loop thread.
//
// IDEMPOTENT. `next_surface` and `encode_surface` can BOTH report
// FallbackToSoftware for the same GPU reset inside one iteration, the loop's own
// arm can see it a third time, and Level 2's fall-through can reach it — so the
// second and every later call must be a cheap no-op. Otherwise it builds a second
// Converter and a second encoder (leaking the first pair), re-pushes a config the
// clients already have, and forces a redundant IDR.
//
// The SW path is 8-bit H.264: no loaded SW encoder emits HEVC Main10, and the
// Converter has no 10-bit input format. An HDR session therefore leaves HDR in
// the same step, and the params MUST be reset BEFORE anything is built from
// them — cloning the live HDR params hands a 10-bit contract to an 8-bit path
// and reconfigures nothing on the capturer, which is still producing
// XRGB2101010 / R10G10B10A2 / 64RGBALeAccurate buffers the Converter would
// misread as BGRA8.
fn degrade_to_software(&mut self) {
    if self.hw_encoder.is_none() {
        return; // already on the software path
    }
    tracing::warn!(target: "pipeline", "hardware encoder unavailable; degrading to software");

    // The HW encoder is dropped FIRST: it may hold an imported surface from the
    // capture handle, and the capture handle is what keeps producing frames on the
    // software path. The capture OBJECT is not touched — there is exactly one of
    // it, and `next_frame` on it is the software path (startup step 4). Only the
    // SURFACE capability is retired.
    self.hw_encoder = None;
    self.capture.as_mut().unwrap().clear_cap(abi::AddonCaps::SURFACE);

    let mut params = self.params.clone();
    let was_hdr = params.hdr;
    if was_hdr {
        params.hdr = false;
        params.bit_depth = 8;
        params.color_space = "bt709".to_string();
    }
    // 4:2:2/4:4:4 survive the degrade only if the SW candidate advertises them
    // (openh264 is 4:2:0-only); otherwise fall to 420 here rather than letting
    // build_software_path fail with ChromaUnsupported.
    if !self.sw_candidate_supports_chroma(&params.chroma_subsampling) {
        params.chroma_subsampling = "420".to_string();
    }

    // Reconfigure the CAPTURER back to an 8-bit surface format BEFORE the first
    // next_frame() on the new path.
    if was_hdr {
        if let Err(e) = self.reconfigure_capture(&params) {
            tracing::error!(target: "pipeline", err = %e, "capture 8-bit reconfigure failed");
            self.pending_terminal = self.record_error(Component::Capture, e).err();
            return;
        }
    }

    // build_software_path walks the SW probe order skipping `self.poisoned`, so a
    // dead x264 cannot be re-selected as the fallback for a failing HW encoder —
    // which is exactly what Level 2 step 2 promises.
    if let Err(e) = self.build_software_path(&params) {
        // No usable SW encoder remains. Terminal, and it must SAY so rather than
        // leave the loop spinning with no encoder at all.
        self.pending_terminal = Some(PipelineError::AddonsExhausted {
            component: "encode",
            reason: format!("no software encoder available after HW degrade: {e}"),
        });
        return;
    }
    self.params = params;

    if was_hdr {
        // The HDR cascade is over for this session. HDR is a one-way trip in the
        // SDR→HDR direction only; this is the forced exit, and it is the
        // mid-session case the cascade's terminal-case text previously lacked.
        self.server.send_control(server::Recipients::Every,
            server::ControlMessage::HdrUnavailable {
                reason: server::HdrUnavailableReason::DegradedToSoftware,
            });
    }
    // The codec string changes (HW HEVC/H.264 → SW H.264 at a possibly different
    // level), so every ALREADY-CONNECTED client MUST reconfigure its
    // VideoDecoder. Push a fresh {"type":"config"} BEFORE the keyframe — same
    // discipline as apply_params. A bare IDR alone would leave existing viewers
    // feeding the new codec into a decoder still configured for the old one.
    self.last_codec = self.active_codec();
    let c = self.current_config();
    self.server.send_config(c);
    self.idr_pending = true;
    self.idr_pending_since.get_or_insert_with(Instant::now);
    let a = self.applied_now(true);
    let _ = self.applied_tx.send(a);
}

// record_error runs the transient ladder for ONE component. The Error Recovery
// table's "1-2 / 3-9 / 10 consecutive" rows are this function; they were
// previously reachable only through a name with no definition.
fn record_error(&mut self, who: Component, e: stream::StreamError) -> Result<(), PipelineError> {
    let now = Instant::now();
    let ladder = match who { Component::Capture => &mut self.capture_ladder, Component::Encode => &mut self.encode_ladder };
    ladder.decay(now);
    ladder.consecutive += 1;
    ladder.last_error_at = Some(now);
    let n = ladder.consecutive;
    tracing::warn!(target: "pipeline", component = who.as_str(), consecutive = n, err = %e, "frame error");

    // Roll the restart WINDOW over first: `decay` resets only `consecutive`, so
    // this is the one place `window_started_at` is read, and it is what makes the
    // cap a real 60 s window rather than "5 restarts, ever".
    if now.duration_since(ladder.window_started_at) >= Duration::from_secs(60) {
        ladder.restarts = 0;
        ladder.window_started_at = now;
    }

    // Absolute cap FIRST. Without it, a healthy capturer next to a dead encoder
    // restarts successfully every 3 frames forever — 50 ms apart at 60 fps — and
    // the 10-consecutive row is never reached because each restart clears the
    // counter. Five successful restarts inside one 60 s window means the restart
    // is not fixing anything.
    let restarts = ladder.restarts;
    if restarts >= 5 {
        let reason = format!("{} restarted 5x in 60s; last: {e}", who.as_str());
        return self.fall_through(who, &reason);
    }
    match n {
        1..=2 => Ok(()),                       // Warn: log, skip the frame, continue
        3..=9 => {
            // Error: restart THIS add-on in place (same add-on, fresh instance).
            // On success the consecutive counter clears and `restarts` advances,
            // which is what makes the absolute cap above reachable.
            match self.restart(who) {
                Ok(()) => {
                    let ladder = match who { Component::Capture => &mut self.capture_ladder, Component::Encode => &mut self.encode_ladder };
                    ladder.consecutive = 0;
                    ladder.restarts += 1;
                    self.idr_pending = true;   // a fresh instance has no reference chain
                    // A rebuilt CAPTURE add-on may report different native
                    // dimensions. Nothing is pushed here: the restarted instance's
                    // first frame carries its dims, the frame path notices the
                    // change and calls `on_capture_dims_changed`, which is the sole
                    // caller of `CursorPublisher::set_capture_dims`.
                    //
                    // The capability set, by contrast, has no such self-announcing
                    // route: a fresh instance may report NARROWER bounds than the
                    // one it replaced (a NVENC rebuilt after a driver reset need not
                    // still offer the old max bitrate). Publish, or `Manager::apply`
                    // keeps clamping against the dead instance's cached
                    // `StreamParamsCapability` and hands the loop parameters the
                    // live encoder rejects on every subsequent client request.
                    // `ok` carries the LAST PARAM OUTCOME, not a claim about the
                    // restart. `applied_tx` is a `watch`: it retains only the latest
                    // value, so hard-coding `true` here would erase an `ok: false`
                    // the Manager has not yet polled — and that edge is the ONLY
                    // report a client gets that its parameter change was refused.
                    let a = self.applied_now(self.last_applied_ok);
                    let _ = self.applied_tx.send(a);
                    Ok(())
                }
                Err(e) => { tracing::error!(target: "pipeline", component = who.as_str(), err = %e, "restart failed"); Ok(()) }
            }
        }
        _ => {
            // 10 consecutive: this add-on is not recovering. Poison + fall
            // through — for BOTH components. Shutting the whole pipeline down on
            // the capture path would contradict Level 2's promise that a dead
            // add-on is replaced, not fatal.
            let reason = format!("{} failed 10 consecutive frames; last: {e}", who.as_str());
            self.fall_through(who, &reason)
        }
    }
}

// fall_through implements Level 2 verbatim: poison, walk the probe order, swap.
// It runs on the frame-loop thread, like every other mutator of the capture
// handle and the encoder — which is structural, not a convention: it takes
// `&mut FrameLoop`, and `FrameLoop` is a value local to that thread.
fn fall_through(&mut self, who: Component, reason: &str) -> Result<(), PipelineError> {
    let dead = self.plan.active_id(who).to_string();
    tracing::error!(target: "pipeline", component = who.as_str(), addon = %dead, reason, "add-on unrecoverable; dropping for the session");
    self.poisoned.insert(dead);
    self.stats.set_poisoned(&self.poisoned);   // → featherdesk_addon_poisoned{addon,component}

    // Walk the SAME probe order used at startup (step 3d/3e), skipping poisoned
    // entries, re-probing each candidate and CONSTRUCTING it on this thread.
    while let Some(cand) = self.plan.next_candidate(who, &self.poisoned) {
        match self.open_component(who, &cand) {
            Ok(()) => {
                let l = match who { Component::Capture => &mut self.capture_ladder, Component::Encode => &mut self.encode_ladder };
                *l = ErrorLadder::fresh(Instant::now());
                // A swapped-in capture add-on may have different caps, so
                // cursorMode is re-resolved here (see "Cursor publishing"); a
                // swapped-in encoder may advertise a different codec string. Both
                // start with no reference chain: push a config, then an IDR.
                if who == Component::Capture {
                    // Clone the policy out of the watch BEFORE taking `&mut self`.
                    // The swap path pushes a fresh `config` unconditionally below,
                    // so it needs no before/after comparison.
                    let policy = self.cfg_rx.borrow().capture.cursor_mode.clone();
                    self.resolve_cursor_mode(&policy);
                    // A swapped-in capturer may have different native dimensions;
                    // `resolve_cursor_mode` step 3 reconstructs the publisher through
                    // `CursorPublisher::new`, which takes `capture_dims`, so the new
                    // dims are seeded at construction and no refresh call is needed.
                }
                let codec = self.active_codec();
                if codec != self.last_codec { self.last_codec = codec; }
                let c = self.current_config();
                self.server.send_config(c);
                self.idr_pending = true;
                let a = self.applied_now(true);
                let _ = self.applied_tx.send(a);
                tracing::warn!(target: "pipeline", component = who.as_str(), addon = %cand, "swapped in replacement add-on");
                return Ok(());
            }
            Err(e) => { tracing::warn!(target: "pipeline", addon = %cand, err = %e, "candidate failed to open; poisoning"); self.poisoned.insert(cand); }
        }
    }

    // Terminal: no candidate remains. This is the same case as the startup check
    // in step 3f ("HW encoder X has no SW fallback add-on loaded"), reached
    // mid-session.
    Err(PipelineError::AddonsExhausted { component: who.as_str(), reason: reason.to_string() })
}

// terminate is the ONLY way the frame loop ends other than cancellation. It
// carries the error out of the thread (the JoinHandle's Result) AND fires the
// shared token, so the server notifies and closes every session with
// close::SERVER_SHUTDOWN (4503) and `Pipeline::start` returns the error as the
// process exit status — rather than leaving a server up, /healthz green, and no
// frames flowing.
fn terminate(&mut self, cancel: &CancellationToken, e: PipelineError) {
    tracing::error!(target: "pipeline", err = %e, "pipeline terminal: shutting down");
    cancel.cancel();
}
```

**`sleep_to_interval(&mut last_frame_t, interval)`** sleeps until `last_frame_t + interval`, then sets `last_frame_t = Instant::now()`. O(1)/cheap. There is no `contains_keyframe` — keyframe status comes from the encoder (`EncodedUnit.keyframe` on both paths), never a pipeline-side NAL scan.

**Internal helpers** referenced above, all `&mut FrameLoop` and therefore all confined to the frame-loop thread by the type system:

- `open() -> Result<(), PipelineError>` constructs every media object on the frame thread, from `self.plan` and `self.params`; declared above, and the first statement of `run`.
- `resolve_cursor_mode(&str) -> CursorMode` re-runs `CursorMode::resolve` against the current `[capture] cursor_mode` policy and the constructed handle's `caps()`, and returns the mode now in force; declared under "Cursor publishing".
- `build_software_path(&stream::Params) -> Result<(), stream::StreamError>` constructs `self.converter` — `Converter::new(p.width, p.height, Subsampling::from(&p.chroma_subsampling), ColorMatrix::from(&p.color_space))` — and a SW `encode::Encoder` for the same params, **skipping every add-on in `self.poisoned`**.
- `reconfigure_or_rebuild(&stream::Params) -> Result<Reconfigured, stream::StreamError>` calls `update_stream_params(np)` on the active capture handle (`as_configurable()`) and encoder **and `Converter::reconfigure` on the converter**, returning `Reconfigured::Hot`; on `StreamError::RequiresRestart`, or when the add-on is not configurable at all, it tears them down and rebuilds them for `np` and returns `Reconfigured::Rebuilt`. The converter is not optional in that list: it is the component that owns output geometry and colour on the software path, and leaving it out is what let the encoder and the advertised `config` disagree.
- `reconfigure_capture(&stream::Params)` calls `update_stream_params` through `as_configurable()` when the handle reports `CONFIGURABLE`, else tears the capturer down and reconstructs it from the same add-on with a `CaptureConfig` built from `p`.
- `sw_candidate_supports_chroma(&str) -> bool` reads the SW candidate's cached probe capabilities.
- `restart(Component)` rebuilds the SAME add-on, fresh instance. `open_component(Component, &str)` constructs a named candidate from the plan.
- `active_codec() -> String` returns the active encoder's `codec()` string; `current_config() -> protocol::ConfigBase` builds the session-independent config from `self.params`, `self.cursor_mode`, `active_codec()` and the borrowed `Option<AudioDescriptor>` — it fills `audio`, `audioCodec`, `audioSampleRate`, `audioChannels`, `audioLayout` and `audioDescription` from the descriptor and blanks all five (with `audio: false`) on `None`, which is what makes them go empty when the audio path retires; `applied_now(ok: bool) -> stream::Applied` packages the same values for the watch (callers that are a param outcome pass their own result; the restart rung passes `self.last_applied_ok`, since a backend swap is not a verdict on the last parameter change) and fills `caps: Some(..)` by re-reading `params_capability()` through each live handle's `as_configurable()` accessor — its three callers — `degrade_to_software`, `fall_through` and the restart-success rung of `record_error` — are each a point where the live backend instance changes, and that `Applied` is the ONLY way `Manager::apply` learns the new backend's bounds and `hot_changeable` map.
- `idle_keyframe() -> Option<stream::EncodedFrame>` — the idle-screen keepalive, below.

```rust
enum Reconfigured { Hot, Rebuilt }
```

> **Frame-drop semantics: pull-latest source assumed.** The skip-a-capture strategy assumes the capturer is a **pull-latest** source: a call to `next_frame` / `next_surface` always returns the CURRENT framebuffer, so skipping cleanly drops stale frames. This is true for KMS+EGL (Linux), ScreenCaptureKit (macOS), and DXGI Desktop Duplication (Windows). Pipe-based subprocess capturers (X11grab, ffmpeg-based) were rejected from the architecture.

> **Idle-screen keyframe keepalive.** `force_keyframe()` is consumed *before the
> next `encode()`*, so on a static screen — where the capturer returns `Ok(None)`
> for seconds at a time — a forced IDR would never be produced, and a joining
> client would time out waiting for one (MODULE_SERVER "Keyframe Caching
> Strategy"). On the **software** path the frame loop therefore retains the most
> recent captured frame in `last_capture: Option<capture::Frame>` and, when an
> IDR has been pending for `[stream] idle_keyframe_ms` (default 100) with no new
> capture, `idle_keyframe()` re-converts and re-encodes it and returns it to step
> (4) for broadcast. The encoder was already armed by step (3)'s
> `force_keyframe()`, so that re-encode IS the IDR — no second force is needed,
> and on the opt-in `x264` bridge no second child respawn. Exactly one such frame
> is emitted per pending force: `idr_pending_since` is cleared by the broadcast
> like any other keyframe, so an idle desktop with N joins costs N IDRs, bounded
> by the same 500 ms coalescer. With no `last_capture` yet (nothing has ever been
> captured) the request is left pending and the first real frame satisfies it.
>
> On the **zero-copy** path nothing is retained: an `FbInfo` is released in the
> iteration that produced it (see "Thread and handle ownership"), and holding one
> across iterations would outlive the display that made it. `idle_keyframe()`
> therefore returns `None` there, the request stays pending, and the first
> surface the compositor produces satisfies it. Until then the joining client is
> served the server's cached IDR — a complete, decodable picture that is at most
> one repaint old, not a broken one.

### Sustainable-rate control

`params.fps` is the **advertised and effective** rate: it drives the loop's
pacing, the encoder's rate control and GOP length, and `config.fps`. A host that
cannot sustain it must lower it, not silently under-deliver — otherwise every
downstream component is calibrated for a rate that does not exist, and the
adaptive loop (which watches only the network) is blind to CPU overload.

The controller runs entirely inside the frame loop — no new channels, no new
cross-module wiring — against `fps_ceiling`, the rate the operator or the client
asked for:

- Every **60 completed frames**, compute the p95 of `frame_start.elapsed()` from
  the existing 60-sample `RollingStats` window.
- **Down:** if `p95 > target_interval` for **2 consecutive windows**, set
  `fps_effective = clamp(1s / p95, 5, self.fps_ceiling)` rounded DOWN to the
  nearest of `{60, 50, 48, 40, 30, 25, 24, 20, 15, 10, 5}` and apply it.
- **Up:** if `p95 < 0.7 * target_interval` for **5 consecutive windows** and
  `params.fps < fps_ceiling`, step UP one entry in that ladder and apply it. One
  step at a time, so a host near the boundary oscillates by one ladder entry
  rather than between 60 and 15.
- **Apply** by building `let mut np = self.params.clone(); np.fps = fps_effective;`
  and calling `self.apply_params(np)` — the same single mutation path
  everything else uses. An fps-only change sends a fresh `config` and does **not**
  force an IDR.
- Log each change once at `info` with the p95 that caused it.
- **5 fps is the real floor**, and it is a floor on the *frame rate* — the value
  `max_skip` was misdescribed as bounding.

`set_fps` and the `[stream] fps` default set `fps_ceiling`, never `params.fps`
directly; the controller then finds the sustainable rate under it. On the Linux
software path this is what turns "advertises 60, delivers 20" into "advertises
and delivers 20", with no per-add-on fps table to maintain and no probe metadata
to plumb.

### Idle suspension

With no authenticated session there is nobody to send to, and the frame loop
would otherwise pace, capture, convert, encode and broadcast into empty rings
forever. Two stages, deliberately unequal in cost and in risk (GAP_TRIAGE
OQ-04):

| | Mechanism | Frees | Wake cost | Default |
|---|---|---|---|---|
| **Stage 1** | The loop parks on `wake.notified()` at step (0-) instead of `sleep_to_interval` | encode work, colour conversion, readback bandwidth | sub-frame; every object stays alive | **always on** |
| **Stage 2** | After `[capture] idle_release_after` of continuous idle, drop the capturer and encoder; rebuild via `FrameLoop::open()` on wake | the GPU encode session, the DXGI duplication handle, the IddCx virtual display | a full `FrameLoop::open()` | **off** (`"0s"`) |

Stage 1 is unconditional because it has no failure mode: the handles are
untouched, so waking is just resuming a loop. Stage 2 releases real OS and GPU
resources and therefore has to be asked for.

**Counting rule — authenticated sessions only.** The gate reads
`authed_clients`, fed by `set_session_count_callback`
([`MODULE_SERVER.md`](./MODULE_SERVER.md) steps 12b and 21). It is **not**
`Server::client_count()`, which counts connected-but-possibly-unauthed sessions:
gating capture on accepted sessions would make an unauthenticated TCP connection
a capture-start primitive, which is a denial-of-service lever and a privacy one
(an unauthenticated peer could start the host capturing its screen).

**Stage 2 fires on the 0→1 transition and no other.** `maybe_release_idle()`
releases only while `authed_clients == 0` and `idle_since` is older than
`idle_release_after`, sets `released = true` and calls
`Stats::record_idle_release()`; `on_idle_wake()` rebuilds via `FrameLoop::open()`
only when `released` is true, and clears it.
A rebuild on any *other* transition — a second viewer joining, a controller
handover — is TD-26 ("a join never restarts the capturer") walking back in, and
it is the one regression this feature can cause. The 1→2 case never reaches this
code at all, because the loop is not parked.

Two interactions must hold or the feature regresses something else:

- **Sustainable-rate control is RESET across a pause, never fed by it.**
  The controller lowers `params.fps` toward the 5 fps floor from the observed
  p95 frame time ("Sustainable-rate control" above). A parked loop produces no
  frames, so feeding the gap to the controller — or simply leaving its window
  populated with pre-idle samples — makes an idle host wake up advertising
  5 fps to the very first viewer. `on_idle_wake()` therefore clears the p95
  window and restores `params.fps` to `fps_ceiling`, so the controller
  re-derives the sustainable rate from post-wake evidence only.
- **Pacing restarts, it does not catch up.** `on_idle_wake()` sets
  `last_frame_t = Instant::now()` and zeroes `skip_budget`. Without this the
  overrun arithmetic at step (1) sees an arbitrarily long interval, computes a
  large `owe`, and burns the first frames after every wake as "skipped".

The first-frame-after-idle case needs no new machinery: the cached IDR is stale
by definition, so the existing join path (`MODULE_SERVER` step 14) forces a
fresh one within `idle_keyframe_ms + join_idr_timeout`.

**Observability.** `featherdesk_capture_idle` (gauge, `Stats::set_idle`) is 1
while parked, and `featherdesk_idle_releases_total` counts Stage 2 releases. An
idle host that shows 0 for the gauge is a wake that never parked — the bug this
metric exists to catch.

### Audio Loop (Separate Thread)

```rust
// The audio loop runs on its own dedicated OS thread (`fd-audio`) for the same
// two reasons the frame loop does: the capture backend is thread-affine (WASAPI
// requires COM on the calling thread; CoreAudio prefers a stable one), and a
// blocking `next_chunk()` must not occupy a Tokio worker. It CONSTRUCTS its
// objects on this thread and drops them here, so nothing audio-related ever
// crosses a thread boundary.
//
// THE THREAD HAS TWO HALVES, and only the playback half is on the A/V-sync path:
//
//   playback (host->client):  AudioCapturer::next_chunk -> AudioEncoder::encode
//                             -> Server::broadcast_audio   [the master clock]
//   mic      (client->host):  mic packet channel (fed by set_mic_callback)
//                             -> AudioEncoder::decode -> resample to
//                                AudioSink::format() -> AudioSink::write_chunk
//
// They share the thread and the codec add-on; they share no clock. The mic half
// is drained on the same loop iteration as the playback half, AFTER it, and is
// strictly best-effort: a slow or failing mic must never delay the master clock,
// because a stalled master clock drops the whole session out of audio-master
// (see "Leaving audio-master"). If the mic channel is empty the half is a no-op;
// if it is backed up, the OLDEST packets are dropped and counted -- mic audio is
// realtime, so a backlog is worthless by the time it would play.
//
// When no session holds the controller slot the mic half writes SILENCE at the
// sink's period rather than nothing: a starved endpoint is dropped by the OS
// audio engine, and repeating the last buffer would leak the previous
// controller's audio (MODULE_AUDIO "Role, rate and privacy").
//
// There is no host-side channel between capture and encode: the ~60-80 ms
// decoupling buffer lives INSIDE the add-on (its OS device callback fills a
// 4-frame drop-oldest ring that `next_chunk` pops), and the per-session
// `audio_out` ring absorbs downstream congestion. Adding a third queue between
// them would only add latency.
impl AudioLoop {
    /// Constructs the audio capturer and then the audio encoder ON THIS THREAD,
    /// from `self.plan` — `plan.capturer_id`, then `plan.codec_id` (`None` =
    /// PCM passthrough) — using `plan.cfg`. Called exactly once, as the first
    /// statement of `run`; startup step 11 SELECTED them and constructed
    /// neither. On `Ok` both `Option` fields are `Some`; on `Err` neither is
    /// and `run` retires audio for the session.
    fn open(&mut self) -> Result<(), PipelineError>;

    /// `run` exists to make the `None` publish unmissable: EVERY exit path other
    /// than a cancelled shutdown goes through the one `send(None)` below, so the
    /// next path added cannot forget it. All the work is in `body`.
    fn run(mut self, cancel: CancellationToken) -> Result<(), PipelineError> {
        let r = self.body(&cancel);
        // Leaving for any reason other than shutdown retires audio for the
        // session: publish `None`, so the frame loop's next tick pushes a fresh
        // `config` with `audio: false` (and `audioCodec: ""`, `audioChannels: 0`,
        // `audioDescription: ""`). Audio does not come back within a session —
        // re-enabling it requires a restart, exactly like a poisoned capture
        // add-on.
        if !cancel.is_cancelled() {
            tracing::warn!(target: "pipeline", "audio path ended; continuing video-only for this session");
            let _ = self.audio_desc_tx.send(None);
        }
        r
        // Epilogue: fields drop in declaration order — encoder, capturer, then the
        // Arc<AddonRegistry> clone, so the library outlives both.
    }

    fn body(&mut self, cancel: &CancellationToken) -> Result<(), PipelineError> {
        if let Err(e) = self.open() {           // builds capturer + encoder ON THIS THREAD
            tracing::warn!(target: "pipeline", err = %e, "audio unavailable; continuing video-only");
            return Ok(());                       // audio is never fatal to the session
        }
        let (capturer, encoder) = (self.capturer.as_mut().unwrap(), self.encoder.as_mut().unwrap());
        let codec_type = audio_codec_type(encoder.codec()); // "opus"→0x08, "pcm/s16le"→0x04

        // The frame loop learns the five mandatory `protocol::ConfigBase` audio
        // facts from HERE and nowhere else — the two objects that produce them
        // live behind `&mut` on this thread.
        let _ = self.audio_desc_tx.send(Some(AudioDescriptor {
            codec:       encoder.codec().to_string(),
            sample_rate: capturer.format().sample_rate,
            channels:    capturer.format().channels,
            layout:      capturer.format().layout,
            description: encoder.description().into_vec(),
        }));

        while !cancel.is_cancelled() {
            // next_chunk BLOCKS until a chunk is ready or its deadline elapses
            // (≤ 200 ms, MODULE_AUDIO), and returns Ok(None) at end of stream.
            let chunk = match capturer.next_chunk() {
                Ok(Some(c)) => { self.ladder.ok(); c }
                Ok(None) => break,                       // capturer stopped
                Err(audio::AudioError::Unrecoverable(reason)) => {
                    // The add-on has exhausted its OWN ladder. Never retried.
                    tracing::error!(target: "pipeline", reason = %reason, "audio capture unrecoverable");
                    break;
                }
                Err(e) => {
                    // The transient rung, on the SAME rules the frame loop uses: one
                    // decayed consecutive counter, a 20 ms backoff and a cap. A bare
                    // `continue` would burn a core and emit one `warn!` per iteration
                    // for the life of the process on an unplugged device — exactly the
                    // shape "Add-On Crash Recovery" forbids.
                    let now = Instant::now();
                    self.stats.record_audio_drop();
                    self.ladder.decay(now);
                    self.ladder.consecutive += 1;
                    self.ladder.last_error_at = Some(now);
                    if self.ladder.consecutive >= 10 {
                        tracing::error!(target: "pipeline", err = %e, "audio error ladder gave up");
                        break;
                    }
                    tracing::warn!(target: "pipeline", err = %e, "audio capture");
                    std::thread::sleep(Duration::from_millis(20));
                    continue;
                }
            };
            match encoder.encode(&chunk) {               // Opus packet OR PCM passthrough
                Err(audio::AudioError::Unrecoverable(reason)) => {
                    tracing::error!(target: "pipeline", reason = %reason, "audio encode unrecoverable");
                    break;
                }
                Err(e) => {
                    // The same transient rung as the capture arm above — one ladder
                    // for the whole audio path, not one per half.
                    let now = Instant::now();
                    self.stats.record_audio_drop();
                    self.ladder.decay(now);
                    self.ladder.consecutive += 1;
                    self.ladder.last_error_at = Some(now);
                    if self.ladder.consecutive >= 10 {
                        tracing::error!(target: "pipeline", err = %e, "audio error ladder gave up");
                        break;
                    }
                    tracing::warn!(target: "pipeline", err = %e, "audio encode");
                    std::thread::sleep(Duration::from_millis(20));
                    continue;
                }
                Ok(payload) => {
                    // chunk.timestamp_ns was sampled at CAPTURE time in the add-on's
                    // read loop, on the SAME CLOCK_MONOTONIC epoch as video. Do NOT
                    // re-stamp here.
                    self.stats.record_audio_chunk();
                    self.server.broadcast_audio(codec_type, payload.into_vec().into(), chunk.timestamp_ns);
                }
            }
        }
        Ok(())
    }

}
```

**Critical:** the audio timestamp comes from `chunk.timestamp_ns` (capture-time, monotonic), never from `Instant::now()` at this point. Re-stamping here would add up to ~640 ms of channel-buffer skew and break A/V sync (this was the original bug).

### Resolution-Change Handling

The pipeline owns the resolution-change orchestration (no other module drives it).
Both triggers funnel through the **single** `apply_params` path (defined above),
so there is exactly one place that mutates the encoder/capturer:

```rust
// Capture-detected change: the frame path observed UPRIGHT capture geometry
// differing from `FrameLoop.capture_dims` — never from the OUTPUT dims, which are
// post-downscale and would compare equal whenever a downscale absorbs the change,
// and with no downscale gate, since a capture change matters just as much when one
// is configured. The two comparison sites (one per frame path, right after
// `record_capture()`) are the only callers. This runs ON
// the frame-loop thread already — it is called from inside `capture_encode` — so
// it applies the change directly instead of round-tripping through the funnel.
// Routing it through the channel would make a SECOND producer, which is exactly
// what makes whole-snapshot lost updates possible.
fn on_capture_dims_changed(&mut self, new_w: u32, new_h: u32) {
    if let Some(c) = self.cursor.as_mut() { c.set_capture_dims((new_w, new_h)); }
    let mut np = self.params.clone();
    np.width = new_w;
    np.height = new_h;
    self.apply_params(np); // the funnel carries no reply channel — see
                           // "param_failure_feedback"; nobody asked for this
}

// Client-requested changes (resize / set_bitrate / set_fps / set_hdr) arrive as
// stream::ParamDelta through stream::Manager, the funnel's SOLE producer, and are
// folded into one apply_params at step (0) of the frame loop.
```

A display **rotation** change is a resolution change: the upright dims transpose
and arrive here like any other (see MODULE_CAPTURE "Display rotation").

`apply_params` resizes input, and sends a fresh `{"type":"config"}` line and
forces a keyframe **only when the change requires each** (see its two gates
above). HDR requested with no HEVC/10-bit encoder is rejected there: the pipeline
calls `Server::send_control(Recipients::Every, ControlMessage::HdrUnavailable {
reason: NoHevcEncoder })`, the client receives `{"type":"hdr_unavailable"}` on
the control stream, and the stream stays SDR (see
[`MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md)).

Invariant: **encoder-output dims == config dims == input-coordinate range.** The
Converter absorbs any native→output scaling on the software path (rotate →
convert → `I4xxScale`, retargeted by `reconfigure_or_rebuild` on every Params
change) and the encoder's VPP/scaler does on the hardware path; this keeps the
client's absolute mouse mapping pixel-accurate.

### Thread and handle ownership

Every handle in the process belongs to exactly one of four threads for its whole
life. "Created" is where the constructor runs; "Used" is where every method call
runs; "Dropped" is where the destructor runs. They are the same thread for every
row that owns OS or GPU state — that is the point.

| Handle class | Created | Used | Dropped |
|---|---|---|---|
| dlopen'd libraries (`AddonRegistry`) | startup thread, step 3a | vtable dispatch only | startup thread, in `start()`'s epilogue, **after every worker has joined** |
| add-on factories (`Box<dyn CaptureAddon>` …) | startup thread, step 3a | any thread (`&self`, `Send + Sync`) | with the registry |
| `transport::Transport` | startup thread, step 6b | server task | with the server (it is a field of `server::Config`) |
| `Arc<dyn server::Server>` | startup thread, step 7 | every thread (`&self`) | startup thread, when the last clone drops in the epilogue |
| `Box<dyn capture::CaptureHandle>` (DRM/EGL/GBM, IDXGIOutputDuplication, SCStream, XFixes) | **frame thread**, `FrameLoop::open()` | **frame thread** | **frame thread**, `FrameLoop` epilogue |
| `capture::FbInfo` / `SurfaceHandle` (DMA-BUF fd, IOSurface, ID3D11Texture2D) | frame thread, per frame | frame thread | frame thread, inside the same loop iteration — the loop never holds an `FbInfo` across iterations, so no surface can outlive the display that produced it |
| `encode::Converter`, `EncoderHandle`, `HwEncoderHandle` (VA display, D3D11 device, VideoToolbox session, `ffmpeg` child) | **frame thread**, `FrameLoop::open()` or a swap | **frame thread** | **frame thread**, `FrameLoop` epilogue |
| `CursorPublisher` | frame thread, `FrameLoop::open()` | frame thread | frame thread, `FrameLoop` epilogue (owns no OS handle) |
| `Box<dyn audio::AudioCapturer>` / `AudioEncoder` (COM apartment, AudioUnit, PipeWire loop) | **audio thread**, `AudioLoop::open()` | **audio thread** | **audio thread**, `AudioLoop` epilogue |
| `Arc<Mutex<Box<dyn input::Dispatcher>>>` + injectors (uinput fd, Interception context, ViGEm bus) | startup thread, step 8 | N session tasks (`dispatch`) + frame thread (`resize`), serialized by the mutex | startup thread, epilogue — the pipeline's clone; `Drop` and `release_all` run at the server drop (step 9) |
| clipboard driver + its OS pump (message-only `HWND`, `XOpenDisplay`) | the driver's own `std::thread`, created by `clipboard::spawn` | that thread | that thread, on `WM_QUIT` / connection close |
| `Arc<FrameOut>` rings | per session, on the session task | frame thread (push) + pump task (pop) | with the `Session` |

**Drop order rules, in force on every path:**

1. **Surfaces before the display that made them.** An `FbInfo` is consumed by
   `encode_surface` in the iteration that produced it; the loop holds none across
   iterations, and `FrameLoop`'s epilogue therefore has no surface to release.
2. **Encoder before capturer.** The encoder may hold an imported DMA-BUF /
   texture from the capturer. `FrameLoop`'s fields are declared in exactly this
   order — `encoder`, `hw_encoder`, `converter`, `cursor`, `capture`, … ,
   `registry` — and Rust drops fields in declaration order, so the order is
   enforced by the struct rather than by a checklist.
3. **Cursor before capturer** is no longer a handle-release question: the cursor
   query is a method on the one capture object, not a separate owner. The
   `CursorPublisher` holds no OS handle and is dropped first only so the
   declaration order reads as the teardown order.
4. **The library outlives every object from it.** `Arc<AddonRegistry>` is the LAST
   field of `FrameLoop`, of `AudioLoop`, and of `Pipeline`, so a library is only
   unloaded after every object whose vtable points into it has been dropped.
5. **Join before drop.** `start()` joins the frame and audio threads before
   dropping anything they might still touch. Nothing that a worker owns is
   dropped from the async context.

### Shutdown Sequence

```
1. CancellationToken fires (signal handler, explicit cancel, or a frame-loop
   terminal error, which cancels the token on its way out).
2. Frame thread: finishes at most the frame in flight, then runs its epilogue —
   drop encoder → hw_encoder → converter → CursorPublisher → capture handle →
   its Arc<AddonRegistry> clone (field declaration order) — and returns
   Result<(), PipelineError>. The capture handle is the single owner of the
   add-on's display/duplication handles, including the cursor query's, so there
   is nothing else to order against.
3. Audio thread: same shape (drop encoder → capturer → registry clone).
3b. Clipboard task exits on its cancel branch; its `changes()` watch receiver is
    dropped, so no further `send_clipboard`. The driver posts WM_QUIT / closes its
    X connection and joins its own OS thread before returning.
4. `server.start()` returns: it sends {"type":"server_shutdown"} to every
   session, waits [server] shutdown_grace_ms (250) for the control lines to
   flush, closes every session with close::SERVER_SHUTDOWN (4503), and completes
   graceful HTTP shutdown (5 s cap) on both listeners.
5. `start()` joins the frame thread, then the audio thread, each with the 2 s
   graceful-shutdown budget. A worker that overruns is reported as
   PipelineError::ShutdownTimeout; a panicked worker as WorkerPanic. Neither is
   swallowed.
6. Await the clipboard task.
6b. Await the `fd-config` applier task (step 12b): it exits on its cancel branch,
    so no applier can run against a half-torn-down handle.
7. Drop the pipeline's `Arc` clone of the input Dispatcher. This is a refcount
   decrement: the server holds the clones captured by `set_input_callback` and
   `set_controller_change_callback`, so `Dispatcher::Drop` — and the
   `release_all` that releases every held key, button and touch contact — runs
   at step 9, when the last `Arc<dyn Server>` is dropped.
8. Drop the ClipboardHandle, then the filetransfer Service (Drop drains
   in-flight, fsyncs, removes orphan .part files).
9. Drop the last Arc<dyn Server>, which drops server::Config.transport and closes
   both the TCP listener and the UDP socket — and, with the server's callbacks,
   the last Dispatcher clones; this is where `Dispatcher::Drop` calls
   `release_all`.
10. Drop Arc<AddonRegistry>: the dlopen'd libraries are unloaded last,
    unconditionally.
11. Print final statistics (from the Arc<Stats> handle taken before start()).
12. Exit with the first error among frame / audio / server, or 0.
```

> Cleanup is RAII: each component releases its resources in its `Drop` impl
> (there is no explicit `Close()`). Drop **order** is enforced structurally — each
> worker struct declares its fields in teardown order and Rust drops fields in
> declaration order — and by the explicit `drop(...)` calls in `start()`'s
> epilogue for the shared handles. Never rely on incidental scope exit.

---
## Capability Probing

The pipeline iterates over the add-ons the host loaded from the add-ons
directory (each a shared library `dlopen`'d at startup; see CENTRAL_SPEC
"Add-on loading model") and asks each one to probe its prerequisites. There is no
fixed `SystemCapabilities` struct — the set of probes is determined by which
add-on libraries are present in the directory.

```rust
// The dlopen loader populates the registry at startup: it scans the add-ons
// directory, loads each library, calls init(HostServices), checks its
// abi_stable version + layout, and adapts its exported #[sabi_trait] object into
// the per-kind traits below (keyed off the capability descriptor's `kind`). The
// registry holds only add-ons whose library was present AND ABI-compatible. All
// eight descriptor kinds have a home: capture, encode/hwencode (both
// EncoderAddon, distinguished by kind()), the three input kinds (all in
// `inputs`, distinguished by InputAddon::kind()), and audio (capture + the opus
// codec).
pub struct AddonRegistry {
    pub captures: Vec<Box<dyn CaptureAddon>>,
    pub encoders: Vec<Box<dyn EncoderAddon>>,
    pub inputs: Vec<Box<dyn InputAddon>>, // injection OVERRIDE add-ons (uinput, interception, win_touch,
                                          // vigem, gcvirtual), one flat vec; step 8 selects by
                                          // InputAddon::kind(). The kb/mouse default is the in-core
                                          // enigo injector.
    pub audio: Vec<Box<dyn AudioAddon>>,  // audio capture add-ons + the opus codec add-on
}

pub trait CaptureAddon: Send + Sync {
    fn name(&self) -> &str; // add-on ID (e.g. "kms_egl", "dxgi_dd")
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    /// Constructs the ONE capture object and wraps it in the single owning
    /// handle. `caps` comes from the cached `ProbeResult`; the adapter stores it
    /// so `CaptureHandle::caps()` can answer without a second probe, then
    /// narrows it to the constructed object's own `caps()`. MUST be called on
    /// the frame-loop thread (see "Thread and handle ownership").
    fn open(&self, cfg: capture::CaptureConfig) -> Result<Box<dyn capture::CaptureHandle>, PipelineError>;
}

pub trait EncoderAddon: Send + Sync {
    fn name(&self) -> &str; // add-on ID (e.g. "nvenc", "openh264", "x264")
    fn kind(&self) -> &str; // "hw" or "sw"
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    // Exactly one constructor is valid per kind() — the other returns
    // PipelineError::AddonBackend. Both MUST be called on the frame-loop thread.
    fn new_sw(&self, cfg: encode::EncoderConfig) -> Result<Box<dyn encode::EncoderHandle>, PipelineError>;          // SW add-ons only
    fn new_hw(&self, cfg: hwencode::HWEncoderConfig) -> Result<Box<dyn hwencode::HwEncoderHandle>, PipelineError>;  // HW add-ons only
}

pub trait InputAddon: Send + Sync {
    fn name(&self) -> &str;      // add-on ID (e.g. "uinput", "interception", "vigem")
    fn kind(&self) -> InputKind; // from the capability descriptor: InputKeyMouse (0x06)
                                 //   | InputTouch (0x07) | InputGamepad (0x08)
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    // Exactly one constructor is valid per kind() — the other two return
    // PipelineError::AddonBackend. `Injector` is the Layer-1 sabi trait, not a
    // Layer-2 trait, so it cannot be a return type here.
    fn new_key_mouse(&self, cfg: input::InjectorConfig) -> Result<Box<dyn input::KeyMouseInjector>, PipelineError>; // kind == KeyMouse
    fn new_touch(&self, cfg: input::InjectorConfig)     -> Result<Box<dyn input::TouchInjector>, PipelineError>;    // kind == Touch
    fn new_gamepad(&self, cfg: input::InjectorConfig)   -> Result<Box<dyn input::GamepadInjector>, PipelineError>;  // kind == Gamepad
}

#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum InputKind { KeyMouse, Touch, Gamepad }

pub trait AudioAddon: Send + Sync {
    fn name(&self) -> &str; // add-on ID (e.g. "pipewire", "wasapi", "sck_audio", "opus")
    fn kind(&self) -> &str; // "capture" (AddonKind::AudioCapture) | "codec" (AddonKind::AudioCodec)
    fn probe(&self) -> Result<ProbeResult, PipelineError>;
    // Exactly one constructor is valid per kind() — the other returns PipelineError.
    // Both MUST be called on the audio thread.
    fn new_capturer(&self, cfg: audio::AudioConfig) -> Result<Box<dyn audio::AudioCapturer>, PipelineError>; // kind=="capture"
    fn new_codec(&self, cfg: audio::AudioConfig) -> Result<Box<dyn audio::AudioEncoder>, PipelineError>;     // kind=="codec"
}

pub struct ProbeResult {
    pub available: bool,
    pub reason: String,                  // human-readable explanation if !available
    pub codecs: Vec<abi::CodecId>,       // what this backend can emit/consume; empty for capture + input
    pub caps: abi::AddonCaps,            // copied VERBATIM from ProbeReport.caps — the adapter never
                                         // masks, filters or re-encodes it, and unknown bits are
                                         // preserved (MODULE_ABI "Optional-method capability flags")
    pub displays: Vec<abi::DisplayInfo>, // capture add-ons only; empty otherwise
    pub details: HashMap<String, serde_json::Value>, // adapter-authored metadata for LOGS and METRICS
                                         // only. It is NEVER a channel for caps, codecs or displays —
                                         // those are typed above. A host that reads a capability out
                                         // of `details` is a bug.
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
probe order in startup steps 3d/3e, which the per-platform `capture/README.md`
and `encoders/README.md` restate.

There are **no probes for rejected subsystems**: the ffmpeg-`h264_vaapi`
subprocess capture path and the Mutter/GNOME-screencast D-Bus capture path were
removed from the architecture along with their prerequisites, so nothing probes
for them. `pipewire` and `uinput` are not in that set — they are the IDs of an
audio capture add-on and an input injection add-on, and each is probed like any
other add-on.

---

## Cursor publishing

`CursorPublisher` is the sole owner of everything between
`CursorCapturer::next_cursor` and the two server sends. Nothing else converts
cursor coordinates, computes a ShapeID, or decides when to re-send. It holds no
OS handle, so it is safe to drop at any point in the shutdown sequence.

`CursorMode` — the RESOLVED mode, its truth table, and the one frame-loop entry
point that re-runs it — is declared here too, because the publisher's existence
is the decision's only consequence.

```rust
/// How the pointer reaches the client for THIS session. Two inhabitants only:
/// it is the RESOLVED answer, never the `[capture] cursor_mode` policy, which has
/// three values ("auto" | "separate" | "embedded"). `as_str()` is what goes into
/// `protocol::ConfigBase.cursorMode` and `stream::Applied.cursor_mode`.
#[derive(Copy, Clone, PartialEq, Eq, Debug)]
pub enum CursorMode { Separate, Embedded }

impl CursorMode {
    /// The ONE implementation of MODULE_CAPTURE "Cursor delivery and the
    /// `separate`/`embedded` decision" truth table. Pure — no `self`, no I/O, no
    /// config parsing beyond the three literals.
    ///
    /// `policy` is the raw `[capture] cursor_mode` value. `caps` is whichever
    /// capability set the caller is deciding against: `ProbeResult.caps` at startup
    /// step 3d (selection — the host holds the Layer-2 `ProbeResult`, whose `caps`
    /// is copied VERBATIM from `ProbeReport.caps`), and the CONSTRUCTED handle's
    /// authoritative `caps()` every time after that.
    ///
    /// `None` means "this add-on cannot satisfy this policy". At step 3d that makes
    /// the candidate INELIGIBLE and it is skipped in the dispatch order exactly like
    /// one whose probe reported `available = false`. After construction it is a
    /// capability lie (MODULE_ABI "Misbehaving add-ons"). A `policy` string outside
    /// the three documented values is also `None`; config validation makes that
    /// unreachable.
    ///
    ///   "auto"     + CURSOR set                     -> Some(Separate)
    ///   "auto"     + CURSOR clear, EMBED_CURSOR set -> Some(Embedded)
    ///   "auto"     + neither                        -> None
    ///   "separate" + CURSOR set                     -> Some(Separate)
    ///   "separate" + CURSOR clear                   -> None
    ///   "embedded" + EMBED_CURSOR set               -> Some(Embedded)
    ///   "embedded" + EMBED_CURSOR clear             -> None
    pub fn resolve(policy: &str, caps: abi::AddonCaps) -> Option<CursorMode>;

    /// `CaptureConfig.embed_cursor` for this mode — `true` for `Embedded`, `false`
    /// for `Separate`. This is the ONLY expression in the tree that derives
    /// `embed_cursor`; nothing else computes it and there is no per-add-on key.
    pub fn embed_cursor(self) -> bool;

    /// The exact `config.cursorMode` wire spelling: "separate" | "embedded".
    pub fn as_str(self) -> &'static str;
}

impl FrameLoop {
    /// Re-runs `CursorMode::resolve` against the CURRENT `[capture] cursor_mode`
    /// policy and the CONSTRUCTED capture handle's authoritative `caps()`, then makes
    /// the pipeline match the answer. Returns the mode now in force so the caller can
    /// compare it with the previous one; it never sends `config` itself. This is the
    /// ONLY re-resolution entry point — there is no `CursorMode::from_config`.
    ///
    /// 1. `CursorMode::resolve(policy, self.capture.as_ref().unwrap().caps())`. `None`
    ///    means the running capturer can no longer satisfy the policy: the mode in
    ///    force is kept unchanged, one `warn!` fires, and the caller pushes no
    ///    `config` because nothing changed.
    /// 2. If `want.embed_cursor() != self.cursor_mode.embed_cursor()`, rebuild the
    ///    capture add-on with a `CaptureConfig` carrying the new `embed_cursor`. That
    ///    is the ONLY thing that rebuilds the capturer here.
    /// 3. `self.cursor = Some(CursorPublisher::new(..))` when `want` is `Separate`;
    ///    when it is `Embedded`, `self.cursor = None` AND the `CURSOR` capability bit
    ///    is cleared on the handle, so `as_cursor()` returns `None` and frame-loop
    ///    step (0b) is skipped by construction rather than by a flag.
    /// 4. `self.cursor_mode = want`, and return it.
    fn resolve_cursor_mode(&mut self, policy: &str) -> CursorMode;
}

// featherdesk-host/src/pipeline/cursor.rs
pub struct CursorPublisher {
    stats: std::sync::Arc<Stats>,                  // record_cursor_update / record_cursor_shape
    capture_w: u32,
    capture_h: u32,
    stream_w: u16,
    stream_h: u16,
    last: Option<protocol::CursorUpdate>,          // most recent update sent; None until the
                                                   //   first tick that carries a CursorState
    active_shape: Option<protocol::CursorShapeRecord>, // record for last.shape_id; None until then
    last_change: Instant,
    idle_resends: u8,
}
// There is deliberately NO `seq` field. `DatagramHeader.FrameID` for CURSOR_UPDATE
// is assigned by the SERVER inside `send_cursor`; `protocol::CursorUpdate` has no
// sequence field and `send_cursor` has no parameter for one, so a publisher-side
// counter has no route to the wire.

impl CursorPublisher {
    /// `capture_dims` are the capturer's native dimensions. `last` and
    /// `active_shape` start `None` and are filled by the first `tick` that carries
    /// a `CursorState` — guaranteed to be the first poll after the capturer is
    /// constructed (`CursorCapturer::next_cursor`, first-call rule).
    pub fn new(capture_dims: (u32, u32), params: &stream::Params, stats: std::sync::Arc<Stats>) -> Self;

    /// Called on EVERY frame-loop tick with the capturer's reply, before the skip
    /// check. For a tick carrying `Some(state)`, in this order:
    ///   1. if `state.shape` is `Some` and its bitmap exceeds the 128-pixel wire
    ///      cap, downscale it HERE — the one and only producer-side clamp in the
    ///      tree (see the wire-clamp rule below the struct);
    ///   2. convert capture pixels to stream pixels (MODULE_CAPTURE "Cursor
    ///      coordinate space");
    ///   3. hash the ShapeID over the POST-clamp canonical bytes (MODULE_CAPTURE
    ///      "Shape identity");
    ///   4. `server.send_cursor_shape(..)` for a shape, THEN `server.send_cursor(..)`
    ///      — always that order, on the same call;
    ///   5. cache the update in `last` and the record in `active_shape`, and reset
    ///      the idle re-send counter.
    /// A tick carrying `None` applies only the idle re-send rule.
    pub fn tick(
        &mut self,
        observed: Option<capture::CursorState>,
        now: Instant,
        server: &dyn server::Server,
    );

    /// Called from `apply_params` when the stream dimensions changed. Recomputes the
    /// active shape's `DrawW`/`DrawH`/`HotspotX`/`HotspotY` for the new stream dims
    /// under the SAME ShapeID (the bitmap and its content hash did not change), then
    /// re-emits it through `server.send_cursor_shape` followed by `server.send_cursor`
    /// with the new `StreamW`/`StreamH` — exactly as `tick` does. It needs no reach
    /// into per-session state: the server's dedupe key includes the draw geometry, so
    /// the re-emission is written to every session still holding the stale geometry.
    pub fn on_stream_dims_changed(&mut self, params: &stream::Params, server: &dyn server::Server);

    /// Refreshes the capture dimensions the publisher scales pointer positions
    /// against. There is exactly ONE caller: `FrameLoop::on_capture_dims_changed`
    /// (frame-loop thread), which fires when a returned `Frame` / `FbInfo`'s
    /// UPRIGHT geometry differs from `FrameLoop.capture_dims` — the comparison is
    /// written into both halves of the frame path, right after `record_capture()`.
    /// The comparand is `capture_dims`, NOT `params`: `params` is the stream
    /// geometry after downscale, and would compare equal across a capture-side
    /// change whenever the downscale absorbs it. See MODULE_CAPTURE "Rotation" for
    /// the portrait-panel case that funnels through the same path.
    ///
    /// It is deliberately NOT called on the rebuild paths, because every path that
    /// rebuilds the publisher already seeds dims through `CursorPublisher::new`:
    /// `resolve_cursor_mode` step 3 reconstructs it (and `fall_through` reaches the
    /// same code through `resolve_cursor_mode`). The capture-restart rung of
    /// `record_error` is covered too, but by the other route — it rebuilds the
    /// capturer WITHOUT reconstructing the publisher, and the restarted add-on's
    /// first frame carries its native dimensions, so the frame path's comparison
    /// against `capture_dims` fires `on_capture_dims_changed` on the next tick.
    /// That is deliberate rather than incidental: it is the same single detection
    /// site every other dimension change uses, so the restart path needs no rule
    /// of its own and cannot drift from the others.
    pub fn set_capture_dims(&mut self, capture_dims: (u32, u32));
}
```

**The 128-pixel bitmap cap is a WIRE cap, applied here and nowhere else.**
Add-ons return the OS's native bitmap at its native size and never resample
(`abi::CursorShape.width`/`height` are documented OS-native). In `tick`, when
`state.shape` is `Some` and `shape.width > 128 || shape.height > 128`,
box-filter `shape.pixels` down so the LONGER side is exactly 128 and the shorter
side is `max(1, round(shorter * 128 / longer))`, and use those as the record's
`PixelW`/`PixelH`. `screen_w`/`screen_h`/`hotspot_x`/`hotspot_y` are NOT touched
and NOT rescaled — they are capture-pixel screen-space quantities, and
`DrawW`/`DrawH`/`HotspotX`/`HotspotY` are computed from them by the "Cursor
coordinate space" formulas, so an enlarged pointer still renders at the right
on-screen size and hotspot after the bitmap is downsampled. The ShapeID is
hashed over the POST-clamp canonical bytes, so the client's shape cache stays
correct. `capture::blend_cursor` is unaffected: an add-on's embed path
composites the full-resolution shape it produced, and the publisher never sees
that path.

**Ordering within `tick`.** A shape record is always written **before** the
CursorUpdate that references it, on the same call. The shape stream is reliable
and the position is not, so this ordering can be relied on: a client that has the
CursorUpdate either already has the shape or will keep drawing its previous one
until the record arrives.

**Idle re-send.** Latest-wins is only self-correcting while updates keep coming;
the *last* update before the pointer goes still has no successor, so losing it
leaves a stale overlay until the user moves the mouse again. `tick` therefore
re-sends the cached `last` update when `next_cursor` has returned `Ok(None)` for
`250 ms`, at most **3 times** (250/500/750 ms after the last change), then stops
until the cursor changes again. Cost is 3 x 22 bytes per idle period; four
consecutive losses at even 1% packet loss is a 1-in-10^8 event. The SERVER
increments its per-type CURSOR_UPDATE counter inside `send_cursor` for a re-send
exactly as for a change, so the client's reorder check treats it as newer and
applies it; the publisher holds no sequence of its own.

**Who re-derives, and when.** The pipeline re-runs `FrameLoop::open()`'s
resolution — and only the pipeline ever does — on each of these, in this order:
build the capturer, resolve `cursorMode`, rebuild or drop `self.cursor`, push a
fresh `config` iff the resolved mode changed. Every comparison is between
RESOLVED modes, never between a policy and a mode.

| Event | Re-resolve | Notes |
|---|---|---|
| Startup (`FrameLoop::open()`, step 13) | yes | Re-verification against the CONSTRUCTED handle's `caps()`, and the only place resolution can fail: `None`, or a mode whose `embed_cursor()` differs from the value the constructor was given, is a capability lie — fall through to the next capture candidate and, if none remains, fail startup with step 3d's rejection list. Startup step 3d already made a candidate that cannot satisfy the policy ineligible. |
| Add-on crash recovery / fall-through to another add-on | yes | The new add-on's caps decide; ShapeIDs are content hashes, so they stay valid across the swap, but the cached shape and position are dropped because the new capturer's dimensions may differ. |
| `degrade_to_software` | no | Same add-on, same caps, same capture object — only the encode path changed. The pointer is unaffected. |
| `[capture] cursor_mode` hot reload (step 0d) | yes | The capturer is rebuilt only if `embed_cursor` changes value. |
| `[capture] mode` hot reload | n/a | `(restart required)` — see MODULE_CONFIG "Hot reload behavior". |
| Stream dimension change (`apply_params`) | no | `cursor_mode` is unchanged; `CursorPublisher::on_stream_dims_changed` recomputes the active shape's draw dimensions and re-emits shape + position. |
| Capture dimension change (monitor mode change) | no | `CursorPublisher::set_capture_dims`, then the next tick converts against the new ratio. |
| `next_cursor` error | yes, via `on_cursor_error` | The one path that may end up with no client-side pointer, and only when the add-on cannot embed. |

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
    client_count: AtomicU32,          // u32, matching Server::client_count()'s return type
    audio_chunks: AtomicU64,
    audio_drops: AtomicU64,
    keyframes_forced: AtomicU64,
    param_updates_dropped: AtomicU64,
    param_changes_failed: AtomicU64,
    effective_fps: AtomicU32,
    effective_bitrate_kbps: AtomicU32,
    cursor_updates: AtomicU64,
    cursor_shapes: AtomicU64,
    cursor_errors: AtomicU64,

    // Two lock-guarded fields: the ring buffer, and the poisoned-add-on set the
    // exporter turns into one gauge series per entry.
    total_frame_time: Mutex<RollingStats>,          // 60-sample sliding window
    poisoned: Mutex<std::collections::HashSet<String>>,
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
    pub client_count: u32,
    pub audio_chunks: u64,
    pub audio_drops: u64,
    pub keyframes_forced: u64,
    pub param_updates_dropped: u64,
    pub param_changes_failed: u64,
    pub effective_fps: u32,
    pub effective_bitrate_kbps: u32,
    pub cursor_updates: u64,
    pub cursor_shapes: u64,
    pub cursor_errors: u64,
    pub poisoned: Vec<(String, &'static str)>, // (addon id, component)
    pub frame_time: RollingSnapshot,           // min, max, avg, p95, p99
}

// Every catalogued series has a named writer here, and every method is O(1).
// `&self` throughout: the frame thread, the audio thread and the exporter all
// hold `Arc<Stats>` clones and none of them needs a lock except on the ring.
impl Stats {
    // ── frame thread ────────────────────────────────────────────────────────
    pub fn record_capture(&self) { /* frames_captured += 1, after a successful next_frame/next_surface */ }
    pub fn record_broadcast(&self, bytes: usize) { /* frames_encoded += 1, frames_broadcast += 1, bytes_broadcast += bytes */ }
    pub fn record_frame(&self, d: Duration) { /* lock-guarded ring append: capture→broadcast latency */ }
    pub fn record_drop(&self) { /* frames_dropped += 1 (no lock) */ }
    pub fn record_forced_keyframe(&self) { /* keyframes_forced += 1 */ }
    pub fn record_param_change_failed(&self) { /* param_changes_failed += 1 */ }
    pub fn set_effective_fps(&self, fps: u32) { /* … */ }
    pub fn set_effective_bitrate_kbps(&self, kbps: u32) { /* … */ }
    pub fn set_poisoned(&self, set: &std::collections::HashSet<String>) { /* replaces the guarded copy */ }
    /// Idle suspension (frame thread). `set_idle(true)` on park, `false` on wake.
    pub fn set_idle(&self, idle: bool) { /* capture_idle = idle as u64 */ }
    pub fn record_idle_release(&self) { /* idle_releases += 1 (Stage 2 only) */ }
    // ── CursorPublisher, on the frame thread ────────────────────────────────
    pub fn record_cursor_update(&self) { /* cursor_updates += 1, including idle re-sends */ }
    pub fn record_cursor_shape(&self) { /* cursor_shapes += 1 */ }
    pub fn record_cursor_error(&self) { /* cursor_errors += 1 */ }
    // ── audio thread ────────────────────────────────────────────────────────
    pub fn record_audio_chunk(&self) { /* audio_chunks += 1 */ }
    pub fn record_audio_drop(&self) { /* audio_drops += 1 */ }
    // ── stream::Manager, when a ParamUpdate cannot be enqueued ───────────────
    pub fn record_param_update_dropped(&self) { /* param_updates_dropped += 1 */ }
    // ── metrics exporter, at scrape time ────────────────────────────────────
    /// The named reader for `Server::client_count()`: the exporter calls
    /// `stats.set_client_count(server.client_count())` immediately before
    /// `snapshot()`, which is the one place the gauge is written and the one
    /// caller that method has.
    pub fn set_client_count(&self, n: u32) { /* … */ }
    pub fn snapshot(&self) -> StatsSnapshot { /* reads atomics + briefly locks the ring and the poisoned set */ }
}

/// RollingStats tracks min/max/avg/p95/p99 over a sliding 60-sample window.
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
    pub fn p95(&self) -> Duration { /* the sustainable-rate controller's input */ }
    pub fn p99(&self) -> Duration { /* … */ }

    /// One pass over the window under the caller's lock, producing the owned copy
    /// `StatsSnapshot` carries. The only way a `RollingStats` reaches a snapshot.
    pub fn snapshot(&self) -> RollingSnapshot { /* … */ }
}

/// An owned, lock-free copy of one `RollingStats` window. `StatsSnapshot`
/// "contains NO Mutex — safe to copy, return by value, serialize", so the
/// snapshot type, not the ring, is what a snapshot field may hold.
#[derive(Clone, Copy)]
pub struct RollingSnapshot {
    pub min: Duration,
    pub max: Duration,
    pub avg: Duration,
    pub p95: Duration,
    pub p99: Duration,
}
```

**Key improvement:** Fixed-size rolling window (60 samples) instead of an unbounded `Vec`. Memory is O(1) regardless of runtime duration. `Stats` is lock-guarded on its ring buffer because the frame loop writes and the Prometheus metrics exporter reads concurrently.

---

## Error Recovery Strategy

| Error Source | Severity | Recovery Action |
|-------------|----------|-----------------|
| Capture returns error (1-2 consecutive) | Warn | Log, skip frame, continue |
| Capture returns error (3-9 consecutive) | Error | Restart the capture add-on in place; on success the consecutive counter clears and the restart counter advances |
| Capture add-on restarted 5x inside one 60 s window | Error | Restarting is not fixing it — poison + fall through the capture probe order |
| Capture returns error (10 consecutive) | Error | Poison + fall through the capture probe order; terminal only if no candidate remains |
| Encoder returns error (1-2 consecutive) | Warn | Log, skip frame, continue |
| Encoder returns error (3-9 consecutive) | Error | Restart the encoder add-on in place under the Level-1 backoff ladder below |
| Encoder add-on restarted 5x inside one 60 s window | Error | Poison + fall through the encoder probe order |
| Encoder returns error (10 consecutive) | Error | Poison + fall through the encoder probe order |
| Encoder returns `StreamError::FallbackToSoftware` | Info | `degrade_to_software()` — permanent for the session, idempotent, skips poisoned SW candidates |
| Encoder or capturer returns `StreamError::Unrecoverable` | Error | **Do not retry.** Poison for the session, fall through to the next candidate in probe order |
| No candidate remains after a fall-through | Fatal | `PipelineError::AddonsExhausted`: the frame loop returns it, cancels the token, every session closes with 4503, the process exits non-zero |
| Capture returns `StreamError::Unsupported` from an optional method | Error | Clear the claimed `AddonCaps` bit, log once, take the non-optional path (MODULE_ABI "Misbehaving add-ons"). Never counted by the capture ladder |
| `next_surface` returns `StreamError::Backend` 3 consecutive times | Error | Clear `AddonCaps::SURFACE` and `degrade_to_software()` — do **not** continue to the 10-consecutive row |
| Encoder returns `StreamError::Unsupported` | Error | Clear `ENC_CONFIGURABLE`; every later param change is a rebuild |
| `CursorCapturer::next_cursor` returns an error | Warn | `on_cursor_error`: hide the pointer, clear `AddonCaps::CURSOR`, re-derive `cursorMode`. Never counted by the capture ladder, never stops video |
| Parameter change refused by the capturer/encoder | Warn | `report_param_failure`: previous params stay in force and are republished on the `Applied` watch with `ok:false`, which MODULE_SERVER turns into `resize_suppressed`. There is no reply channel to answer |
| Server broadcast fails | - | Per-client: drop frame (handled internally) |
| Audio capture or encode returns an error (10 consecutive, no 60 s error-free gap) | Error | Retire the audio path for the session and publish `None`; video is untouched. No fall-through — there is one audio add-on per kind |
| Audio loop ends (`next_chunk` returns `Ok(None)`, or its error ladder gives up) | Warn | Drop the audio path for the session, push a fresh `config` with `audio: false`; video is untouched. No reconnect — audio returns only on restart |
| Input device error | Warn | Log, disable input (viewers still work) |
| Clipboard Monitor error (Wayland unsupported, X display drop) | Warn | Log, disable clipboard sync, leave video unaffected |
| File-transfer write error | Warn | Send `ERROR` to client for that transfer; drop only that transfer |
| Gamepad add-on connect failure | Warn | Drop subsequent gamepad records; log once per index |
| Context cancelled | - | Graceful shutdown |

---

## Add-On Crash Recovery (backoff + circuit breaker)

Fixes **TD-39**. The Go `ffmpeg.go` `restart()` was a flat kill -> 50 ms sleep ->
respawn with no attempt counter, retried from `Encode()` on every frame, so a
persistently failing encoder (bad driver, missing `ffmpeg`, GPU wedged)
respawned once per frame period for the life of the session — burning a core and
flooding the log while producing no frames. Nothing in v1 may reproduce that
shape. This section is the **normative policy for every restartable add-on**
(subprocess-backed encoders like the opt-in `x264`, in-process encoders, and
capturers alike); an add-on spec may tighten the numbers but may not opt out.

### Two levels, one rule each

**Level 1 — inside the add-on (self-healing).** An add-on that owns a restartable
resource (`x264` owns an `ffmpeg` child; `libva` owns a VA display) restarts it
itself on failure, under a **bounded, exponential** ladder:

| Attempt | Delay before respawn |
|---------|----------------------|
| 1 | 100 ms |
| 2 | 200 ms |
| 3 | 400 ms |
| 4 | 800 ms |
| 5 | 1600 ms |
| 6+ | — give up |

- Delay is `min(100 ms × 2^(n-1), 1600 ms)`, with **±20 % jitter** so N add-ons
  (or N sessions on one host) that fail on the same cause do not resynchronize
  into a thundering herd.
- The counter is **decayed, not cumulative**: it resets to 0 after the resource
  has run **60 s** without a failure. A process that dies once an hour is healthy
  and self-heals forever; one that dies five times in a minute is broken.
- A restart the HOST asked for — a `RequiresRestart` parameter change, or
  `force_keyframe()` on the opt-in `x264` bridge, whose only IDR mechanism is
  killing and respawning its child — is **not** counted by this ladder. The
  add-on marks the exit as expected before the kill.
- While a restart is pending, `encode()` returns `StreamError::Backend(..)` for
  each frame — the frame is dropped by the ladder above, the pipeline is not
  blocked, and no frame is queued for the dead resource.
- On the 6th failure within the window the add-on **stops restarting** and returns
  `StreamError::Unrecoverable(reason)` (`AbiErr::Unrecoverable`, code 7) from that
  call and every subsequent one. It must be idempotent and cheap after that — no
  further spawns, no further sleeps.
- Every restart logs at `warn` **once per attempt** with attempt number and delay
  (visible only via the installed log sink — MODULE_ABI); the give-up logs at
  `error`. No per-frame logging on a dead resource — that is the other half of
  the TD-39 symptom.

**Level 2 — inside the pipeline (fall-through).** The pipeline never retries an
add-on that reported `Unrecoverable`. `fall_through` (defined above, on the
frame-loop thread) does the work:

1. Logs at `error` with the add-on ID and `AbiError.detail` — the reason the
   add-on carried across the boundary (for `x264`, the last ffmpeg stderr line).
   Empty detail is logged as `(no detail)`, never as a blank.
2. Marks that add-on **poisoned for the session** — `FrameLoop.poisoned:
   HashSet<String>`, a plain set on the frame-loop thread, insert-only. A poisoned
   add-on is skipped for the rest of the **process** lifetime, including by
   `degrade_to_software`, so a dead `x264` cannot be re-selected as the SW
   fallback for a failing HW encoder. The set is **not** cleared by SIGHUP, by a
   client reconnect, or by a successful swap — the only thing that clears it is a
   process restart, because nothing else re-establishes the environment that made
   the add-on fail. It is visible in metrics as
   `featherdesk_addon_poisoned{addon="…",component="capture"|"encode"} 1`.
3. Walks to the **next candidate in the same probe order used at startup**
   (steps 3d/3e, per OS: Linux HW `nvenc → amf_rocm → libva`, Windows HW
   `nvenc → amf → qsv → mf_hw`, macOS HW `vt_hw`; SW `openh264 → vt_sw` on macOS
   and `openh264` alone elsewhere — a forced `x264` that reports `Unrecoverable`
   falls through to the auto order, which does not contain it), skipping poisoned
   entries, and re-probes it. A successful swap forces an IDR on the first frame
   so clients get fresh SPS/PPS, and pushes a new `config` message (the codec
   string may have changed — see MODULE_SERVER).
4. If no candidate remains, returns `PipelineError::AddonsExhausted`; the frame
   loop cancels the token and the process exits non-zero rather than spinning.
   This is the same terminal case as the startup check in step 3f ("HW encoder X
   has no SW fallback add-on loaded"), just reached mid-session.

Every step above runs on the **frame-loop thread**: `record_error`,
`fall_through`, `restart`, `open_component`, `degrade_to_software` and
`build_software_path` all take `&mut FrameLoop`, and `FrameLoop` is a value local
to that thread — so the swap cannot race an in-flight `encode` (M-6), and that is
enforced by the type system rather than asserted here.

After a successful swap the pipeline **re-reads the new add-on's
`StreamParamsCapability`** (the step-3g call) and replaces the cached copy on the
`stream::Manager`. Without this the Manager keeps clamping client requests to the
dead backend's limits.

The pipeline applies the identical two-level policy to **capturers**: the
"3-9 consecutive → restart / 10 consecutive → poison + fall through" rows above
are the capture ladder, and a capture add-on returning `Unrecoverable` is dropped
and fallen through the capture probe order (`nvfbc → kms_egl → wl_screencopy →
pw_portal` on Linux, etc.) the same
way. A capture swap additionally re-resolves `cursorMode` against the new
add-on's caps (see "Cursor publishing").

`Unrecoverable` is deliberately **not** a wire-visible condition on its own — the
client sees only the resulting `config` + keyframe on a successful swap, or a
`server_shutdown` notice plus a normal close on the terminal case.

---
## Refactoring Directives

### R-PIP-01: Extract from main.rs
Move all logic from `src/main.rs` into this module. `main.rs` should only:
1. Dispatch `argv[1]` per MODULE_CONFIG "CLI surface": `hash-password` /
   `revoke-device` / `list-devices` / `--version` / `--help` run their tool and
   exit **before** any config is loaded or any subsystem is touched. Only the
   no-subcommand case proceeds.
2. (Windows only) Establish `PER_MONITOR_AWARE_V2` as the first statement, per
   startup step 0 — the manifest is the primary route and this call is the
   fallback.
3. Parse `--config <path>` → `config::load(path)` → `Arc<config::Config>`,
   keeping the resolved path (the pipeline needs it for `config::watch`).
4. Initialize `tracing-subscriber` per `[log]`, installing a
   `tracing_subscriber::reload::Layer` and wrapping its `reload::Handle` in a
   `config::LogReload` — that boxed closure is the applier for the `[log]`
   hot-reload row, and it is what keeps `tracing_subscriber`'s generic
   parameters inside `main.rs`.
5. Build the Tokio runtime EXPLICITLY (`tokio::runtime::Builder::new_multi_thread()
   .enable_all().build()`) rather than with `#[tokio::main]`, so the main thread
   stays under `main.rs`'s control.
6. Install signal handlers: SIGINT/SIGTERM → `cancel.cancel()`; SIGHUP is handled
   inside `config::watch`, which the pipeline registers in `start()` — that
   registration is the producer; step 12b's `fd-config` task and the frame
   loop's step-(0d) applier are the two receiving halves. It is also why a
   process that would otherwise be *killed* by SIGHUP's default disposition
   survives a documented token rotation.
7. `pipeline::new(cfg, cfg_path, log_reload)` → `let stats = pipeline.stats_handle();` →
   `rt.spawn(pipeline.start(cancel.clone()))`.
8. On macOS ONLY: run the platform main loop on this thread until `cancel` fires
   (`CFRunLoopRun` with a cancel source), so AppKit-dependent add-ons have a main
   thread to post to (see "Thread and handle ownership"). On Linux and Windows,
   skip this step.
9. `rt.block_on(join)` for the pipeline's result, print the final stats from
   `stats`, and exit with its status.

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
| `featherdesk_frames_captured_total` | counter | `Stats.record_capture`, frame loop | frames pulled from the capturer |
| `featherdesk_frames_encoded_total` | counter | `Stats.record_broadcast`, frame loop step (4) | access units produced by the encoder |
| `featherdesk_frames_dropped_total` | counter | `Stats.record_drop`, frame loop step (1) | frames skipped at capture (pacing/overload) |
| `featherdesk_frames_broadcast_total` | counter | `Stats.record_broadcast`, frame loop step (4) | frames fanned out to ≥1 session |
| `featherdesk_bytes_broadcast_total` | counter | `Stats.record_broadcast`, frame loop step (4) | encoded video bytes sent (pre-fragmentation) |
| `featherdesk_clients` | gauge | `Stats.set_client_count(server.client_count())`, written by the exporter at scrape time; labeled `carrier` (`webtransport` / `websocket`) so a degraded deployment is visible | currently connected sessions |
| `featherdesk_audio_chunks_total` | counter | `Stats.record_audio_chunk`, audio thread (0 while audio deferred) | PCM chunks encoded |
| `featherdesk_audio_drops_total` | counter | `Stats.record_audio_drop`, audio thread | audio chunks dropped |
| `featherdesk_frame_time_seconds` | summary | `Stats.record_frame` (60-sample window) | capture→broadcast latency; exports min/max/avg/p95/p99 |
| `featherdesk_datagram_send_drops_total` | counter | `FrameOut::dropped()`, labeled `kind` (`video`/`audio`) **and `reason`** (`congestion`/`policy`) | per-session ring drop-oldest events. `reason="congestion"` is the fast-path congestion signal; `reason="policy"` is a `[transport] per_session_max_bps` drop and is **excluded** from the adaptive loop (MODULE_TRANSPORT "Per-session pacing cap") |
| `featherdesk_rtt_seconds` | gauge | QUIC `smoothed_rtt` (+ app ping/pong) | per-session RTT, labeled `client` (an opaque ordinal — see below); also the adaptive input |
| `featherdesk_effective_bitrate_kbps` | gauge | `Stats.effective_bitrate_kbps`, written by `apply_params` | current adaptive target bitrate |
| `featherdesk_effective_fps` | gauge | `Stats.effective_fps`, written by `apply_params` | current target fps — the rate the frame loop is actually pacing at |
| `featherdesk_keyframes_forced_total` | counter | `Stats.keyframes_forced`, written by the frame loop at step (3) | IDRs forced (join / gap / swap), useful for storm detection |
| `featherdesk_keyframe_requests_total` | counter | server keyframe path, labeled `reason` (`client` / `join` / `queue_drop`) | keyframe requests entering the limiter, before coalescing |
| `featherdesk_keyframe_requests_dropped_total` | counter | per-session token bucket | requests refused by `keyframe_request_burst` |
| `featherdesk_param_updates_dropped_total` | counter | `Stats.param_updates_dropped` | parameter changes rejected because the funnel was full (the frame loop is wedged) |
| `featherdesk_param_changes_failed_total` | counter | `Stats.param_changes_failed`, written by `report_param_failure` | parameter changes the encoder/capturer refused |
| `featherdesk_addon_poisoned` | gauge=1 | `FrameLoop.poisoned` via `Stats.set_poisoned` | one series per poisoned add-on, labeled `addon` and `component` |
| `featherdesk_adaptive_reference_client` | gauge=1 | `stream::Manager` | which session the adaptive loop is tuning to (label `client`) |
| `featherdesk_mic_packets_total` | counter | mic half of the audio loop | `AUDIO_MIC` packets decoded and written to the sink |
| `featherdesk_mic_decode_errors_total` | counter | mic half, on `AudioEncoder::decode` failure | packets dropped; never a session error |
| `featherdesk_mic_drops_total` | counter | mic half, oldest-first on a backed-up channel | mic packets discarded as stale |
| `featherdesk_display_provisioned` | gauge | set once at startup, after step 3d | 1 when the captured display was provisioned by FeatherDesk (IddCx / spawned compositor), 0 when it is a real display |
| `featherdesk_capture_idle` | gauge | `FrameLoop` step (0-) via `Stats::set_idle` | 1 while the frame loop is parked with zero authenticated sessions, 0 otherwise |
| `featherdesk_idle_releases_total` | counter | `Stats::record_idle_release`, from `FrameLoop::maybe_release_idle` | Stage 2 releases of the capturer + encoder after `[capture] idle_release_after` |
| `featherdesk_cursor_updates_total` | counter | `CursorPublisher::tick` | CursorUpdate datagrams sent (including idle re-sends) |
| `featherdesk_cursor_shapes_total` | counter | `CursorPublisher::tick` | Shape records written across all sessions |
| `featherdesk_cursor_errors_total` | counter | `on_cursor_error` | `next_cursor` failures |
| `featherdesk_cursor_shape_budget_exhausted_total` | counter | `Server::send_cursor_shape` | sessions that hit the 4 MiB cumulative shape budget |
| `featherdesk_control_send_drops_total` | counter | `Server::send_control` | S→C control messages dropped for a slow or closed control stream |
| `featherdesk_clipboard_drops_total` | counter | the Contract 9 gate lists, labeled `direction` (`h2c`/`c2h`) and `reason` | clipboard payloads dropped, by why |
| `featherdesk_role_rejects_total` | counter | MODULE_SERVER "Role gate table", labeled `op` | control messages refused by the role gate |
| `featherdesk_input_acks_dropped_total` | counter | `AckRing::push` (on the input-reader task), read back via `AckRing::dropped()` | InputAcks dropped by the 64-deep drop-oldest queue |
| `featherdesk_gamepad_rumble_dropped_total` | counter | the rumble drain task (step 12) | rumble events dropped on a full 64-slot channel |
| `featherdesk_datagram_oversize_total` | counter | `datagram_pump` | `TransportError::DatagramTooLarge` — the path MTU shrank mid-frame |
| `featherdesk_reassembly_timeouts_total` | counter | receiver | partial frames dropped on the `fragment_reassembly_ms` deadline |
| `featherdesk_reassembly_evictions_total` | counter | receiver | in-progress frames evicted by `reassembly_max_bytes` |
| `featherdesk_build_info` | gauge=1 | host | version/codec/OS as labels |

**The `client` label.** Its value is an **opaque per-session ordinal**: a `u64` assigned
from a process-wide monotonic counter when the server accepts a session, starting at 1,
never reused within a process lifetime, and reset only by a restart. It is the same value
the client is told as `auth_ok.session_id`, so an operator can correlate a log line
with a series without either one carrying an identity.

**Forbidden label values, in every series.** A remote address or any part of one; a session
token or any prefix, suffix or hash of one; a `user_id`, `device_id`, username, or
hostname; a file name or clipboard content; a URL. The endpoint is unauthenticated by
design, so a label is a publication. `featherdesk_build_info`'s labels (version, codec set,
OS) are the only free-form labels in the catalog and carry no per-connection data.

**Cardinality and lifetime.** Every per-session series is **removed** when the session
closes (lifecycle step 21). Live cardinality is therefore bounded by
`[server] max_clients` (default 25), not by cumulative connection count, and a scrape after
a churn of ten thousand short sessions returns twenty-five series, not ten thousand.

**Per-client counters (R-SRV-04)** — frames sent, frames dropped, bytes sent, connection
duration, RTT — carry the same `client` label and are sourced from the server's
per-session counters, **not** from `StatsSnapshot`, which is process-aggregate:

| Metric | Type | Source |
|--------|------|--------|
| `featherdesk_client_frames_sent_total{client}` | counter | per-session `frame_out` enqueues that reached `datagram_pump` |
| `featherdesk_client_frames_dropped_total{client}` | counter | per-session `frame_out` drop-oldest events (also aggregated as `featherdesk_datagram_send_drops_total`) |
| `featherdesk_client_bytes_sent_total{client}` | counter | per-session bytes handed to `send_datagram`, pre-fragmentation |
| `featherdesk_client_connected_seconds{client}` | gauge | now − session accept time |

**Metrics endpoint.** Plain HTTP, no TLS, no authentication, on `[metrics] bind` (default
`127.0.0.1`) port `[metrics] port` (default 9090) at `[metrics] path` (default
`/metrics`), serving the Prometheus text exposition format and nothing else — no `/status`,
no debug routes, no pprof. It exposes exactly the catalog above and the standard
`process_*` collectors. When `bind` is not a loopback address the server prints a startup
warning naming the port, because the endpoint publishes per-session timing and volume data
to anyone who can reach it. Bearer-token scrape auth is **not** in v1; deployments that
need it front the port with a reverse proxy or bind it to a private interface.

Add-on selection (which capture/encoder add-on won the probe) is emitted once at
startup as a `tracing` event and as labels on `featherdesk_build_info`, not as a
time series. The exporter reads `snapshot()` (atomics + a brief ring-buffer lock)
on each scrape — scraping never blocks the frame loop.

### R-PIP-03: Hot-Reload Encoder
If hardware encoder becomes unavailable mid-stream (GPU reset, driver crash), fall back to software encoder without dropping the connection:
1. Detect encode error
2. Reset the params the software path cannot carry (HDR → SDR, 10-bit → 8-bit, bt2020 → bt709, and chroma down to `420` unless the SW candidate advertises more), reconfigure the capturer to an 8-bit surface format, and create the software encoder for those params, skipping poisoned add-ons
3. Send `{"type":"hdr_unavailable","reason":"degraded_to_software"}` if the session was HDR, then a fresh `{"type":"config"}` — the codec string changes (HW HEVC/H.264 → SW H.264), so connected clients reconfigure their `VideoDecoder`
4. Latch an IDR for the new encoder
5. The swap happens between two `encode` calls on the frame-loop thread, and the function is idempotent — repeated `FallbackToSoftware` in one iteration costs one swap

### R-PIP-04: Configuration Validation
Validate `config::Config` at `new()` time (in addition to MODULE_CONFIG validation):
- `stream.fps` must be 1-240
- `stream.qp` must be 0-51 (H.264 range)
- `stream.bitrate_bps` must be 0 (QP mode) or >= 100000 (100kbps minimum)
- At least one capture add-on loaded
- At least one encoder add-on loaded

### R-PIP-05: Structured Shutdown Logging
On shutdown, log a summary. `pipeline::log_shutdown_summary(&Stats)` is the
emitter, and it is the last statement of `Pipeline::start`:
```
INFO [pipeline] Shutdown complete: 18432 frames captured, 147 dropped (0.8%), 2h14m uptime
```

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Config validation at `pipeline::new()` | No |
| Unit | Frame drop calculation logic | No |
| Unit | `max_skip` bounds consecutive skips at ~200 ms and does not set a frame rate | No |
| Unit | Stats rolling window (min/max/avg/p95/p99) | No |
| Unit | Capability probe result parsing | No |
| Unit | `cursorMode` resolution truth table, all seven rows of MODULE_CAPTURE "Cursor delivery", including the three that make an add-on ineligible — an ineligible add-on is skipped in the dispatch order and never selected, and with no eligible candidate startup fails with the rejection list (never a cursorless stream) | No |
| Unit | Windows: with the process forced DPI-unaware, `dxgi_dd.probe()` returns `available: false` with the DPI reason rather than an under-sized surface | Yes (Windows) |
| Integration | Full pipeline with mock capturer + mock encoder | No |
| Integration | Cursor keeps flowing while video does not: with a mock capturer returning `Ok(None)` (static screen) and with `skip_budget > 0`, `send_cursor` is still called on every tick. The mock's `next_cursor` MUST only report a change when its own latch was refreshed — a mock decoupled from `next_frame` cannot catch the acquisition-order bug it exists to guard | No |
| Integration | Idle re-send: after the last change, exactly 3 further `send_cursor` calls at 250/500/750 ms, each stamped with a fresh `DatagramHeader.FrameID` by the server, then silence until the cursor changes again | No |
| Integration | A `CursorCapturer` error sends `visible = 0`, stops polling, does **not** touch the capture-error escalation counter and does not stop video; with `EMBED_CURSOR` on the add-on it rebuilds with `embed_cursor = true` and pushes `cursorMode: "embedded"`; without it, `cursorMode` stays `"separate"` and no `config` is pushed | No |
| Integration | A stream-dimension change re-emits the active shape under the same ShapeID with recomputed `DrawW`/`DrawH`, and an already-connected session that held the pre-resize geometry receives the new record | No |
| Integration | Clipboard H→C drain: a `Monitor::changes()` emission reaches `server.send_clipboard`; with `[clipboard] direction` set to client→host only, the server drops it and the viewer session never receives a clipboard write | No |
| Integration | Clipboard C→H while the driver is running: with `Monitor::run` awaiting, a `set` from a session task reaches the OS clipboard (regression guard for the guard-held-across-await deadlock) | No |
| Integration | Graceful shutdown order verification: the frame and audio threads are JOINED before any handle they own is dropped, and the `AddonRegistry` is dropped last | No |
| Integration | Hardware → software fallback transition | Partially |
| Integration | HDR degrade: a HW-path session with `hdr=true, bit_depth=10, color_space="bt2020"` that hits `FallbackToSoftware` ends with `params.hdr==false, bit_depth==8, color_space=="bt709"`, a capturer reconfigured to an 8-bit format, an `hdr_unavailable` with reason `degraded_to_software`, and a `config` whose `hdr` is `false` | No |
| Integration | SW-path resize: a `resize` to 1280x720 produces `EncodedFrame.width/height == 1280x720` on the next frame AND a `config` advertising 1280x720 — the two can never disagree | No |
| Integration | 100 adaptive bitrate ticks on a mock encoder produce zero `send_config` calls and zero forced IDRs | No |
| Integration | `set_fps` 60 → 30 changes the OBSERVED capture cadence, not only the encoder config: a mock capturer records ~30 `next_frame` calls in the second after the change | No |
| Integration | Sustainable-rate control: a mock capture+encode taking 50 ms/frame with `[stream] fps = 60` settles at `params.fps == 20`, re-advertises `config.fps: 20`, and forces no IDR on the way | No |
| Integration | Idle-screen keyframe keepalive: on the software path with a mock capturer returning `Ok(None)` forever, a `kf_req` produces exactly ONE broadcast IDR within `idle_keyframe_ms` and never a second | No |
| Integration | A failed parameter change republishes the UNCHANGED params on the `Applied` watch with `ok:false` and leaves `self.params` untouched — there is no reply channel to answer | No |
| Integration | Crash-recovery backoff (TD-39): a mock encoder that fails on every call produces exactly 5 restarts with monotonically increasing delays, then `Unrecoverable` — never a 6th spawn and never a per-frame log line | No |
| Integration | Restart-counter decay: a mock encoder failing once, then succeeding for >60 s, then failing again starts its second ladder at attempt 1 (100 ms), not attempt 2 | No |
| Integration | The transient ladder's arms: 1-2 consecutive errors log and skip; the 3rd restarts the add-on in place; a 5th successful restart inside one 60 s window poisons instead of restarting; a 10th consecutive error poisons and falls through | No |
| Integration | Fall-through on `Unrecoverable`: the poisoned add-on is skipped for the rest of the session (including by `degrade_to_software`), the next probe-order candidate is selected, and an IDR + fresh `config` follow the swap | No |
| Integration | A mock encoder that fails every call while the capturer stays healthy poisons the ENCODER and falls through — it never restarts the capturer in a loop, and the process does not stay up producing nothing | No |
| Integration | No candidate remains after fall-through → `PipelineError::AddonsExhausted`, the token is cancelled, every session closes with 4503, and the process exits non-zero — not a spin loop | No |
| Integration | Audio: a mock `AudioCapturer` whose `next_chunk` blocks for a full frame period never delays a video frame, and its `Ok(None)` ends the audio thread without touching video | No |
| Integration | Audio retirement is total: a mock `AudioCapturer` that fails `open()`, one that returns `Unrecoverable`, and one that fails 10 consecutive `next_chunk` calls each leave the watch at `None`, and the next `config` advertises `audio: false` with the five audio fields blank | No |
| Integration | Restart cap window: a mock encoder failing once every 55 s restarts each time and is never poisoned, while five restarts inside one 60 s window poison it | No |
| System | End-to-end: capture → encode → broadcast → client decode | Yes |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| Pipeline overhead (frame loop bookkeeping) | <0.1ms/frame |
| Motion-to-photon contribution (bookkeeping only) | <0.1 ms/frame — see CENTRAL_SPEC "Motion-to-photon budget" |
| Shutdown time (graceful) | <2 seconds |
| Memory (stats + pipeline state) | <1MB |
| Frame pacing jitter | <2ms from target interval |
| Drop decision latency | O(1) — single comparison |
| Sustainable-rate convergence | within 2 s of a sustained overrun (2 x 60-frame windows) |
