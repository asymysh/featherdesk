# Module Spec: Add-on ABI (`featherdesk-abi`)

## Overview

`featherdesk-abi` is the **stable contract crate** compiled into BOTH the host
and every add-on `cdylib`. It is the **single source of truth** for everything
the two sides must agree on at the `dlopen` boundary: the capability descriptor,
the `AddonKind` / `CodecId` / `VideoProfile` / `AbiErr` registries, the
root-module surface, and the ABI version. Neither side hardcodes a literal — both
`use featherdesk_abi::*`, so a number means the same thing on both sides, or the
load is rejected.

Because the host and add-ons are **all Rust with no runtime/GC**, a
runtime-loaded add-on is just a native library — there is **no two-runtime
problem** (the reason Go was rejected). The crate is built on
[`abi_stable`](https://crates.io/crates/abi_stable), and was validated by a
working spike (a host scanned a dir, loaded a separately-compiled `cdylib`, and
frame buffers + an error code crossed the boundary with deterministic cleanup).

> Referenced from [`../CENTRAL_SPEC.md`](../CENTRAL_SPEC.md) "Pluggable
> Architecture"; the host-side adapters live in
> [`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md); the per-OS add-on impl specs
> under [`../addons/`](../addons/) implement this contract. Where this file and
> another disagree, **this file wins for the ABI surface.**

---

## Two layers — do not conflate them

- **Layer 1 — raw `cdylib` export (crosses `dlopen`).** Each add-on exports
  exactly ONE symbol: the `abi_stable` root module `FeatherDeskAddon`. It carries
  the ABI version, the capability descriptor, a `probe()`, and a `construct()`
  returning the kind's `#[sabi_trait]` object. **Every type here is an
  `abi_stable` type** (`RResult`, `RVec`, `RString`, `RStr`, sabi-trait objects) —
  **never** `std::result::Result`, `Box<dyn _>`, `String`, or a host error enum,
  none of which are layout-stable across `dlopen`.
- **Layer 2 — host-side registry adapter.** The loader wraps each loaded root
  module in a `CaptureAddon` / `EncoderAddon` / `InputAddon` / `AudioAddon`
  adapter (see [`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md)), translating
  Layer-1 abi types into the host's ergonomic `Result<_, HostError>` +
  `Box<dyn HostTrait>`. The per-OS add-on impl specs describe the concrete
  backend; the host only ever talks to it through these adapters. (Where an impl
  spec shows a `probe()`/`new()` snippet, that is the Layer-2 adapter shape:
  `probe(&self) -> Result<ProbeResult, PipelineError>` and a typed `new(&self, …)`.)

---

## Root module surface (Layer 1)

Every add-on exports this and nothing else. The four methods are called in a
fixed order: `init` once at load, then `descriptor`, then `probe` (once per
selection pass), then `construct` for the add-on the pipeline selects.

```rust
// crate: featherdesk-abi   (compiled into the host AND every add-on)
pub const ABI_VERSION: u32 = 1;     // bumped on ANY breaking change to a type or
                                    // trait in this crate (see "ABI versioning");
                                    // NOT bumped by a new AddonCaps bit or AbiErr code

#[repr(C)] #[derive(StableAbi)]
pub struct CapabilityDescriptor {
    pub kind: AddonKind,        // what this add-on is (registry below)
    pub id: RString,            // add-on id, e.g. "kms_egl" (== filename + config-section suffix)
    pub codecs: RVec<CodecId>,  // codecs it can emit/consume (encoders + audio); empty otherwise
    pub os: Os, pub arch: Arch, // must equal HOST_OS / HOST_ARCH, else the load is rejected
    pub abi_version: u32,       // == ABI_VERSION it was built against
}

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum Os { Linux = 1, MacOs = 2, Windows = 3 }

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum Arch { X86_64 = 1, Aarch64 = 2 }
// Two arch variants because those are the only targets the tree builds for.
// Both spaces start at 1, so a zeroed descriptor is never a valid os or arch.

/// The build target's OS. The host compares `descriptor().os` against this and
/// rejects the library on a mismatch; an add-on fills `CapabilityDescriptor.os`
/// from it. ONE derivation from `cfg!(target_os)`, compiled into both sides.
pub const HOST_OS: Os = /* cfg!(target_os): "linux" => Os::Linux,
                           "macos" => Os::MacOs, "windows" => Os::Windows */;

/// The build target's architecture; see `HOST_OS`.
pub const HOST_ARCH: Arch = /* cfg!(target_arch): "x86_64" => Arch::X86_64,
                               "aarch64" => Arch::Aarch64 */;

#[sabi_trait]
pub trait FeatherDeskAddon: Send + Sync {
    /// Called ONCE, immediately after the load-time version / layout-hash /
    /// os-arch check and BEFORE `descriptor()`. The add-on MUST call
    /// `featherdesk_abi::install_log_sink(host.log)` here — nothing it logs
    /// afterwards reaches the operator otherwise. It MAY refuse to run against
    /// this host by returning an error (the library is then skipped + warned).
    fn init(&self, host: HostServices) -> RResult<(), AbiError>;
    fn descriptor(&self) -> RResult<CapabilityDescriptor, AbiError>;
    fn probe(&self) -> RResult<ProbeReport, AbiError>;
    fn construct(&self, cfg: RAddonConfig) -> RResult<AddonObject, AbiError>;
}

/// What the host hands every add-on at `init`. Copy-able and thread-safe: the
/// add-on may keep it for the process lifetime.
#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct HostServices {
    pub abi_version: u32,   // the HOST's ABI_VERSION (the add-on's own is in its descriptor)
    pub log: LogSink,
}

/// The add-on's only route to the operator. Every `cdylib` links its own copy of
/// `tracing_core`'s dispatcher, so an add-on's `warn!` / `error!` resolve against
/// a `NoSubscriber` and are DISCARDED unless it forwards them through this.
#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct LogSink {
    /// Emits one already-formatted event on the HOST's global `tracing`
    /// subscriber. `target` is the add-on's module path, `message` the formatted
    /// line; both UTF-8, NOT NUL-terminated. Callable from ANY thread. The host
    /// wraps its body in `catch_unwind` and never unwinds into the add-on.
    /// Messages longer than 8192 bytes are truncated host-side on a UTF-8
    /// boundary and suffixed with `"…(truncated)"`.
    pub emit: for<'a> extern "C" fn(level: LogLevel, target: RStr<'a>, message: RStr<'a>),
    /// The host's maximum enabled level, from `[log] level`. The add-on skips
    /// formatting anything more verbose. Fixed for the process lifetime in v1.
    pub max_level: LogLevel,
}

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub enum LogLevel { Error = 1, Warn = 2, Info = 3, Debug = 4, Trace = 5 }

/// Installs `sink` as the process-local `tracing` subscriber for THIS cdylib.
/// Idempotent — a second call is a no-op. Called from `init()`, never later.
pub fn install_log_sink(sink: LogSink);

/// The error every Layer-1 call returns. `code` is the contract; `detail` is a
/// diagnostic the host LOGS and never puts on the wire. `detail` may be empty and
/// MUST NOT be parsed for control flow. The host truncates it at 4096 bytes on a
/// UTF-8 boundary.
#[repr(C)] #[derive(StableAbi, Clone)]
pub struct AbiError {
    pub code: u32,        // an AbiErr code (registry below)
    pub detail: RString,
}

impl AbiError {
    pub fn new(code: u32, detail: impl Into<RString>) -> Self { /* … */ }
    pub fn code(code: u32) -> Self { /* detail = RString::new() */ }
}

// Layer-1 probe result — ALL abi_stable types. (The host's richer Layer-2
// `ProbeResult` in MODULE_PIPELINE carries String / HashMap / serde_json::Value
// and is NOT boundary-safe; the adapter BUILDS it from this report. It carries
// `caps` and `displays` TYPED, copied through verbatim; only `reason` and
// `details` are host-authored.)
#[repr(C)] #[derive(StableAbi)]
pub struct ProbeReport {
    pub available: bool,        // false = prerequisite missing — this is ROk(available:false), NOT an error
    pub reason: RString,        // human-readable detail when !available; empty when available
    pub codecs: RVec<CodecId>,  // what this backend can actually emit/consume; empty for capture + input
    pub caps: AddonCaps,        // which OPTIONAL methods this add-on serves (below)
    pub displays: RVec<DisplayInfo>, // Capture kind only: the outputs `probe()` enumerated.
                                     // Empty for every other kind, and for a capture add-on
                                     // that cannot enumerate before `construct()`.
}

/// One display a capture add-on can serve. This is the ONLY display description
/// any host type receives: a private per-add-on capabilities struct cannot cross
/// the boundary, so `probe()` fills these rows instead.
#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct DisplayInfo {
    pub id: u64,           // add-on-scoped opaque id: DRM connector id (kms_egl),
                           //   CGDirectDisplayID (sck), DXGI output index (dxgi_dd),
                           //   NvFBC output id (nvfbc). Same value the add-on's
                           //   [addon_module_<id>] display/output key selects.
    pub width: u32,        // native size in PIXELS (never points — see MODULE_CAPTURE), as the
    pub height: u32,       //   BUFFER is delivered — NOT upright. For `R90`/`R270` the upright
                           //   geometry is `(height, width)`, exactly as on `Frame` / `FbInfo`.
    pub rotation: Rotation, // the output's rotation at probe time. Required, not cosmetic:
                           //   `FrameLoop::open()` step 6 derives the stream dims from the UPRIGHT
                           //   geometry and seeds `FrameLoop.capture_dims` with it, and it cannot
                           //   transpose without this. A change after probe arrives the normal way,
                           //   as a differently shaped `Frame` / `FbInfo` caught by the frame path's
                           //   dimension comparison (MODULE_CAPTURE "Rotation" 5).
    pub refresh_mhz: u32,  // milliHertz: 60000 = 60.000 Hz, 59940 = 59.94 Hz; 0 = unknown
    pub scale_num: u32,    // backing scale as a rational (Retina 2x → 2/1; 150% → 3/2);
    pub scale_den: u32,    //   0/0 = unknown, host assumes 1/1
    pub primary: bool,
}
```

### Optional-method capability flags

A `#[sabi_trait]` object is one flat vtable — the host **cannot** downcast a
`CapturerBox` to ask "do you also implement `SurfaceCapturer`?". So each sabi
object below carries the **union** of its kind's optional methods, and the add-on
declares which of them are real via a bitflag. A method the flag does not claim
MUST return `RErr(AbiError::code(AbiErr::Unsupported))`; the host never calls it.
The host-side adapter turns each claimed bit into a **borrow** of the one owning
handle (`as_surface()` / `as_cursor()` / …, see
[`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) "Add-on handles") — never into a
second `Box`, because there is only ever one object.

`probe()` is **config-blind**: it runs before `construct(cfg)` and cannot know
what the configured adapter will actually do (`dxgi_dd` loses `SURFACE` when it
falls back to WARP). So the constructed object's `caps()` is **authoritative**
and may be a strict subset of the probe's. It is never a superset — the adapter
masks any bit the probe did not claim and logs once at `warn`.

```rust
#[repr(C)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub struct AddonCaps(pub u32);   // Width is u32 — fixed, forever. The BIT SPACE is
                                 // append-only (a retired bit is never reused, same
                                 // discipline as the wire `frame_type::` and `close::`
                                 // spaces). Adding a bit does NOT bump ABI_VERSION:
                                 // a bit is data, not layout, so two builds of the
                                 // same ABI_VERSION may know different bit sets.

impl AddonCaps {
    // ── Capture kind (0x01) — bits 0..7 ──────────────────────────────────────
    pub const SURFACE: u32          = 1 << 0;  // serves next_surface      (zero-copy path eligible)
    pub const CURSOR: u32           = 1 << 1;  // serves next_cursor       (cursorMode "separate" eligible)
    pub const CONFIGURABLE: u32     = 1 << 2;  // serves update_stream_params + params_capability
                                               //   (hot param change; else the pipeline rebuilds)
    pub const EMBED_CURSOR: u32     = 1 << 4;  // honors CaptureConfig.embed_cursor = true on every
                                               //   Frame returned by next_frame
                                               //   (cursorMode "embedded" eligible)
    pub const EMBED_CURSOR_SURF: u32 = 1 << 5; // …and on every FbInfo returned by next_surface.
                                               //   Implies EMBED_CURSOR; set without it, both are ignored.
    // ── Encoder kinds (0x02 / 0x03) — bits 8..15 ─────────────────────────────
    pub const ENC_CONFIGURABLE: u32 = 1 << 8;  // serves update_stream_params + params_capability
    // ── Input kinds (0x06 / 0x07 / 0x08) — bits 16..23 ───────────────────────
    pub const SECURE_ATTENTION: u32 = 1 << 16; // serves send_sas (Ctrl+Alt+Del; `interception` only)
    pub const RUMBLE: u32           = 1 << 17; // serves set_rumble_sink AND actually fires it
                                               //   (a stub that stores the sink and never emits
                                               //    MUST NOT set this bit — see `gcvirtual`)
    // ── Audio kinds (0x04 / 0x05) — bits 24..31: none defined in v1 ──────────

    /// Every bit this build defines. Anything outside is "unknown".
    pub const KNOWN: u32 = Self::SURFACE | Self::CURSOR | Self::CONFIGURABLE
                         | Self::EMBED_CURSOR | Self::EMBED_CURSOR_SURF
                         | Self::ENC_CONFIGURABLE | Self::SECURE_ATTENTION | Self::RUMBLE;

    pub fn has(self, bit: u32) -> bool { self.0 & bit != 0 }
}
```

Each `AddonKind` has exactly one sabi object, and each object declares the union
of its kind's optional methods:

```rust
#[sabi_trait]
pub trait Capturer: Send {                      // AddonKind::Capture (0x01)
    fn caps(&self) -> AddonCaps;
    fn next_frame(&mut self) -> RResult<ROption<RFrame>, AbiError>;
    // ── optional, served iff caps has SURFACE ──
    fn next_surface(&mut self) -> RResult<ROption<RFbInfo>, AbiError>;
    // ── optional, served iff caps has CURSOR ──
    fn next_cursor(&mut self) -> RResult<ROption<CursorState>, AbiError>;
    // ── optional, served iff caps has CONFIGURABLE ──
    fn update_stream_params(&mut self, p: RParams) -> RResult<(), AbiError>;
    fn params_capability(&self) -> RResult<RParamsCapability, AbiError>;
}

#[sabi_trait]
pub trait Encoder: Send {                       // AddonKind::SoftwareEncode (0x02)
    fn caps(&self) -> AddonCaps;
    fn codec(&self) -> CodecId;
    /// The profile the add-on ACTUALLY configured on the underlying encoder,
    /// re-read after `construct()` and after every successful
    /// `update_stream_params`. The Layer-2 adapter builds the wire codec string
    /// with `codec_string(self.profile(), width, height, fps)` at the active
    /// geometry. `codec()` alone cannot: one `CodecId::H264` maps to five
    /// distinct `avc1.` prefixes (0x42E0 … 0xF400), and the level the add-on
    /// computed never crossed.
    fn profile(&self) -> VideoProfile;
    fn encode(&mut self, frame: RYuvFrame<'_>) -> RResult<ROption<REncodedUnit>, AbiError>;
    /// Latches a request for the next access unit to be an IDR. Infallible and
    /// MUST return promptly. An add-on whose only IDR mechanism is restarting a
    /// child process (`x264`) latches the request here and satisfies it inside
    /// the next `encode()` — it MUST NOT restart anything in this call.
    /// NOT callable concurrently with `encode` (`&mut self`).
    fn force_keyframe(&mut self);
    // ── optional, served iff caps has ENC_CONFIGURABLE ──
    fn update_stream_params(&mut self, p: RParams) -> RResult<(), AbiError>;
    fn params_capability(&self) -> RResult<RParamsCapability, AbiError>;
}

#[sabi_trait]
pub trait HwEncoder: Send {                     // AddonKind::HardwareEncode (0x03)
    fn caps(&self) -> AddonCaps;
    fn codec(&self) -> CodecId;
    /// As `Encoder::profile`. An HDR session reports `VideoProfile::HevcMain10`.
    fn profile(&self) -> VideoProfile;
    fn encode_surface(&mut self, surface: RFbInfo) -> RResult<REncodedUnit, AbiError>;
    fn force_keyframe(&mut self);
    // ── optional, served iff caps has ENC_CONFIGURABLE ──
    fn update_stream_params(&mut self, p: RParams) -> RResult<(), AbiError>;
    fn params_capability(&self) -> RResult<RParamsCapability, AbiError>;
}

#[sabi_trait]
pub trait AudioCapturer: Send {                 // AddonKind::AudioCapture (0x04)
    fn caps(&self) -> AddonCaps;                // AddonCaps(0) in v1 — no audio bits defined
    fn next_chunk(&mut self) -> RResult<ROption<RPcmChunk>, AbiError>;
    fn format(&self) -> RAudioFormat;
}

#[sabi_trait]
pub trait AudioEncoder: Send {                  // AddonKind::AudioCodec (0x05)
    fn caps(&self) -> AddonCaps;                // AddonCaps(0) in v1
    fn codec(&self) -> CodecId;
    fn encode(&mut self, chunk: &RPcmChunk) -> RResult<RVec<u8>, AbiError>;
    /// Client→host MIC direction: decode one packet of this add-on's codec back
    /// to PCM for the virtual-mic sink (MODULE_AUDIO "Microphone (client→host)").
    ///
    /// MANDATORY on this kind, deliberately NOT behind an `AddonCaps` bit. An
    /// audio codec is symmetric in practice — the one add-on that exists,
    /// `opus`, wraps a library that does both — and an optional bit would buy a
    /// half-capable codec add-on nothing except a way for the mic path to fail
    /// at runtime instead of at load. An encoder-only codec is not a supported
    /// shape; return `AbiErr::Unsupported` only if a future codec genuinely
    /// cannot decode, and expect the host to refuse the mic path, not the
    /// session.
    fn decode(&mut self, packet: &[u8]) -> RResult<RPcmChunk, AbiError>;
}

#[sabi_trait]
pub trait AudioSink: Send {                     // AddonKind::AudioSink (0x0A)
    fn caps(&self) -> AddonCaps;                // AddonCaps(0) in v1
    /// Writes one decoded PCM chunk into the host's virtual input device. The
    /// host guarantees chunks arrive in timestamp order on ONE thread (the audio
    /// loop's mic half); the sink never reorders and never buffers more than the
    /// device's own period.
    fn write_chunk(&mut self, chunk: &RPcmChunk) -> RResult<(), AbiError>;
    /// The format the virtual device presents to host applications. The host
    /// resamples the client's stream to this before calling `write_chunk`.
    fn format(&self) -> RAudioFormat;
}

#[sabi_trait]
pub trait Injector: Send {   // AddonKind::InputKeyMouse / InputTouch / InputGamepad
    fn caps(&self) -> AddonCaps;
    // ── served iff descriptor().kind == InputKeyMouse (0x06) ──
    fn inject_key(&mut self, hid_usage: u16, down: bool) -> RResult<(), AbiError>;
    fn inject_pointer_abs(&mut self, x: i32, y: i32) -> RResult<(), AbiError>;
    fn inject_pointer_rel(&mut self, dx: i32, dy: i32) -> RResult<(), AbiError>;
    fn inject_button(&mut self, button: u8, down: bool) -> RResult<(), AbiError>;
    fn inject_scroll(&mut self, dx: i32, dy: i32, unit: u8) -> RResult<(), AbiError>;
    fn resize(&mut self, width: u32, height: u32) -> RResult<(), AbiError>;
    // ── optional, KeyMouse only, served iff caps has SECURE_ATTENTION ──
    fn send_sas(&mut self) -> RResult<(), AbiError>;
    // ── served iff descriptor().kind == InputTouch (0x07) ──
    fn inject_touch(&mut self, contacts: RSlice<'_, RTouchContact>) -> RResult<(), AbiError>;
    // ── served iff descriptor().kind == InputGamepad (0x08) ──
    fn connect(&mut self, index: u8, id: RStr<'_>) -> RResult<(), AbiError>;
    fn disconnect(&mut self, index: u8) -> RResult<(), AbiError>;
    fn update(&mut self, state: RGamepadState) -> RResult<(), AbiError>;
    // ── optional, Gamepad only, served iff caps has RUMBLE ──
    fn set_rumble_sink(&mut self, sink: RumbleSinkBox) -> RResult<(), AbiError>;
}

/// The one HOST→add-on object in the ABI.
#[sabi_trait]
pub trait RumbleSink: Send + Sync {
    /// Non-blocking and infallible from the add-on's side. `index` is the same
    /// index the host passed to `connect`. Magnitudes are 0..=65535. MUST NOT be
    /// called from inside `set_rumble_sink` / `connect` / `update` on the same
    /// object — the host holds that object's `&mut` there.
    fn emit(&self, index: u8, weak: u16, strong: u16, duration_ms: u32);
}

// The boxed forms named throughout this spec:
pub type CapturerBox      = Capturer_TO<'static, RBox<()>>;
pub type EncoderBox       = Encoder_TO<'static, RBox<()>>;
pub type HwEncoderBox     = HwEncoder_TO<'static, RBox<()>>;
pub type AudioCapturerBox = AudioCapturer_TO<'static, RBox<()>>;
pub type AudioEncoderBox  = AudioEncoder_TO<'static, RBox<()>>;
pub type InjectorBox      = Injector_TO<'static, RBox<()>>;
pub type RumbleSinkBox    = RumbleSink_TO<'static, RBox<()>>;

#[repr(u8)] #[derive(StableAbi)]
pub enum AddonObject {
    Capture(CapturerBox),
    Encoder(EncoderBox),
    HwEncoder(HwEncoderBox),
    AudioCapture(AudioCapturerBox),
    AudioCodec(AudioEncoderBox),
    Injector(InjectorBox),
}
```

`construct()` MUST return the `AddonObject` variant matching `descriptor().kind`.
A mismatch is a contract violation, not a runtime error: the host drops the add-on
with an `error` log naming the add-on id, the declared kind and the returned
variant, and continues (see the load-failure taxonomy).

**Why `ProbeReport` and not `CapabilityDescriptor`:** these are *runtime* facts,
not compile-time ones. The same `kms_egl` binary can serve `CURSOR` on X11
(XFixes present) and not on Wayland; `dxgi_dd` can lose `SURFACE` when it falls
back to a WARP adapter. `descriptor()` answers "what am I", which never changes;
`probe()` answers "what can I do *here, now*", which is exactly this. The
descriptor's static superset is not separately encoded — a capability an add-on
never implements simply never appears in any of its probe reports.

The host reads `caps` twice: from `ProbeReport` during selection, and again from
the constructed object (`caps()`), which is authoritative. The result is cached on
the one owning handle and is the sole input to that handle's capability accessors
(`CaptureHandle::as_surface` / `as_cursor` / `as_configurable`,
`EncoderHandle::as_configurable`, `HwEncoderHandle::as_configurable`,
`KeyMouseInjector::send_sas`, `GamepadInjector::set_rumble_sink`) — and the one
thing `clear_cap(bit)` retires. The probe's `caps` is additionally the sole
input to **capture eligibility**: `caps & (CURSOR | EMBED_CURSOR)` combined with
`[capture] cursor_mode` decides whether an add-on may be selected as the capturer
at all ([`../media/MODULE_CAPTURE.md`](../media/MODULE_CAPTURE.md) "Cursor
delivery"), because an add-on that can deliver no cursor in the requested mode
must be skipped before it is chosen, not diagnosed after. The ABI supplies only
the bits; the truth table that reads them is MODULE_CAPTURE's, and its one
implementation is `pipeline::CursorMode::resolve(policy, caps)` — evaluated
against `ProbeResult.caps` at selection, and re-evaluated against the constructed
handle's `caps()` inside `FrameLoop::open()`.

**Unknown bits** — set bits outside `AddonCaps::KNOWN` — are ignored for
behaviour and are **logged once per add-on** at `info`: `add-on '<id>' reports
AddonCaps bits 0x<mask> that this host (ABI_VERSION <n>) does not define; ignored`.
They are never an error and never a load rejection. The log line is the point:
without it, an add-on offering something this host cannot use is
indistinguishable from one offering nothing.

> **`AddonObject`** is declared with the sabi traits above; it is the abi-stable
> enum wrapping the kind's sabi object (`CapturerBox` / `EncoderBox` / …). The
> Layer-2 adapter converts host config → `RAddonConfig`, calls `construct()`, and
> wraps the returned `AddonObject` in the matching handle type
> ([`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) "Add-on handles").

### Rich types across the boundary

- **Buffers** — `RVec<u8>`. An `RVec<u8>` returned by an add-on **transfers
  ownership to the host**; it carries the add-on's deallocator, so dropping it on
  the host side is deterministic — **no GC, no use-after-free.** A buffer the
  host *lends* to an add-on is `RSlice<'_, u8>`, borrowed for the call only.
- **Strings** — `RString` (owned) / `RStr<'_>` (borrowed).
- **Options** — `ROption<T>`; **Results** — `RResult<T, AbiError>`, where
  `AbiError` is `{ code: u32, detail: RString }`: the code is the contract, the
  detail is the diagnostic the host logs (never the wire). See the `AbiErr`
  registry.
- **Trait objects** — `#[sabi_trait]` objects (`CapturerBox`, `EncoderBox`, …).
- **Enumerations** — a `#[repr(u8)]` `StableAbi` enum, never a `String`. A
  stringly-typed value at the boundary is a bug: it costs an allocation per call
  and moves a fixed, closed set out of the layout hash's reach.
- **Where a crossing type is declared** — `featherdesk-abi` may not name a type
  declared in any crate that imports it. Every type that appears in a Layer-1
  signature, or inside a `#[derive(StableAbi)]` struct, is declared **here**. The
  downstream crate re-exports it when the host form is identical
  (`pub use abi::Rotation;`, `pub use abi::{CursorState, CursorShape};`,
  `pub use abi::ChannelLayout;`), and declares a separate host type with an
  adapter conversion only when the host form genuinely differs (`RFrame` →
  `capture::Frame`, `RParams` → `stream::Params`, `RParamsCapability` →
  `stream::StreamParamsCapability`).

Every type that crosses, and its Layer-1 form:

| Host type (Layer 2) | Where it crosses | Layer-1 form |
|---------------------|------------------|--------------|
| `capture::Frame` | `next_frame` return | `RFrame` — `data: RVec<u8>` (owned, transfers), and `rotation`, carried verbatim as `abi::Rotation` |
| `capture::FbInfo` | `next_surface` return, `encode_surface` argument | `RFbInfo` with `RSurfaceHandle`, and `rotation`, carried verbatim as `abi::Rotation` |
| `capture::SurfaceHandle` (`OwnedFd` / `objc2_io_surface::IOSurface` / `ID3D11Texture2D`) | inside `FbInfo` | `RSurfaceHandle` — raw `i32` fd / `*mut c_void`; the adapter re-wraps into the RAII host types the instant it crosses, so `Drop` behaviour is unchanged above Layer 1 |
| `encode::YuvFrame` (`y`/`u`/`v`: `Vec<u8>`) | `encode` argument | `RYuvFrame<'_>` — three `RSlice<'_, u8>`, **borrowed**: the host owns and reuses the plane buffers, and the add-on MUST NOT retain them past the call |
| `encode::EncodedUnit` | `encode` / `encode_surface` return | `REncodedUnit` — `data: RVec<u8>` (owned, transfers) |
| `stream::Params` (`color_space`, `chroma_subsampling`: `String`) | `construct`, `update_stream_params` | `RParams` with `ColorSpace` and `Chroma` `#[repr(u8)]` enums |
| `stream::StreamParamsCapability` (`HashMap` + `serde_json::Value`) | `params_capability` return | `RParamsCapability` — a fixed struct, below |
| `capture::CursorState` / `capture::CursorShape` | `next_cursor` return | declared in this crate as `CursorState` / `CursorShape`; `featherdesk-capture` re-exports them, so the Layer-2 name is unchanged. `pixels` is an owned `RVec<u8>`, `shape` an `ROption<CursorShape>`, every other field a fixed-width scalar. The wire type `protocol::CursorUpdate` does **not** cross: the host builds it |
| `audio::PcmChunk` | `next_chunk` return, `encode` argument | `RPcmChunk` — `data: RVec<u8>` |
| `audio::Format` | `format` return | `RAudioFormat`; `ChannelLayout` is declared in this crate and re-exported by `featherdesk-audio` |
| `audio::AudioConfig.channels: String` | `construct` | `ChannelMode` `#[repr(u8)]`: `Auto = 0`, `Stereo = 1` |
| `hwencode::HWEncoderConfig.codec_hint: String` | `construct` | `CodecId` |
| `Encoder::codec() -> &str`, `HardwareEncoder::codec() -> &str` | `codec` + `profile` return | `CodecId` **and** `VideoProfile`. The adapter builds the Layer-2 string with `codec_string(profile(), width, height, fps)` at the active geometry; `CodecId` alone cannot produce it, because one `CodecId::H264` covers five `avc1.` prefixes. `CodecId` is what the wire `frame_type` dispatch reads. |
| `input::TouchContact`, `&[TouchContact]` | `inject_touch` argument | `RTouchContact`, `RSlice<'_, RTouchContact>` |
| `GamepadInjector::connect(index, id: &str)` | `connect` argument | `RStr<'_>` |
| `GamepadState` | `update` argument | `RGamepadState` |
| `Box<dyn Fn(u8,u16,u16,u32) + Send + Sync>` (rumble emitter) | `set_rumble_sink` | **cannot cross at all** — replaced by `RumbleSinkBox`, a `#[sabi_trait]` object |
| `HashMap<String, toml::Value>` (`[addon_module_*]`) | `construct` | `RString` (`section_toml`), see "Add-on configuration" |
| `Result<_, PipelineError/StreamError/AudioError/InputError>` | every call | `RResult<_, AbiError>` |
| `Option<T>` | every `Ok(Some/None)` return | `ROption<T>` |
| `serde_json::Value` (`ProbeResult.details`) | — | **never crosses**; host-authored at Layer 2 only |

```rust
#[repr(C)] #[derive(StableAbi)]
pub struct RFrame {
    pub data: RVec<u8>,         // stride * height bytes; ownership transfers to the host
    pub stride: u32,            // bytes per row; MAY exceed width*4 (padded GPU readback)
    pub pixel_fmt: PixelFormat,
    pub width: u32, pub height: u32,
    pub rotation: Rotation,     // how far CLOCKWISE this buffer must be turned to be
                                // upright; width/height describe it AS DELIVERED, so
                                // for R90/R270 the UPRIGHT geometry is (height, width).
                                // Only the add-on can know this (DXGI_OUTDUPL_DESC.Rotation,
                                // the DRM plane property, NvFBC), so it must cross.
    pub timestamp_ns: u64,      // CLOCK_MONOTONIC ns, sampled at capture
}

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum PixelFormat { Bgra = 0, Rgba = 1 }

/// How many degrees CLOCKWISE a delivered buffer or surface must be turned to
/// appear upright. The add-on REPORTS it and never rotates (MODULE_CAPTURE
/// "Display rotation"). Declared here, not in `featherdesk-capture`, because it
/// is a field of `RFrame` and `RFbInfo` and `featherdesk-abi` may not name a
/// type from a crate that imports it.
#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum Rotation { R0 = 0, R90 = 1, R180 = 2, R270 = 3 }

#[repr(C)] #[derive(StableAbi)]
pub struct RFbInfo {
    pub width: u32, pub height: u32,
    pub rotation: Rotation,     // as RFrame.rotation. The HW encoder add-on reads it
                                // and applies it in the same VPP pass as the downscale,
                                // or returns StreamError::FallbackToSoftware
                                // (MODULE_HARDWARE_ENCODE).
    pub timestamp_ns: u64,
    pub handle: RSurfaceHandle,
}

/// OWNERSHIP: an `RSurfaceHandle` returned by `next_surface` is owned by the
/// receiver, which releases it exactly once — `close(fd)` / `CFRelease` /
/// `Release()`. The Layer-2 adapter re-wraps it into the RAII
/// `capture::SurfaceHandle` immediately, so nothing above Layer 1 sees a raw
/// handle. On the zero-copy path the host hands the same value straight back down
/// into `encode_surface`, which then owns it. NOT `Send` (see "Thread
/// requirements"): it is produced and consumed inside one frame-loop iteration.
#[repr(u8)] #[derive(StableAbi)]
pub enum RSurfaceHandle {
    /// Linux DMA-BUF. A real, dup'd fd the receiver closes.
    DmaBuf { fd: i32, stride: u32, fourcc: u32, modifier: u64 },
    /// macOS `IOSurfaceRef`, retained (+1) by the producer. Receiver `CFRelease`s.
    IoSurface { surface: *mut std::ffi::c_void },
    /// Windows `ID3D11Texture2D*`, `AddRef`'d (+1) by the producer. Receiver `Release`s.
    D3D11Texture { texture: *mut std::ffi::c_void },
}

// Moved here VERBATIM from MODULE_CAPTURE — field set, types and doc comments
// unchanged. It lives in `featherdesk-abi` because it is the return type of
// `Capturer::next_cursor`, and `featherdesk-abi` may not name a type declared in
// `featherdesk-capture`, which imports it.
//
// CursorState is one observation of the OS pointer, in CAPTURE-DEVICE pixels.
// The host converts to stream pixels and assigns the wire ShapeID — the add-on
// never does either (see MODULE_CAPTURE "Cursor coordinate space" and
// "Shape identity").
#[repr(C)] #[derive(StableAbi)]
pub struct CursorState {
    pub x: i32,
    pub y: i32,
    pub visible: bool,
    pub shape: ROption<CursorShape>,
}

// Moved here from MODULE_CAPTURE; MODULE_CAPTURE keeps its prose and becomes
// `pub use abi::CursorShape;`. See abi::CursorState for why the type lives in this
// crate.
//
// CursorShape is a cursor bitmap in the one canonical wire format (see MODULE_CAPTURE
// "Cursor pixel format"), at the OS's NATIVE resolution. `screen_*` and `hotspot_*`
// are in CAPTURE pixels and describe where and how large the pointer is drawn on the
// captured display; `width`/`height` describe the buffer. They differ when the OS
// draws a low-resolution bitmap scaled up (hi-dpi) — never because the add-on
// resampled, because no add-on resamples. The 128-pixel cap is a WIRE cap and is
// applied by `CursorPublisher::tick` on the host.
#[repr(C)] #[derive(StableAbi)]
pub struct CursorShape {
    pub width: u16,       // bitmap width  in bitmap pixels, >= 1, OS-native — NOT capped at 128
    pub height: u16,      // bitmap height in bitmap pixels, >= 1, OS-native — NOT capped at 128
    pub screen_w: u16,    // on-screen width  in capture pixels
    pub screen_h: u16,    // on-screen height in capture pixels
    pub hotspot_x: u16,   // hotspot X in capture pixels, 0 <= hotspot_x < screen_w
    pub hotspot_y: u16,   // hotspot Y in capture pixels, 0 <= hotspot_y < screen_h
    pub pixels: RVec<u8>, // width * height * 4, straight RGBA, top-down, owned
}

/// Borrowed planar YUV the HOST owns. `subsampling` is the discriminator that
/// sizes the chroma planes (encode::Subsampling, MODULE_ENCODE): for a frame
/// `width x height` with strides `y_stride`/`uv_stride`,
/// `y.len() == y_stride * height` and `u.len() == v.len() == uv_stride *
/// chroma_height`, where `chroma_height` is `ceil(height/2)` for I420 and
/// `height` for I422/I444. The add-on MUST NOT retain the slices past the
/// `encode` call.
#[repr(C)] #[derive(StableAbi)]
pub struct RYuvFrame<'a> {
    pub y: RSlice<'a, u8>, pub u: RSlice<'a, u8>, pub v: RSlice<'a, u8>,
    pub y_stride: u32, pub uv_stride: u32,
    pub width: u32, pub height: u32,
    pub subsampling: Chroma,
    pub matrix: ColorSpace,     // the matrix the planes were produced with; the
                                // encoder writes it into the VUI (MODULE_ENCODE
                                // "Colour signalling")
    pub timestamp_ns: u64,      // CLOCK_MONOTONIC ns, carried through from capture
}

#[repr(C)] #[derive(StableAbi)]
pub struct REncodedUnit {
    pub data: RVec<u8>,     // ONE contiguous Annex B access unit; ownership transfers
    pub keyframe: bool,
    pub timestamp_ns: u64,  // CLOCK_MONOTONIC ns of the CAPTURE that produced this
                            // access unit — a PIPELINED encoder keeps a FIFO of
                            // submitted timestamps and attaches the head one here
                            // (MODULE_ENCODE)
}

#[repr(C)] #[derive(StableAbi)]
pub struct RPcmChunk { pub data: RVec<u8>, pub timestamp_ns: u64 }

#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct RAudioFormat { pub sample_rate: u32, pub channels: u8, pub layout: ChannelLayout }

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum ChannelMode { Auto = 0, Stereo = 1 }

/// The speaker mapping. Declared here, next to `ChannelMode`, because it is a
/// field of `RAudioFormat` and every field of a `#[derive(StableAbi)]` struct
/// must itself be `StableAbi` — `#[repr(u8)]` alone provides nothing.
/// `featherdesk-audio` re-exports it. THE ORDER IS THE VORBIS I ORDER (MODULE_AUDIO
/// "Surround"); the discriminants are the values the old `featherdesk-audio`
/// declaration already had implicitly.
#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum ChannelLayout {
    Mono      = 0,  // 1ch: M
    Stereo    = 1,  // 2ch: L R
    Layout5_1 = 2,  // 6ch: L C R Ls Rs LFE
    Layout7_1 = 3,  // 8ch: L C R Ls Rs Rls Rrs LFE
}

#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct RTouchContact { pub pointer_id: u16, pub phase: u8, pub x: i32, pub y: i32 }

#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct RGamepadState {
    pub index: u8, pub buttons: u32,
    pub lx: i16, pub ly: i16, pub rx: i16, pub ry: i16,
    pub lt: u16, pub rt: u16,
}

#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct RParams {
    pub width: u32, pub height: u32, pub fps: u32,
    pub bitrate_bps: u32, pub qp: u32,
    pub bit_depth: u32, pub hdr: bool,
    pub color_space: ColorSpace,
    pub chroma: Chroma,
    pub keyframe_interval: u32,
    pub network_rtt_ms: u32,
    pub packet_loss_pct: f64,
}

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum ColorSpace { Bt709 = 0, Bt2020 = 1 }   // ↔ stream::Params.color_space "bt709" / "bt2020"

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum Chroma { C420 = 0, C422 = 1, C444 = 2 } // ↔ stream::Params.chroma_subsampling "420"/"422"/"444"

/// The boundary-safe reply MODULE_STREAM_PARAMS's `StreamParamsCapability` is
/// built from. Returned by `params_capability()` on the Capturer / Encoder /
/// HwEncoder unions; served iff caps has CONFIGURABLE / ENC_CONFIGURABLE.
#[repr(C)] #[derive(StableAbi, Copy, Clone)]
pub struct RParamsCapability {
    /// Bit `ParamField as u32` set = that field can be changed by
    /// `update_stream_params` without a teardown. A field the add-on will not
    /// accept at all is also 0 — the host rebuilds on a change either way.
    pub hot_changeable: u32,
    // 0 in a max_* field means "no add-on-declared upper bound"; 0 in a min_*
    // field means "no lower bound". The host then clamps to the [stream] config
    // bounds only, and OMITS that key from min_values / max_values entirely.
    pub min_width: u32,       pub max_width: u32,
    pub min_height: u32,      pub max_height: u32,
    pub min_fps: u32,         pub max_fps: u32,
    pub min_bitrate_bps: u32, pub max_bitrate_bps: u32,
    pub min_qp: u32,          pub max_qp: u32,
}

#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum ParamField {
    Width = 0, Height = 1, Fps = 2, BitrateBps = 3, Qp = 4, BitDepth = 5,
    Hdr = 6, ColorSpace = 7, ChromaSubsampling = 8, KeyframeInterval = 9,
}
// `network_rtt_ms` and `packet_loss_pct` are host-set hints, never "changed" by a
// client request, so they have no ParamField and no bounds.
```

No manual offset table is given for these: field order above is normative and
`abi_stable`'s structural layout hash makes any divergence a **load-time
rejection**, not a miscompile.

### Add-on configuration

An add-on's tuning knobs live in its `[addon_module_<id>]` TOML section, and the
**add-on owns that schema** — the struct with `#[serde(deny_unknown_fields)]` is
inside the `cdylib`. The host therefore does not decode the section; it
re-serializes the table it captured in config phase A into
`RAddonConfig.section_toml` and hands it over at `construct()`.

```rust
/// What `construct()` receives. Two parts: the add-on's own TOML section, and the
/// kind-appropriate initial configuration.
#[repr(C)] #[derive(StableAbi)]
pub struct RAddonConfig {
    /// The add-on's OWN `[addon_module_<id>]` table, re-serialized by the host with
    /// `toml::to_string` as a TOML **fragment**: the table's key/value lines and any
    /// sub-tables, WITHOUT the `[addon_module_<id>]` header, UTF-8, `\n`-separated.
    /// Empty string when the config file has no such table — the add-on then uses its
    /// built-in defaults. The host does NOT parse it and has no schema for it. Capped
    /// at 64 KiB; a larger table fails startup with `PipelineError::AddonConfig`
    /// before `construct()` is called.
    pub section_toml: RString,
    /// The kind-appropriate initial configuration, in abi-stable types.
    pub kind: RAddonConfigKind,
}

#[repr(u8)] #[derive(StableAbi)]
pub enum RAddonConfigKind {
    Capture   { params: RParams, embed_cursor: bool },
    Encoder   { params: RParams },
    HwEncoder { params: RParams, codec_hint: CodecId },
    Injector  { width: u32, height: u32 },
    Audio     { frame_ms: u32, channels: ChannelMode },
}
```

The add-on deserializes `section_toml` against its own struct and, on an unknown
key or a bad value, returns `RErr(AbiError { code: AbiErr::BadConfig, detail })`
where `detail` names the offending key and, where the add-on's parser reports one,
the line. At startup this **fails startup** with the add-on id, the key and the
file path — the same operator experience a mistyped core key gets, adjudicated by
the party that owns the schema. Mid-session (`degrade_to_software`, an add-on
swap) the same config has already been accepted once, so `BadConfig` there is
treated as `Unrecoverable` for that add-on and the pipeline falls through.

A section whose add-on is not loaded is never forwarded and never decoded, by
anyone — that is what makes one config file serve any set of loaded add-ons.

### Errors cross as `AbiError`

Errors cross as `RResult<T, AbiError>` where `AbiError.code` is a stable `AbiErr`
code and `AbiError.detail` is a human-readable diagnostic; the host **maps the
code back into its own `StreamError` / `AudioError` / `InputError` /
`PipelineError` enum** so the pipeline's normal `match`/`?` works. (In
Go this mapping silently failed because sentinel error *values* differ per copy;
in Rust it is explicit and centrally registered.)

### No channel crosses the boundary

A streaming add-on exposes a pull method (`next_frame()` / `next_surface()` /
`next_chunk()`) that the host's frame or audio thread calls directly; any
decoupling ring belongs to the add-on, on its own side of the boundary.

### Thread requirements

Each sabi object above is declared with the supertraits the host's threading
model requires. `abi_stable` derives a `Trait_TO`'s auto-traits **only** from the
declared supertrait list — an object with no declared supertraits is
`!Send + !Sync` no matter what is inside it — so these are part of the frozen ABI
surface, not a detail an implementor can add later.

| Object | Bound | Why |
|--------|-------|-----|
| `FeatherDeskAddon` (root module) | `Send + Sync` | held in the host's registry, read from the startup thread and re-probed later from a tokio task |
| `CapturerBox`, `EncoderBox`, `HwEncoderBox` | `Send` | constructed by `FrameLoop::open()` ON the frame thread and dropped there (MODULE_PIPELINE step 13). `Send` is required only because the `FrameLoop` struct that will hold them is moved onto that thread before `open()` runs — the objects themselves never cross a boundary. |
| `AudioCapturerBox`, `AudioEncoderBox` | `Send` | constructed by `AudioLoop::open()` on the audio thread, used and dropped there; `Send` for the same structural reason as the row above. |
| `InjectorBox` | `Send` | reached from N per-session tasks through the server's `Send + Sync` input callback; `Sync` comes from the host's `Mutex`, not from the object |
| `RumbleSinkBox` | `Send + Sync` | the add-on calls it from its own OS notification thread |

`construct()` is called on the thread that will use the object, so a backend may
hold a bound EGL / D3D / COM context for the object's whole life.

**What this obliges an add-on author to do.** Declaring the object `Send` makes
the *concrete* type's `Send`-ness the author's responsibility. A concrete type
holding a raw handle (`PVIGEM_CLIENT`, `InterceptionContext`, `EGLDisplay`,
`IDXGIOutputDuplication`) is `!Send` by Rust's rules, so the author MUST do one
of exactly two things, and say which in the add-on's spec:

1. write `unsafe impl Send` **with a comment citing the vendor's documented
   thread-safety** for that handle; or
2. pin the handle to a funnel thread owned by the add-on and make the sabi
   object a `Send` proxy that messages it — the shape
   [`../addons/windows/input/WIN_TOUCH_WINDOWS_SPEC.md`](../addons/windows/input/WIN_TOUCH_WINDOWS_SPEC.md)
   already uses.

A bare `unsafe impl Send` with no argument is a data race no test will catch.

`RFbInfo` is deliberately **not** `Send`: it wraps a thread-affine GPU handle, is
produced by `next_surface` and consumed by `encode_surface` within one frame-loop
iteration on one thread, and never crosses a thread boundary.

---

## AddonKind registry

Stable ids; the descriptor's `kind` is one of these. The old loose phrasing
"kind = capture/encode/hwencode/audio/input" is **superseded by this table** —
`encode`→`SoftwareEncode`, `hwencode`→`HardwareEncode`, and `audio`/`input` split
into the sub-kinds below so one descriptor unambiguously names one trait.

| Kind | Id | Layer-1 sabi object | Layer-2 host trait | Host trait the backend implements |
|------|----|---------------------|--------------------|-----------------------------------|
| `Capture`        | `0x01` | `CapturerBox`         | `CaptureAddon` | `capture::Capturer` (+ optional `SurfaceCapturer`, `CursorCapturer`, `ConfigurableCapturer`) |
| `SoftwareEncode` | `0x02` | `EncoderBox`          | `EncoderAddon` (`kind()=="sw"`) | `encode::Encoder` |
| `HardwareEncode` | `0x03` | `HwEncoderBox`        | `EncoderAddon` (`kind()=="hw"`) | `hwencode::HardwareEncoder` |
| `AudioCapture`   | `0x04` | `AudioCapturerBox`    | `AudioAddon` (`kind()=="capture"`) | `audio::AudioCapturer` |
| `AudioCodec`     | `0x05` | `AudioEncoderBox`     | `AudioAddon` (`kind()=="codec"`) | `audio::AudioEncoder` |
| `InputKeyMouse`  | `0x06` | `InjectorBox`         | `InputAddon` (`kind()==KeyMouse`) | `input::KeyMouseInjector` |
| `InputTouch`     | `0x07` | `InjectorBox`         | `InputAddon` (`kind()==Touch`) | `input::TouchInjector` |
| `InputGamepad`   | `0x08` | `InjectorBox`         | `InputAddon` (`kind()==Gamepad`) | `input::GamepadInjector` |
| `Network`        | `0x09` | `ProviderBox`         | `network::Provider` | (v2; **reserved**, see [`../v2/MODULE_NETWORK.md`](../v2/MODULE_NETWORK.md)) |
| `AudioSink`      | `0x0A` | `AudioSinkBox`        | `AudioAddon` (`kind()=="sink"`) | `audio::AudioSink` — the client→host virtual microphone (`pw_vmic`, `win_vmic`) |

`Injector` is the Layer-1 `#[sabi_trait]` union declared above, carrying the
methods of all three `Input*` kinds behind one tagged object — one flat vtable,
gated by `descriptor().kind` and by `caps`. It is **not** a Layer-2 trait and
never a Layer-2 return type: `input::KeyMouseInjector` / `TouchInjector` /
`GamepadInjector` are the host-side traits
([`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md)), and `InputAddon` has one
typed constructor per kind.

## CodecId registry

An add-on's codec ↔ its wire frame type (see
[`./MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) `frame_type`):

| CodecId | Id | Media | Wire `frame_type` | Notes |
|---------|----|-------|-------------------|-------|
| `H264`     | `0x01` | video | `VIDEO_H264` (1) | universal default |
| `Hevc`     | `0x02` | video | `VIDEO_HEVC` (7) | HW only; HDR sessions only |
| `Av1`      | `0x03` | video | `VIDEO_AV1` (16) | HW only; wire type reserved, no add-on implements it yet (see `PLATFORM_COMPAT.md`) |
| `Opus`     | `0x10` | audio | `AUDIO_OPUS` (8) | default audio codec |
| `PcmS16le` | `0x11` | audio | `AUDIO_PCM` (4)  | built-in fallback (no codec add-on) |

A descriptor naming a `CodecId` that has no wire `frame_type` assigned at all
is skipped with a warning — see the load-failure taxonomy. (As of this spec,
every registered `CodecId` — including `Av1` — has a wire type; this path
exists for a future `CodecId` added before its wire type is picked.)

## Codec-string computation

`config.codec` is a **computed** value, never a literal. Both sides of the
boundary call the same function, so a host-side expectation and an add-on's
`codec()` can never disagree about a level:

```rust
// crate: featherdesk-abi

/// The profile an encoder is ACTUALLY configured to emit. Not a preference —
/// the add-on reports what it set on the underlying encoder. An encoder that
/// emits CABAC or B-frames may not report H264ConstrainedBaseline.
#[repr(u8)] #[derive(StableAbi, Copy, Clone, PartialEq, Eq)]
pub enum VideoProfile {
    H264ConstrainedBaseline = 0, // profile_idc 0x42, constraint flags 0xE0
    H264Main                = 1, // profile_idc 0x4D, constraint flags 0x40
    H264High                = 2, // profile_idc 0x64, constraint flags 0x00
    H264High422             = 3, // profile_idc 0x7A, constraint flags 0x00 (chroma "422")
    H264High444             = 4, // profile_idc 0xF4, constraint flags 0x00 (chroma "444")
    HevcMain                = 5, // general_profile_idc 1, compatibility 6
    HevcMain10              = 6, // general_profile_idc 2, compatibility 4 (HDR)
}

/// Builds the WebCodecs codec string for `profile` at the active geometry and
/// frame rate. THE LEVEL IS COMPUTED. `Encoder::codec()` and
/// `HardwareEncoder::codec()` both return exactly this string, and the five
/// fixed decode-probe constants in `protocol::DecodeCaps` are this function's
/// output at (1920, 1080, 60). No codec string anywhere in the tree — advertised,
/// probed, or quoted in an example — may be a literal this function cannot produce.
pub fn codec_string(profile: VideoProfile, width: u32, height: u32, fps: u32) -> RString {
    match profile {
        VideoProfile::HevcMain | VideoProfile::HevcMain10 => {
            let (idc, compat) = if profile == VideoProfile::HevcMain { (1, 6) } else { (2, 4) };
            rformat!("hvc1.{}.{}.L{}.B0", idc, compat, hevc_level_idc(width, height, fps))
        }
        _ => {
            let (pidc, cflags): (u8, u8) = match profile {
                VideoProfile::H264ConstrainedBaseline => (0x42, 0xE0),
                VideoProfile::H264Main                => (0x4D, 0x40),
                VideoProfile::H264High                => (0x64, 0x00),
                VideoProfile::H264High422             => (0x7A, 0x00),
                VideoProfile::H264High444             => (0xF4, 0x00),
                _ => unreachable!(),
            };
            rformat!("avc1.{:02X}{:02X}{:02X}", pidc, cflags,
                     h264_level_idc(width, height, fps))
        }
    }
}

/// ITU-T H.264 Annex A, Table A-1. Returns the SMALLEST `level_idc` that can
/// carry `width x height` at `fps`. Levels below 3.0 are never emitted: the
/// config floor is 320 px and over-advertising a level is always legal (a level
/// is an upper bound on what the decoder must cope with, not a claim about the
/// bitstream).
fn h264_level_idc(width: u32, height: u32, fps: u32) -> u8 {
    const T: &[(u8, u32, u32)] = &[ // (level_idc, MaxFS in MBs, MaxMBPS)
        (0x1E,   1_620,     40_500),   // 3.0
        (0x1F,   3_600,    108_000),   // 3.1
        (0x20,   5_120,    216_000),   // 3.2
        (0x28,   8_192,    245_760),   // 4.0
        (0x29,   8_192,    245_760),   // 4.1
        (0x2A,   8_704,    522_240),   // 4.2
        (0x32,  22_080,    589_824),   // 5.0
        (0x33,  36_864,    983_040),   // 5.1
        (0x34,  36_864,  2_073_600),   // 5.2
        (0x3C, 139_264,  4_177_920),   // 6.0
        (0x3D, 139_264,  8_355_840),   // 6.1
        (0x3E, 139_264, 16_711_680),   // 6.2
    ];
    let mbs_w = width.div_ceil(16);
    let mbs_h = height.div_ceil(16);
    let fs    = mbs_w * mbs_h;
    let mbps  = fs.saturating_mul(fps);
    for &(idc, max_fs, max_mbps) in T {
        // Annex A also bounds each dimension: PicWidthInMbs and FrameHeightInMbs
        // must each be <= sqrt(MaxFS * 8).
        let side = isqrt(max_fs * 8);
        if fs <= max_fs && mbps <= max_mbps && mbs_w <= side && mbs_h <= side {
            return idc;
        }
    }
    0x3E // 6.2 — clamped; log once at warn!. Nothing v1 supports reaches it.
}

/// ITU-T H.265 Annex A, Tables A.8 / A.9 (Main tier). Returns
/// `general_level_idc` (= level x 30).
fn hevc_level_idc(width: u32, height: u32, fps: u32) -> u8 {
    const T: &[(u8, u64, u64)] = &[ // (general_level_idc, MaxLumaPs, MaxLumaSr)
        ( 90,      552_960,    16_588_800),  // 3.0
        ( 93,      983_040,    33_177_600),  // 3.1
        (120,    2_228_224,    66_846_720),  // 4.0
        (123,    2_228_224,   133_693_440),  // 4.1
        (150,    8_912_896,   267_386_880),  // 5.0
        (153,    8_912_896,   534_773_760),  // 5.1
        (156,    8_912_896, 1_069_547_520),  // 5.2
        (180,   35_651_584, 1_069_547_520),  // 6.0
        (183,   35_651_584, 2_139_095_040),  // 6.1
        (186,   35_651_584, 4_278_190_080),  // 6.2
    ];
    let ps = (width as u64) * (height as u64);
    let sr = ps.saturating_mul(fps as u64);
    for &(idc, max_ps, max_sr) in T {
        let side = isqrt64(max_ps * 8);
        if ps <= max_ps && sr <= max_sr
            && (width as u64) <= side && (height as u64) <= side { return idc; }
    }
    186 // 6.2 — clamped; log once at warn!.
}
```

### Reference strings

These are the values the computation produces for the geometries v1 supports.
No spec may quote a codec string that is not in this table.

| Geometry | H.264 High (SDR default) | HEVC Main10 (HDR) |
|----------|--------------------------|-------------------|
| 1280x720 @30  | `avc1.64001F` (L3.1) | `hvc1.2.4.L93.B0` (L3.1) |
| 1280x720 @60  | `avc1.640020` (L3.2) | `hvc1.2.4.L120.B0` (L4.0) |
| 1920x1080 @30 | `avc1.640028` (L4.0) | `hvc1.2.4.L120.B0` (L4.0) |
| 1920x1080 @60 | `avc1.64002A` (L4.2) | `hvc1.2.4.L123.B0` (L4.1) |
| 2560x1440 @60 | `avc1.640033` (L5.1) | `hvc1.2.4.L150.B0` (L5.0) |
| 3840x2160 @60 | `avc1.640034` (L5.2) | `hvc1.2.4.L153.B0` (L5.1) |

**Decode-probe constants.** The five fixed probe strings in
`protocol::DecodeCaps` are this same computation at the 1920x1080 @60 row, for the
profiles the table's two columns do not show: `avc1.64002A` (High), `avc1.7A002A`
(High 4:2:2), `avc1.F4002A` (High 4:4:4 Predictive), `hvc1.1.6.L123.B0` (HEVC
Main) and `hvc1.2.4.L123.B0` (HEVC Main10). They are in this table for the
purposes of the rule above.

A resolution or fps change re-computes the string: `apply_params` re-reads
`codec()` after `reconfigure_or_rebuild` and includes it in the fresh `config`.

## AbiErr registry

The `code` carried by every `AbiError` across the boundary. The space is
**partitioned by domain** — which host enum the calling site's return type maps
into — and within each domain the code ↔ variant mapping is a **bijection**, so
it is total in both directions:

| Domain | Add-on kinds | Host enum | Codes |
|--------|--------------|-----------|-------|
| **V** (video) | `Capture` `0x01`, `SoftwareEncode` `0x02`, `HardwareEncode` `0x03` | `stream::StreamError` | 1, 2, 3, 4, 5, 6, 7, 8 |
| **A** (audio) | `AudioCapture` `0x04`, `AudioCodec` `0x05` | `audio::AudioError` | 1, 6, 7, 8 |
| **I** (input) | `InputKeyMouse` `0x06`, `InputTouch` `0x07`, `InputGamepad` `0x08` | `input::InputError` | 1, 6, 7, 8, 9 |
| **L** (load) | every kind — `init` / `descriptor` / `probe` / `construct` | `pipeline::PipelineError` | 1, 7, 10 |

| AbiErr | Code | Domains | `StreamError` (V) | `AudioError` (A) | `InputError` (I) | `PipelineError` (L) |
|--------|------|---------|-------------------|------------------|------------------|---------------------|
| `Generic`            | `1`  | V A I L | `Backend(detail)`       | `Backend(detail)`       | `Backend(detail)`       | `AddonBackend{id,detail}` |
| `FallbackToSoftware` | `2`  | V       | `FallbackToSoftware`    | —                       | —                       | — |
| `ChromaUnsupported`  | `3`  | V       | `ChromaUnsupported`     | —                       | —                       | — |
| `HdrUnsupported`     | `4`  | V       | `HdrUnsupported`        | —                       | —                       | — |
| `RequiresRestart`    | `5`  | V       | `RequiresRestart`       | —                       | —                       | — |
| `DeviceLost`         | `6`  | V A I   | `DeviceLost`            | `DeviceLost`            | `DeviceLost`            | — |
| `Unrecoverable`      | `7`  | V A I L | `Unrecoverable(detail)` | `Unrecoverable(detail)` | `Unrecoverable(detail)` | `AddonUnrecoverable{id,detail}` |
| `Unsupported`        | `8`  | V A I   | `Unsupported`           | `Unsupported`           | `Unsupported`           | — |
| `SasUnavailable`     | `9`  | I       | —                       | —                       | `SasUnavailable`        | — |
| `BadConfig`          | `10` | L       | —                       | —                       | —                       | `AddonConfig{id,detail}` |

**Per-code meaning.**

| AbiErr | Meaning |
|--------|---------|
| `Generic`            | unspecified backend failure; `detail` is logged, never on the wire. A **single transient call failure** — the host skips the frame and continues. |
| `FallbackToSoftware` | HW path unusable → degrade to SW for the session. |
| `ChromaUnsupported`  | encoder can't emit the requested chroma. |
| `HdrUnsupported`     | encoder can't emit 10-bit / HEVC-Main10. |
| `RequiresRestart`    | param change needs teardown + rebuild. |
| `DeviceLost`         | capture/encode/audio/input device or context lost. |
| `Unrecoverable`      | the add-on has exhausted its own restart budget, **or** it caught a panic. Terminal for that instance: the host MUST drop it and fall through to the next candidate, never retry ([`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) "Add-On Crash Recovery"). |
| `Unsupported`        | the host called a method the object's `caps()` does not claim. Always a **capability lie** or a host bug — see "Misbehaving add-ons". |
| `SasUnavailable`     | `send_sas` refused by policy or privilege. |
| `BadConfig`          | `section_toml` failed the add-on's own schema; `detail` names the key. Fatal at startup. |

Code `0` is reserved and MUST NOT appear in an `RErr`; success is `ROk`, never an
error code. The space is **append-only** (a retired code is never reused, same
discipline as the wire `frame_type::` and `close::` spaces) and, like an
`AddonCaps` bit, adding a code does **not** bump `ABI_VERSION`.

**Availability is NOT an error** — a negative probe is
`ROk(ProbeReport { available: false, reason, .. })`, never an `AbiErr`.

**Out-of-domain and unknown codes.** A code that is valid but not in the calling
domain's set (an audio add-on returning `FallbackToSoftware`), and a code this
host does not define at all, both map to that domain's `Generic` variant with the
raw code preserved: `Backend("add-on returned out-of-domain AbiErr code 2: …")`
/ `Backend("unknown AbiErr code 250: …")`. The host logs once per (add-on id,
code) at `warn`. This is what makes the reverse mapping **total** without a
silent default and without a panic.

---

## Fault Recovery

The `dlopen` boundary is a **soundness** boundary, not a **safety** boundary.
This section states exactly what the contract catches and what it does not.

### Panics — caught (mandatory)

A panic that unwinds **across** the `dlopen` boundary is undefined behavior. Two
rules close it:

- **No UB.** `abi_stable`'s `#[sabi_trait]` guards every boundary call, so an
  escaped panic becomes a controlled process *abort*, never UB.
- **No abort.** A bare abort would still take the host down with a buggy
  third-party add-on, so each add-on MUST wrap **every** Layer-1 entry point —
  `init`, `descriptor`, `probe`, `construct`, and every method on every
  `#[sabi_trait]` object it returns — in `std::panic::catch_unwind`, and return
  `AbiErr::Unrecoverable` (`7`) with `AbiError.detail` set to the panic payload
  when it is a `&str` / `String`, else `"panic (non-string payload)"`. Add-on
  `cdylib`s are therefore built `panic = "unwind"`, never `panic = "abort"`.

**`Unrecoverable`, not `Generic`.** A caught panic is a *deterministic, permanent*
condition. `Generic` maps to `StreamError::Backend`, which
[`./MODULE_STREAM_PARAMS.md`](./MODULE_STREAM_PARAMS.md) defines as "a single
transient call failure" — returning it for a panic makes the pipeline retry a
deterministic failure ten times and then shut down with a working software
fallback unused. `Unrecoverable` routes it to the one correct response: drop this
add-on for the session and fall through to the next candidate
([`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) "Add-On Crash Recovery", Level 2).

### Not caught — stated plainly

`catch_unwind` catches Rust panics. Nothing in this contract catches:

| Fault | What actually happens |
|-------|-----------------------|
| `SIGSEGV` / `SIGBUS` / `SIGILL` in the add-on or in a vendor SDK it calls (NVENC, AMF, ViGEmClient, libva, `interception.dll`) | the host process dies. No add-on error, no fall-through |
| `abort()` / `std::process::exit()` / a `panic = "abort"` build | the host process dies immediately |
| stack overflow inside a boundary call | `SIGSEGV` on the guard page — as above |
| memory corruption of *host* state through a raw handle the add-on was given (an `RSurfaceHandle`, an `RSlice`) | undefined behavior anywhere in the host, arbitrarily later |
| a boundary call that never returns (driver deadlock, blocking IOCTL) | the calling thread hangs and cannot be preempted |
| a thread the add-on spawned outliving the object's `Drop` | use-after-free inside the add-on, on the add-on's schedule |

The only structural mitigation v1 ships is **thread isolation of the hot path**:
the frame loop runs on its own `std::thread`, so a hung capture or encode call
does not stall the tokio runtime — the server keeps serving, sessions stay up,
and the operator sees `featherdesk_frames_encoded_total` stop advancing while
`/metrics` still answers. A hung input or audio call has no such isolation.
Out-of-process add-on hosting, the only real fix, is **out of scope for v1**.

### What the host promises instead

The host treats every `AbiError` as a backend failure — log, then skip the frame
/ degrade / drop the add-on per that domain's rules — never as a host panic.
**An add-on that returns errors, lies about its capabilities, or panics cannot
take the host down. An add-on that corrupts memory, segfaults, aborts or hangs
can, and no part of this ABI prevents it.** The add-ons directory is a trust
boundary, exactly as [`../CENTRAL_SPEC.md`](../CENTRAL_SPEC.md) "Add-on directory
(portable, user-controlled)" states: loading a library runs its native code, with
the host's privileges, inside the host's address space.

---

## Misbehaving add-ons

An add-on's `caps` is a **claim**, not a proof. The host cannot verify it — that
is why the bit exists — so it must have a defined response when the claim turns
out to be false. In every row the response is the same shape: **clear the bit,
log once, take the non-optional path.** Never fatal, never a retry, never the
error ladder.

| Situation | Host behavior |
|-----------|---------------|
| Claims a bit; the method returns `Unsupported` (`8`) | `handle.clear_cap(bit)`; log once at `error` with the add-on id and the method name; take the non-optional path for the rest of the session (below). The method is never called again. |
| Claims `SURFACE`; `next_surface` returns `Generic` (`1`) three times consecutively | `clear_cap(SURFACE)` + `degrade_to_software()` — **not** the capture ladder's "3 consecutive → restart / 10 consecutive → fatal". The SW fallback is guaranteed to exist by startup step 3f; shutting down with it unused is the wrong answer to a deterministic failure. |
| Claims a bit; the method returns any other code | ordinary handling for that code in that domain. Not a capability lie. |
| Implements a capability but does not set the bit | the method is never called, and the host cannot detect it. The add-on's own Testing Strategy carries the obligation: `probe()`'s `caps` equals the set of optional methods the object actually serves. |
| The constructed object's `caps()` sets a bit `probe()` did not | the extra bits are masked off and one `warn` is logged. `caps()` may only narrow. |
| `construct()` returns an `AddonObject` variant that does not match `descriptor().kind` | drop the add-on for the session; log at `error` with the id, the declared kind and the returned variant. Never fatal for the host. |
| Any Layer-1 call returns `Unrecoverable` (`7`) — including a caught panic | [`./MODULE_PIPELINE.md`](./MODULE_PIPELINE.md) "Add-On Crash Recovery" Level 2: log with `AbiError.detail`, poison the add-on for the session, fall through to the next candidate. Never retry. |

The non-optional path per bit:

| Bit | Non-optional path once cleared |
|-----|--------------------------------|
| `SURFACE` | `degrade_to_software()` — CPU readback via `next_frame` on the SAME object |
| `CURSOR` | if the selected add-on's `ProbeReport.caps` sets `EMBED_CURSOR`, rebuild through Add-On Crash Recovery with `CaptureConfig.embed_cursor = true`, set `cursor_mode = Embedded` and push a fresh `config`; otherwise `cursor_mode` stays `Separate` and NO `config` is pushed — the session runs without a client-side pointer |
| `EMBED_CURSOR` / `EMBED_CURSOR_SURF` | the add-on was selected on a claim it cannot honour: the frame arrives cursor-free in a session that resolved `"embedded"`. Fall through to the next capture candidate rather than run a pointerless stream |
| `CONFIGURABLE` / `ENC_CONFIGURABLE` | every parameter change becomes a teardown + rebuild of that add-on |
| `SECURE_ATTENTION` | the CAD chord is dropped with the one-time warning; the constituent keys are still **not** injected |
| `RUMBLE` | no sink is installed; `[gamepad] allow_rumble` is logged inert |

---

## Load-failure taxonomy

Default = skip the library with a `WARN`; `[addons] abi_strict = true` aborts
startup for any of these:

| Failure | Default behavior |
|---------|------------------|
| library fails to load (corrupt, wrong **os/arch**, missing transitive dep, **or rejected by macOS library validation** — see [`../addons/macos/MACOS_SPEC.md`](../addons/macos/MACOS_SPEC.md)) | skip + warn, with the loader's own error text (`dlerror()` / `GetLastError`) in the log line |
| not an add-on (no `abi_stable` root module — stray library in dir) | skip + warn |
| root-module constructor returns an error | skip + warn |
| `init()` returns an error (the add-on refuses this host) | skip + warn, with `AbiError.detail` |
| ABI version / layout-hash mismatch (`abi_stable`) | skip + warn |
| capability descriptor names an unknown `AddonKind`, or a `CodecId` with no wire type assigned | skip + warn |
| `probe()` sets `AddonCaps` bits outside `AddonCaps::KNOWN` | **load succeeds**; unknown bits ignored, logged once at `info` |
| `construct()` returns an `AddonObject` variant that mismatches `descriptor().kind` | drop that add-on + `error`; never affected by `abi_strict`, never fatal for the host |
| `construct()` returns `BadConfig` (`10`) at startup | **fails startup**, naming the add-on id, the key and the config path; never affected by `abi_strict` (an operator typo is always fatal, exactly like a mistyped core key) |
| add-ons dir does not exist | treated as empty + warn |

An add-on library MUST match the host's **OS *and* CPU arch** (an x86_64 `.dylib`
will not load into an arm64 host); add-ons are built natively per target, not
cross-composed.

---

## ABI versioning

- `ABI_VERSION` (this crate) is **`1`**. It is bumped on any breaking change to a
  **type or trait** that crosses the boundary: a struct's fields, an enum's
  variants or layout, a `#[sabi_trait]`'s method list or supertraits.
- It is **not** bumped by adding an `AddonCaps` bit or an `AbiErr` code. Both are
  append-only *data* spaces inside types whose layout does not change, so two
  builds of the same `ABI_VERSION` may know different sets — which is exactly the
  forward compatibility the unknown-value rules below provide.
- **Acceptance is exact equality** in v1: the host accepts an add-on iff
  `descriptor.abi_version == ABI_VERSION` **and** `abi_stable`'s structural layout
  hash matches **and** `descriptor.os == HOST_OS` **and**
  `descriptor.arch == HOST_ARCH`. There is no compatibility range; a range is
  introduced, if ever, when a v2 exists to need one.
- `abi_stable` **independently** verifies the layout hash of every crossing type
  on load, so even a forgotten version bump is caught (mismatch = rejected, not
  miscompiled).

**Unknown values, both directions.**

| Unknown | Direction | Behavior |
|---------|-----------|----------|
| `AddonCaps` bits outside `AddonCaps::KNOWN` | add-on → host | ignored for behaviour; logged once per add-on at `info` with the raw mask. Never an error, never a load rejection. Width is `u32`, fixed |
| An `AbiErr` code the host does not define, or one outside the calling domain | add-on → host | mapped to that domain's `Generic` variant with the raw code preserved in the detail string; one `warn` per (add-on id, code). The mapping stays total; there is no silent default |
| `AddonKind` the host does not define | add-on → host | the library is skipped with a warning (load-failure taxonomy) |
| `CodecId` with no wire `frame_type` assigned | add-on → host | the library is skipped with a warning |
| The host's `ABI_VERSION`, at `init()` | host → add-on | the add-on may refuse to run against a host version it does not expect by returning an error from `init()`; the library is then skipped with a warning. This is the add-on's only forward-looking lever, and it is why `HostServices` carries the host's version at all |
| An `AddonObject` variant the host does not expect for the declared kind | add-on → host | the add-on is dropped with an `error` (see "Misbehaving add-ons") |

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `AbiErr` → host enum mapping is **total per domain**: for each of V/A/I/L every code in that domain's set maps to exactly one variant and every variant maps back to exactly one code. Codes 1–8 ↔ `StreamError`'s eight variants is a bijection | No |
| Unit | An out-of-domain code (`FallbackToSoftware` from an audio add-on) and an unknown code (`250`) both land on that domain's `Backend` variant with the raw code preserved in the detail, and log once per (add-on, code) — never a silent default, never a panic | No |
| Unit | `CodecId` → wire `frame_type` lookup for all five registered codecs, plus the "no wire type assigned" skip path for a hypothetical future codec | No |
| Unit | `codec_string` for the whole {720p, 1080p, 1440p, 2160p} x {30, 60} matrix yields a `level_idc` at least as large as the Annex A minimum for that geometry, and exactly the string in the reference table | No |
| Unit | `AddonCaps` masking: a `ProbeReport` with bits outside `KNOWN` loads, behaves as if those bits were `0`, and logs once at `info`; `AddonCaps::KNOWN` equals the OR of every declared constant | No |
| Unit | `RParamsCapability.hot_changeable` ↔ `StreamParamsCapability.hot_changeable` round-trips for all ten `ParamField`s, and a `0` bound is **omitted** from `min_values`/`max_values` rather than encoded as `0` | No |
| Unit | `RVec<u8>` / `RString` returned across the boundary drop deterministically exactly once (no double-free, no leak) — verified via a counting allocator in the fixture add-on | No |
| Integration | Load a real, separately-compiled `cdylib` fixture and drive `init()` → `descriptor()` → `probe()` → `construct()` end-to-end (the validated spike scenario from the Overview) | No (needs a built cdylib fixture) |
| Integration | `install_log_sink`: a `warn!` inside the fixture cdylib appears on the host's `tracing` subscriber with the add-on's target, and does **not** appear when the fixture's `init()` skips the install | No (fixture cdylib) |
| Integration | The constructed object's `caps()` wins over the probe's: a fixture reporting `SURFACE` at probe and clearing it at construct never has `next_surface` called; one reporting `0` at probe and `SURFACE` at construct is masked back to `0` with a `warn` | No (fixture cdylib) |
| Integration | A fixture that sets `AddonCaps::SURFACE` and returns `Unsupported` (`8`) from `next_surface` produces exactly one `error`, one `clear_cap(SURFACE)` and one `degrade_to_software()` — never the capture ladder, never a shutdown | No (fixture cdylib) |
| Integration | A panic inside `init()`/`probe()`/`construct()`/any trait method, wrapped in `catch_unwind`, surfaces as `AbiErr::Unrecoverable` (`7`) with the panic message in `detail` — never `Generic`, never an unwind across `dlopen` | No (fixture cdylib) |
| Integration | `construct()` returning an `AddonObject` variant that mismatches `descriptor().kind` drops that add-on with an `error` and the host keeps running | No (fixture cdylib) |
| Integration | `RAddonConfig.section_toml` round-trip: a fixture whose section has an unknown key returns `BadConfig` (`10`) and startup fails naming the add-on id, the key and the config path; an absent section arrives as an empty `RString` and the fixture uses its defaults | No (fixture cdylib) |
| Integration | `ABI_VERSION` mismatch and `abi_stable` structural layout-hash mismatch are both rejected at load, never silently miscompiled | No |
| Integration | A `.cdylib` built for the wrong OS/arch (x86_64 `.so` on an arm64 host) is rejected at load, never partially loaded | No (cross-compiled fixture) |
| Integration | `[addons] abi_strict = true` aborts startup on every skip-and-warn row of the load-failure taxonomy; default config only warns + skips. `BadConfig` and the kind-mismatch row are **unaffected** by `abi_strict` — the first always fails startup, the second never does | No |
| Integration | macOS: a notarized hardened-runtime build **with** `com.apple.security.cs.disable-library-validation` loads a differently-signed fixture `.dylib`; **without** it the load is rejected and the log line carries the code-signature error | Yes (macOS, Developer ID) |

---

## Status

📋 **Specced.** `featherdesk-abi` is the first crate built (everything depends on
it). It has no runtime behavior of its own — only types, constants, and
`#[sabi_trait]` definitions shared by the host and every add-on.
