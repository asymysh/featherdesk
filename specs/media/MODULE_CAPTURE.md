# Module Spec: Capture (Cross-Platform Capturer Interface Contract)

## Overview

The Capture module defines the **abstract capturer interface contract** that
every capture add-on implements. It owns no capturer implementation itself —
concrete capturers live in their respective add-on specs:

- `addons/capture/kms_egl/` — Linux KMS+EGL DMA-BUF (add-on ID `kms_egl`)
- `addons/capture/nvfbc/` — Linux NvFBC NVIDIA proprietary (add-on ID `nvfbc`)
- `addons/capture/sck/` — macOS ScreenCaptureKit (add-on ID `sck`)
- `addons/capture/dxgi_dd/` — Windows DXGI Desktop Duplication (add-on ID `dxgi_dd`)

This separation keeps the module spec stable while letting per-OS capture
implementations evolve independently.

---

## Public Interface

```rust
// crate: featherdesk-capture

// Frame holds raw screen pixels (CPU-resident).
// Used by the SW encoder path (pixels → I420 → encoder). Cleanup is RAII (Drop)
// — no Close(). Across the add-on ABI `data` is an owned RVec<u8> whose
// ownership transfers to the host (deterministic drop).
pub struct Frame {
    pub data: RVec<u8>,         // Pixel buffer (stride * height bytes), owned
    pub stride: u32,            // Bytes per row. MAY exceed width*4 (GPU readback
                                // rows are often padded to a power-of-two pitch).
                                // The libyuv `*Matrix` entry points take this stride
                                // directly; assuming stride == width*4 corrupts the
                                // image on padded backends (e.g. DXGI). width*4 is
                                // the valid byte count per row.
    pub pixel_fmt: PixelFormat, // Bgra (macOS/Windows) or Rgba (Linux GL)
    pub width: u32,
    pub height: u32,
    pub rotation: Rotation,     // how far CLOCKWISE this buffer must be turned to
                                // be upright. width/height describe the buffer AS
                                // DELIVERED — for R90/R270 they are the transposed
                                // (sideways) dimensions, and the UPRIGHT geometry
                                // is (height, width).
    pub timestamp_ns: u64,      // CLOCK_MONOTONIC ns, sampled at capture
}

#[repr(u8)]
pub enum PixelFormat {
    Bgra = 0,  // BGRA in memory = libyuv "ARGB" → kArgb* constants
    Rgba = 1,  // RGBA in memory = libyuv "ABGR" → kAbgr* constants
}
// Both variants are 8-bit. There is no 10-bit CPU-readback format: the SW
// path is 8-bit end to end, and a 10-bit HDR session that degrades to it
// leaves HDR first (see MODULE_PIPELINE "degrade_to_software").

// Rotation is how many degrees CLOCKWISE the returned buffer must be turned to
// appear upright. It describes the buffer the add-on is handing over; the add-on
// does NOT rotate. Sources: DXGI_OUTDUPL_DESC.Rotation (dxgi_dd), the DRM plane's
// "rotation" property (kms_egl), NvFBC's tracking of the same (nvfbc). SCK
// composites rotation itself and always reports R0.
// Declared in `featherdesk-abi` (MODULE_ABI), not here: it is a field of `RFrame`
// and `RFbInfo`, and `featherdesk-abi` may not name a type from a crate that
// imports it. This crate re-exports it, so every `capture::Rotation` path is
// unchanged.
pub use abi::Rotation;

// FbInfo holds a GPU-resident surface handle (zero-copy path).
// Used by the HW encoder path — the capturer never touches CPU memory.
// `handle` is a tagged enum (SurfaceHandle), not a struct of nullable per-OS
// fields; FbInfo's Drop releases the underlying resource exactly once (RAII
// replaces the manual `Release func()`). Per-OS handle variants:
//   Linux:   DMA-BUF fd + DRM format + modifier
//   macOS:   IOSurface backing the CMSampleBuffer
//   Windows: ID3D11Texture2D
pub struct FbInfo {
    pub width: u32,
    pub height: u32,
    pub rotation: Rotation,       // how far CLOCKWISE this surface must be turned to
                                  // be upright. width/height describe the surface AS
                                  // DELIVERED — for R90/R270 they are the transposed
                                  // (sideways) dimensions, and the UPRIGHT geometry
                                  // is (height, width).
    pub timestamp_ns: u64,        // CLOCK_MONOTONIC ns, stamped at capture
    pub handle: SurfaceHandle,
}

// Across the add-on ABI the boundary form is `RSurfaceHandle` — a raw fd for
// DmaBuf, a `*mut c_void` for IoSurface and D3D11Texture — which the Layer-2
// adapter re-wraps into the variants below, so `Drop` is unchanged host-side
// (see MODULE_ABI "Rich types across the boundary").
pub enum SurfaceHandle {
    // Linux: DMA-BUF. `OwnedFd` closes the fd on Drop (no manual close).
    DmaBuf { fd: std::os::fd::OwnedFd, stride: u32, fourcc: u32, modifier: u64 },
    // macOS: CVPixelBuffer-backed IOSurface (retained; released on Drop).
    IoSurface(objc2_io_surface::IOSurface),
    // Windows: ID3D11Texture2D (COM ref released on Drop via windows-rs).
    D3D11Texture(windows::Win32::Graphics::Direct3D11::ID3D11Texture2D),
}

// Capturer is the contract every capture add-on must satisfy.
//
// CPU readback path: next_frame() returns CPU-resident BGRA pixels.
// Used by the SW encoder path. Cleanup is RAII (Drop) — no Close().
//
// Thread-affine, but declared `Send`. The host constructs, uses and drops this
// object on a single worker thread (the frame-loop thread — CENTRAL_SPEC
// "Concurrency Model") and it never crosses a thread boundary once built —
// `Send` is required only because the empty `FrameLoop` / `AudioLoop` struct that
// will build it is moved onto that thread by `Pipeline::start`. The object may
// hold a bound EGL/D3D/COM context for its whole life (MODULE_ABI "Thread
// requirements").
pub trait Capturer: Send {
    /// next_frame returns the most recent screen frame as CPU pixels. It BLOCKS
    /// waiting for NEW CONTENT, up to the per-frame deadline (~one frame
    /// interval). The deadline bounds only the WAIT: once the add-on has new
    /// content it completes the call, even if its own readback then takes longer
    /// than one interval. It never returns Ok(None) because its own work was
    /// slow — a capturer whose steady-state cost exceeds the interval is handled
    /// by the pipeline's sustainable-rate control (MODULE_PIPELINE), which lowers
    /// the advertised fps, not by the capturer pretending the screen is idle.
    /// Three outcomes:
    ///   Ok(Some(frame)) — new content (owned: `frame.data` is an RVec<u8> whose
    ///                     ownership transfers to the caller — no borrowed-slice
    ///                     footgun).
    ///   Ok(None)        — NO NEW CONTENT within the deadline (screen idle). The
    ///                     caller skips this tick; it does NOT re-encode. A newly
    ///                     joined client is still served from the IDR cache via
    ///                     the bootstrap stream, so idle screens cost ~zero
    ///                     bandwidth.
    ///   Err(_)          — capture failed (device lost, etc.).
    /// It never returns a stale frame as if it were new, and never blocks
    /// forever on an idle screen.
    fn next_frame(&mut self) -> Result<Option<Frame>, StreamError>;
}

// SurfaceCapturer is the optional zero-copy contract.
// Capture add-ons that can produce GPU surfaces (KMS+EGL, NvFBC, SCK, DXGI DD)
// implement this in addition to Capturer.
//
// The pipeline uses this when a hardware encoder is selected. Reached through
// `CaptureHandle::as_surface()` — never a separate `Box`, because there is only
// one add-on object.
// NOTE: the old name "DMABufCapturer" was Linux-specific. The trait is
// cross-platform — the returned FbInfo carries a SurfaceHandle tagged enum with
// per-OS variants (DmaBuf, IoSurface, D3D11Texture).
pub trait SurfaceCapturer: Capturer {
    /// next_surface returns a GPU-resident surface handle (same blocking + None
    /// semantics as next_frame: Ok(None) means "no new content this interval").
    /// OWNERSHIP: the FbInfo is normally passed BY VALUE straight into a HW
    /// encoder's encode_surface, where FbInfo's Drop releases the resource
    /// exactly once on every path. The pipeline does NOT release it itself in
    /// that case. ONLY if the surface is never handed to an encoder (e.g.
    /// probe/teardown) does dropping the FbInfo here release it — release is
    /// automatic (RAII) per-platform via SurfaceHandle's Drop.
    fn next_surface(&mut self) -> Result<Option<FbInfo>, StreamError>;
}

// CaptureConfig holds the capturer's INITIAL configuration. Once running,
// dynamic parameters (width, height, fps, HDR) flow through stream::Params
// and ConfigurableCapturer (see MODULE_STREAM_PARAMS.md). Logging is via the
// `tracing` crate. An add-on's events reach the operator only if it installed
// the host's sink — `featherdesk_abi::install_log_sink(host.log)` in `init()`
// (MODULE_ABI "Root module surface"); each `cdylib` otherwise has its own
// uninitialised dispatcher and its output is discarded.
//
// Per-add-on STATIC tuning (DRM card path, IOSurface format, DXGI adapter
// index) comes from the [addon_module_<id>] TOML section.
pub struct CaptureConfig {
    pub initial_params: stream::Params,  // initial width/height/fps/HDR/bit_depth
    pub embed_cursor: bool,              // true = composite the OS pointer into every
                                         //   buffer returned by next_frame (and
                                         //   next_surface, if this add-on declared
                                         //   EMBED_CURSOR_SURF); false = return
                                         //   cursor-free buffers and serve
                                         //   next_cursor instead. This is the ONLY
                                         //   input an add-on has on the question —
                                         //   there is no per-add-on cursor key.
}

// ConfigurableCapturer lets the pipeline change capture parameters at
// runtime (resolution, HDR mode). Add-ons that don't implement this are
// torn down + recreated whenever capture parameters change. Reached through
// `CaptureHandle::as_configurable()` — never a separate `Box`, because there is
// only one add-on object.
pub trait ConfigurableCapturer: Capturer {
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), StreamError>;
    fn params_capability(&self) -> Result<StreamParamsCapability, StreamError>;
}

// CursorState is one observation of the OS pointer, in CAPTURE-DEVICE pixels.
// The host converts to stream pixels and assigns the wire ShapeID — the add-on
// never does either (see "Cursor coordinate space" and "Shape identity").
//
// CursorShape is a cursor bitmap in the one canonical wire format (see "Cursor
// pixel format"), at the OS's NATIVE resolution. `screen_*` and `hotspot_*` are
// in CAPTURE pixels and describe where and how large the pointer is drawn on the
// captured display; `width`/`height` describe the buffer. They differ when the OS
// draws a low-resolution bitmap scaled up (hi-dpi) — never because the add-on
// resampled, because no add-on resamples. The 128-pixel cap is a WIRE cap and is
// applied by `CursorPublisher::tick` on the host.
//
// Both are declared in `featherdesk-abi` (MODULE_ABI) — `CursorState` is the
// return type of `Capturer::next_cursor` at Layer 1, and `featherdesk-abi` may not
// name a type declared in `featherdesk-capture`, which imports it. Field set,
// types and per-field comments are unchanged, so the Layer-2 name and every
// `capture::CursorState` / `capture::CursorShape` path is unchanged too.
pub use abi::{CursorState, CursorShape};

// CursorCapturer is the HOST-SIDE PRODUCER of the client-side cursor overlay.
// It is OPTIONAL and orthogonal to the frame path: a capture add-on implements
// it when the OS can report the cursor separately from the framebuffer, and
// declares `AddonCaps::CURSOR` at probe time so the host knows (a sabi object
// cannot be downcast). Reached through `CaptureHandle::as_cursor()` — never a
// separate `Box`, because there is only one add-on object.
//
// Why it lives on the capture add-on rather than in its own module: the add-on
// already owns the OS display handle the cursor query needs, and on Windows the
// cursor arrives as METADATA OF THE SAME AcquireNextFrame call — splitting it
// into a separate module would mean a second, redundant duplication handle.
//
// This is what feeds `server.send_cursor` / `server.send_cursor_shape`
// (MODULE_SERVER) → datagram `frame_type::CURSOR_UPDATE` (11) + the cursor stream
// (`stream_type::CURSOR`, 0x11) → the client's `cursor.js` overlay.
pub trait CursorCapturer: Capturer {
    /// Returns the pointer state IF it changed since the last call, else `Ok(None)`.
    /// Non-blocking — the pipeline polls this on the frame loop, so it must never wait
    /// on the compositor.
    ///
    /// FIRST CALL: the first call after this object is constructed MUST return
    /// `Ok(Some(..))` carrying the current position, the current visibility AND the
    /// current bitmap (`shape: Some(..)`), even though nothing has changed. The host
    /// has no other way to learn the initial pointer state, and a client joining a host
    /// whose pointer never moves is seeded from it (MODULE_SERVER lifecycle step 15).
    /// From the second call on, the "only if it changed" rule applies.
    ///
    /// `shape` is `Some` ONLY when the cursor BITMAP changed (arrow -> I-beam ->
    /// resize). A pure position move returns `shape: None`, which is the common case
    /// and costs one 22-byte datagram. The pipeline does not diff bitmaps on the hot
    /// loop; the add-on is the one the OS tells.
    ///
    /// The bitmap is returned at the OS's NATIVE size. An add-on never downscales and
    /// never applies the 128-pixel wire cap — that cap is the host's, enforced in
    /// `CursorPublisher::tick`.
    ///
    /// LATCHING: `next_cursor` MUST report a change without `next_frame` /
    /// `next_surface` having been called since the previous poll. On a platform where
    /// the pointer arrives as frame metadata, the add-on issues that OS call from HERE
    /// and serves the frame from what it produced — see "Cursor and frame acquisition
    /// order".
    fn next_cursor(&mut self) -> Result<Option<CursorState>, StreamError>;
}

/// blend_cursor alpha-composites a cursor shape over a CPU pixel buffer, in
/// place. This is the ONE software cursor compositor in the tree. Add-ons that
/// declare `AddonCaps::EMBED_CURSOR` call it from `next_frame` immediately before
/// returning the Frame; nothing else anywhere blends a cursor, and the host
/// never does (on the zero-copy path the frame never reaches the CPU, which is
/// why embedding is an add-on capability and not a pipeline stage).
///
/// `dst` is `frame.data`: `stride * height` bytes, channel order `fmt`, top-down.
/// `shape.pixels` is straight (non-premultiplied) RGBA, so the blend is plain
/// source-over with an integer divide, per channel:
///
///     out = (sa * src + (255 - sa) * dst + 127) / 255
///
/// with the two fast paths that matter for a mostly-transparent bitmap:
/// `sa == 0` skips the pixel, `sa == 255` copies it. The destination alpha byte
/// is set to 255 (the SW path's YUV conversion ignores it).
///
/// `x`/`y` place the shape's TOP-LEFT corner in `dst` pixels — that is, the
/// hotspot position minus the shape's hotspot — and MAY be negative. Pixels
/// outside `dst` are clipped, never wrapped.
///
/// PRECONDITION: `shape.width`/`shape.height` are the add-on's own full-resolution
/// bitmap; the 128-pixel cap is a WIRE cap applied by `CursorPublisher::tick` and
/// never by an add-on.
pub fn blend_cursor(
    dst: &mut [u8],
    stride: u32,
    width: u32,
    height: u32,
    fmt: PixelFormat,
    shape: &CursorShape,
    x: i32,
    y: i32,
);

// CaptureHandle is what the HOST holds: exactly ONE owning value per capture
// add-on instance. The Layer-2 adapter is a concrete host-side type that
// implements Capturer plus whichever optional traits the constructed object's
// `caps()` claims, forwarding each call to the one `CapturerBox`.
// The optional traits are reached through accessors rather than through a second
// Box, because a `#[sabi_trait]` object is one flat vtable and cannot be
// downcast (MODULE_ABI "Optional-method capability flags") — and because three
// owning Boxes over one object means dropping any one of them frees the other
// two.
pub trait CaptureHandle: Capturer {
    /// The AUTHORITATIVE capability set: read from the CONSTRUCTED object and
    /// masked to the probe's claim. `probe()` is config-blind — `dxgi_dd` only
    /// learns it lost SURFACE when construct() falls back to a WARP adapter —
    /// so `caps()` may be a strict subset of the probe's and is never a
    /// superset; the adapter masks any extra bit and logs that masking once at
    /// `warn`. It never widens after construction, and it is the SOLE input to
    /// the accessors below (MODULE_ABI "Optional-method capability flags").
    fn caps(&self) -> abi::AddonCaps;

    /// Clears one bit for the rest of the session: the matching accessor
    /// returns `None` from here on. Idempotent. This is how the host disables a
    /// capability — on a capability lie (`StreamError::Unsupported`), on
    /// `degrade_to_software` (clears `SURFACE`), and on a `next_cursor` failure
    /// (clears `CURSOR`). It NEVER drops the object: on X11 and DXGI the cursor
    /// query and the surface path borrow the SAME display/duplication handle
    /// the frame path is still using.
    fn clear_cap(&mut self, bit: u32);

    /// `Some` iff `caps().has(abi::AddonCaps::SURFACE)`. Zero-copy path.
    fn as_surface(&mut self) -> Option<&mut dyn SurfaceCapturer>;

    /// `Some` iff `caps().has(abi::AddonCaps::CURSOR)`. There is no second
    /// gate: when the resolved cursor mode is not "separate" the pipeline
    /// clears CURSOR with `clear_cap`, so this returns `None` by construction
    /// (see "Cursor delivery"). Returning `None` disables cursor polling for
    /// the session; it never frees the capture object.
    fn as_cursor(&mut self) -> Option<&mut dyn CursorCapturer>;

    /// `Some` iff `caps().has(abi::AddonCaps::CONFIGURABLE)`. Hot parameter
    /// changes; `None` means every parameter change is a teardown + rebuild.
    fn as_configurable(&mut self) -> Option<&mut dyn ConfigurableCapturer>;
}
```

### Cursor delivery and the `"separate"` / `"embedded"` decision

`Config.cursorMode` is **derived from capability**, with `[capture] cursor_mode`
choosing the policy rather than the answer. Because a capturer that can deliver no
cursor at all must never be selected, the check runs during **capture selection**
(MODULE_PIPELINE startup step 3d, inside `pipeline::new()`), not after it: an
ineligible add-on is skipped exactly like one whose probe reported
`available = false`, and the existing dispatch order falls through to the next
candidate.

The table below is that truth table, and `pipeline::CursorMode::resolve(policy,
caps)` is its ONE implementation — the "**no**" rows are the ones where it returns
`None`. It is evaluated twice, against two different capability sets:

1. at startup step 3d, against the cached `ProbeResult.caps`, to decide
   eligibility and to record the resolved mode on the `SelectionPlan`;
2. inside `FrameLoop::open()` (startup step 13, on the frame thread), against the
   CONSTRUCTED handle's authoritative `caps()`, which may be a strict subset of
   the probe's.

Only the second evaluation can fail late, and it is the only place the resolution
can fail at all: if it now returns `None`, or a mode whose `embed_cursor()` differs
from the value the constructor was given, the add-on lied about a capability
(MODULE_ABI "Misbehaving add-ons") and the pipeline falls through to the next
capture candidate, failing startup with the same rejection list step 3d would have
produced if none remains.

| `[capture] cursor_mode` | The `caps` being decided against | Eligible? | Resolved `cursorMode` | `CaptureConfig.embed_cursor` |
|---|---|---|---|---|
| `"auto"` | `CURSOR` set | yes | `"separate"` | `false` |
| `"auto"` | `CURSOR` clear, `EMBED_CURSOR` set | yes | `"embedded"` | `true` |
| `"auto"` | neither set | **no** — skipped in the dispatch order | — | — |
| `"separate"` | `CURSOR` set | yes | `"separate"` | `false` |
| `"separate"` | `CURSOR` clear | **no** — skipped | — | — |
| `"embedded"` | `EMBED_CURSOR` set | yes | `"embedded"` | `true` |
| `"embedded"` | `EMBED_CURSOR` clear | **no** — skipped | — | — |

`CURSOR` and `EMBED_CURSOR` are not exclusive; when both are set, `"auto"`
prefers `"separate"` — the frame stays cursor-free and the pointer gets its own
latency. `"separate"` is **not** a precondition of the zero-copy path: an add-on
that declares `EMBED_CURSOR_SURF` composites onto the surface itself, which is how
the macOS `sck` + `vt_hw` pairing runs zero-copy while always resolving
`"embedded"`.

If **no** capture add-on is eligible, startup fails with the add-ons it rejected
and why — never with a running stream that has no visible pointer:

    capture: no eligible capture add-on. [capture] cursor_mode = "separate"
    requires AddonCaps::CURSOR; rejected: dxgi_dd (probe unavailable: no output),
    sck (no CURSOR).

**Path selection interacts with embedding.** When the resolved mode is
`"embedded"`, the zero-copy pairing additionally requires
`caps & EMBED_CURSOR_SURF`; an add-on that can only embed into a CPU frame is
paired with the software path (MODULE_PIPELINE startup step 4). `"separate"`
imposes no such constraint — the pointer is not in the buffer either way.

**What the resolved mode then switches.** `FrameLoop::open()` either builds the
session's `CursorPublisher` (mode `"separate"`), or calls
`clear_cap(abi::AddonCaps::CURSOR)` on the handle and builds none (mode
`"embedded"`), so `CaptureHandle::as_cursor()` returns `None` and frame-loop step
(0b) is skipped by construction. There is no second gate on the polling decision.

**Per-OS availability:**

| Add-on | `CURSOR` source | `EMBED_CURSOR` mechanism |
|--------|-----------------|--------------------------|
| `kms_egl` (Linux, X11 **and** Wayland) | The DRM **cursor plane**: `drmModeGetPlane(cursor_plane)` gives `crtc_x`/`crtc_y` every poll with no readback; `drmModeGetFB2` + `drmPrimeHandleToFD` + EGL import + `glReadPixels` gives the bitmap at the plane's ADVERTISED size (`drmModeGetFB2`'s `width`/`height` — commonly 64x64, up to 256x256 on AMD/Intel CRTCs), only when `fb_id` changes. Declared when a plane of type `DRM_PLANE_TYPE_CURSOR` exists on the selected CRTC. | `capture::blend_cursor` into the CPU readback buffer (CPU path only, so `EMBED_CURSOR` without `EMBED_CURSOR_SURF`). When no cursor plane exists the compositor has already composited the pointer into the primary plane, so embedding is a no-op and both embed bits are set while `CURSOR` is clear. |
| `nvfbc` (Linux, X11 only) | XFixes `XFixesGetCursorImage` (position, shape and hotspot in one call). NvFBC requires X11, so its probe is unavailable under Wayland and the question does not arise. | `bWithCursor = NVFBC_TRUE` — the driver composites, on every capture type, so both embed bits are set. |
| `dxgi_dd` (Windows) | `DXGI_OUTDUPL_FRAME_INFO.PointerPosition` (position + visibility, delivered by every `AcquireNextFrame` including mouse-only updates) and `IDXGIOutputDuplication::GetFramePointerShape` (shape, only when `PointerShapeBufferSize > 0`). Always declared. | `capture::blend_cursor` into the CPU staging copy (CPU path only). The desktop texture never contains the pointer, so `EMBED_CURSOR_SURF` is never set. |
| `sck` (macOS) | Not declared. The only public shape source is `NSCursor`, which is AppKit and therefore main-thread-affine: every poll would have to be posted to the process main queue and read back through a latest-only slot, for a pointer ScreenCaptureKit already composites for free — see SCK_MACOS_SPEC "Cursor Handling". | `SCStreamConfiguration.showsCursor = YES` — ScreenCaptureKit composites before the `CMSampleBuffer` is delivered, so it costs nothing and works on the zero-copy path; both embed bits are set. macOS therefore always resolves `"embedded"`. |

The resolved value goes into the `config` handshake message, so the client is told
which mode it got — it never assumes. A mid-session capturer swap (fall-through per
MODULE_PIPELINE "Add-On Crash Recovery") re-runs `CursorMode::resolve` against the
new add-on's constructed `caps()` inside `fall_through` and pushes a fresh `config`
if the mode changed.

**One input, so there can be no second answer.** `CaptureConfig.embed_cursor` is
the only thing that tells an add-on whether to composite the pointer, and
`CaptureConfig` is the only thing an add-on's constructor receives. Its value is
`CursorMode::embed_cursor()` for the mode resolved at step 3d, supplied by
`FrameLoop::open()` when it constructs the add-on — the only expression in the tree
that derives it, and no other value may be passed to the constructor. There is no
per-add-on cursor key in any `[addon_module_*]` section, and an add-on MUST NOT
read one: two switches for one question is exactly how a stream ends up with a
compositor cursor burned into the frame *and* a client overlay drawn on top of
it. `probe()` takes no config, so a per-add-on key could never be reflected in
`caps` either — the capability answers "can you", the config answers "do it", and
they are asked of different objects at different times.

---

## Surface Handle Abstraction Across OSes

The `FbInfo` struct carries a `SurfaceHandle` tagged enum — only the variant
corresponding to the current OS is constructed. HW encoder add-ons `match` on the
variant and consume the appropriate handle:

| OS | Capture output | HW encoder input |
|----|----------------|------------------|
| Linux | DMA-BUF fd | libva imports via `vaCreateSurfaces` + `VASurfaceAttribExternalBuffers` |
| Linux | DMA-BUF fd | NVENC imports via `cuGraphicsEGLRegisterImage` |
| macOS | IOSurface | VideoToolbox accepts CVPixelBuffer directly |
| Windows | ID3D11Texture2D | MF HW / NVENC / AMF / QSV all accept D3D11 textures |

The pipeline never converts between formats — it pairs a capturer with an
encoder both on the same platform, and the HW encoder add-on knows which
`SurfaceHandle` variant of `FbInfo` to read.

---

## Capturer Selection (Pipeline Owns This)

The Capture module does **not** decide which capturer to use. That dispatch
lives in [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md), which:

1. Reads `[capture]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes each loaded capture add-on (NvFBC > KMS+EGL on Linux; SCK on
   macOS; DXGI DD on Windows), skipping any add-on whose `caps` cannot satisfy
   `[capture] cursor_mode` (see "Cursor delivery") — that check runs here, before
   selection, so a capturer that can deliver no cursor is never chosen
3. On Windows headless: triggers IddCx VDD auto-install before retrying probe
4. Calls the chosen add-on's constructor with `CaptureConfig`
5. Passes the resulting `CaptureHandle` to the frame loop, which reaches the
   optional contracts through its accessors

There is **no `CaptureBackend` enum** in this module. Selection is purely
runtime — loaded add-ons + TOML config decide.

---

## Display selection (which monitor)

**v1 captures exactly one display.** Which one is chosen by a **static
per-capturer config key**, resolved once at startup (a restart applies a change —
consistent with the rest of the add-on model). The key is uniform in intent
across every capture add-on, named per each backend's native concept:

| Add-on | Config key | `[addon_module_*]` default | Selects |
|--------|-----------|----------------------------|---------|
| `kms_egl` (Linux) | `output_index` | `0` | connected CRTC/connector index |
| `nvfbc` (Linux) | `output_index` | `0` | NvFBC output index |
| `dxgi_dd` (Windows) | `output_index` (+ `adapter_index`) | `0` | `IDXGIOutput` index |
| `wl_screencopy` (Linux) | `output_name` | `""` | `wl_output` name (e.g. `HEADLESS-1`, `DP-1`); `""` = the compositor's first output |
| `pw_portal` (Linux) | `output_name` | `""` | a monitor-name *hint*; the portal remains the authority over source choice |
| `sck` (macOS) | `display_id` | `0` | main display sentinel / a `CGDirectDisplayID` (`NSScreenNumber`), not an index |

- **Default `0` = the primary/first active display.**
- The selected display's native resolution flows through the pipeline; `[stream]
  width/height = 0` means "use the display's native mode" (the encoder scales, per
  "Resolution: capture is always native"). There is no `[capture] width` or
  `[capture] height`: `[capture]` declares exactly `mode`, `force_addon` and
  `cursor_mode`.
- **Out of scope for v1 (deferred to v2 / the native client):** runtime display
  **enumeration** advertised to the client, client-driven **monitor switching**,
  and **multi-monitor capture** (stitched or per-monitor streams). See
  [`MODULE_NATIVE_CLIENT.md`](../client/MODULE_NATIVE_CLIENT.md) (multi-monitor row)
  and `PLATFORM_COMPAT.md`. A monitor hotplug/mode change on the *selected* display
  is still handled live via the resolution-change flow (CENTRAL_SPEC
  "Resolution-Change Flow").

---

## What Was Rejected

The module intentionally does NOT support:

- **X11grab via ffmpeg subprocess** — rejected (subprocess overhead, no
  zero-copy path, deprecated in favor of KMS+EGL)
- **X11-specific capture (XShm, XComposite/XDamage)** — rejected; KMS+EGL
  covers X11, and no X11-only deployment needs a no-root path the two Wayland
  add-ons do not cover better
- **`vkms` (kernel virtual KMS)** — evaluated as a headless answer for KMS+EGL
  and rejected: DRM planes with no accelerated, DMA-BUF-exportable framebuffer

> **PipeWire ScreenCast and wlr-screencopy are no longer rejected.** Both are now
> capture add-ons — [`pw_portal`](../addons/linux/capture/PW_PORTAL_LINUX_SPEC.md)
> and [`wl_screencopy`](../addons/linux/capture/WL_SCREENCOPY_LINUX_SPEC.md).
> The original rejection ("no advantage over KMS+EGL") held only under the
> assumption that KMS+EGL is always available, and it is not: it needs root **and
> an active KMS CRTC**, so it cannot serve a host that grants no
> `CAP_SYS_ADMIN`, nor headless Wayland with no forceable connector. Neither
> add-on is selected ahead of KMS+EGL, and neither reopens containerized
> deployment (see `PROJECT_ARTIFACTS/GAP_TRIAGE.md`, closed finding C1).
- **Windows WGC / GDI / Magnification** — rejected (slower than DXGI DD
  with no benefit)
- **Pipeline-level frame buffering** — capture add-ons are pull-latest:
  each `next_frame()` returns the most recent frame, not a queued one.

---

## Per-Add-On Implementation Pointers

| Add-on | Add-on ID | OS | Spec |
|--------|-----------|----|------|
| KMS+EGL DMA-BUF | `kms_egl` | Linux | [`specs/addons/linux/capture/KMS_EGL_LINUX_SPEC.md`](../addons/linux/capture/KMS_EGL_LINUX_SPEC.md) |
| NvFBC | `nvfbc` | Linux | [`specs/addons/linux/capture/NVFBC_LINUX_SPEC.md`](../addons/linux/capture/NVFBC_LINUX_SPEC.md) |
| wlr-screencopy (no root) | `wl_screencopy` | Linux | [`specs/addons/linux/capture/WL_SCREENCOPY_LINUX_SPEC.md`](../addons/linux/capture/WL_SCREENCOPY_LINUX_SPEC.md) |
| Portal ScreenCast (no root) | `pw_portal` | Linux | [`specs/addons/linux/capture/PW_PORTAL_LINUX_SPEC.md`](../addons/linux/capture/PW_PORTAL_LINUX_SPEC.md) |
| ScreenCaptureKit | `sck` | macOS | [`specs/addons/macos/capture/SCK_MACOS_SPEC.md`](../addons/macos/capture/SCK_MACOS_SPEC.md) |
| DXGI Desktop Duplication (+ IddCx headless) | `dxgi_dd` | Windows | [`specs/addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md`](../addons/windows/capture/DXGI_DD_WINDOWS_SPEC.md) |

---

## Buffer Ownership Contract

- `Capturer::next_frame()` returns an **owned** `Frame` whose `data` is an
  `RVec<u8>` — ownership transfers to the caller (the Go "borrowed slice valid
  only until the next call" hazard is gone). Callers (the SW path's Converter)
  read it directly or copy into pinned encoder input buffers as needed.
- `SurfaceCapturer::next_surface()` returns an `FbInfo` whose ownership passes
  **by value** to the HW encoder: `encode_surface(surface: FbInfo)` consumes it
  and `FbInfo`'s `Drop` releases the resource **exactly once on every path**
  (success, error, and `StreamError::FallbackToSoftware`). The capturer and
  pipeline never release it. The single exception is a surface that is never
  handed to an encoder, which the caller drops itself. (This is the single-owner
  rule — RAII — that resolves the prior capture/encode/pipeline ambiguity, the
  M-1/TD-01 win; see
  [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md).)
- The Capturer takes `&mut self`, so the borrow checker statically prevents
  concurrent `next_frame()` calls; the pipeline drives it from a single thread.

### Resolution: capture is always native; the encoder scales

Capture geometry and stream geometry are different things; the stream space is
defined once in [`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)
"Coordinate space". Capturers always emit frames/surfaces at the **display's
native resolution**. They do **not** downscale to the stream resolution — that is
the **Converter's** job on the SW path (`featherdesk-encode`, rotate → convert →
`I4xxScale`; see MODULE_ENCODE) and the **encoder's** job on the HW path
(in-encoder VPP/scaler). Consequently a `ConfigurableCapturer::update_stream_params`
call uses only the HDR / bit-depth / FPS fields; a width/height change does **not**
resize capture output (the Converter or the encoder absorbs it). This keeps the
invariant **encoder-output dims == `config` dims == input-coordinate range**
without the capturer and encoder both trying to scale.

### Readback cost

The zero-copy path exports a handle and costs nothing; the CPU-readback path
copies a whole framebuffer and is the software path's dominant per-frame cost.
Each add-on's own Performance Targets table carries its measured figure — the
`kms_egl` readback in particular is only affordable because it is PBO
double-buffered (see KMS_EGL_LINUX_SPEC "CPU readback"). No capturer signals a
slow readback as `Ok(None)`: a host that cannot sustain the requested rate is
settled by the pipeline's sustainable-rate control (MODULE_PIPELINE), which
lowers the advertised fps to what is actually delivered.

### Display rotation

A rotated display is a per-OS asymmetry, so it is handled once, on the host side:

1. **The add-on reports, never rotates.** `Frame.rotation` / `FbInfo.rotation`
   describe the buffer as delivered. `width`/`height` are the buffer's own
   dimensions; for `R90`/`R270` the **upright** geometry is `(height, width)`.
   Both are as stable as the dimensions are: a change in either is signalled
   through the resolution-change flow, never by silently emitting a differently
   shaped buffer.
2. **The pipeline derives stream dims from the upright geometry** (startup step
   6, and `on_capture_dims_changed` thereafter). A 1080x1920 portrait panel
   therefore streams as 1080x1920, not as a squashed 1920x1080.
3. **SW path:** the `Converter` applies the rotation first, in the packed-RGB
   domain, with libyuv `ARGBRotate` (`kRotate90` / `180` / `270`) — rotating
   subsampled chroma by 90 degrees is not closed over 4:2:2, so rotation precedes
   conversion. See MODULE_ENCODE.
4. **HW path:** the HW encoder add-on reads `FbInfo.rotation` (it owns the
   surface after the move) and applies it in its VPP/scaler alongside the
   downscale it already performs. An encoder whose VPP cannot rotate returns
   `StreamError::FallbackToSoftware` on the first rotated surface, and the
   session degrades to the software path, which always can.
5. **A rotation change mid-session is a resolution change.** It arrives as
   different upright dims and funnels through `on_capture_dims_changed` →
   `apply_params`, which re-advertises `config` and forces an IDR. Nothing else
   is needed.
6. **Input needs no rotation handling at all.** Windows, X11 and Wayland all
   present an *upright* desktop coordinate space regardless of panel rotation —
   only the scanout buffer is rotated. Since the stream space is upright too, the
   injectors' mapping is unchanged.

### Cursor coordinate space

The cursor has the same two-space problem the frame path has, and it is resolved
the same way: the add-on works in capture pixels, the host converts once.

- `CursorCapturer::next_cursor` reports `x`/`y` as the **hotspot** position in
  **capture-device pixels**, origin at the top-left pixel of the captured
  display, and `hotspot_x`/`hotspot_y`/`screen_w`/`screen_h` on the shape in the
  same units. An add-on whose OS reports the bitmap's top-left corner instead of
  the hotspot (Windows `DXGI_OUTDUPL_POINTER_POSITION.Position`) MUST add the
  shape's hotspot back before returning, so every add-on reports the same point.
- The pipeline's cursor publisher converts to the **stream coordinate space**
  before anything goes on the wire, with `sx = params.width / capture_w` and
  `sy = params.height / capture_h`:

      X        = clamp(round(x        * sx), 0, params.width  - 1)
      Y        = clamp(round(y        * sy), 0, params.height - 1)
      DrawW    = max(1, round(screen_w  * sx))
      DrawH    = max(1, round(screen_h  * sy))
      HotspotX = clamp(round(hotspot_x * sx), 0, DrawW - 1)
      HotspotY = clamp(round(hotspot_y * sy), 0, DrawH - 1)

  `StreamW`/`StreamH` on the wire are `params.width`/`params.height` at the
  moment of the send, so an update that crosses a resolution change is still
  interpretable (MODULE_PROTOCOL "Wire Format — 14-byte CursorUpdate").
- **Cursor scaling invariant** — the parallel of the input-coordinate invariant
  in [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md) "Scaling
  invariant":

  > **cursor coordinate range == encoder-output dims == `config` dims == input-coordinate range.**

  One consequence worth stating: a client that maps a pointer event to
  `(X, Y)` and a cursor overlay drawn at `(X, Y)` land on the same pixel. If
  they do not, one of the two mappings is wrong — they share one space by
  construction, not by convention.

### Cursor pixel format

`CursorShape.pixels` is **always** straight (non-premultiplied) `R, G, B, A` in
memory order, top-down, `stride == width * 4`, exactly `width * height * 4`
bytes. There is no format discriminator on the wire and none is needed — the add-on
converts, once per shape change, on one OS-native cursor bitmap. This is the
format `ImageData`/`createImageBitmap` consumes, so the client never converts
either.

Each add-on converts from its OS source:

| Add-on | OS source | Conversion |
|--------|-----------|------------|
| `kms_egl` | DRM cursor plane, `DRM_FORMAT_ARGB8888` (B,G,R,A in memory), **premultiplied** | swap B and R; un-premultiply: for `a > 0`, `c' = min(255, (c * 255 + a/2) / a)`; for `a == 0` emit `0,0,0,0` |
| `nvfbc` | XFixes `XFixesGetCursorImage`, `unsigned long` per pixel, ARGB **premultiplied** in the low 32 bits | take the low 32 bits (this is the classic 64-bit XFixes trap), split A,R,G,B, un-premultiply as above, emit R,G,B,A |
| `dxgi_dd` | `DXGI_OUTDUPL_POINTER_SHAPE_TYPE_COLOR`: B,G,R,A, straight alpha | swap B and R |
| `dxgi_dd` | `..._MASKED_COLOR`: B,G,R,X where `X == 0x00` means "copy" and `X == 0xFF` means "XOR with the screen" | copy pixels → `R,G,B,255`; XOR pixels → `(r ^ 0x80, g ^ 0x80, b ^ 0x80, 255)` — composited against mid-grey so an inverting pointer stays visible on light and dark backgrounds alike |
| `dxgi_dd` | `..._MONOCHROME`: a 1bpp AND mask of `Height/2` rows followed by a 1bpp XOR mask of `Height/2` rows; the real shape height is `Height/2` | `(AND,XOR) = (0,0)` → `0,0,0,255`; `(0,1)` → `255,255,255,255`; `(1,0)` → `0,0,0,0`; `(1,1)` → `127,127,127,255` |
| `sck` | — | not applicable; `sck` does not implement `CursorCapturer` (see "Per-OS availability") |

An add-on that cannot produce this format for a given shape returns
`Ok(None)` for that poll and keeps the previous shape active. It never returns a
buffer in another format and never returns a partially converted one.

### Shape identity

`ShapeID` is **FNV-1a 32-bit** over the shape's canonical bytes, computed in
`CursorPublisher::tick` — never by the add-on — over the POST-clamp bytes, so the
`pixel_w`/`pixel_h` hashed are the record's `PixelW`/`PixelH`:

    offset basis 0x811C9DC5, prime 0x01000193, over
    [pixel_w u16 LE][pixel_h u16 LE][screen_w u16 LE][screen_h u16 LE]
    [hotspot_x u16 LE][hotspot_y u16 LE][pixels …]

If the result is `0`, use `1` — `ShapeID = 0` is reserved for "no shape published
yet".

Consequences the rest of the contract relies on:

- **Dedupe is per session, and the key is the shape AS WRITTEN.** The server keeps
  a per-session `ShapeLru` of `HeldShape { shape_id, draw_w, draw_h, hotspot_x,
  hotspot_y }` entries — the records it has already written on that session's
  cursor stream (MODULE_SERVER). `send_cursor_shape` skips a session only when its
  held set already contains an entry equal in all five fields; a record with a
  known ShapeID but different draw geometry IS written and supersedes the earlier
  one on the client. Every other session gets nothing on the wire.
- **The LRU holds 64 entries.** Beyond that, the least recently written entry is
  evicted and its shape may be re-sent later. 64 covers every desktop's shape
  vocabulary with room to spare.
- **Bitmap budget.** A session that has been written 4 MiB of cumulative Shape
  records stops receiving new ones for the rest of the session: it keeps the last
  shape it holds, `featherdesk_cursor_shape_budget_exhausted_total` increments,
  and a `warn!` fires once. This bounds a pathological shape-churning host to a
  known cost per client.
- **Re-emission on a dimension change.** `DrawW`/`DrawH`/`HotspotX`/`HotspotY`
  are stream-space quantities, so a resolution change invalidates them.
  `CursorPublisher::on_stream_dims_changed` recomputes them for the active shape
  and re-emits it under the SAME ShapeID through `send_cursor_shape`; because the
  server's dedupe key includes the draw geometry, that re-emission is written to
  every session still holding the stale geometry and suppressed on any session
  that already holds the new one. The publisher needs no reach into per-session
  state and there is no cache-invalidation call.

### Cursor and frame acquisition order

The pipeline calls `next_cursor()` at frame-loop step (0b) on **every** tick,
including ticks where the frame is skipped, and `next_frame()`/`next_surface()`
at step (3) only on ticks that are not skipped. That asymmetry is the whole point
of `"separate"` — the pointer keeps moving while video is skipped — and it forces
one rule on add-ons:

> An add-on MUST NOT require `next_frame()` / `next_surface()` to have been called
> in order for `next_cursor()` to report a change. Where one OS call produces both
> the pointer and the frame, that call is issued from `next_cursor()`, and
> `next_frame()` / `next_surface()` returns what it produced (or `Ok(None)` if it
> produced no new frame).

- `kms_egl`, `nvfbc`: the pointer has its own OS query (DRM cursor plane, XFixes).
  The two calls are independent and the rule is satisfied trivially.
- `dxgi_dd`: the pointer is metadata of `AcquireNextFrame`, so a session that
  resolved `"separate"` takes **two** zero-timeout acquires per tick, not one —
  the second one is what keeps the encoded pixels from being a whole pacing
  interval stale.
  - `next_cursor()`, at step (0b), **before** pacing: `AcquireNextFrame(0, …)` →
    latch position/visibility from `LastMouseUpdateTime` / `PointerPosition`
    (adding the shape's hotspot back) and the shape from `GetFramePointerShape`
    when `PointerShapeBufferSize > 0` → if `LastPresentTime` advanced,
    `CopyResource` the desktop texture into the add-on's own `ID3D11Texture2D` and
    latch it → `ReleaseFrame()`. The copy STAYS here: DXGI never re-delivers an
    already-acquired frame, so releasing without copying loses that desktop image
    outright.
  - `next_frame()`/`next_surface()`, at step (3), **after** `sleep_to_interval`:
    one more `AcquireNextFrame(0, …)`. `DXGI_ERROR_WAIT_TIMEOUT` → serve the
    already-latched texture unchanged. Success → update the pointer latch from
    THIS `frame_info` too (its pointer metadata is delivered exactly once and
    would otherwise be lost, surfacing on the next tick's `next_cursor`),
    `CopyResource` the newer desktop image over the latch, `ReleaseFrame()`, then
    serve it.
  Neither call ever blocks; the second acquire is a cheap `WAIT_TIMEOUT` on a
  static screen. No GPU→CPU readback is introduced by either: the desktop copies
  are GPU→GPU, and the shape buffer comes from DXGI's own metadata.
- When the session resolved `"embedded"`, `next_cursor` is not polled (so
  `CaptureConfig.embed_cursor` is `true`) and the single acquire lives in
  `next_frame()` as before. `embed_cursor` is on `CaptureConfig`, so the add-on
  knows which regime it is in at construction.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `next_frame()` returns `Ok(None)` on an idle screen within the per-frame deadline, never a stale frame reused as new | No |
| Unit | `next_frame()` whose readback exceeds one frame interval returns `Ok(Some(frame))`, never `Ok(None)` — `Ok(None)` is reserved for "no new content" | No |
| Unit | Stride handling: a `Frame` with `stride > width*4` (padded GPU readback row) converts correctly via libyuv — a test that assumes `stride == width*4` must fail on a synthetic padded fixture | No |
| Unit | A capturer reporting `rotation = R90` with a 1920x1080 buffer causes the pipeline to advertise 1080x1920 and the Converter to emit an upright 1080x1920 picture | No |
| Unit | `FbInfo::Drop` releases the underlying resource (DMA-BUF fd close / IOSurface release / D3D11Texture COM release) exactly once, verified across all three outcomes of `encode_surface`: success, error, and `StreamError::FallbackToSoftware` | No |
| Unit | A surface that is never handed to an encoder (probe/teardown path) is still released via `FbInfo::Drop` — no fd/handle leak | No |
| Unit | `probe()`'s `caps` equals the set of optional methods the object serves: a bit claimed and then refused with `StreamError::Unsupported` is cleared with `clear_cap(bit)` and the host takes the non-optional path — it never reaches the capture error ladder | No |
| Integration | Capturer selection order: NvFBC preferred over KMS+EGL on Linux when both probe available; DXGI DD triggers IddCx virtual-display auto-install and retries probe on Windows headless | Yes (per platform) |
| Integration | `ConfigurableCapturer::update_stream_params` with only HDR/bit-depth/FPS changes never resizes capture output — width/height changes are absorbed by the Converter or the encoder, not the capturer | Yes (per add-on) |
| Integration | End-to-end `next_frame`/`next_surface` capture loop for 5s per platform add-on (KMS+EGL, NvFBC, SCK, DXGI DD), confirming native-resolution output and correct `PixelFormat`/`SurfaceHandle` variant for that OS | Yes (per OS) |
| Integration | Monitor hotplug/mode change on the selected display is handled via the resolution-change flow without a capturer crash or leaked surface | Yes |
| Integration | Portrait secondary monitor (`output_index = 1`, 1080x1920 panel) streams upright on all three OSes and the client's absolute pointer mapping is not transposed | Yes (per OS) |
| Unit | `CursorCapturer::next_cursor()` returns `Ok(None)` when nothing moved; a pure position move returns `shape: None` (a 22-byte datagram on the wire); only a shape swap returns `Some(CursorShape)` | No |
| Unit | The FIRST `next_cursor()` after construction returns `Ok(Some(..))` carrying position, visibility **and** `shape: Some(..)`, on every add-on that declares `CURSOR` — the guarantee the join-resync seed (MODULE_SERVER lifecycle step 15) depends on | No |
| Unit | An oversized OS bitmap (e.g. a 256x256 Windows accessibility pointer) crosses `next_cursor` at its native size and is clamped only once, in `CursorPublisher::tick`; no add-on resamples | No |
| Unit | Serialized `CursorUpdate` round-trips byte-exactly against the 14-byte `[x][y][streamW][streamH][shapeId][visible][reserved]` layout, and a `CursorShapeRecord` against the 22+N-byte Kind 0x01 layout — shared golden fixtures, so a host-side field reorder cannot silently break `cursor.js` | No |
| Unit | Every pixel-format conversion in "Cursor pixel format" against a golden fixture per source: premultiplied ARGB (kms_egl), 64-bit-element XFixes ARGB (nvfbc), DXGI `COLOR` / `MASKED_COLOR` / `MONOCHROME` — a converter that reads XFixes pixels as `u32` must fail | No |
| Unit | `blend_cursor` on a padded `stride > width*4` buffer, with the shape clipped at each of the four frame edges and at a negative origin — no wrap, no out-of-bounds, alpha 0 and 255 fast paths equal the general formula | No |
| Unit | ShapeID is FNV-1a over the canonical bytes, is stable across two independent captures of the same shape, and never returns 0 | No |
| Integration | `next_cursor` reports a pointer move **without** `next_frame` having been called since the previous poll — the latching rule, run against each platform add-on, and the direct regression guard for a Windows pointer that only moves when the desktop does | Yes (per OS) |
| Integration | An add-on that reports `caps & CURSOR` actually produces a moving cursor across a 5 s drag, and one that does not report it is never polled (calling `next_cursor` on it would be a host bug) | Yes (per OS) |
| Integration | `ProbeReport.caps` is truthful per environment: `kms_egl` reports `CURSOR` when a `DRM_PLANE_TYPE_CURSOR` plane exists (under X11 **and** Wayland) and `EMBED_CURSOR` instead when none does; `nvfbc` clears `CURSOR` without XFixes; `sck` never reports `CURSOR`; `dxgi_dd` clears `SURFACE` on a WARP adapter | Yes (per platform) |
| Integration | `embed_cursor = true` produces frames containing the pointer and `embed_cursor = false` produces frames without it, on the same add-on and the same display — the double-cursor regression guard | Yes (per OS) |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| `next_surface()` (zero-copy, p50) | <1 ms — the handle export only; the encoder pays the real cost |
| `next_frame()` (CPU readback, 1080p, p50) | <8 ms — see each add-on's own table; `kms_egl` requires the PBO ring |
| `next_frame()` (CPU readback, 1440p, p50) | <13 ms |
| `next_cursor()` (non-blocking poll) | <0.2 ms — it runs before the skip check on every tick |
| Steady-state allocation per frame | zero after warmup (owned `RVec` reuse) |
| Contribution to motion-to-photon | 7 ms (capture acquire) — see CENTRAL_SPEC "Motion-to-photon budget" |

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| KMS+EGL DMA-BUF | 📋 Specced — a Go prototype exists on `feature-libav-vp8s8` but is not the capture path that branch runs; it was abandoned on tiled 10-bit scanout framebuffers (see `KMS_EGL_LINUX_SPEC.md` "Status"). The refactor lands it as `addons/capture/kms_egl/`. |
| NvFBC | 📋 Specced; Rust FFI bindings pending |
| ScreenCaptureKit | 📋 Specced. Benchmarked only on a Hackintosh, with raw data outside this repo (`MACOS_SPEC.md` cites `/tmp/fd_bench/*.csv`); there is no macOS session in `PROJECT_ARTIFACTS/bench_out`. macOS is not a built/shipped platform. |
| DXGI Desktop Duplication | 📋 Specced — acquisition cost not yet measured (the bench tool's DXGI backend is a stub pending COM bindings; every recorded session reports `available: false`). The virtual-display `DXGI_ERROR_UNSUPPORTED` case is handled by the IddCx virtual-display fallback (capture stays add-on-based). |

The old Go `internal/capture/x11grab.go` (subprocess-based X11 capture) and
`internal/capture/screencast.py` (Mutter/PipeWire ScreenCast helper) from the
working `feature-libav-vp8s8` branch are **rejected** — they are not ported to
the Rust rewrite.
