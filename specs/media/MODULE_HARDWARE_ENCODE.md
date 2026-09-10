# Module Spec: Hardware Encode (Cross-Platform Interface Contract)

## Overview

The Hardware Encode module defines the **abstract zero-copy hardware encoder
interface contract** that every HW encoder add-on implements. It owns no
encoder implementation itself — concrete encoders live in their respective
add-on specs:

- `addons/encode/libva/` — Linux VA-API (add-on ID `libva`, MIT)
- `addons/encode/nvenc/` — NVENC SDK direct (add-on ID `nvenc`, NVIDIA proprietary)
- `addons/encode/amf/` — AMD AMF (add-on ID `amf` Windows, `amf_rocm` Linux, Apache 2.0)
- `addons/encode/mf_hw/` — Windows MediaFoundation (add-on ID `mf_hw`, Microsoft system)
- `addons/encode/qsv/` — Intel oneVPL / QSV (add-on ID `qsv`, MIT)
- `addons/encode/vt/` — macOS VideoToolbox HW (add-on ID `vt_hw`, Apple system)

> **Software encoders** implement a different contract in
> [`MODULE_ENCODE.md`](./MODULE_ENCODE.md). Hardware encoders consume GPU
> surface handles directly (zero-copy); software encoders consume CPU-resident
> planar YUV frames. Both produce identical `EncodedFrame` output for the pipeline.

---

## Public Interface

```rust
// crate: featherdesk-hwencode

// The opaque GPU-resident input to a hardware encoder is `capture::FbInfo`,
// which carries a `SurfaceHandle` tagged enum — the HW encoder add-on `match`es
// on the runtime SurfaceHandle variant (there is no separate `SurfaceHandle`
// alias here anymore; CENTRAL's SurfaceHandle IS the enum inside FbInfo).
//
// Platform mapping:
//   Linux:   DMA-BUF fd from KMS+EGL or NvFBC, imported via VA-API or NVENC SDK
//   macOS:   IOSurface from ScreenCaptureKit, consumed by VTCompressionSession
//   Windows: ID3D11Texture2D from DXGI Desktop Duplication, consumed by MF / NVENC / AMF / QSV
//
// This is the SAME type as capture::FbInfo, re-exported here for clarity.
pub use capture::FbInfo;

// EncodedUnit (ONE contiguous Annex B access unit + keyframe flag + the capture
// timestamp of the frame that produced it) is the encoder's direct output,
// shared by the SW and HW paths (see MODULE_ENCODE).
// The pipeline wraps it into stream::EncodedFrame, which lives in
// featherdesk-stream — shared by SW and HW paths:
//
//   pub struct EncodedFrame {
//       pub data: bytes::Bytes,// Contiguous Annex B bitstream (NOT split per-NAL), host-side
//       pub width: u16,
//       pub height: u16,
//       pub timestamp_ns: u64, // carried through from EncodedUnit.timestamp_ns
//       pub keyframe: bool,
//       pub codec_type: u8,    // frame_type::VIDEO_H264 or frame_type::VIDEO_HEVC
//   }
pub use stream::{EncodedFrame, EncodedUnit};

// HardwareEncoder is the contract every HW encoder add-on must satisfy.
// Cleanup is RAII (Drop) — no Close() (Drop releases all encoder resources:
// D3D11 / VA / MF / Metal contexts).
//
// Thread-affine, but declared `Send`. The host constructs, uses and drops this
// object on a single worker thread (the frame-loop thread — CENTRAL_SPEC
// "Concurrency Model") and it never crosses a thread boundary once built —
// `Send` is required only because the empty `FrameLoop` / `AudioLoop` struct that
// will build it is moved onto that thread by `Pipeline::start`. The object may
// hold a bound D3D11 device or VA/COM context for its whole life (MODULE_ABI
// "Thread requirements").
pub trait HardwareEncoder: Send {
    /// encode_surface CONSUMES a GPU-resident surface (moved in BY VALUE) and
    /// returns one encoded access unit (H.264 or HEVC today; AV1 once an `av1`
    /// add-on exists) as an EncodedUnit with `keyframe` set BY THE ENCODER. It
    /// fills EncodedUnit.timestamp_ns from the surface's timestamp_ns before
    /// consuming it, so the pipeline reads the capture time off the unit exactly
    /// as it does on the software path.
    ///
    /// NOTE: EncodedUnit.data is defined as a contiguous Annex B access unit
    /// for H.264/HEVC (see MODULE_ENCODE.md). AV1 has no Annex B framing --
    /// its `data` is a raw low-overhead OBU temporal unit instead (see
    /// CENTRAL_SPEC.md frame_type::VIDEO_AV1). An `av1` add-on's EncodedUnit
    /// carries OBUs, not start-coded NALs; the pipeline dispatches on
    /// codec_type to know which framing a given EncodedFrame holds.
    ///
    /// SURFACE OWNERSHIP (single owner — RAII resolves the prior 4-way
    /// ambiguity): the FbInfo is moved in and its Drop releases the resource
    /// exactly ONCE on EVERY path — success, error, AND
    /// StreamError::FallbackToSoftware. The capturer does NOT release it; the
    /// pipeline does NOT release it. After encode_surface returns the surface is
    /// already gone — this is the M-1/TD-01 win (released exactly once on every
    /// path, automatically).
    ///
    /// The input surface is at native capture resolution; the encoder scales it
    /// on-GPU to initial_params.width/height (the Config dims) so its output
    /// always matches the advertised stream dimensions (see "Scaling invariant").
    ///
    /// The surface may be rotated: FbInfo.rotation says how far clockwise it must
    /// be turned to be upright — it crosses the add-on ABI verbatim as
    /// `abi::RFbInfo.rotation` (MODULE_ABI), so an add-on reads it at Layer 1 too.
    /// The encoder applies it in the same VPP pass as the downscale, so its output
    /// is always upright at initial_params.width x height. An encoder whose VPP
    /// cannot rotate returns StreamError::FallbackToSoftware.
    ///
    /// Returns StreamError::FallbackToSoftware if the surface cannot be imported
    /// (format mismatch, GPU reset, driver constraint) — and STILL drops (and so
    /// releases) the surface. The pipeline catches this and switches to the SW
    /// path for the session.
    fn encode_surface(&mut self, surface: FbInfo) -> Result<EncodedUnit, StreamError>;

    /// force_keyframe requests that the next encoded frame be an IDR.
    ///
    /// It is called ONLY from the frame-loop thread, and only BETWEEN two
    /// `encode_surface` calls — never concurrently with one. The host owns the
    /// cross-thread signalling: server tasks set an `Arc<AtomicBool>` and the
    /// frame loop reads, clears and acts on it (MODULE_PIPELINE "Main Frame
    /// Loop", step 0c/3). An add-on therefore needs no interior mutability, no
    /// atomic and no lock for this method — `&mut self` is honest, and
    /// `encode_surface` and reconfigure can never alias (borrow-checker enforced).
    fn force_keyframe(&mut self);

    /// codec returns the WebCodecs codec string for the Config handshake. It is
    /// COMPUTED, never a constant: the add-on calls
    /// featherdesk_abi::codec_string(active_profile, p.width, p.height, p.fps)
    /// after every successful update_stream_params, so the advertised level always
    /// matches the geometry actually being encoded (see MODULE_ABI "Codec-string
    /// computation"). Boundary form: the add-on returns `CodecId` **and**
    /// `VideoProfile`; the Layer-2 adapter calls
    /// `featherdesk_abi::codec_string(profile, width, height, fps)` with the
    /// params now in force, after construction and after every successful
    /// `update_stream_params`. An HDR session reports `VideoProfile::HevcMain10`.
    fn codec(&self) -> &str;
}

// HWEncoderConfig holds the encoder's INITIAL configuration. Once running,
// dynamic parameters flow through the stream::Params contract and the
// ConfigurableHardwareEncoder trait (see MODULE_STREAM_PARAMS.md).
pub struct HWEncoderConfig {
    pub initial_params: stream::Params,  // initial width/height/fps/bitrate_bps/qp/HDR/etc.
    pub codec_hint: String,              // "h264" | "hevc" | "av1" — add-on picks the best
                                         // match (boundary form: CodecId)
}

// ConfigurableHardwareEncoder lets the pipeline change stream parameters
// at runtime. HW encoders that don't implement this are torn down + recreated.
pub trait ConfigurableHardwareEncoder: HardwareEncoder {
    /// Called only on the frame-loop thread, serialized with encode_surface via
    /// the pipeline's param-change channel — no locking vs encode_surface.
    /// Returns StreamError::RequiresRestart if the change needs a fresh session.
    fn update_stream_params(&mut self, p: stream::Params) -> Result<(), StreamError>;
    fn params_capability(&self) -> Result<StreamParamsCapability, StreamError>;
}

// HwEncoderHandle is what the HOST holds for a hardware encoder: exactly ONE
// owning value per add-on instance, wrapping one `abi::HwEncoderBox`. Same
// borrow-not-a-second-Box rule as `encode::EncoderHandle`.
pub trait HwEncoderHandle: HardwareEncoder {
    /// The AUTHORITATIVE capability set: read from the CONSTRUCTED object and
    /// masked to the probe's claim; never widens after construction, and it is
    /// the SOLE input to `as_configurable()`.
    fn caps(&self) -> abi::AddonCaps;

    /// Clears one bit for the rest of the session: `as_configurable()` returns
    /// `None` from here on. Idempotent; never drops the encoder object.
    fn clear_cap(&mut self, bit: u32);

    /// `Some` iff `caps().has(abi::AddonCaps::ENC_CONFIGURABLE)`.
    fn as_configurable(&mut self) -> Option<&mut dyn ConfigurableHardwareEncoder>;
}

// StreamError::FallbackToSoftware lives in the stream crate (not hwencode) to
// avoid dependency cycles -- capture add-ons also need to return it when
// GPU surface export fails.
// Defined at: stream::StreamError::FallbackToSoftware
// See MODULE_STREAM_PARAMS.md for all error variants.
```

### Bitstream Output Contract

Identical to the SW path (see MODULE_ENCODE):
- `EncodedFrame.data` is ONE **contiguous** Annex B access unit (start code
  `00 00 00 01` retained) — **NOT** split per-NAL. (The old "one NAL per
  `Vec<u8>`" contract is rejected.)
- Keyframe access unit contains SPS + PPS + IDR (H.264) or VPS + SPS + PPS + IDR
  (HEVC) concatenated.
- `EncodedFrame.keyframe` is set by the encoder; the server never re-scans NALs.

---

## Zero-Copy Pipeline

```
Capture add-on              Hardware encoder add-on
┌──────────────────┐        ┌────────────────────────┐
│ next_surface()   │───────>│ encode_surface(surface)│
│                  │surface │                        │
│ Returns          │(moved) │ Imports GPU resource   │
│ FbInfo           │        │ scale + encode on-GPU  │
│ (DMA-BUF /       │        │ returns 1 access unit  │
│  IOSurface /     │        │ surface dropped: freed │
│  D3D11 texture)  │        │ once  ~30KB compressed │
└──────────────────┘        └────────────────────────┘
                                       │
                                       v
                            ┌────────────────────────┐
                            │ EncodedFrame to server │
                            │ (only the compressed   │
                            │  bitstream crosses     │
                            │  GPU→CPU boundary)     │
                            └────────────────────────┘
```

Bandwidth across GPU→CPU boundary: ~30 KB compressed bitstream per frame
(vs ~8 MB uncompressed BGRA in the CPU readback path).

### Scaling invariant

Capture add-ons always produce a surface at the display's **native** resolution.
When the stream runs at a lower resolution (adaptive downscale, or a fixed
`[stream]` width/height), the HW encoder scales the surface **in-encoder** (VPP /
MFT scaler / VideoToolbox scaling) to `initial_params.width × height`. The SW path
achieves the same in the `Converter`, which is retargeted by
`reconfigure_or_rebuild` on every Params change (see MODULE_ENCODE,
MODULE_PIPELINE). The invariant the rest of the system relies on:

> **encoder-output dims == `config` dims == input-coordinate range.**

So the client's input scaling (which maps pointer coordinates into the advertised
`config` width/height) is always correct regardless of native capture size. The
**cursor overlay shares that space**: `CursorUpdate.X/Y` are in the same
coordinates as absolute pointer input, converted from capture pixels by the
pipeline — see [`MODULE_CAPTURE.md`](./MODULE_CAPTURE.md) "Cursor coordinate
space". Nothing in the hardware encode path touches the cursor — but the pointer
may already be *in* the surface it is handed. The zero-copy path runs under BOTH
resolved cursor modes: under `"separate"` the surface is pointer-free and the
client draws the overlay (requires `AddonCaps::CURSOR`), and under `"embedded"`
the capture add-on composites the pointer onto the surface before delivery
(requires `AddonCaps::EMBED_CURSOR_SURF` — `sck` `showsCursor`, `nvfbc`
`bWithCursor`). `"separate"` is not a precondition of this path: the macOS
zero-copy pairing is `sck` + `vt_hw`, which always resolves `"embedded"`. Either
way the HW encoder neither reads nor draws a cursor.

### Colour

The HW encoder converts RGB to YUV inside the encoder. It MUST use **BT.709
limited range** for an SDR session and **BT.2020 non-constant-luminance limited
range with the PQ transfer** for an HDR session, and MUST write the matching VUI
(see MODULE_ENCODE "Colour signalling"). Per-vendor: NVENC
`NV_ENC_CONFIG_H264_VUI_PARAMETERS`; AMF `AMF_VIDEO_ENCODER_*_COLOR_PROFILE` +
`_TRANSFER_CHARACTERISTIC` + `_COLOR_PRIMARIES`; QSV/oneVPL
`mfxExtVideoSignalInfo`; MF `MF_MT_VIDEO_PRIMARIES` / `_TRANSFER_FUNCTION` /
`_YUV_MATRIX` / `_NOMINAL_RANGE`; VideoToolbox
`kVTCompressionPropertyKey_ColorPrimaries` / `_TransferFunction` /
`_YCbCrMatrix`. An encoder that cannot be made to emit BT.709 returns
`StreamError::Backend` at construction rather than shipping a mislabelled stream.

### Chroma subsampling

HW encoders subsample **inside the encoder** — the input GPU surface is RGB/4:4:4,
and the encoder produces `Params.chroma_subsampling` (4:2:0 default, or 4:2:2/4:4:4
where the silicon supports it: NVENC 4:4:4 on Turing+, others vendor-dependent).
No CPU converter is involved. The add-on **advertises its supported chroma** in its
probe capabilities and returns `StreamError::ChromaUnsupported` if asked for one it
can't emit, so the pipeline falls back to 4:2:0 (see MODULE_STREAM_PARAMS). The
client-side decode gate (`isConfigSupported` → `decode_unsupported`) is identical
to the SW path.

---

## Selection (Pipeline Owns This)

The Hardware Encode module does **not** decide which HW encoder to use.
That dispatch lives in [`MODULE_PIPELINE.md`](../core/MODULE_PIPELINE.md), which:

1. Reads `[encode]` config (mode = "auto" | "forced", force_addon if forced)
2. Probes loaded HW add-ons in the order for THIS OS — an add-on for another OS
   cannot be loaded, so a single global order would list entries that can never
   fire:
   - Linux: `nvenc` → `amf_rocm` → `libva`
   - Windows: `nvenc` → `amf` → `qsv` → `mf_hw`
   - macOS: `vt_hw`

   Vendor-specific SDKs precede generic abstractions (`nvenc`/`amf` before
   `libva` on Linux; `nvenc`/`amf`/`qsv` before `mf_hw` on Windows)
3. Verifies the capture add-on's `SurfaceHandle` variant is acceptable to the
   chosen HW encoder
4. On `StreamError::FallbackToSoftware`, switches to a SW encoder add-on for the
   remainder of the session

There is **no `HWCodec` enum** in this module. The add-on advertises its active
codec via `codec()` — H.264 (`avc1.*`) by default, HEVC (`hvc1.*`) only when HDR
is requested (HDR needs HEVC Main10 — see [`../core/MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)
"HDR Pipeline"). The server forwards that exact string to the client in `config`,
having already gated the choice on the decode capability every attached client
reported at auth (see [`../core/MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md)
"Decode capability").

---

## Per-Add-On Implementation Pointers

Each HW encoder add-on owns its own spec. The Hardware Encode module spec is
the interface contract above; implementation details, performance numbers,
licensing, add-on IDs, and Rust FFI specifics all live in the add-on specs.

| Add-on | Add-on ID | Linux | macOS | Windows |
|--------|-----------|-------|-------|---------|
| libva (VA-API) | `libva` | [`specs/addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md`](../addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md) | — | — |
| NVENC | `nvenc` | [`specs/addons/linux/encoders/HW/NVENC_LINUX_SPEC.md`](../addons/linux/encoders/HW/NVENC_LINUX_SPEC.md) | — | [`specs/addons/windows/encoders/HW/NVENC_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/NVENC_WINDOWS_SPEC.md) |
| AMF | `amf_rocm` (Linux) / `amf` (Windows) | [`specs/addons/linux/encoders/HW/AMF_ROCM_SPEC.md`](../addons/linux/encoders/HW/AMF_ROCM_SPEC.md) | — | [`specs/addons/windows/encoders/HW/AMF_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/AMF_WINDOWS_SPEC.md) |
| MediaFoundation HW | `mf_hw` | — | — | [`specs/addons/windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md) |
| QSV (oneVPL) | `qsv` | — | — | [`specs/addons/windows/encoders/HW/QSV_WINDOWS_SPEC.md`](../addons/windows/encoders/HW/QSV_WINDOWS_SPEC.md) |
| VideoToolbox HW | `vt_hw` | — | [`specs/addons/macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](../addons/macos/encoders/HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md) | — |

---

## What This Module Does NOT Do

- **No platform-specific code in the interface.** No DMA-BUF imports, no
  VA-API context creation, no MFT setup. Those all live in add-on specs.
- **No subprocess management.** Hardware encoders are always in-process via
  Rust FFI. (Subprocess-isolated GPL is only relevant for the opt-in SW x264.)
- **No codec selection algorithm.** Each add-on advertises its active codec via
  `codec()` (H.264 by default; HEVC only for HDR — see MODULE_STREAM_PARAMS); the
  server forwards it to the client in `config`, having gated it on the clients'
  reported decode capability (MODULE_PROTOCOL "Decode capability").
- **No keyframe interval logic.** Periodic IDRs are not configured — every
  IDR is on-demand via `force_keyframe()`, triggered by a client's gap detection
  from any authenticated role and rate-limited by the server (MODULE_SERVER
  "Keyframe-Request Rate Limiting").

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `encode_surface`'s `FbInfo` argument is dropped (and its resource released) exactly once across all three outcomes: success, error, and `StreamError::FallbackToSoftware` — the M-1/TD-01 invariant | No |
| Unit | `codec()` re-parsed for the whole {720p,1080p,1440p,2160p} x {30,60} matrix yields a `level_idc` at least as large as the Annex A minimum for that geometry, and exactly the string in MODULE_ABI's reference table | No |
| Unit | Scaling invariant: a native-resolution input surface always produces output at exactly `initial_params.width × height`, regardless of native capture size | Yes (per vendor) |
| Unit | A surface reporting `rotation = R90` is emitted upright at exactly `initial_params.width × height`; an encoder whose VPP cannot rotate returns `StreamError::FallbackToSoftware` on that surface instead of shipping a sideways picture | Yes (per vendor) |
| Integration | Every keyframe access unit's SPS carries `colour_primaries=1, transfer_characteristics=1, matrix_coefficients=1, video_full_range_flag=0` for SDR and `9/16/9/0` for HDR — parsed out of the bitstream. A stream with no VUI fails | Yes (per vendor) |
| Integration | `StreamError::FallbackToSoftware` degrades the pipeline to a SW encoder add-on for the remainder of the session — never retries the HW path mid-session | Yes (per vendor) |
| Integration | HW probe priority order: NVENC preferred over `libva` on NVIDIA, AMF preferred over `libva` on AMD (Linux only — `libva` is not a Windows path, so it never appears in the Windows order), MF HW falls back to whatever vendor MFT is registered | Yes (per vendor) |
| Integration | Chroma capability advertisement + `StreamError::ChromaUnsupported` fallback to 4:2:0 matches the SW path's client-side decode gate | Yes (per vendor, e.g. NVENC 4:4:4 on Turing+) |
| Integration | `SurfaceHandle` variant compatibility: the capture add-on's produced handle (DMA-BUF / IOSurface / D3D11Texture) is accepted by the paired HW encoder on that OS without a format-mismatch fallback | Yes (per OS pairing) |
| Benchmark | Per-vendor encode latency at 1080p matches (or is tracked against regressions from) the numbers recorded in the Implementation Status table below | Yes (per vendor) |

---

## Performance Targets

| Metric | Target |
|--------|--------|
| `encode_surface()` at 1080p (p50) | <6 ms — see Implementation Status for the per-vendor measured figures |
| Surface import overhead (per call) | <0.5 ms |
| GPU→CPU bandwidth per frame | ~30 KB compressed (vs ~8 MB uncompressed BGRA) |
| Contribution to motion-to-photon | 4.5 ms (encode) — see CENTRAL_SPEC "Motion-to-photon budget" |
| Steady-state allocation per frame | zero after warmup |

---

## Implementation Status

| Add-on | Status |
|--------|--------|
| NVENC (Windows) | ✅ Benchmarked: GTX 1080 Ti 4.5ms @ 1080p, Quadro RTX 4000 5.4ms |
| AMF (Windows) | ✅ Benchmarked: RX 6800 XT 5.9ms H.264 / 5.0ms HEVC @ 1080p |
| MF HW (Windows) | ✅ Benchmarked: 6.9-7.7ms @ 1080p depending on GPU |
| libva (Linux) | 📋 Specced; Rust FFI bindings pending |
| NVENC (Linux) | 📋 Specced; Rust FFI bindings pending |
| AMF (Linux ROCm) | 📋 Specced; Rust FFI bindings pending |
| QSV (Windows) | 📋 Specced (on good-faith; no Intel hardware to benchmark) |
| VideoToolbox HW (macOS) | 📋 Specced; Hackintosh measured 8.6ms @ 1080p (AMD VCE) |
