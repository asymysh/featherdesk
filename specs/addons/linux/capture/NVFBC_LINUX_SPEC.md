# Linux Add-On: NVIDIA NvFBC Capture

## Purpose

Optional add-on capture backend for **NVIDIA proprietary driver users**. Uses NvFBC
(NVIDIA Frame Buffer Capture) — NVIDIA's official direct-from-GPU framebuffer capture
API — to bypass the display compositor and reduce capture latency to the absolute
minimum NVIDIA hardware can produce.

**Why this matters vs KMS+EGL on NVIDIA:**

| Method | Latency | Notes |
|--------|---------|-------|
| KMS+EGL on NVIDIA proprietary | ~3–5ms | Requires `nvidia-drm.modeset=1`, sometimes finicky on multi-monitor |
| **NvFBC** | **~1–2ms** | Reads GPU framebuffer directly, no compositor mediation |

NvFBC is the same capture path NVIDIA's own GeForce Experience / GameStream used.
It's what Sunshine prefers on NVIDIA hardware. The ~2–3ms reduction matters for
60fps+ streaming.

---

## License & Distribution Constraint

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Video Codec SDK (NvFBC headers) | NVIDIA Software License (free use) | Cannot redistribute headers as standalone package |
| `libnvidia-fbc.so` (driver-shipped) | proprietary NVIDIA | Ships with NVIDIA proprietary driver |
| `NvFBCUnlock` patcher (consumer cards) | community / various | Required on GeForce; **not** required on Quadro/Tesla |
| Our Rust FFI binding | MIT | We own this code |

**No royalties.** No GPL/LGPL contamination. Same SDK license model as NVENC.

---

## The Consumer Card Restriction (important)

NVIDIA artificially restricts NvFBC on consumer hardware:

| GPU class | Native NvFBC support |
|-----------|---------------------|
| GeForce (GTX, RTX) | ❌ Disabled by driver |
| Quadro / RTX Workstation | ✅ Enabled |
| Tesla / Data Center | ✅ Enabled |

**The community workaround:** `nvfbc-patcher` / `NvFBCUnlock` modifies the proprietary
driver's `libnvidia-fbc.so` to enable NvFBC on consumer cards. This is what Sunshine
documents as standard procedure for its NVIDIA users.

> **FeatherDesk does NOT ship a patcher.** We document that users on consumer cards
> need to run the patcher themselves (well-known projects on GitHub). On Quadro/Tesla
> the add-on works out of the box.

---

## Hardware Compatibility

NvFBC has been in every NVIDIA proprietary driver from ~2014 onward. Supported on:

| GPU | NvFBC available | Patcher needed? |
|-----|----------------|----------------|
| Kepler Quadro (K-series) | ✅ | No |
| Maxwell+ Quadro / RTX Workstation | ✅ | No |
| Tesla / Data Center (any) | ✅ | No |
| Kepler+ GeForce (GTX 600–RTX 5000) | ✅ via patcher | Yes |

---

## Build & Distribution

### Shared library build

```bash
cargo build --release -p featherdesk-addon-nvfbc   # cdylib → featherdesk-addon-nvfbc.so
```

The `nvfbc` add-on cdylib is built from the `addons/capture/nvfbc/` crate.

### Runtime dependencies

- NVIDIA proprietary driver 470+
- `libnvidia-fbc.so` (ships with driver)
- If GeForce: NvFBCUnlock patcher applied to driver
- Video Codec SDK headers (build-time only — checked into source tree per NVIDIA
  SDK license, same as the NVENC add-on)

### FFI configuration

The bindings are generated with `bindgen` in `build.rs`, which adds the SDK
include path, links the driver libraries, and wraps the unified header:

```rust
// build.rs
println!("cargo:rustc-link-search=native=/usr/lib/x86_64-linux-gnu");
println!("cargo:rustc-link-lib=nvidia-fbc");
println!("cargo:rustc-link-lib=cuda");
println!("cargo:rustc-link-lib=dl");

bindgen::Builder::default()
    .clang_arg("-Isdk")
    // Unified NvFBC 7.x+ header (replaces old NvFBCToSys.h / NvFBCToCuda.h)
    .header("sdk/NvFBC.h")
    .generate().unwrap();
```

---

## FFI Implementation Sketch

```c
// Session init (one-time)
NVFBC_GET_STATUS_PARAMS statusParams = { 0 };
statusParams.dwVersion = NVFBC_GET_STATUS_PARAMS_VER;
pFnList->nvFBCGetStatus(handle, &statusParams);
// statusParams.bIsCapturePossible must be true

NvFBC_CreateHandleParams createParams = { NVFBC_CREATE_HANDLE_VER };
NVFBC_SESSION_HANDLE session;
pNvFBCAPI->nvFBCCreateHandle(&session, &createParams);

// Create capture session
NvFBC_CreateCaptureSessionParams sessionParams = { NVFBC_CREATE_CAPTURE_SESSION_VER };
sessionParams.eCaptureType    = NVFBC_CAPTURE_SHARED_CUDA;
sessionParams.eTrackingType   = NVFBC_TRACKING_DEFAULT;
sessionParams.bWithCursor     = cfg.embed_cursor ? NVFBC_TRUE : NVFBC_FALSE;  // see "Cursor Handling"
sessionParams.dwSamplingRateMs = 0;             // capture on demand, not periodic
sessionParams.bDisableAutoModesetRecovery = NVFBC_FALSE;
pNvFBCAPI->nvFBCCreateCaptureSession(session, &sessionParams);

// Set output format
NvFBC_ToCudaSetUpParams cudaParams = { NVFBC_TOCUDA_SETUP_PARAMS_VER };
cudaParams.eBufferFormat = NVFBC_BUFFER_FORMAT_NV12;  // direct feed to NVENC
pNvFBCAPI->nvFBCToCudaSetUp(session, &cudaParams);

// Per-frame capture
NvFBC_FrameGrabInfo frameInfo = { 0 };
CUdeviceptr cudaPtr;
NvFBC_ToCudaGrabFrameParams grabParams = { NVFBC_TOCUDA_GRAB_FRAME_PARAMS_VER };
grabParams.dwFlags = NVFBC_TOCUDA_GRAB_FLAGS_NOFLAGS;  // blocking, full frame
grabParams.pCUDADeviceBuffer = &cudaPtr;
grabParams.pNvFBCFrameGrabInfo = &frameInfo;
pNvFBCAPI->nvFBCToCudaGrabFrame(session, &grabParams);

// cudaPtr now points to an NV12 frame in GPU memory
// Pass directly to NVENC encoder — true zero-copy
```

---

## Integration with NVENC encoder

NvFBC pairs perfectly with the NVENC add-on:

```
NvFBC (GPU framebuffer)
    → CUDA buffer (NV12, GPU memory)
    → NVENC encoder (same GPU, no CPU touch)
    → H.264 / HEVC / AV1 NAL units (small CPU readback)
```

This is the same zero-copy GPU-resident pipeline Sunshine uses for NVIDIA streaming.
Total bus bandwidth per frame: ~30KB compressed NALs only. Sub-2ms total capture +
encode latency on RTX 3060+.

If NvFBC is paired with VA-API instead, an extra GPU→GPU copy is needed to
bridge CUDA → VA-API. Still better than KMS+EGL but loses the purest zero-copy
story. For NVIDIA deployments, **ship NvFBC and NVENC together**.

---

## Performance Targets

| GPU class | 1080p capture p50 | 1440p capture p50 | CPU |
|-----------|------------------|------------------|-----|
| RTX 3060 | ~1ms | ~1.5ms | <1% |
| RTX 4090 | <1ms | ~1ms | <1% |

Compared to KMS+EGL on the same GPU (NVIDIA proprietary driver): roughly 2× faster
capture and significantly more reliable on multi-monitor setups.

---

## Capture Modes

NvFBC supports multiple output buffer types:

| Mode | Surface | Use case |
|------|---------|----------|
| `NVFBC_CAPTURE_SHARED_CUDA` | CUDA device pointer | **Preferred** — direct feed to NVENC |
| `NVFBC_CAPTURE_TO_SYS` | System memory NV12 | Fallback — CPU readback, ~5ms slower |
| `NVFBC_CAPTURE_TO_GL` | OpenGL texture | For interop with EGL/Vulkan |
| `NVFBC_CAPTURE_TO_HW_ENCODER` | Direct-to-NVENC | Internal, used by GFE |

We default to `NVFBC_CAPTURE_SHARED_CUDA` because it's the cleanest interop with the
NVENC add-on encoder.

NvFBC also reports the display's rotation; the add-on maps it to
`capture::Rotation::{R0,R90,R180,R270}` and puts it on every `Frame` and `FbInfo`.
It reports the rotation and never rotates the buffer itself — the pipeline derives
the upright stream geometry (MODULE_CAPTURE "Display rotation").

---

## Cursor Handling

NvFBC has no separate pointer readout, and it is an X11-only API — under Wayland
the probe reports `available = false`, so the Wayland question never reaches this
add-on. The cursor therefore comes from X11 itself, or from NvFBC's own compositor.

**Capabilities declared at probe.** `AddonCaps::CURSOR` when the X11 `XFIXES`
extension is present (`XFixesQueryExtension`), plus `AddonCaps::EMBED_CURSOR |
AddonCaps::EMBED_CURSOR_SURF` unconditionally — the driver can composite on every
capture type.

**`capture::CursorCapturer` (`embed_cursor = false`).** `sessionParams.bWithCursor
= NVFBC_FALSE`, and:

```rust
fn next_cursor(&mut self) -> Result<Option<capture::CursorState>, StreamError>;
```

- One call gives everything: `XFixesGetCursorImage` returns position (`x`/`y`, the
  hotspot), hotspot (`xhot`/`yhot`), size (`width`/`height`) and pixels.
- The pixel buffer is `unsigned long*`, **not** `uint32_t*` — on a 64-bit host each
  element is 8 bytes with the ARGB value in the low 32 bits. Reading it as a
  `u32` slice yields a cursor that is half transparent and twice as wide. Take the
  low 32 bits per element, then apply the ARGB-premultiplied → straight-RGBA
  conversion from MODULE_CAPTURE "Cursor pixel format".
- Change detection: `XFixesGetCursorImage`'s `cursor_serial` identifies the shape.
  A serial equal to the previous poll's returns `shape: None`; only position and
  visibility are reported.
- The bitmap is returned at the OS's native size; the 128-pixel wire cap is
  applied by the host's cursor publisher. X11 hands back whatever size the
  accessibility pointer settings produce, and this add-on does not resample it.

**First call.** MODULE_CAPTURE's `next_cursor` contract requires the first call
after construction to return `Ok(Some(..))` carrying the current position, the
current visibility **and** the current bitmap (`shape: Some(..)`), even though
nothing has changed — the host has no other way to seed a joining client. The
`cursor_serial` change detection above applies from the second call on.

**`embed_cursor = true`.** `sessionParams.bWithCursor = NVFBC_TRUE` at session
creation — the driver composites into every grabbed frame, including
`NVFBC_CAPTURE_SHARED_CUDA`, so both embed capabilities hold and the zero-copy
pairing is unaffected. This is the only cursor setting the add-on has; there is no
`[addon_module_nvfbc]` cursor key.

**Failure behavior.** No XFIXES extension → `CURSOR` is simply not declared, and
the session resolves `"embedded"` at selection rather than failing. A failed
`XFixesGetCursorImage` mid-session returns `Err(StreamError::Backend(..))` from
`next_cursor` only; the frame path is untouched.

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-nvfbc   (cfg(target_os = "linux"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

```rust
/// The root module's `probe`. A missing prerequisite is NOT an error — it is
/// `ROk(ProbeReport { available: false, reason, .. })`. `RErr` means the probe
/// itself broke.
fn probe(&self) -> RResult<ProbeReport, AbiError> {
    // 1. dlopen libnvidia-fbc.so (check NVIDIA proprietary driver presence)
    // 2. Confirm an X11 display is reachable — NvFBC is X11-only, so under
    //      Wayland this is ROk(ProbeReport { available: false,
    //        reason: "NvFBC requires X11; this session is Wayland".into(), .. })
    // 3. NvFBC_GetStatus → check bIsCapturePossible; false on a consumer card is
    //      ROk(ProbeReport { available: false,
    //        reason: "NvFBC is restricted on GeForce; apply the NvFBCUnlock
    //                 patcher, or use a Quadro/Tesla card".into(), .. })
    // 4. Enumerate display outputs into a DisplayInfo per output:
    //      id           = the NvFBC output id (the value [addon_module_nvfbc]
    //                     output_index selects)
    //      width/height = the output's size in PIXELS, as delivered (NOT upright)
    //      rotation     = NvFBC's tracking of the output rotation, mapped to
    //                     abi::Rotation; the host transposes for R90/R270
    //      refresh_mhz  = the output's refresh in milliHertz
    //      scale_num/den = 1/1 — NvFBC reports physical pixels
    // 5. XFixesQueryExtension decides the CURSOR bit (see "Cursor Handling")
    // 6. Otherwise → ROk(ProbeReport {
    //      available: true, reason: RString::new(), codecs: RVec::new(),
    //      caps: AddonCaps(AddonCaps::SURFACE            // CUDA device pointer export
    //                    | AddonCaps::CURSOR             // ONLY with XFIXES
    //                    | AddonCaps::EMBED_CURSOR
    //                    | AddonCaps::EMBED_CURSOR_SURF  // bWithCursor composites on every type
    //                    | AddonCaps::CONFIGURABLE),     // the session is re-set-up in place
    //      displays })
}
```

**Availability is not an error.** A missing driver, a denied permission or an
absent device is `ROk(ProbeReport { available: false, reason })`. `RErr` is
reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` means
no zero-copy path, no separate cursor and no hot parameter change — silently, with
no error and no warning.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); a capability that only
`construct()` can settle is reported by the constructed object's `caps()`, which
is authoritative and may be a strict subset of this one.

Pipeline probes capture in this order on Linux:
```
NvFBC available AND on NVIDIA?      → use NvFBC (this add-on)
KMS+EGL with root / CAP_SYS_ADMIN?  → use KMS+EGL
None?                                → fatal: no capture add-on configured
```

---

## File Structure

```
addons/capture/nvfbc/
├── nvfbc.rs                 // Capturer struct, NvFbcCapturer::new
├── ffi.rs                   // Rust FFI bindings (built into the add-on cdylib)
├── probe.rs                 // the root module's probe() -> ProbeReport
├── cursor.rs                // XFixesGetCursorImage: next_cursor
├── cuda_to_nvenc.rs         // Direct CUDA → NVENC handoff
├── sdk/                     // NVIDIA SDK headers (NvFBC.h etc.)
└── tests.rs                 // Integration tests
```

---

## When to use this add-on

Use this add-on when:
- Target deployment has NVIDIA GPUs running the proprietary driver
- Minimum capture latency matters (60fps+ streaming, gaming-grade)
- Paired with NVENC add-on for full zero-copy GPU-resident pipeline
- Quadro/Tesla deployment (no patcher needed) — biggest win

Stick with default KMS+EGL when:
- NVIDIA open-source driver (Nouveau / NVK) — NvFBC unavailable
- Don't want to run a driver patcher on consumer cards
- Single-monitor 30fps remote control is sufficient (latency budget already met)

---

## Status

📋 Specced — not yet built. Implementation priority: medium-high for NVIDIA-targeted
deployments. Pairs with NVENC encoder add-on for the canonical NVIDIA streaming
pipeline (same approach as Sunshine).

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_nvfbc]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.

The keys, their defaults and their domains are in MODULE_CONFIG "Schema", under
`[addon_module_nvfbc]`; this spec does not restate them.



---

## Stream Params Translation

This add-on implements `capture::ConfigurableCapturer` and sets `AddonCaps::CONFIGURABLE` at probe: the capture session is re-set-up in place (see [`specs/core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)). NvFBC captures at native resolution; the pipeline handles scaling.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Output is native -- pipeline scales. No capturer change needed. | n/a (pipeline) |
| `FPS` | Pipeline pacing. NvFBC grabs are synchronous (`NvFBCFrameGrab`). | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | NvFBC does NOT have a dedicated HDR buffer format constant. Use `NVFBC_BUFFER_FORMAT_BGRA` -- HDR metadata (if any) comes from the display driver. NvFBC HDR support is limited and Quadro/Tesla only. | requires re-init |
| `ColorSpace` | Reported per frame; pipeline annotates encoder | n/a (read-only) |