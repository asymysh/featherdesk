# Module Spec: Add-on ABI (`featherdesk-abi`)

## Overview

`featherdesk-abi` is the **stable contract crate** compiled into BOTH the host
and every add-on `cdylib`. It is the **single source of truth** for everything
the two sides must agree on at the `dlopen` boundary: the capability descriptor,
the `AddonKind` / `CodecId` / `AbiErr` registries, the root-module surface, and
the ABI version. Neither side hardcodes a literal — both `use featherdesk_abi::*`,
so a number means the same thing on both sides, or the load is rejected.

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

Every add-on exports this and nothing else:

```rust
// crate: featherdesk-abi   (compiled into the host AND every add-on)
pub const ABI_VERSION: u32 = 1;     // bumped on ANY breaking change to this crate

#[repr(C)] #[derive(StableAbi)]
pub struct CapabilityDescriptor {
    pub kind: AddonKind,        // what this add-on is (registry below)
    pub id: RString,            // add-on id, e.g. "kms_egl" (== filename + config-section suffix)
    pub codecs: RVec<CodecId>,  // codecs it can emit/consume (encoders + audio); empty otherwise
    pub os: Os, pub arch: Arch, // must equal the host's, else the load is rejected
    pub abi_version: u32,       // == ABI_VERSION it was built against
}

#[sabi_trait]
pub trait FeatherDeskAddon {
    fn descriptor(&self) -> CapabilityDescriptor;
    fn probe(&self) -> RResult<ProbeReport, u32>;          // u32 = AbiErr code (registry below)
    fn construct(&self, cfg: RAddonConfig) -> RResult<AddonObject, u32>; // sabi object for `kind`
}

// Layer-1 probe result — ALL abi_stable types. (The host's richer Layer-2
// `ProbeResult` in MODULE_PIPELINE carries String / HashMap / serde_json::Value
// and is NOT boundary-safe; the adapter BUILDS it from this report.)
#[repr(C)] #[derive(StableAbi)]
pub struct ProbeReport {
    pub available: bool,        // false = prerequisite missing — this is Ok(available:false), NOT an error
    pub reason: RString,        // human-readable detail when !available
    pub codecs: RVec<CodecId>,  // what this backend can actually emit/consume
}
```

> **`RAddonConfig` / `AddonObject` are abi-stable unions.** `RAddonConfig` carries
> the kind-appropriate config (the host's `CaptureConfig` / `EncoderConfig` /
> `HWEncoderConfig` / `InjectorConfig` / `AudioConfig`, rendered in abi-stable
> types — `RString`, not `String`). `AddonObject` is the abi-stable enum wrapping
> the kind's sabi object (`CapturerBox` / `EncoderBox` / …). The Layer-2 adapter
> converts host config → `RAddonConfig`, calls `construct()`, and wraps the
> returned `AddonObject` as a `Box<dyn HostTrait>`.

### Rich types across the boundary

- **Buffers** — `RVec<u8>`. An `RVec<u8>` returned by an add-on **transfers
  ownership to the host**; it carries the add-on's deallocator, so dropping it on
  the host side is deterministic — **no GC, no use-after-free.**
- **Strings** — `RString` (owned) / `RStr<'_>` (borrowed).
- **Results** — `RResult<T, u32>` (the `u32` is an `AbiErr` code; see registry).
- **Trait objects** — `#[sabi_trait]` objects (`CapturerBox`, `EncoderBox`, …).

### Errors cross as `u32`

Errors cross as `RResult<T, u32>` where the `u32` is a stable `AbiErr` code; the
host **maps the code back into its own `StreamError` / `AudioError` /
`InputError` enum** so the pipeline's normal `match`/`?` works. (In
Go this mapping silently failed because sentinel error *values* differ per copy;
in Rust it is explicit and centrally registered.)

### Channels are host-side

A streaming add-on exposes a pull method (`next_frame()` / `next_surface()` /
`next_chunk()`); the **host** task pumps it into a `tokio::sync::mpsc` channel.
The channel itself **never crosses the boundary**.

---

## AddonKind registry

Stable ids; the descriptor's `kind` is one of these. The old loose phrasing
"kind = capture/encode/hwencode/audio/input" is **superseded by this table** —
`encode`→`SoftwareEncode`, `hwencode`→`HardwareEncode`, and `audio`/`input` split
into the sub-kinds below so one descriptor unambiguously names one trait.

| Kind | Id | Layer-1 sabi object | Layer-2 host trait | Host trait the backend implements |
|------|----|---------------------|--------------------|-----------------------------------|
| `Capture`        | `0x01` | `CapturerBox`         | `CaptureAddon` | `capture::Capturer` (+ optional `SurfaceCapturer`) |
| `SoftwareEncode` | `0x02` | `EncoderBox`          | `EncoderAddon` (`kind()=="sw"`) | `encode::Encoder` |
| `HardwareEncode` | `0x03` | `HwEncoderBox`        | `EncoderAddon` (`kind()=="hw"`) | `hwencode::HardwareEncoder` |
| `AudioCapture`   | `0x04` | `AudioCapturerBox`    | `AudioAddon` (`kind()=="capture"`) | `audio::AudioCapturer` |
| `AudioCodec`     | `0x05` | `AudioEncoderBox`     | `AudioAddon` (`kind()=="codec"`) | `audio::AudioEncoder` |
| `InputKeyMouse`  | `0x06` | `InjectorBox`         | `InputAddon` | `input::KeyMouseInjector` |
| `InputTouch`     | `0x07` | `InjectorBox`         | `InputAddon` | `input::TouchInjector` |
| `InputGamepad`   | `0x08` | `InjectorBox`         | `InputAddon` | `input::GamepadInjector` |
| `Network`        | `0x09` | `ProviderBox`         | `network::Provider` | (v2; **reserved**, see [`../v2/MODULE_NETWORK.md`](../v2/MODULE_NETWORK.md)) |

`Injector` is the umbrella [`MODULE_INPUT.md`](../interaction/MODULE_INPUT.md) uses
for the three `Input*` kinds.

## CodecId registry

An add-on's codec ↔ its wire frame type (see
[`./MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) `frame_type`):

| CodecId | Id | Media | Wire `frame_type` | Notes |
|---------|----|-------|-------------------|-------|
| `H264`     | `0x01` | video | `VIDEO_H264` (1) | universal default |
| `Hevc`     | `0x02` | video | `VIDEO_HEVC` (7) | HW only; HDR |
| `Av1`      | `0x03` | video | — (none yet)     | descriptor accepted but **load-rejected** (no wire type assigned) |
| `Opus`     | `0x10` | audio | `AUDIO_OPUS` (8) | default audio codec |
| `PcmS16le` | `0x11` | audio | `AUDIO_PCM` (4)  | built-in fallback (no codec add-on) |

A descriptor naming a `CodecId` that has no wire `frame_type` (e.g. `Av1` today)
is skipped with a warning — see the load-failure taxonomy.

## AbiErr registry

The `u32` carried by every `RResult<_, u32>` across the boundary. The host maps
each code back to the matching host enum:

| AbiErr | Code | Maps to host variant | Meaning |
|--------|------|----------------------|---------|
| `Generic`            | `1` | the crate's catch-all (`StreamError::Backend` / `AudioError::Backend` / `InputError::Io`) | unspecified failure (detail logged, not on the wire); **also the code an add-on returns for a caught panic** (see "FFI panic safety") |
| `FallbackToSoftware` | `2` | `StreamError::FallbackToSoftware` | HW path unusable → degrade to SW for the session |
| `ChromaUnsupported`  | `3` | `StreamError::ChromaUnsupported`  | encoder can't emit the requested chroma |
| `HdrUnsupported`     | `4` | `StreamError::HdrUnsupported`     | encoder can't emit 10-bit / HEVC-Main10 |
| `RequiresRestart`    | `5` | `StreamError::RequiresRestart`    | param change needs teardown + rebuild |
| `DeviceLost`         | `6` | `StreamError::DeviceLost` / `AudioError::DeviceLost` | capture/encode/audio device or context lost |

Code `0` is reserved; success is `ROk`, never an error code. **Availability is NOT
an error** — a negative probe is `Ok(ProbeReport { available: false, reason })`,
never an `AbiErr`. The codes above are the full v1 set; the space is **append-only**
(a retired code is never reused — same discipline as the wire `frame_type::` and
`close::` spaces), so new conditions (e.g. a future `Unsupported`) take the next
free id.

---

## FFI panic safety (mandatory)

A panic that unwinds **across** the `dlopen` boundary is **undefined behavior**.
The contract closes this two ways:

- **No UB.** `abi_stable`'s `#[sabi_trait]` already guards every boundary call so
  a panic becomes a controlled process *abort*, not UB.
- **No host crash.** A bare abort would still take the host down with a buggy
  third-party add-on, so each add-on MUST wrap its Layer-1 entry points
  (`descriptor` / `probe` / `construct` and every trait method) in
  `std::panic::catch_unwind` and return **`AbiErr::Generic`** instead of
  unwinding. Add-on `cdylib`s are therefore built `panic = "unwind"` (so
  `catch_unwind` works), never `panic = "abort"`.

The host treats `AbiErr::Generic` from any add-on call as a backend failure (log +
skip the frame / degrade / drop that add-on), never as a host panic. **A
misbehaving add-on can fail a call but cannot UB-corrupt or crash the host through
the ABI.**

---

## Load-failure taxonomy

Default = skip the library with a `WARN`; `[addons] abi_strict = true` aborts
startup for any of these:

| Failure | Default behavior |
|---------|------------------|
| library fails to load (corrupt, wrong **os/arch**, missing transitive dep) | skip + warn |
| not an add-on (no `abi_stable` root module — stray library in dir) | skip + warn |
| root-module constructor returns an error | skip + warn |
| ABI version / layout-hash mismatch (`abi_stable`) | skip + warn |
| capability descriptor names an unknown `AddonKind`, or a `CodecId` with no wire type (e.g. `Av1`) | skip + warn |
| add-ons dir does not exist | treated as empty + warn |

An add-on library MUST match the host's **OS *and* CPU arch** (an x86_64 `.dylib`
will not load into an arm64 host); add-ons are built natively per target, not
cross-composed.

---

## ABI versioning

- `ABI_VERSION` (this crate) is bumped on **any** breaking change to a type or
  trait that crosses the boundary.
- `abi_stable` **independently** verifies a structural **layout hash** of every
  crossing type on load — so even a forgotten version bump is caught (mismatch =
  rejected, not miscompiled).
- The host accepts an add-on iff its ABI version is compatible **and** the layout
  hash matches **and** os/arch match.

---

## Status

📋 **Specced.** `featherdesk-abi` is the first crate built (everything depends on
it). It has no runtime behavior of its own — only types, constants, and
`#[sabi_trait]` definitions shared by the host and every add-on.
