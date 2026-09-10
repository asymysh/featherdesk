# Module Spec: Encode (Software Encoder Interface Contract)

## Overview

The Encode module defines the **abstract software encoder interface contract**
that every SW encoder add-on implements. It owns no encoder implementation
itself — concrete encoders live in their respective add-on specs:

- `addons/encode/openh264/` — Cisco OpenH264 via Rust FFI (add-on ID `openh264`, BSD)
- `addons/encode/x264/` — x264 via ffmpeg subprocess (add-on ID `x264`, GPL-isolated, **opt-in**)
- `addons/encode/vt/` — VideoToolbox SW (add-on ID `vt_sw`, macOS-only)

This separation keeps the module spec stable while allowing add-on
implementations to evolve independently.

> **Hardware encoders** implement a separate contract in
> [`MODULE_HARDWARE_ENCODE.md`](./MODULE_HARDWARE_ENCODE.md) (zero-copy GPU
> surface input). Both contracts produce identical `EncodedFrame` output for
> the pipeline; the dispatch decision lives in
> [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md).

---

## Public Interface

```rust
// crate: featherdesk-encode

// Encoder is the contract every software encoder add-on must satisfy.
// Cleanup is RAII (Drop) — no Close() (Drop releases all encoder resources,
// including subprocesses for out-of-process add-ons like x264).
//
// Thread-affine, but declared `Send`. The host constructs, uses and drops this
// object on a single worker thread (the frame-loop thread — CENTRAL_SPEC
// "Concurrency Model") and it never crosses a thread boundary once built —
// `Send` is required only because the empty `FrameLoop` / `AudioLoop` struct that
// will build it is moved onto that thread by `Pipeline::start`. The object may
// hold a bound library or child-process context for its whole life (MODULE_ABI
// "Thread requirements").
pub trait Encoder: Send {
    /// encode takes a YUV frame and returns the encoded bitstream as one
    /// contiguous Annex B access unit (start codes retained) plus a keyframe
    /// flag, wrapped as an EncodedUnit. The data is ONE complete access unit --
    /// NO per-NAL splitting (the Vec<Vec<u8>> per-NAL contract is rejected).
    ///
    /// keyframe is set by the encoder itself (it knows when it emitted an
    /// IDR/IRAP) — the server does NOT re-scan the bitstream. This flag becomes
    /// stream::EncodedFrame::keyframe, driving the IDR cache + bootstrap stream.
    ///
    /// Returns Ok(None) when there is no complete access unit for this call —
    /// either the frame was intentionally skipped (rate control) or the encoder
    /// is PIPELINED and this frame's access unit has not emerged yet (the x264
    /// subprocess bridge, whose child holds one or two frames). Ok(None) is
    /// never an error and never a dropped frame: a pipelined add-on delivers
    /// that frame's access unit from a later call.
    ///
    /// `unit.data` is an owned RVec<u8> whose ownership transfers to the caller
    /// (deterministic drop across the add-on ABI).
    fn encode(&mut self, frame: &YuvFrame) -> Result<Option<EncodedUnit>, StreamError>;

    /// force_keyframe requests that the next encoded frame be an IDR.
    ///
    /// It is called ONLY from the frame-loop thread, and only BETWEEN two
    /// `encode` calls — never concurrently with one. The host owns the
    /// cross-thread signalling: server tasks set an `Arc<AtomicBool>` and the
    /// frame loop reads, clears and acts on it (MODULE_PIPELINE "Main Frame
    /// Loop", step 0c/3). An add-on therefore needs no interior mutability, no
    /// atomic and no lock for this method — `&mut self` is honest, and `encode`
    /// and reconfigure can never alias (borrow-checker enforced).
    fn force_keyframe(&mut self);

    /// codec returns the WebCodecs codec string for the `config` handshake. It
    /// is COMPUTED, never a constant: the add-on calls
    /// featherdesk_abi::codec_string(active_profile, p.width, p.height, p.fps)
    /// after construction and after every successful update_stream_params, so
    /// the advertised profile and level always match what is actually being
    /// encoded (see MODULE_ABI "Codec-string computation" and "Profile" below).
    /// The pipeline dispatches the wire frame_type off this string
    /// ("avc1.*" -> VIDEO_H264, "hvc1.*" -> VIDEO_HEVC), so a SW add-on that
    /// emits HEVC is labelled correctly on the wire.
    ///
    /// Boundary form: the add-on returns `CodecId` **and** `VideoProfile`; the
    /// Layer-2 adapter calls `featherdesk_abi::codec_string(profile, width,
    /// height, fps)` with the params now in force, after construction and after
    /// every successful `update_stream_params`.
    fn codec(&self) -> &str;
}

// EncodedUnit is ONE contiguous Annex B access unit + a keyframe flag (shared
// with the HW path; the pipeline wraps it into stream::EncodedFrame).
pub struct EncodedUnit {
    pub data: RVec<u8>,     // ONE contiguous Annex B access unit (owned)
    pub keyframe: bool,
    pub timestamp_ns: u64,  // CLOCK_MONOTONIC ns of the CAPTURE that produced this
                            // access unit. An in-process encoder echoes the input
                            // frame's timestamp; a PIPELINED encoder keeps a FIFO
                            // of submitted timestamps and attaches the head one to
                            // each completed AU. Without this the pipeline stamps a
                            // pipelined encoder's output with the wrong frame's
                            // capture time — by the pipe depth — which is exactly
                            // the TD-25 timestamp error, reintroduced on the SW path.
}

// Subsampling is the chroma format of a YuvFrame. It is the discriminator the
// encoder reads to size the U/V planes — without it, a 4:2:2 frame handed to an
// encoder expecting 4:2:0 reads half its chroma as garbage.
#[repr(u8)]
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Subsampling {
    I420 = 0, // 4:2:0 — chroma is half-width, half-height (the default)
    I422 = 1, // 4:2:2 — chroma is half-width, full-height
    I444 = 2, // 4:4:4 — chroma is full-width, full-height
}

// YuvFrame holds planar YUV — the universal SW encoder input format. Strides are
// explicit because libyuv's scalers write padded rows; `width * n` is the valid
// byte count per row, exactly as on capture::Frame.
//
// Across the add-on ABI the planes cross as HOST-OWNED `RSlice`s — never owned
// buffers — carrying their strides with them, and an add-on MUST NOT retain them
// past the `encode` call (MODULE_ABI "Rich types across the boundary").
pub struct YuvFrame {
    pub y: Vec<u8>,
    pub u: Vec<u8>,
    pub v: Vec<u8>,
    pub y_stride:  u32,     // >= width
    pub uv_stride: u32,     // >= chroma_width (table below)
    pub width:  u32,
    pub height: u32,
    pub subsampling: Subsampling,
    pub matrix: ColorMatrix, // the matrix the planes were produced with; the
                             // encoder writes it into the VUI (see "Colour signalling")
    pub timestamp_ns: u64,   // CLOCK_MONOTONIC ns, carried through from capture::Frame
}

//   | subsampling | chroma_width      | chroma_height      | y bytes          | u/v bytes each                |
//   |-------------|-------------------|--------------------|------------------|-------------------------------|
//   | I420        | ceil(width / 2)   | ceil(height / 2)   | y_stride*height  | uv_stride * ceil(height/2)    |
//   | I422        | ceil(width / 2)   | height             | y_stride*height  | uv_stride * height            |
//   | I444        | width             | height             | y_stride*height  | uv_stride * height            |

// EncoderConfig holds the encoder's INITIAL configuration. Once running,
// dynamic parameters (width, height, fps, bitrate, qp, HDR) flow through
// the stream::Params contract and the ConfigurableEncoder trait (see
// MODULE_STREAM_PARAMS.md).
//
// Per-add-on STATIC tuning (preset, threads, profile) comes from the
// [addon_module_<id>] TOML section. This struct holds only the initial
// dynamic values needed for first-frame encoding.
pub struct EncoderConfig {
    pub initial_params: stream::Params,  // initial width/height/fps/bitrate_bps/qp/HDR/etc.
}

// ConfigurableEncoder lets the pipeline change stream parameters at runtime
// without restarting the encoder. Add-ons that don't implement this
// trait are torn down + recreated whenever parameters change.
pub trait ConfigurableEncoder: Encoder {
    /// update_stream_params applies new parameters to the running encoder.
    /// Called ONLY on the frame-loop thread, serialized with encode via the
    /// pipeline's param-change channel (MODULE_PIPELINE) — so `&mut self` plus
    /// single-thread drive means no locking between encode and
    /// update_stream_params. Returns StreamError::RequiresRestart if the change
    /// cannot be applied mid-stream (caller tears down and recreates the
    /// encoder).
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), StreamError>;
    fn params_capability(&self) -> Result<StreamParamsCapability, StreamError>;
}

// EncoderHandle is what the HOST holds for a software encoder: exactly ONE
// owning value per add-on instance, wrapping one `abi::EncoderBox`. The optional
// capability is reached through a BORROW, never a second Box — a `#[sabi_trait]`
// object is one flat vtable and cannot be downcast, and two owning Boxes over one
// object means dropping either frees the other.
pub trait EncoderHandle: Encoder {
    /// The AUTHORITATIVE capability set: read from the CONSTRUCTED object and
    /// masked to the probe's claim; never widens after construction, and it is
    /// the SOLE input to `as_configurable()` (MODULE_ABI "Optional-method
    /// capability flags").
    fn caps(&self) -> abi::AddonCaps;

    /// Clears one bit for the rest of the session: `as_configurable()` returns
    /// `None` from here on. Idempotent. Used when the add-on lies about
    /// `ENC_CONFIGURABLE` (`StreamError::Unsupported` from
    /// `update_stream_params`), after which every parameter change is a
    /// teardown + rebuild. It NEVER drops the encoder object.
    fn clear_cap(&mut self, bit: u32);

    /// `Some` iff `caps().has(abi::AddonCaps::ENC_CONFIGURABLE)`. The borrow
    /// ends at the end of the statement, so it can never outlive or alias the
    /// owning handle.
    fn as_configurable(&mut self) -> Option<&mut dyn ConfigurableEncoder>;
}

// ColorMatrix selects the RGB->YUV coefficients AND the quantisation range. It
// is a PARAMETER, not a constant: the value here is the value the encoder writes
// into the SPS VUI (see "Colour signalling") and the value the server advertises
// as `config.color_space`, so all three can never drift apart.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum ColorMatrix {
    Bt709Limited,  // SDR default. primaries 1, transfer 1, matrix 1, full_range 0
    Bt2020Limited, // HDR.        primaries 9, transfer 16 (PQ), matrix 9, full_range 0
    Bt601Limited,  // legacy; never selected by v1 policy. Kept so the enum can
                   // describe a bitstream produced elsewhere (e.g. a test vector).
}

// Converter turns a capture::Frame into the YuvFrame the SW encoders consume.
// It owns THREE steps, in this order, and nothing else in the pipeline does any
// of them:
//   1. rotate  — libyuv `ARGBRotate` when `f.rotation != Rotation::R0`.
//                Rotation happens in the packed-RGB domain because rotating
//                subsampled chroma by 90 degrees is not closed over 4:2:2.
//   2. convert — libyuv `ARGBToI420Matrix` / `ARGBToI422Matrix` /
//                `ARGBToI444Matrix`, with the `ArgbConstants` selected from
//                (pixel_fmt, matrix) by the table below.
//   3. scale   — libyuv `I420Scale` / `I422Scale` / `I444Scale` with
//                `kFilterBilinear`, from the rotated native geometry to the
//                configured output geometry. This is the mechanism the software
//                scaling invariant previously asserted without having.
//
// Not a trait — a single implementation in the featherdesk-encode crate
// (convert module). HW encoders bypass it entirely (they consume GPU surfaces).
//
// libyuv naming convention: libyuv names formats by 32-bit register value
// (big-endian), NOT memory byte order. BGRA-in-memory is libyuv "ARGB";
// RGBA-in-memory is libyuv "ABGR". That is why the byte order is carried by the
// CONSTANTS (kArgb* vs kAbgr*) rather than by the function name — all three
// entry points above take an ARGB-typed pointer.
//
//   | Frame.pixel_fmt | ColorMatrix    | ArgbConstants           |
//   |-----------------|----------------|-------------------------|
//   | Bgra (mac/Win)  | Bt709Limited   | kArgbH709Constants      |
//   | Bgra            | Bt2020Limited  | kArgbU2020Constants     |
//   | Bgra            | Bt601Limited   | kArgbI601Constants      |
//   | Rgba (Linux GL) | Bt709Limited   | kAbgrH709Constants      |
//   | Rgba            | Bt2020Limited  | kAbgrU2020Constants     |
//   | Rgba            | Bt601Limited   | kAbgrI601Constants      |
//
// `ARGBToI420` / `ABGRToI420` are BT.601-limited-only and MUST NOT be used on
// the v1 path — calling them is exactly the defect this replaces.
pub struct Converter { /* … */ }

impl Converter {
    /// Builds a Converter for one output geometry, subsampling and matrix.
    /// Allocates the rotate scratch buffer and the output planes once.
    pub fn new(out_w: u32, out_h: u32, sub: Subsampling, matrix: ColorMatrix) -> Self { /* … */ }

    /// Retargets an existing Converter; reallocates only when a plane size
    /// actually changes. `reconfigure_or_rebuild` calls this on EVERY Params
    /// change, alongside the capturer and the encoder, so the Converter's output
    /// geometry can never drift from `Pipeline.params`.
    pub fn reconfigure(&mut self, out_w: u32, out_h: u32, sub: Subsampling, matrix: ColorMatrix) { /* … */ }

    /// Runs rotate -> convert -> scale. The returned YuvFrame is reused on the
    /// next call (zero-alloc steady state) — hence the borrow tied to `&mut self`.
    /// Its width/height are ALWAYS `output_dims()`, whatever `f`'s geometry is.
    pub fn convert(&mut self, f: &capture::Frame) -> &YuvFrame { /* … */ }

    /// The geometry the next `convert` will emit. Exists so the frame loop can
    /// `debug_assert_eq!` it against `self.params.{width,height}` — before the
    /// `convert` call, because `convert` returns a borrow of the converter.
    pub fn output_dims(&self) -> (u32, u32) { /* … */ }
}
// Drop on Converter releases its scratch buffers (RAII — no Close()).
```

### Bitstream Output Contract

`encode()` returns an `EncodedUnit` whose `data` is a **contiguous Annex B
bitstream** (start codes `00 00 00 01` retained) plus a `keyframe: bool`. A
keyframe access unit contains SPS + PPS + IDR (H.264) or VPS + SPS + PPS + IDR
(HEVC, e.g. VideoToolbox SW on macOS 12+) in order. The output is NOT split
per-NAL -- this avoids the decompose/recompose copy overhead. A pipelined
out-of-process encoder detects the AU boundary with the Access Unit Delimiter
(NAL type 9) its child is configured to emit — never by a start code alone,
which cannot distinguish a NAL boundary from an AU boundary. The server
prepends the 22-byte `FrameHeader` and queues the bitstream as one whole access
unit (the datagram pump fragments it later). `data` ownership transfers (owned
`RVec<u8>`) -- steady-state encoding is zero-alloc after warmup (the add-on
reuses internal scratch buffers).

### Profile

Each add-on reports the profile it **actually configured**, as a
`featherdesk_abi::VideoProfile`, through `abi::Encoder::profile()` (Layer 1,
re-read after `construct()` and after every successful `update_stream_params`);
the Layer-2 adapter derives `codec()`'s string from it with
`codec_string(self.profile(), width, height, fps)` at the active geometry.
`codec()` alone could not be derived from `CodecId`: one `CodecId::H264` maps to
five distinct `avc1.` prefixes. An
add-on may not report a profile whose tools it exceeds — reporting Constrained
Baseline while emitting CABAC is what makes a decoder fall out of its hardware
path.

| Add-on | 4:2:0 profile | 4:2:2 | 4:4:4 | Why |
|--------|---------------|-------|-------|-----|
| `openh264` | `H264ConstrainedBaseline` | — | — | the OpenH264 **encoder** is Constrained-Baseline-only (its decoder is not); it emits CAVLC, no B-frames |
| `x264` | `H264High` | `H264High422` | `H264High444` | the bridge passes `-profile:v high\|high422\|high444` explicitly; `-preset ultrafast` disables CABAC, which High permits |
| `vt_sw` | `H264High` | — | — | `kVTProfileLevel_H264_High_AutoLevel`; the level in the string is still ours, not VT's |
| `nvenc` / `amf` / `qsv` / `mf_hw` / `vt_hw` | `H264High` | `H264High422` where advertised | `H264High444` where advertised | all default to High with CABAC |
| any HW encoder, HDR session | `HevcMain10` | — | — | see MODULE_STREAM_PARAMS "HDR Pipeline" |

The `[addon_module_openh264] profile` key is **removed**: it offered
`"baseline" | "main" | "high"` for an encoder that can only emit Constrained
Baseline. `[addon_module_x264] profile` keeps `"high" | "high422" | "high444"`
and defaults to `"high"`; `"baseline"` and `"main"` are removed because the
chroma negotiation, not the operator, picks the profile.

---

## Encoder Selection (Pipeline Owns This)

The Encode module does **not** decide which encoder to use. That dispatch lives
in [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md), which:

1. Reads `[encode]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes each loaded HW encoder add-on, in the **per-OS** order defined
   authoritatively in MODULE_PIPELINE step 3e (`libva` is Linux-only; `qsv`/`mf_hw`
   are Windows-only; `vt_hw` is macOS-only, so no single global order is meaningful)
3. Falls through to loaded SW encoder add-ons, in the auto order below
4. Calls the chosen add-on's constructor with `EncoderConfig`
5. Passes the resulting `Encoder` to the frame loop

There is **no `EncoderBackend` enum** in this module. Selection is purely
runtime — the loaded set of add-ons determines what's available, and
the TOML config decides how to choose among them.

### Software encoder order

Auto order: **`openh264` → `vt_sw` (macOS only)**. `x264` is **not** in it.

- **`openh264` is the default.** It is in-process, BSD-licensed with Cisco
  carrying the MPEG-LA royalty, has no external binary to find or lose, and — the
  property that decides it — can force an IDR in place, for the cost of one flag
  on the next frame.
- **`vt_sw` precedes `x264` on macOS** because it is Apple's own encoder, tuned
  for the silicon it runs on, and needs no third-party library.
- **`x264` is an explicit opt-in**, selected only by
  `[encode] force_addon = "x264"`. It is roughly 2x faster than OpenH264 on a
  CPU-bound host and is the right choice for a deployment that has *measured*
  that. It is not the right default, because **x264 here cannot force an IDR
  cheaply**: the add-on is a persistent `ffmpeg` child read over a pipe, and its
  only mechanism for an on-demand keyframe is **killing and respawning that
  child**. Every client join, every gap recovery, every resolution change and
  every param change therefore costs a process restart, a discontinuity in the
  Annex B stream, dropped frames across the gap, and a restart that is
  indistinguishable from a crash to the three death detectors in "Add-On Crash
  Recovery".

A `force_addon` SW selection overrides the auto order entirely and is also the
target `degrade_to_software` uses. Startup step 3f's "HW encoder X has no SW
fallback add-on loaded" check is satisfied by any loaded SW add-on, forced or not.

---

## Color Space Conversion

Every SW encoder accepts planar YUV. Capture add-ons produce BGRA (CPU readback
path) or GPU surfaces (zero-copy HW path). For the SW path the Converter
converts, using the matrix-taking libyuv entry points:

```
BGRA &[u8] -> ARGBToI420Matrix(kArgbH709Constants)  -> Y/U/V   (macOS, Windows)
RGBA &[u8] -> ARGBToI420Matrix(kAbgrH709Constants)  -> Y/U/V   (Linux GL)
```

- Links: `-lyuv`
- SIMD-optimized (SSE2/AVX2/NEON depending on platform)
- Pre-allocated output buffers (zero per-frame allocation)
- **Colour matrix: BT.709, limited range** (`Y` in 16..235, `Cb`/`Cr` in
  16..240) — the SDR value for the whole project, and the value written into the
  VUI (see "Colour signalling"). `ARGBToI420`/`ABGRToI420` are the BT.601
  entry points and are never called on this path.
- 4:2:2 and 4:4:4 use `ARGBToI422Matrix` / `ARGBToI444Matrix` with the same
  constants.

The Converter is **shared across all SW encoder add-ons** — it lives in the
`featherdesk-encode` crate (convert module) and is compiled whenever a SW encoder
add-on is loaded.

### Colour signalling (mandatory, both paths)

The bitstream MUST carry the colour description; a stream tagged only by
`config.color_space` is a stream the decoder guesses at. Every encoder add-on
writes these fields into the SPS `vui_parameters` (H.264) or the SPS VUI (HEVC)
of every keyframe access unit:

| VUI field | SDR value | HDR value |
|-----------|-----------|-----------|
| `video_signal_type_present_flag` | 1 | 1 |
| `video_format` | 5 (Unspecified) | 5 (Unspecified) |
| `video_full_range_flag` | 0 (limited / studio range) | 0 |
| `colour_description_present_flag` | 1 | 1 |
| `colour_primaries` | 1 (BT.709) | 9 (BT.2020) |
| `transfer_characteristics` | 1 (BT.709) | 16 (SMPTE ST 2084, PQ) |
| `matrix_coefficients` | 1 (BT.709) | 9 (BT.2020 non-constant luminance) |

`config.color_space` is a **restatement** of `matrix_coefficients`, not an
independent value: `"bt709"` ⇔ 1, `"bt2020"` ⇔ 9. v1 is always limited range, on
both paths, in both modes, so there is no range field on the wire — the VUI is
authoritative and the client never needs to be told.

### Chroma subsampling (4:2:0 / 4:2:2 / 4:4:4)

`Subsampling::I420` is 4:2:0 — the default. For `Params.chroma_subsampling =
"422"|"444"` the path generalizes: the Converter emits **I422** (`*ToI422Matrix`)
or **I444** (`*ToI444Matrix`) instead. The Converter is configured for one
`Subsampling` at a time (`Converter::new` / `reconfigure`) and stamps it on every
frame it emits. An encoder MUST check `frame.subsampling` against what it
configured and return `StreamError::ChromaUnsupported` on a mismatch — defence in
depth; the pipeline never sends a mismatch, because the same
`Params.chroma_subsampling` configures both. The SW encoder must accept the
matching format:

- **OpenH264** is **4:2:0-only** → it returns `StreamError::ChromaUnsupported` for
  422/444; the pipeline falls back to 4:2:0 (see MODULE_STREAM_PARAMS).
- **x264** supports 4:2:0 / 4:2:2 / 4:4:4 (`-pix_fmt yuv420p|yuv422p|yuv444p` with
  the matching `-profile:v high|high422|high444`; see "Profile").
- HW encoders subsample **inside the encoder** from the GPU surface (see
  MODULE_HARDWARE_ENCODE), so the converter is not involved on that path.

Each SW encoder add-on **advertises its supported chroma** in its probe
capabilities; the pipeline never asks an encoder for a chroma it can't produce.

---

## Per-Add-On Implementation Pointers

Each SW encoder add-on owns its own spec. The Encode module spec is the
interface contract above; the implementation details, performance numbers,
licensing, add-on IDs, and FFI / subprocess details all live in the add-on
specs.

| Add-on | Add-on ID | License | Linux | macOS | Windows |
|--------|-----------|---------|-------|-------|---------|
| OpenH264 (FFI) | `openh264` | BSD-2 (Cisco) | [`specs/addons/linux/encoders/SW/OPENH264_LINUX_SPEC.md`](../addons/linux/encoders/SW/OPENH264_LINUX_SPEC.md) | [`specs/addons/macos/encoders/SW/OPENH264_MACOS_SPEC.md`](../addons/macos/encoders/SW/OPENH264_MACOS_SPEC.md) | [`specs/addons/windows/encoders/SW/OPENH264_WINDOWS_SPEC.md`](../addons/windows/encoders/SW/OPENH264_WINDOWS_SPEC.md) |
| x264 subprocess (opt-in) | `x264` | GPL-2 (isolated) | [`specs/addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md`](../addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md) | [`specs/addons/macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md`](../addons/macos/encoders/SW/X264_SUBPROCESS_MACOS_SPEC.md) | [`specs/addons/windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](../addons/windows/encoders/SW/X264_SUBPROCESS_WINDOWS_SPEC.md) |
| VideoToolbox SW | `vt_sw` | Apple system | — | [`specs/addons/macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](../addons/macos/encoders/SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) | — |

---

## What This Module Does NOT Do

To avoid leaking implementation details into the interface contract, the
Encode module deliberately excludes:

- **No backend enum.** Selection is by loaded add-ons + runtime config.
- **No subprocess management.** The x264 add-on owns its ffmpeg subprocess
  lifecycle internally; the pipeline sees only the `Encoder` interface.
- **No codec parameters beyond `EncoderConfig`.** Per-add-on tuning (x264
  preset, OpenH264 slice count, VT realtime flag) lives in
  `[addon_module_<id>]` TOML sections.
- **No server-side NAL parsing.** Encoders return one Annex B access unit **plus
  a `keyframe bool`** (the encoder knows when it produced an IDR/IRAP). The
  server trusts that flag for IDR caching + the bootstrap stream and never
  re-scans NALs. (The client independently inspects the first VCL NAL only to
  set the WebCodecs key/delta hint — see MODULE_PROTOCOL "Video Payload Framing".)
- **No rate control switching.** Each add-on implements its own RC mode
  selection from `EncoderConfig.initial_params.bitrate_bps` (0 = QP mode)
  and its own TOML section.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `encode()` returns ONE contiguous Annex B access unit per call — never the rejected per-NAL `Vec<Vec<u8>>` shape | No |
| Unit | `keyframe` flag is set by the encoder itself; a keyframe access unit contains SPS+PPS+IDR (H.264) or VPS+SPS+PPS+IDR (HEVC) in order, never a bare IDR | No |
| Unit | AU boundary: a synthetic Annex B pipe delivering SPS, PPS and a large IDR across three reads yields ONE EncodedUnit with `keyframe = true`, emitted only after the next AUD | No |
| Unit | `force_keyframe()` is called only between `encode()` calls, never concurrently with one; the very next `encode()` after it returns a keyframe access unit | No |
| Unit | `codec()` re-parsed for the whole {720p,1080p,1440p,2160p} x {30,60} matrix yields a `level_idc` at least as large as the Annex A minimum for that geometry, and exactly the string in MODULE_ABI's reference table | No |
| Unit | `codec()` on the SW path drives `EncodedFrame.codec_type`: a `vt_sw` instance configured for HEVC Main10 produces `codec_type == VIDEO_HEVC` and an `hvc1.*` string, never `VIDEO_H264` | No |
| Unit | Colour conversion correctness: a known BGRA test pattern through `ARGBToI420Matrix(kArgbH709Constants)` and the same pattern as RGBA through `kAbgrH709Constants` produce identical Y/U/V planes (verifies the libyuv byte-order convention isn't inverted), and the Y values land in 16..235 (verifies limited range, not full) | No |
| Unit | Every keyframe access unit's SPS carries `colour_primaries=1, transfer_characteristics=1, matrix_coefficients=1, video_full_range_flag=0` for SDR and `9/16/9/0` for HDR — parsed out of the bitstream, per SW add-on. A stream with no VUI fails | No |
| Unit | Retarget: a Converter built for 1920x1080 and `reconfigure`d to 1280x720 emits 1280x720 planes on the very next `convert`, whatever the input frame's geometry, and reallocates exactly once | No |
| Unit | Chroma capability gate: requesting 4:2:2/4:4:4 from OpenH264 returns `StreamError::ChromaUnsupported`; requesting the same from x264 succeeds | No |
| Unit | `update_stream_params` returns `StreamError::RequiresRestart` for a change a given add-on can't apply live, and the pipeline tears down + recreates on that signal | No |
| Integration | Pipeline SW auto order when no hardware encoder is loaded: `openh264 > vt_sw (macOS only)`; `x264` is never auto-selected and is reached only by `force_addon` | Yes (per add-on) |
| Integration | `Converter` reuse: steady-state `convert()` calls after warmup perform zero additional allocation | No |
| Integration | Per-add-on encode of a 5s synthetic YUV sequence produces a decodable Annex B stream (round-trip through a reference decoder) for OpenH264, x264, and VT SW | Yes (per add-on/OS) |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| `Converter::convert` (1080p, BGRA→I420, no scale, p50) | <3 ms |
| `Converter::convert` (1080p→720p, with scale, p50) | <4.5 ms |
| `Converter::convert` (rotated, +`ARGBRotate`, p50) | +2 ms over the unrotated figure |
| SW `encode()` at 1080p (openh264, 4 threads, p50) | <8 ms (benchmarked 7.9 ms, Ryzen 9 5900X) |
| SW `encode()` at 1080p (x264 `ultrafast`, 4 threads, p50) | <4.5 ms (benchmarked 4.3 ms, Ryzen 9 5900X; 3.3 ms at 12 threads) |
| Steady-state allocation per frame | zero after warmup (pooled output + reused planes) |
| Sustained software path at 1080p | 30 fps (see CENTRAL_SPEC "Motion-to-photon budget") |

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| OpenH264 (FFI) | ✅ Working in current code; refactor moves to `addons/encode/openh264/` under add-on ID `openh264` |
| x264 subprocess | ✅ Benchmarked via ffmpeg pipe (4.3 ms p50 @ 1080p, ultrafast, 4 threads, Ryzen 9 5900X; 3.3 ms at 12 threads); implementation pending. Opt-in only — never chosen by `[encode] mode = "auto"` |
| VideoToolbox SW | 📋 Specced; macOS native benchmarks pending |

The old Go `internal/encode/{ffmpeg,vp8,vaapi}.go` files (FFmpeg subprocess
encoder, libvpx VP8 via libavcodec, VA-API probe stub) are **rejected** and
will be removed as part of the implementation refactor. They served the
pre-pluggable architecture and are superseded by:

- FFmpeg subprocess (in-process libavcodec) → replaced by `x264` subprocess add-on
- VP8 → rejected codec (no demand, libavcodec dependency)
- VA-API probe → moved into `libva` HW encoder add-on
