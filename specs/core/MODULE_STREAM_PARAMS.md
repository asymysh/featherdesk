# Module Spec: Stream Parameters

## Overview

The Stream Parameters module defines the **central, cross-platform, codec-agnostic
streaming contract** that the pipeline owns and add-ons translate. It is the
single source of truth for resolution, frame rate, bitrate, quality, HDR, and
color space — values that change at runtime based on client requests, network
feedback, or admin actions.

**Why centralize:** Without this contract, each add-on would interpret
"resolution change" or "lower bitrate" differently, and bandwidth adaptation
would require N implementations. Centralizing the contract means the
**pipeline drives, add-ons translate**.

---

## The Contract

```rust
// crate: featherdesk-stream

/// EncodedFrame is the pipeline-to-server handoff type. Shared by both
/// SW and HW encoder paths so the server is path-agnostic.
pub struct EncodedFrame {
    pub data: bytes::Bytes, // Contiguous Annex B bitstream (start codes retained).
                            // NOT split per-NAL — avoids decompose/recompose copy.
                            // The server prepends the 22-byte header and sends
                            // data directly into the assembled access unit (then fragmented into datagrams).
                            // bytes::Bytes = cheap clones into every session's out-queue.
    pub width: u16,
    pub height: u16,
    pub timestamp_ns: u64, // CLOCK_MONOTONIC ns of the CAPTURE that produced this
                           // access unit, carried through the encoder. A PIPELINED
                           // encoder attaches it from its own submitted-timestamp
                           // FIFO, so it is never the wrong frame's capture time
                           // (see MODULE_ENCODE `EncodedUnit`).
    pub keyframe: bool,    // true if this access unit is a keyframe
    pub codec_type: u8,    // protocol::frame_type::VIDEO_H264 or VIDEO_HEVC
}

/// Params is the cross-platform streaming contract.
/// Owned by the pipeline; translated by each capture + encoder add-on.
#[derive(Clone)]
pub struct Params {
    // ── Resolution ──────────────────────────────────────────────
    // width and height in pixels. Both 0 = use display native resolution.
    // Changes here trigger re-init of capture (if resolution-fixed) and
    // re-config of encoder via update_stream_params.
    pub width: u32,
    pub height: u32,

    // ── Frame rate ──────────────────────────────────────────────
    // Target capture+encode rate. Capture paces below this if compositor
    // produces fewer frames; encoder treats as ceiling for rate control.
    // The pipeline may LOWER this below the requested ceiling when the host
    // cannot sustain it (MODULE_PIPELINE "Sustainable-rate control"); config.fps
    // always reports the effective value, never the request.
    pub fps: u32,

    // ── Quality (mutually exclusive) ────────────────────────────
    // bitrate_bps > 0 enables bandwidth-target mode (variable QP).
    // bitrate_bps = 0 falls back to constant-QP mode using qp.
    pub bitrate_bps: u32,
    pub qp: u32, // 0..51 for H.264, codec-dependent range otherwise

    // ── Color / HDR ─────────────────────────────────────────────
    // bit_depth = 8 or 10. 10-bit requires hdr=true or explicit override.
    // hdr=true forces bit_depth=10, color_space="bt2020", and switches
    // codec selection from H.264 → HEVC Main10 (no H.264 HDR profile).
    pub bit_depth: u32,
    pub hdr: bool,
    pub color_space: String, // "bt709" (SDR) | "bt2020" (HDR)
                             // (boundary form: the ColorSpace #[repr(u8)] enum —
                             //  MODULE_ABI "Rich types across the boundary")
    // color_space RESTATES the bitstream's matrix_coefficients ("bt709" = 1,
    // "bt2020" = 9). The VUI in the SPS is authoritative; v1 is always limited
    // range on both paths (see MODULE_ENCODE "Colour signalling").

    // Chroma subsampling. "420" (default, universally decodable) | "422" | "444".
    // 4:2:2/4:4:4 sharpen text/fine detail (the remote-desktop use case) but need
    // BOTH an encoder that supports them AND a client that can decode them — so
    // they are CAPABILITY-NEGOTIATED with a transparent fall-back to "420" (see
    // "Chroma Subsampling" below). Reliable on the native client; best-effort in
    // the browser. The frame type that carries it is encode::YuvFrame (see
    // MODULE_ENCODE); Subsampling is its discriminator.
    // (boundary form: the Chroma #[repr(u8)] enum — MODULE_ABI)
    pub chroma_subsampling: String,

    // ── Keyframe behavior ───────────────────────────────────────
    // 0 = on-demand only (current default — clients request {"type":"keyframe"};
    //     joins force one when the cached IDR is stale, and the frame loop's idle
    //     keepalive guarantees one materialises even on a static screen)
    // >0 = periodic IDR every N frames (only useful for stateless clients)
    pub keyframe_interval: u32,

    // ── Adaptive signals (pipeline measures, feeds back) ────────
    // These are READ-ONLY from add-ons' perspective — set by pipeline
    // based on network telemetry. Add-ons use them only as hints.
    pub network_rtt_ms: u32,  // From Session::path_stats().smoothed_rtt + the app
                              //   Ping/pong sample
    pub packet_loss_pct: f64, // Smoothed; from server datagram-drop rate + client stats
}
```

> **Field-name note.** The struct above is canonical: fields are snake_case
> (`width`, `height`, `fps`, `bitrate_bps`, `qp`, `bit_depth`, `hdr`,
> `color_space`, `chroma_subsampling`, `keyframe_interval`, `network_rtt_ms`,
> `packet_loss_pct`). The translation tables and prose below use shorthand
> (`Width`, `BitrateBps`, `HDR`, `ChromaSubsampling`, …) — read those as the same
> fields, not different ones.

A change to `Params` travels as a **delta**, never as a whole snapshot, and the
frame loop's result travels back as `Applied`:

```rust
// crate: featherdesk-stream

/// ParamDelta is what travels through the funnel. It names only the fields the
/// producer intends to change; the frame loop applies it onto its own
/// authoritative `Params`. Whole-snapshot messages are deliberately NOT
/// supported: a snapshot built by read-modify-write against a cached "current"
/// reverts every field another producer changed in the meantime, which is how a
/// client resize gets silently undone by the next adaptive tick.
#[derive(Clone, Debug)]
pub enum ParamDelta {
    Resize { width: u32, height: u32 },
    Bitrate { bps: u32 },
    Fps { fps: u32 },
    Qp { qp: u32 },
    /// The whole HDR TRIPLE, in one variant. `[stream] hdr`, `[stream]
    /// bit_depth` and `[stream] color_space` are not independently settable at
    /// runtime — validation pins them (`bit_depth = 10` only with `hdr = true`,
    /// `color_space = "bt2020"` only with `hdr = true`, and `hdr = true` forces
    /// both) — so a `[stream]` reload emits ONE `Hdr { on }` for all three.
    /// There is no `BitDepth` variant and no `ColorSpace` variant, and neither
    /// is to be created.
    Hdr { on: bool },
    Chroma { subsampling: String },        // "420" | "422" | "444"
    KeyframeInterval { frames: u32 },
    /// Adaptive telemetry. Hints only: it never changes a client-visible field,
    /// never pushes a `config`, and never forces an IDR.
    Telemetry { network_rtt_ms: u32, packet_loss_pct: f64 },
}
// NOT a variant here, and not a `Params` field either: `[stream]
// idle_keyframe_ms`. It is the frame loop's own keepalive timer
// (`FrameLoop.idle_keyframe_ms`), seeded in `FrameLoop::open()` and refreshed by
// the frame loop's step-(0d) config arm, so it never travels through the funnel
// and never reaches `stream::Manager`. No `ParamDelta::IdleKeyframeMs` exists or
// is to be created.

impl Params {
    /// Folds one delta into these params IN PLACE. A `None` field leaves the
    /// current value untouched; a `Some(v)` overwrites it. Pure: it validates
    /// nothing and clamps nothing — `Manager::apply` already did both before the
    /// delta entered the funnel — so folding N deltas in send order is
    /// last-write-wins per field, which is what makes the frame loop's
    /// coalescing loop correct.
    fn apply_delta(&mut self, d: &ParamDelta);
}

/// One message on the funnel.
pub struct ParamUpdate {
    /// Strictly increasing, assigned by `Manager::apply` under the Manager's
    /// mutex. The frame loop records the highest epoch it has applied and
    /// ignores any message at or below it.
    pub epoch: u64,
    pub delta: ParamDelta,
    // There is deliberately NO reply channel. `Manager::apply` — the funnel's
    // SOLE producer — takes no `oneshot::Sender` and carries no session
    // identity, and `Server::set_stream_params_callback` returns
    // `Result<Params, StreamError>` synchronously with nowhere to hand a
    // receiver back, so neither half of a reply can exist. A change the frame
    // loop could not apply is reported by republishing the unchanged params on
    // the `Applied` watch with `ok: false` (see "param_failure_feedback").
}

/// Applied is what the frame loop PUBLISHES after every `apply_params`,
/// successful or not. It is the single source of truth for "what is the pipeline
/// actually running", and it is what the Manager and the server read instead of
/// each keeping their own guess.
#[derive(Clone, Debug)]
pub struct Applied {
    pub epoch: u64,       // the highest ParamUpdate.epoch folded into this result
    pub params: Params,   // the params in force RIGHT NOW
    pub codec: String,    // the active WebCodecs codec string (changes on a swap)
    pub cursor_mode: String, // `pipeline::CursorMode::as_str()`: "separate" | "embedded"
    pub ok: bool,         // false ⇒ the change at `epoch` did NOT land; `params` is unchanged
    /// The `StreamParamsCapability` of the add-ons NOW in force, re-read through
    /// their `as_configurable()` accessors on the frame thread. `Some` only on
    /// the `Applied` that follows a construction or a swap — `FrameLoop::open`,
    /// `fall_through`, `degrade_to_software`, `restart` — and `None` on every
    /// ordinary parameter change. `Manager::apply` refreshes its cached copy
    /// from `applied_rx.borrow()` before it clamps; that is the ONLY way the
    /// Manager learns a new backend's bounds and `hot_changeable` map, and
    /// without it a HW→SW degrade leaves it clamping against the dead
    /// backend's limits.
    pub caps: Option<std::sync::Arc<StreamParamsCapability>>,
}
```

---

## Coordinate space (normative)

There is exactly ONE coordinate space in this system, and every wire value that
names a position is in it.

> **The stream space** is `[0, config.width - 1] x [0, config.height - 1]`,
> measured in **encoded output pixels** of the selected display, **upright**
> (display rotation already applied — see "Display rotation" in
> [`../media/MODULE_CAPTURE.md`](../media/MODULE_CAPTURE.md)), origin at the
> top-left, x increasing right, y increasing down.

Consequences, each of which closes a place the tree previously disagreed:

- **`config.width`/`config.height` are encoder-output pixels.** They are what the
  encoder emits, what the client's canvas is sized to, and what the client
  divides by when it maps a pointer position. This is the existing invariant
  **encoder-output dims == `config` dims == input-coordinate range**, now with a
  unit attached.
- **`InputRecord.X`/`Y`, `TouchContact.X`/`Y` and `CursorUpdate.x`/`y` are all in
  the stream space.** No wire message carries points, logical pixels, virtual-
  screen pixels, normalized coordinates, or native capture pixels.
- **Each input add-on converts at its own boundary.** Converting from the stream
  space into the OS's own space is the injector's job, done inside the injector,
  using geometry the injector queries from the OS — not something the pipeline
  pre-adjusts. `Injector::resize(width, height)` is how the injector learns the
  stream space; how it maps into the OS is its own business. See CGEVENT (points)
  and WIN_TOUCH (virtual-screen physical pixels) for the two non-identity cases.
- **The host process must be declared DPI-aware** so that "pixel" means physical
  pixel on every OS, not a scaled logical pixel. On Windows that declaration is
  owned by the process entry point (see
  [`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) startup step 0).
- **Rotation is absorbed on the capture side**, so the stream space is always
  upright and no injector, no client and no protocol field ever sees a rotated
  frame.

---

## Configurable Interfaces

Add-ons implement these in addition to their base contract to support runtime
parameter changes:

```rust
// crate: featherdesk-stream

// Error sentinels — defined in the stream crate (ONE thiserror-derived enum) to
// avoid the cross-crate cycle between encode, hwencode, and capture. It is the
// SINGLE hot-path error: capture (next_frame / next_surface), the SW Encoder, and
// the HardwareEncoder all return StreamError — there is NO separate CaptureError.
// Across the add-on ABI these cross as an AbiError { code, detail } whose code
// the host maps back into StreamError; the detail is logged, never put on the
// wire (see MODULE_ABI "AbiErr registry").
#[derive(Debug, thiserror::Error)]
pub enum StreamError {
    /// Returned by update_stream_params when the requested change cannot be
    /// applied mid-stream (pipeline tears down + recreates).
    #[error("stream: parameter change requires add-on restart")]
    RequiresRestart,

    /// Returned by update_stream_params when an encoder cannot produce HDR
    /// output (8-bit only). The pipeline switches to an HEVC-Main10-capable
    /// encoder IF one is loaded; if NONE is available (terminal case), the
    /// pipeline rejects the HDR request and sends
    /// {"type":"hdr_unavailable","reason":"no_hevc_encoder"} via
    /// Server::send_control, and the session stays SDR.
    #[error("stream: encoder does not support HDR/10-bit")]
    HdrUnsupported,

    /// Returned by an encoder that cannot produce the requested 4:2:2/4:4:4
    /// subsampling (e.g. OpenH264 is 4:2:0-only). The pipeline falls back to
    /// "420". A CLIENT that cannot DECODE the advertised config replies
    /// {"type":"decode_unsupported"} and the server runs the downgrade ladder,
    /// whose first rung is "420" + new config + keyframe (see "Chroma
    /// Subsampling"). Also returned by an encoder handed a YuvFrame whose
    /// `subsampling` is not what it configured — defence in depth; the pipeline
    /// never sends a mismatch.
    #[error("stream: encoder does not support requested chroma subsampling")]
    ChromaUnsupported,

    /// Returned by encode_surface (HW encoder) or next_surface (capturer) when
    /// the GPU path fails (surface import error, driver constraint, GPU reset).
    /// Pipeline catches this once per session and degrades permanently to SW path.
    #[error("stream: hardware path unavailable, fall back to software")]
    FallbackToSoftware,

    /// Capture/encode device or context lost (monitor unplugged, GPU reset on the
    /// SW path, device invalidated). The pipeline's error-recovery ladder
    /// (warn → restart → shutdown, see MODULE_PIPELINE) handles it.
    #[error("stream: capture/encode device lost")]
    DeviceLost,

    /// The add-on has failed repeatedly and has exhausted its own internal
    /// recovery (see "Add-On Crash Recovery" in MODULE_PIPELINE). This is the
    /// TERMINAL signal for one add-on instance: the pipeline must NOT retry it,
    /// it drops the add-on and falls through to the next candidate in the
    /// dispatch order (or shuts the pipeline down if none remain). Distinct from
    /// `DeviceLost` (which the pipeline itself retries) and from
    /// `Backend` (a single transient call failure). Across the ABI it is the
    /// `AbiErr::Unrecoverable` code and the add-on's own reason travels in
    /// `AbiError.detail` — unlike `Backend`, the host cannot fill this one in,
    /// because the reason (a dead child process's last stderr line, a driver
    /// error string) exists only inside the add-on. The pipeline tags the
    /// failing component at the call site, since this enum deliberately does
    /// not carry it.
    #[error("stream: add-on unrecoverable, do not retry: {0}")]
    Unrecoverable(String),

    /// The host called a method the add-on's `AddonCaps` did not claim. Always a
    /// capability lie (or a host bug): the host clears the bit for the rest of the
    /// session and takes the non-optional path — it never retries and never
    /// escalates to the capture/encode error ladder. See MODULE_ABI
    /// "Misbehaving add-ons".
    #[error("stream: add-on does not serve this optional method")]
    Unsupported,

    /// Catch-all backend failure with no more specific variant. Carries a
    /// human-readable detail for the LOG (never the wire); across the ABI it is
    /// the `AbiErr::Generic` code and the host fills the detail. A SINGLE
    /// TRANSIENT call failure — the host skips the frame and continues; it is
    /// never the code for a caught panic, which is `AbiErr::Unrecoverable`.
    #[error("stream: backend failure: {0}")]
    Backend(String),
}

/// Manager coordinates dynamic parameter changes. It is the SOLE producer on the
/// pipeline's param funnel: the server feeds it client-driven changes
/// (resize / set_bitrate / set_fps / set_hdr) and bandwidth-adaptation signals,
/// and the `fd-config` task feeds it the `[stream]` diff of a reload; it clamps,
/// applies hysteresis, stamps an epoch and enqueues. It does NOT
/// call update_stream_params itself — the change is applied ON THE FRAME-LOOP
/// THREAD (M-6: encode and update_stream_params are never concurrent). See
/// MODULE_PIPELINE "apply_params".
///
/// `Manager: Send`, and the pipeline builds ONE
/// `Arc<Mutex<Box<dyn stream::Manager>>>`: step 12 hands it to the server
/// through `set_stream_params_callback`, and the pipeline retains a clone of the
/// same `Arc` as `Pipeline.manager`, which is how the `fd-config` task
/// (`pipeline::config_applier`) reaches `apply` and `set_policy` at all. The
/// critical section is clamping arithmetic plus one `try_send` — no I/O, no
/// await, no blocking call — so N session tasks contend for single-digit
/// microseconds.
///
/// The Manager is also constructed with an `Arc<Stats>`. That handle exists for
/// exactly one purpose — calling `Stats::record_param_update_dropped` when a
/// `ParamUpdate` cannot be enqueued — and the Manager reads nothing else from it.
pub trait Manager: Send {
    /// Clamps the delta against `[stream]` bounds and the cached
    /// `StreamParamsCapability` — refreshed from `applied_rx.borrow()`
    /// (`Applied.caps`) BEFORE the clamp, so a backend swap's bounds are the ones
    /// in force — stamps the next epoch, and enqueues a `ParamUpdate` for the
    /// frame loop.
    ///
    /// Returns the CLAMPED REQUEST, not the applied result: what the encoder
    /// actually did is known one frame later and arrives through the `Applied`
    /// watch (`Server::set_params_watch`). Returning immediately is what keeps a
    /// control-stream reader from blocking behind an encode.
    ///
    /// Errors: `StreamError::Backend("param queue full")` when the funnel is at
    /// capacity (32) — the frame loop is wedged, and the caller must not retry in
    /// a loop. The Manager calls `Stats::record_param_update_dropped` on the
    /// `Arc<Stats>` it holds, which is what increments
    /// `featherdesk_param_updates_dropped_total`; the adaptive loop then skips
    /// the tick.
    fn apply(&mut self, delta: ParamDelta) -> Result<Params, StreamError>;

    /// The parameters the FRAME LOOP last reported as in force — the latest value
    /// from the `Applied` watch, not the Manager's own bookkeeping, so a change
    /// that failed to apply cannot leave the Manager clamping against params the
    /// pipeline is not running. Its callers are the clamp path and the
    /// congestion-reactive loop.
    fn current(&self) -> Params;

    /// Replaces the adaptive policy after a `[stream.adaptive]` hot reload. Its
    /// ONE caller is the `fd-config` task (`pipeline::config_applier`), which
    /// reaches the Manager through `Pipeline.manager`.
    fn set_policy(&mut self, p: AdaptivePolicy);
}

/// AdaptivePolicy is the `[stream.adaptive]` section, decoded. One struct, one
/// owner: the Manager holds it and nothing else reads the TOML keys directly.
#[derive(Clone, Copy, Debug)]
pub struct AdaptivePolicy {
    pub enabled: bool,
    pub interval_ms: u32,              // telemetry window; 100 ms
    pub min_bitrate_bps: u32,
    pub max_bitrate_bps: u32,
    pub loss_threshold_pct: f64,       // trigger reduction (slow path)
    pub recovery_threshold_pct: f64,   // allow increase
    pub fast_reduction_factor: f64,    // immediate cut on a send-side drop spike
    pub adjustment_factor: f64,        // multiply on slow-path degradation
    pub recovery_factor: f64,          // multiply on recovery
}
```

The `Configurable*` traits live in their respective crates but import
`stream::Params` and return `StreamError::RequiresRestart` / `StreamError::HdrUnsupported`:

```rust
// crate: featherdesk-encode (SW encoder add-ons)
pub trait ConfigurableEncoder: Encoder {
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), stream::StreamError>;
    fn params_capability(&self) -> Result<stream::StreamParamsCapability, stream::StreamError>;
}

// crate: featherdesk-hwencode (HW encoder add-ons)
pub trait ConfigurableHardwareEncoder: HardwareEncoder {
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), stream::StreamError>;
    fn params_capability(&self) -> Result<stream::StreamParamsCapability, stream::StreamError>;
}

// crate: featherdesk-capture (Capture add-ons)
pub trait ConfigurableCapturer: Capturer {
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), stream::StreamError>;
    fn params_capability(&self) -> Result<stream::StreamParamsCapability, stream::StreamError>;
}
```

Add-ons that do **not** implement these interfaces are treated as immutable:
the pipeline tears them down and recreates with the new params whenever
parameters change.

---

## Per-Add-On Translation Table

Each add-on translates `stream::Params` to its native concepts:

### Encoders

| `stream::Params` field | OpenH264 | x264 (subprocess, opt-in) | NVENC | AMF | MF HW | VT HW |
|----------------------|----------|-------------------|-------|-----|-------|-------|
| `Width`, `Height` | `SetOption(SVC_ENCODE_PARAM_EXT)` — requires re-init | Restart ffmpeg with new `-s WxH` | `nvEncReconfigureEncoder` (hot if within initial `maxEncodeWidth/Height`) | `Terminate` + `ReInit` (cold -- AMF does NOT support hot resolution change) | `IMFTransform` teardown + reinit | `VTCompressionSessionInvalidate` + recreate |
| `FPS` | `SetOption(FRAMERATE)` (hot) | Restart with new `-r` | `nvEncReconfigureEncoder` (hot) | `SetProperty(FRAMERATE)` (hot) | `MF_MT_FRAME_RATE` (requires reinit) | `kVTCompressionPropertyKey_ExpectedFrameRate` (hot) |
| `BitrateBps` | `SetOption(BITRATE)` (hot) | Restart with new `-b:v` | `nvEncReconfigureEncoder` (hot) | `SetProperty(TARGET_BITRATE)` (hot) | `CODECAPI_AVEncCommonMeanBitRate` (hot via property store) | `kVTCompressionPropertyKey_AverageBitRate` (hot) |
| `QP` | `SetOption(SVC_ENCODE_PARAM)` (hot) | Restart with new `-crf` | `nvEncReconfigureEncoder` (hot) | `SetProperty(QP_I/QP_P)` (hot) | `CODECAPI_AVEncCommonQuality` (hot) | `kVTCompressionPropertyKey_Quality` (hot) |
| `BitDepth=10` + `HDR` | ❌ Unsupported (8-bit only) — pipeline switches encoder | `-pix_fmt yuv420p10le -profile:v high10` (restart, but **AVC HDR is not in WebCodecs scope** — pipeline switches to HEVC) | `NV_ENC_PIC_PARAMS_HEVC` Main10 profile (requires HEVC codec selection) | `SetProperty(BIT_DEPTH=10, PROFILE=MAIN10)` (HEVC only) | HEVC Main10 MFT subtype (requires HEVC mf_hw add-on) | `kVTProfileLevel_HEVC_Main10_AutoLevel` (requires vt_hw HEVC support) |
| `KeyframeInterval` | `SetOption(SVC_ENCODE_PARAM)` | Restart with `-g` | `nvEncReconfigureEncoder` (hot) | `SetProperty(IDR_PERIOD)` (hot) | `CODECAPI_AVEncMPVGOPSize` | `kVTCompressionPropertyKey_MaxKeyFrameInterval` (hot) |
| `NetworkRTTMs`, `PacketLossPct` | Ignored (no rate-distortion hooks) | Ignored | Used by `nvEncSetIOCudaStreams` for low-latency RC | Used by `RATE_CONTROL_HQVBR_QVBR` quality boost | Ignored | Used by `kVTCompressionPropertyKey_AverageBitRate` headroom |

**x264 is opt-in**, reachable only through `[encode] force_addon = "x264"`; it is
not in the automatic software probe order (see
[`../media/MODULE_ENCODE.md`](../media/MODULE_ENCODE.md) "Software encoder order").
Every x264 cell above is a restart because the bridge drives a child `ffmpeg`
process: it declares `AddonCaps::ENC_CONFIGURABLE = false`, so the pipeline
rebuilds it for **any** parameter change. It also **cannot force an IDR cheaply** —
its only mechanism is killing and respawning that child. `force_keyframe()`
therefore only latches the request; the next `encode()` performs the restart.

### Capturers

| `stream::Params` field | KMS+EGL | NvFBC | SCK (macOS) | DXGI DD |
|----------------------|---------|-------|-------------|---------|
| `Width`, `Height` | Native capture; the **Converter** scales on the SW path and the **encoder** scales in-GPU on the HW path | Same — capture is native, scaling happens downstream | Same (SCK *can* also scale via `SCStreamConfiguration.{width,height}`, but the default is downstream scaling, to keep the invariant) | Same — capture is native, scaling happens downstream |
| `FPS` | Pipeline pacing (capture is event-driven) | Pipeline pacing | `SCStreamConfiguration.minimumFrameInterval` (hot) | `IDXGIOutputDuplication::AcquireNextFrame` timeout |
| `BitDepth=10` + `HDR` | Request `DRM_FORMAT_XRGB2101010` framebuffer (driver-dependent) | NvFBC supports HDR via `NVFBC_FRAME_GRAB_FLAGS_NOWAIT` + 10-bit pixel format | `SCStreamConfiguration.pixelFormat = kCVPixelFormatType_64RGBALeAccurate` (requires macOS 14+) | `DXGI_FORMAT_R10G10B10A2_UNORM` (requires HDR enabled in Display Settings) |
| `ColorSpace` | Reported per surface metadata; pipeline annotates encoder | Reported per surface | Set automatically based on display | `IDXGIOutput6::GetDesc1()` → `DXGI_OUTPUT_DESC1.ColorSpace` |
| `NetworkRTTMs`, `PacketLossPct` | Ignored (capture isn't bandwidth-sensitive) | Ignored | Ignored | Ignored |

---

## HDR Pipeline (Full Flow)

When `Params.HDR = true`, the pipeline enforces an HDR-capable chain end-to-end.
HDR has multiple touchpoints and **only HEVC Main10 is HDR-viable** (no H.264 HDR
profile is in the WebCodecs spec).

### Selection cascade

```
1. Pipeline receives Params{HDR: true}
1b. Admission gate: refuse if ANY attached session reported decode.hevc10 == false
    at auth — send {"type":"hdr_unavailable","reason":"attached_client_cannot_decode"}
    to the requester and stop. HDR is session-global and one-way, so it may not be
    entered over the head of an attached viewer (see MODULE_PROTOCOL "Decode
    capability").
2. Validate selected capture add-on supports 10-bit:
   - KMS+EGL: requires DRM_FORMAT_XRGB2101010 support on the active card
   - DXGI DD: requires HDR enabled in Windows Display Settings
   - SCK:     requires macOS 14+ and an HDR-capable display
   - NvFBC:   requires Capture SDK 8.0+ with bWithHDR flag
   - If it cannot: the request is rejected the same way as step 3, with
     {"type":"hdr_unavailable","reason":"no_ten_bit_capture"}.
3. Switch encoder selection:
   - Reject H.264-only encoders (OpenH264, x264)
   - Require HEVC Main10 capable: NVENC, AMF, MF HW HEVC, VT HW HEVC
   - TERMINAL CASE: if NO HEVC-Main10 encoder is loaded, the HDR request
     is rejected — the pipeline sends
     {"type":"hdr_unavailable","reason":"no_hevc_encoder"} via
     Server::send_control and the session STAYS SDR (H.264, bt709). The pipeline
     does not half-switch capture to 10-bit. This is the only graceful failure
     mode.
4. Configure capture add-on:
   - update_stream_params() with bit_depth=10, hdr=true, color_space="bt2020"
   - Capture re-initializes with 10-bit pixel format
5. Configure encoder add-on:
   - update_stream_params() — encoder switches to HEVC Main10 profile
   - The ENCODER ADD-ON owns SEI insertion: it emits the HDR10 mastering-display
     + content-light-level SEI NALs inside each keyframe access unit
     (VPS + SPS + PPS + prefix-SEI + IDR), and writes the HDR VUI
     (primaries 9, transfer 16, matrix 9, full_range 0 — see MODULE_ENCODE
     "Colour signalling"). The pipeline/server never synthesize SEI — they only
     carry the bytes the encoder produced. There is no hdr_metadata field on the
     wire; the bitstream carries it.
6. Issue config handshake to client:
   - codec = the computed HEVC Main10 string (hvc1.2.4.L123.B0 at 1080p60 —
     see MODULE_ABI "Codec-string computation")
   - hdr = true, color_space = "bt2020"
7. Client configures VideoDecoder and canvas:
   - {codec: cfg.codec, codedWidth, codedHeight, optimizeForLatency: true,
      hardwareAcceleration: "prefer-hardware"}
   - The canvas is created with colorSpace "display-p3" where the browser
     reports it and "srgb" otherwise. rec2100-pq / rec2100-hlg are NOT values in
     the shipped CanvasRenderingContext2DSettings enum and MUST NOT be requested.
   - v1's browser presentation is therefore TONE-MAPPED: the 10-bit BT.2020 PQ
     stream is decoded at full precision and colour-managed down to the canvas's
     output space by the browser. True HDR display output is the native client's
     (see MODULE_NATIVE_CLIENT).
```

### HDR is a one-way trip per session

Once an encoder is initialized for HDR (10-bit Main10), it cannot revert to
SDR (8-bit) without full teardown. The pipeline treats `HDR` changes as
restart-requiring on every add-on.

The single exception is `degrade_to_software`: a mid-session hardware failure
forces the session out of HDR, because the software path is 8-bit H.264 only. The
pipeline resets `hdr`/`bit_depth`/`color_space` **before** it builds anything from
them — including reconfiguring the capturer back to an 8-bit surface format — and
sends `{"type":"hdr_unavailable","reason":"degraded_to_software"}` (see
[`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md)).

### What's deferred for HDR

- **Tonemapping** (HDR capture → SDR encode for clients without HDR): not in
  scope; HDR mode is all-or-nothing per session.
- **Dolby Vision / HDR10+** (dynamic metadata): not in scope; HDR10 static
  metadata only.
- **AV1 HDR** (HEVC alternative): not in scope until AV1 HW encoders are
  ubiquitous (currently RDNA3+/RTX 40+/M2+ only).

---

## Chroma Subsampling (4:2:0 / 4:2:2 / 4:4:4)

`Params.ChromaSubsampling` selects the chroma format. **4:2:0** is the universal
default; **4:2:2/4:4:4** sharpen text and fine UI detail — the remote-desktop win
(this is Parsec's headline feature) — but are gated by both encoder and decoder
support, so they are **capability-negotiated with a transparent fall-back to
4:2:0**. They are **reliable on the native client (v2)** and **best-effort in the
browser**.

### Encoder support (advertised via probe; see MODULE_ENCODE / MODULE_HARDWARE_ENCODE)

| Encoder | 4:2:0 | 4:2:2 | 4:4:4 |
|---------|:----:|:----:|:----:|
| OpenH264 | ✅ | ❌ | ❌ (4:2:0-only — picking 444 here falls back) |
| x264 (subprocess, opt-in) | ✅ | ✅ | ✅ |
| NVENC | ✅ | 🔶 (HEVC RExt) | ✅ (H.264 + HEVC on Turing+) |
| AMF / QSV / VideoToolbox | ✅ | 🔶 vendor-dependent | 🔶 vendor-dependent |

### Codec string carries the chroma (so the client can probe it)

The chroma is encoded in the **profile** of the WebCodecs codec string the
`config` message advertises — the client passes it to `VideoDecoder` and probes it:
- H.264 4:2:0 = High (`avc1.64…`); **4:2:2** = High 4:2:2 (`avc1.7A…`); **4:4:4** =
  High 4:4:4 Predictive (`avc1.F4…`).
- HEVC 4:2:0 = Main; **4:2:2/4:4:4** = Range Extensions profiles (`hvc1.4…` RExt).

### Negotiation + fall-back (three gates, all ending at 4:2:0)

```
1. Pipeline wants ChromaSubsampling = "444".
2. CLIENT-CAPABILITY gate (auth time): every session declares what it can decode
   in its `auth` message (decode.h264_422 / decode.h264_444 — see MODULE_PROTOCOL
   "Decode capability"). Manager::apply clamps chroma_subsampling to the MINIMUM
   of what every attached session reports: the WEAKEST ATTACHED CLIENT SETS
   CHROMA, because the stream is one broadcast. A session that sent no `decode`
   object is read conservatively as 4:2:0.
3. ENCODER gate: the active encoder advertises its max chroma. If it can't do the
   clamped value, it returns StreamError::ChromaUnsupported → the pipeline
   downgrades to the encoder's best (e.g. 420 for OpenH264; 422 if that's the
   ceiling).
4. RUNTIME BACKSTOP: the server advertises the resulting codec string in `config`.
   A client whose auth-time claim was optimistic runs
   VideoDecoder.isConfigSupported({codec}) and, on a negative, sends
   {"type":"decode_unsupported"} on the control stream. The server runs the
   downgrade ladder (MODULE_PROTOCOL "Decode capability"); its first rung
   downgrades chroma to "420" session-wide, re-sends `config` with the 4:2:0
   codec string, and forces a keyframe. 4:2:0 is guaranteed decodable everywhere
   — this gate always terminates.
```

This mirrors the HDR terminal-case pattern. A change to `ChromaSubsampling` is
**restart-requiring** on the encoder (new profile), like a resolution change.

### What's not in scope

- **Per-client chroma** (one viewer 4:4:4, another 4:2:0): the stream is one
  broadcast, so chroma is session-wide — gate 2's reducer settles it for the whole
  room, and the operator may instead pin 4:2:0.

---

## Dynamic Resolution Change Flow

The client (browser window resize) drives resolution changes. The pipeline owns
the policy; add-ons translate.

### Sequence

```
[client]                              [server]                       [pipeline]
   │                                     │                              │
   │ window.onresize fires               │                              │
   │ debounce 250ms                      │                              │
   │                                     │                              │
   ├─ control-stream JSON: {"type":"resize",                            │
   │  "width":1920, "height":1080}       │                              │
   ├─────────────────────────────────────>                              │
   │                                     │ validate against display     │
   │                                     │ (clamp to native dims)       │
   │                                     ├──── paramCh ────────────────>│
   │                                     │                              │ effective Params{W:1920,H:1080}
   │                                     │                              │ applyParams() on the FRAME LOOP:
   │                                     │                              │   → capture.update_stream_params
   │                                     │                              │   → encoder.update_stream_params
   │                                     │                              │     (or restart if required)
   │                                     │                              │ force_keyframe (new dims invalidate
   │                                     │                              │  reference frames)
   │                                     │                              │
   │                                     │ Send fresh config message    │
   │                                     │ (JSON line) new width/height │
   │ <───────────────────────────────────┤                              │
   │                                     │                              │
   │ Reconfigure VideoDecoder            │                              │
   │ Resize canvas                       │                              │
   │ Resume decoding (new keyframe       │                              │
   │  arrives next)                      │                              │
```

### Constraints

- **Native resolution is a per-dimension CEILING, not the aspect ratio.** Each
  requested dimension is clamped independently against the corresponding native
  dimension — a client on a 1080p host cannot request 4K — but the **aspect
  ratio follows the client viewport**, and a request whose ratio differs from
  the host's is honoured rather than corrected (GAP_TRIAGE OQ-03). This is the
  whole of the host-side-resolution answer: FeatherDesk never mode-sets the host
  desktop on Linux (MODULE_CAPTURE "The module intentionally does NOT support"),
  so letting the encoder's *output* follow the viewport is what removes the two
  costs a fixed native ratio imposes — the client letterboxing every mismatched
  session, and the encoder spending bitrate on detail the client resamples away.
  Capture stays native on every backend; only the scale target moves, so this is
  a clamp change and nothing downstream is affected (the Converter scales on the
  SW path, the encoder's VPP on the HW path).
- **The client requests DEVICE pixels, not CSS pixels.** The requesting client
  multiplies its canvas dimensions by `devicePixelRatio` before sending
  `resize`, so a HiDPI viewport asks for the resolution it will actually display
  at instead of asking for half of it and upscaling (MODULE_WEB_CLIENT "Resize
  requests"). The server applies its clamp to the device-pixel figure it
  receives and needs no knowledge of the client's DPR.
- **One reconfig at a time.** Concurrent resize requests are coalesced; the
  pipeline applies only the latest.
- **Hysteresis.** Server applies a resize if ANY dimension changes by >5%
  OR if the aspect ratio changes by >2%. This prevents oscillation during
  continuous resize drags while avoiding the stretching artifact that a
  10% per-dimension threshold would cause on aspect ratio changes.
  When a resize IS suppressed, the server sends a control message
  `{"type":"resize_suppressed","width":W,"height":H}` — W/H being the dimensions
  **actually in force**, not the request — so the client can maintain correct
  aspect ratio (letterbox/pillarbox) instead of stretching. That message has a
  second producer: a change the encoder refused, which the frame loop publishes
  as `Applied { ok: false }` and the server fans out to every session (see
  [`./MODULE_SERVER.md`](./MODULE_SERVER.md) step 19).
- **Re-keyframe required.** New resolution invalidates the GOP; the
  pipeline forces an IDR on the first frame after the change.

---

## Congestion-Reactive Bitrate Control (Server-Measured, Manager-Driven)

**There is no bandwidth estimator in v1.** This is a congestion-*reactive*
multiplicative controller over a running `current` bitrate: multiplicative
decrease on a congestion signal, multiplicative increase on a quiet interval. It
never measures available throughput; it probes for it by ramping up until
something drops. Any text describing bandwidth as "derived", "estimated" or
"measured" is describing something this system does not do.

The **server** collects three signals — path statistics
(`smoothed_rtt`, `congestion_window`, `lost_packets`, from
`Session::path_stats()`), its own per-session `frame_out` drop rate, and the
client's `{"type":"stats"}` `dropped` delta — and feeds them into
`stream::Manager` every `[stream.adaptive] interval_ms`. The Manager applies the
policy below, stamps an epoch and enqueues a `ParamDelta` on the pipeline's param
funnel; the pipeline's frame loop is the sole mutator (M-6). The pipeline does
**not** measure telemetry itself.

### Aggregating N clients into one encoder setting

There is ONE encoder and N sessions, so the per-session signals must be reduced to
one number before the policy runs. The reducer is:

```
reference := the session holding the CONTROLLER slot.
             If no controller is connected, the session with the lowest session id
             among role="player", else among role="view".
             If no session is connected at all, the loop makes no change and the
             encoder keeps its current settings.

network_rtt_ms  := reference.smoothed_rtt
packet_loss_pct := reference.loss          // client stats + that session's frame_out drops

MAJORITY OVERRIDE: if at least 50% of connected sessions have been above
`loss_threshold_pct` for 2 consecutive windows (200 ms), use the MEDIAN loss and
the MEDIAN RTT across all sessions instead of the reference's. One slow client can
never move the encoder; a problem affecting half the room can.

FAST PATH: the immediate `fast_reduction_factor` cut fires only when the
`frame_out` drop is observed on the reference session, OR on at least 50% of
sessions inside the same 100 ms window. A per-session ring dropping stale frames
for one congested client is that ring doing its job (MODULE_SERVER "Broadcasting")
and MUST NOT touch the encoder.
```

The chosen reference is exported as the `client` label on
`featherdesk_adaptive_reference_client` (gauge = 1), so an operator can see whose
link the stream is being tuned to.

### The policy

```
[telemetry loop, runs every interval_ms]
   ↓
if params.bitrate_bps == 0 and no congestion signal yet: do nothing (see "Cold start")
   ↓
reduce the per-session signals to one RTT and one loss (see the reducer above)
   ↓
send ParamDelta::Telemetry { network_rtt_ms, packet_loss_pct }   // hints only
   ↓
[adaptation policy — two-tier response]
   FAST path (server-side): the datagram out-queue (frame_out) is dropping frames
       on overflow, on the reference session or on ≥50 % of sessions — the server,
       not the client, observes this immediately (a datagram send is
       fire-and-forget and reports nothing). CONGESTION DROPS ONLY: this tier
       counts featherdesk_datagram_send_drops_total{reason="congestion"}. A drop
       attributed to [transport] per_session_max_bps carries reason="policy"
       and is EXCLUDED — it is the operator's cap, not the network.
       On a sustained drop spike:
       new_bitrate = max(current * fast_reduction_factor, min_bitrate)
       (single measurement; 0.5x under the shipped defaults).
   SLOW path (client feedback):
       if packet_loss_pct > loss_threshold_pct for 2 consecutive windows (200ms):
           new_bitrate = max(current * adjustment_factor, min_bitrate)
       else if packet_loss_pct < recovery_threshold_pct for 10 consecutive
               windows (1s) AND RTT stable:
           new_bitrate = min(current * recovery_factor, max_bitrate)
   ↓
if new_bitrate != current:
   send ParamDelta::Bitrate { bps: new_bitrate }   // applied on the frame loop (M-6)
   (no config message — a bitrate change is transparent to the client decoder; the
    client's HUD shows measured throughput, not an advertised target)
```

### Cold start

The controller has no `current` to multiply until the session has one. Seeding is
defined for both quality modes:

```
seed_bitrate_bps =
    if params.bitrate_bps > 0 {
        params.bitrate_bps                       // explicit VBR: the config value
    } else {
        // Constant-QP mode. Take the congestion window of the attached session
        // with the LARGEST congestion_window/rtt ratio — the broadcast is one
        // stream, so the best-connected client is what the encoder can aim at,
        // and the per-session drop signal is what pulls it back down.
        //   bps = 8 * congestion_window_bytes / smoothed_rtt_seconds
        clamp(8.0 * congestion_window as f64 / smoothed_rtt.as_secs_f64(),
              min_bitrate_bps as f64, max_bitrate_bps as f64) as u32
    }
```

`congestion_window` and `smoothed_rtt` come from `Session::path_stats()`
([`./MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md)). With no session attached, with
`smoothed_rtt == 0`, or on a carrier that exposes no congestion window (it reports
`0`), the seed is `(min_bitrate_bps + max_bitrate_bps) / 2` — 13 Mbps under the
shipped bounds.

**The CQP→VBR transition is one-way and lazy.** While the session is in QP mode
the controller does **nothing**: no ticks, no adjustment, no config messages. It
arms on the first congestion signal (a `frame_out` drop spike, or loss above
`loss_threshold_pct` for two consecutive windows), and on that first signal it:

1. computes `seed_bitrate_bps` as above,
2. sets `params.bitrate_bps = seed_bitrate_bps` — the session is now in
   bandwidth-target mode for the rest of its life,
3. applies the reduction that armed it, and
4. logs once at `info`: `adaptive: leaving constant-QP (qp={qp}) for
   bitrate={seed} bps on first congestion signal`.

There is **no path back to constant-QP** within a session. A deployment that
wants CQP to be permanent sets `[stream.adaptive] enabled = false`; a deployment
that wants adaptation from the first frame sets `bitrate_bps` explicitly. The
shipped default — CQP-26 with adaptation armed — means "visually lossless until
the network says otherwise", which is the right behaviour for a LAN-first
product and is now a defined one.

### Bounds and policy knobs

Lives in `[stream.adaptive]` config section, and is the `AdaptivePolicy` the
Manager holds:

```toml
[stream.adaptive]
enabled                = true
interval_ms            = 100         # telemetry window
min_bitrate_bps        = 1_000_000   # 1 Mbps floor
max_bitrate_bps        = 25_000_000  # 25 Mbps ceiling
loss_threshold_pct     = 5.0         # trigger downscale
recovery_threshold_pct = 1.0         # allow upscale
fast_reduction_factor  = 0.5         # multiply on a send-side drop spike
adjustment_factor      = 0.7         # multiply on slow-path downscale
recovery_factor        = 1.1         # multiply on upscale
```

### What a parameter change costs the client

Two independent gates, because the two actions answer two different questions: a
fresh `config` answers "did anything the client can see change?", and a forced IDR
answers "did the reference chain become invalid?".

| Change to `Params` | Send `config`? | Force IDR? | Why |
|---|:---:|:---:|---|
| `bitrate_bps`, `qp` | no | no | invisible to the decoder; `ConfigPayload` carries neither |
| `fps` | **yes** | no | `config.fps` is client-visible (pacing, HUD) but the reference chain is intact |
| `width`, `height` | yes | **yes** | new dims invalidate every reference frame |
| `chroma_subsampling` | yes | **yes** | new profile ⇒ new SPS |
| `hdr`, `bit_depth`, `color_space` | yes | **yes** | new codec/profile/bit depth ⇒ new VPS/SPS |
| codec string changed (the level moved with the geometry) | yes | **yes** | the client must reconfigure its decoder |
| `keyframe_interval` | no | no | encoder-internal |
| `network_rtt_ms`, `packet_loss_pct` | no | no | hints; never reach the wire |

A *rebuilt* encoder needs no forced IDR on top: it emits fresh SPS/PPS on its own
first frame, and on the opt-in `x264` add-on a `force_keyframe()` over a rebuild
would kill and respawn `ffmpeg` a second time.

### Resolution-level adaptation (deferred)

For severe network degradation (>15% loss for 30s), reducing resolution is
more effective than reducing bitrate. **Deferred to v2** — initial release
adapts only bitrate.

---

## Capability Probing

Add-ons advertise which `stream::Params` fields they can change without restart:

```rust
pub struct StreamParamsCapability {
    pub hot_changeable: HashMap<String, bool>,          // e.g. {"bitrate_bps": true, "width": false}
    pub min_values: HashMap<String, serde_json::Value>,
    pub max_values: HashMap<String, serde_json::Value>,
}

// Optional add-on method, folded onto the base trait and gated by AddonCaps:
//   capture::ConfigurableCapturer, encode::ConfigurableEncoder and
//   hwencode::ConfigurableHardwareEncoder each carry
//     fn params_capability(&self) -> Result<StreamParamsCapability, StreamError>;
// alongside update_stream_params. There is no separate `StreamParamsCapable`
// trait: an optional capability reached by a downcast has no implementation
// (MODULE_ABI "Optional-method capability flags").
```

`FrameLoop::open()` reads this **on the frame thread** (MODULE_PIPELINE step 13),
through the CONSTRUCTED handle's `as_configurable()` accessor, for each selected
add-on whose authoritative `caps()` sets `CONFIGURABLE` / `ENC_CONFIGURABLE`.
Startup step 3g, inside `pipeline::new()`, only RECORDS whether each selected
add-on's `ProbeResult.caps` claimed those bits: it constructs nothing, and the
`stream::Manager` does not exist until step 12, nine steps after it. The
capability reaches the Manager on the first `Applied` that follows a construction
or a swap — `stream::Applied.caps` above, which `Manager::apply` re-reads from
`applied_rx.borrow()` before it clamps. It decides:
- Which fields trigger `update_stream_params()` vs a full add-on restart. The
  `AddonCaps` bit only answers "configurable at all?"; `hot_changeable` answers
  it **per field** (e.g. bitrate hot, width not).
- Validation bounds for incoming client requests — `Manager::apply` clamps
  `resize`/`set_bitrate`/`set_fps` to `min_values`/`max_values` rather than
  letting an out-of-range value reach the encoder.

Add-ons that do not set `CONFIGURABLE` / `ENC_CONFIGURABLE` are treated as fully
immutable: every parameter change requires restart, and requests are clamped to
the `[stream]` config bounds only.

> **Layer-2 only.** `StreamParamsCapability` carries `HashMap` and
> `serde_json::Value`, neither of which is `StableAbi`. The boundary form is
> `abi::RParamsCapability` — a fixed struct, returned by `params_capability()` on
> the add-on's sabi object ([`MODULE_ABI.md`](./MODULE_ABI.md)). The adapter expands
> it host-side: `hot_changeable`'s bits become the `HashMap<String, bool>` keyed by
> the exact snake_case `stream::Params` field names (`"width"`, `"height"`, `"fps"`,
> `"bitrate_bps"`, `"qp"`, `"bit_depth"`, `"hdr"`, `"color_space"`,
> `"chroma_subsampling"`, `"keyframe_interval"`), and each non-zero bound becomes a
> `serde_json::Value::Number` under the same key. A bound of `0` is **omitted**, not
> encoded — an omitted key means "clamp to the `[stream]` config bounds only".

---

## What Stays in `[addon_module_*]` TOML Sections

`stream::Params` fields **leave** `[encode]` and `[capture]` sections. The
add-on-specific TOML sections keep only **static, startup-read tuning** that
doesn't fit the dynamic Params model:

| Add-on | Static keys retained in `[addon_module_*]` |
|--------|--------------------------------------------|
| openh264 | `threads`, `slice_mode` |
| x264 | `ffmpeg_path`, `preset` (ultrafast..medium), `threads`, `tune`, `profile` (high/high422/high444) |
| nvenc | `gpu` index, `preset` (p1..p7), `tune` (ull/ll/hq), `multipass` |
| amf | `usage` (lowlatency/transcoding), `quality` (speed/balanced/quality) |
| mf_hw | `adapter_index`, `rate_control_mode` enum |
| qsv | `adapter_index`, `target_usage` (1..7) |
| vt_hw | `realtime` bool, `profile` enum, `allow_frame_reordering` |
| dxgi_dd | `output_index`, `auto_install_vdd`, `virtual_display_*` |
| sck | `display_id` |
| kms_egl | `drm_card`, `output_index` |

The TOML `[stream]` section provides **initial defaults**; runtime
`stream::Params` may diverge based on client requests and adaptive policy.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | HDR terminal case: `HDR:true` with no HEVC-Main10-capable encoder loaded → `{"type":"hdr_unavailable","reason":"no_hevc_encoder"}`, session stays SDR, capture is never half-switched to 10-bit | No |
| Unit | HDR admission: `set_hdr:true` with one attached session reporting `decode.hevc10 == false` is refused with reason `attached_client_cannot_decode`; capture is never switched to 10-bit | No |
| Unit | Chroma fallback cascade: the auth-time reducer clamps to the weakest attached client; encoder `ChromaUnsupported` → downgrade to the encoder's best; separately, a synthetic client `{"type":"decode_unsupported"}` → forced downgrade to 420 + fresh `config` + keyframe | No |
| Unit | Resize hysteresis: dimension change ≤5% and aspect-ratio change ≤2% is suppressed (`resize_suppressed` sent); either threshold exceeded applies the resize | No |
| Unit | Native is a per-dimension ceiling, not a ratio: on a 1920×1080 host, a `resize` to 1280×1024 (a ratio the host does not have) is applied as 1280×1024, and a `resize` to 3840×2160 is clamped to 1920×1080. Proves the aspect ratio follows the client and is never corrected toward the host's | No |
| Unit | **Policy drops are not congestion.** With `[transport] per_session_max_bps` capping one viewer below the stream bitrate, that session drops frames continuously and the session-wide bitrate is UNCHANGED: the FAST tier counts only `reason="congestion"` drops. Mixing the two would let one capped viewer drag the whole room down — the failure the ≥50 %/majority-override rule exists to prevent | No |
| Unit | Congestion-reactive policy math: sustained loss above `loss_threshold_pct` for 200ms → `current * adjustment_factor` clamped to `min_bitrate_bps`; loss below `recovery_threshold_pct` for 1s + stable RTT → `current * recovery_factor` clamped to `max_bitrate_bps`; a single-window drop-queue spike → `current * fast_reduction_factor` | No |
| Unit | Adaptive reducer: one viewer at 40 % loss with the controller clean leaves the bitrate unchanged; half the sessions above threshold for 200 ms applies the median | No |
| Unit | Cold start: with the shipped defaults (`bitrate_bps = 0`, `qp = 26`, adaptive enabled), the controller emits nothing until the first congestion signal, then seeds from `8*congestion_window/smoothed_rtt` clamped into `[min,max]` and stays in bitrate mode | No |
| Unit | A bitrate-only or QP-only change produces NO config message and NO forced IDR; a dimension, chroma, bit-depth or codec-string change produces both; a rebuild produces a config but no extra `force_keyframe` | No |
| Unit | `StreamParamsCapability.hot_changeable` gates whether a param change calls `update_stream_params()` vs. tears down and recreates the add-on | No |
| Unit | `ParamDelta` folding: a `Resize` and a later `Bitrate` delta both land; neither reverts the other's field, and a delta at or below the applied epoch is ignored | No |
| Integration | Concurrent resize requests are coalesced to the latest only; the pipeline never applies a stale intermediate resolution | No |
| Integration | A resolution or HDR change forces an IDR on the first frame after the change (GOP invalidated), and a bitrate- or qp-only change forces neither an IDR nor a `config` | No |
| Integration | Per-add-on translation table spot-checks: `BitrateBps` is hot on OpenH264/NVENC/AMF/VT HW but requires an ffmpeg restart on x264; `Width`/`Height` is cold (`Terminate`+`ReInit`) on AMF specifically | Yes (per encoder add-on) |
| Integration | A failed parameter change republishes the UNCHANGED params with `ok:false`, so the Manager's `current()` never diverges from what the frame loop is running | No |
| Integration | Add-ons that do not set `CONFIGURABLE`/`ENC_CONFIGURABLE` are torn down and recreated on every param change, with no dropped-frame gap wider than one GOP | Yes (per add-on) |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| `Manager::apply` (clamp + hysteresis + enqueue) | <50 us — it runs on the server's telemetry tick and on every client control message |
| Adaptive telemetry tick | 100 ms (`[stream.adaptive] interval_ms`) |
| Param change applied on the frame loop | within one frame interval of the enqueue |
| Resize end-to-end (client `resize` → first frame at the new dims) | <250 ms, dominated by the client's own 250 ms debounce and one encoder rebuild |

---

## Status

📋 **Specced.** Implementation requires:

1. `featherdesk-stream` crate (`params.rs`) — `stream::Params`, `ParamDelta` /
   `ParamUpdate` / `Applied`, `AdaptivePolicy`, `StreamError`, and the
   `ConfigurableEncoder` / `ConfigurableHardwareEncoder` / `ConfigurableCapturer`
   interfaces
2. Pipeline updates — the param funnel drain, the congestion-reactive controller,
   sustainable-rate control, the resize message handler
3. Per-add-on `update_stream_params` + `params_capability` implementations
4. Protocol additions — `{"type":"resize"}` control-stream JSON message handler,
   and the `Server::send_control` producer for `hdr_unavailable` /
   `resize_suppressed`
5. HDR pipeline — the admission gate, encoder switching when `Params.HDR` flips,
   and the forced exit from HDR in `degrade_to_software`
