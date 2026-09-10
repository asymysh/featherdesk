# Windows HW Encoder Add-On: AMD AMF Direct

## Purpose

Direct AMD Advanced Media Framework (AMF) access on Windows. AMF is **first-class on
Windows** (much more mature than the AMF-on-ROCm Linux add-on) and is what OBS,
Sunshine, and ReLive all use for AMD streaming.

Bypasses MediaFoundation to unlock AMF-specific features:
- **Pre-Analysis (PA)** — better quality per bit (~10% smaller frames at same quality)
- **AMF Smart Access Video (SAV)** — RDNA2+ low-latency mode
- Direct rate-control tuning beyond what MF exposes
- AV1 encode on RDNA3+ (RX 7000+) with full AMF tuning

For AMD-only Windows deployments where quality and tuning matter, this is the
preferred encoder.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| AMD AMF SDK | **Apache 2.0** ✅ | Cleanest of the vendor SDK licenses |
| `libamf` runtime | proprietary AMD | Ships with AMD GPU driver (no separate install) |
| Our Rust FFI binding | MIT | We own this code |

Apache 2.0 is genuinely the friendliest license of the three vendor encoder SDKs
(NVIDIA's is a custom license, Intel's is MIT, AMD's is Apache 2.0). Headers can
be redistributed without restriction.

---

## Hardware Compatibility

| AMD GPU | H.264 | HEVC | AV1 | AMF version |
|---------|-------|------|-----|------------|
| GCN 1.0–3.0 (HD 7000–R9 200) | ✅ | ❌ | ❌ | 1.x |
| Polaris (RX 400/500) | ✅ | ✅ | ❌ | 1.4+ |
| Vega | ✅ | ✅ | ❌ | 1.4+ |
| RDNA 1 (RX 5000) | ✅ | ✅ | ❌ | 1.4+ |
| RDNA 2 (RX 6000) | ✅ | ✅ | ❌ decode only | 1.4+ |
| RDNA 3 (RX 7000) | ✅ | ✅ | ✅ | 1.4+ |
| RDNA 4 (RX 9000) | ✅ | ✅ | ✅ | 1.4+ |

`probe()` calls `AMFCreateContext` + `InitDX11` and enumerates the available codec
components, reporting them as `ProbeReport.codecs` — H.264 on every GCN+ part,
HEVC on Polaris+, AV1 on RDNA3+ — with `caps` setting
`AddonCaps::ENC_CONFIGURABLE`, because `SetProperty` on the running VCE component
changes bitrate, QP, frame rate and IDR period without a rebuild. Availability is
not an error: no AMD adapter, or an `amfrt64.dll` too old, is
`ROk(ProbeReport { available: false, reason })`, never an `RErr`. Set every bit
the add-on actually serves, and claim only what the probe can prove — a bit
claimed here and refused later is a capability lie (MODULE_ABI "Misbehaving
add-ons").

The add-on emits **H.264 High** for every SDR session and HEVC Main10 only for an
HDR one; enumerating HEVC or AV1 support says what the silicon can do, not what
the session will use (see [`../README.md`](../README.md) "Codec fallback order at
runtime").

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-amf   # cdylib  featherdesk-addon-amf.dll
```

FFI link config (in `build.rs`):

```rust
// build.rs — link the interop libs; the AMF runtime (amfrt64.dll) is loaded
// dynamically at runtime (see below), not at link time:
//   println!("cargo:rustc-link-lib=dylib=ole32");
//   println!("cargo:rustc-link-lib=dylib=oleaut32");
//   println!("cargo:rustc-link-lib=dylib=d3d11");
//   println!("cargo:rustc-link-lib=dylib=dxgi");
//
// The AMF SDK headers (Factory.h / Context.h / VideoEncoderVCE.h / HEVC / AV1)
// are vendored and bound via bindgen into an `amf-sys` module.
```

`amf.lib` is loaded dynamically via `LoadLibrary` (the AMF SDK doesn't ship a
static import library). SDK headers vendored under `addons/amf/amf/`.

---

## FFI Implementation Sketch

```rust
// Raw FFI to amfrt64.dll (an `amf-sys`-style binding). AMF is a C vtable API,
// so each call goes through the object's `vtbl`.
// 1. Load AMF DLL and get factory
let amf_module = unsafe { LoadLibraryW(w!("amfrt64.dll"))? };
type AmfInitFn = unsafe extern "C" fn(version: u64, factory: *mut *mut AMFFactory) -> AMF_RESULT;
let amf_init: AmfInitFn = unsafe { std::mem::transmute(GetProcAddress(amf_module, s!("AMFInit"))) };
let mut factory: *mut AMFFactory = std::ptr::null_mut();
unsafe { amf_init(AMF_FULL_VERSION, &mut factory); }

// 2. Create context bound to D3D11 device (shared with DXGI capture)
let mut ctx: *mut AMFContext = std::ptr::null_mut();
unsafe {
    ((*(*factory).vtbl).CreateContext)(factory, &mut ctx);
    ((*(*ctx).vtbl).InitDX11)(ctx, d3d11_device, AMF_DX11_0);
}

// 3. Create encoder
let mut encoder: *mut AMFComponent = std::ptr::null_mut();
unsafe { ((*(*factory).vtbl).CreateComponent)(factory, ctx, AMFVideoEncoderVCE_AVC, &mut encoder); }

// 4. Configure for ultra low latency
unsafe {
    let e = &*(*encoder).vtbl;
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_USAGE, AMF_VIDEO_ENCODER_USAGE_ULTRA_LOW_LATENCY);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_PROFILE, AMF_VIDEO_ENCODER_PROFILE_HIGH);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_RATE_CONTROL_METHOD, AMF_VIDEO_ENCODER_RATE_CONTROL_METHOD_CBR);
    // Colour: BT.709 limited range for SDR (BT.2020 PQ for HDR). Mandatory —
    // see MODULE_ENCODE "Colour signalling".
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_OUTPUT_COLOR_PROFILE, AMF_VIDEO_CONVERTER_COLOR_PROFILE_709);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_OUTPUT_TRANSFER_CHARACTERISTIC, AMF_COLOR_TRANSFER_CHARACTERISTIC_BT709);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_OUTPUT_COLOR_PRIMARIES, AMF_COLOR_PRIMARIES_BT709);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_TARGET_BITRATE, bitrate);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_B_PIC_PATTERN, 0);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_IDR_PERIOD, 0);
    (e.SetProperty)(encoder, AMF_VIDEO_ENCODER_PRE_ANALYSIS_ENABLE, true); // PA!
    (e.Init)(encoder, AMF_SURFACE_NV12, W, H);
}

// 5. Per frame: wrap D3D11 texture as AMFSurface, submit
let mut surface: *mut AMFSurface = std::ptr::null_mut();
unsafe {
    ((*(*ctx).vtbl).CreateSurfaceFromDX11Native)(ctx, d3d11_texture, &mut surface, std::ptr::null_mut());
    ((*(*encoder).vtbl).SubmitInput)(encoder, surface as *mut AMFData);
}

// 6. Read output
let mut output: *mut AMFData = std::ptr::null_mut();
while unsafe { ((*(*encoder).vtbl).QueryOutput)(encoder, &mut output) } == AMF_OK {
    let buf: *mut AMFBuffer = amf_buffer_from_data(output);
    // buf bytes = encoded NALs (Annex B)
}
```

### Zero-copy DXGI → AMF

`ctx->CreateSurfaceFromDX11Native` wraps an existing `ID3D11Texture2D` as an
`AMFSurface` without copying. This is the canonical Windows AMD path:
DXGI Desktop Duplication → ID3D11Texture2D → AMFSurface → encoder.

---

## Performance

### Measured (real hardware, Ryzen 9 5900X host)

| AMD GPU | Codec | 1080p ms | 1440p ms | FPS @ 1080p |
|---------|-------|---------|---------|------------|
| **RX 6800 XT (RDNA2)** | H.264 | **5.9ms** | **9.3ms** | 169 |
| **RX 6800 XT (RDNA2)** | HEVC | **5.0ms** | **7.5ms** | 199 |

**Interesting finding:** HEVC is faster than H.264 on the RX 6800 XT. AMD's
VCN3 encoder is HEVC-optimized — the H.264 path goes through a less-optimized
code path. It is not a reason to select HEVC: both figures are comfortably inside
the encode budget, and the codec policy is H.264 unless the session is HDR,
because Firefox's WebCodecs cannot decode HEVC at all.

### Estimated (no measured hardware — earlier rough projections)

| AMD GPU | 1080p p50 | 1440p p50 |
|---------|----------|----------|
| RX 6600 (RDNA2) | ~6ms | ~10ms |
| RX 7900 XTX (RDNA3) | ~3ms | ~5ms |

Pre-Analysis reduces bandwidth by ~10–20% at the same visual quality for a small
encode latency cost — often worth it for bandwidth-constrained scenarios.

---

## File Structure

```
addons/amf/               // Rust source shared by the amf (Windows) + amf_rocm (Linux) crates
├── src/
│   ├── lib.rs            // encoder impl
│   ├── windows.rs        // Windows AMF FFI (cfg(windows))
│   ├── linux_rocm.rs     // Linux ROCm variant (cfg(target_os = "linux"))
│   ├── d3d11_interop.rs  // CreateSurfaceFromDX11Native wrapping
│   └── probe.rs
├── amf/                  // AMF SDK headers (Apache 2.0)
└── tests.rs
```

No conditional-compilation stub files are needed — each variant is its own cdylib
crate (`amf` on Windows, `amf_rocm` on Linux); an absent add-on is simply a
`.dll`/`.so` that isn't in the add-ons directory.

---

## When to use this add-on

Use this add-on when:
- AMD-only deployment
- Quality at given bitrate matters (Pre-Analysis advantage)
- RDNA3+ with AV1 needs full AMF tuning (not just MF defaults). RDNA 2 has no AV1 encode.

Skip when:
- Heterogeneous fleet (use MF HW for cross-vendor in one binary)
- NVIDIA or Intel deployment

---

## Status

📋 Specced — not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_amf]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.

The keys, their defaults and their domains are in MODULE_CONFIG "Schema", under
`[addon_module_amf]`; this spec does not restate them.



---

## Stream Params Translation

This add-on implements the `hwencode::ConfigurableHardwareEncoder` trait (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). AMF supports hot reconfiguration for most parameters via `SetProperty` on the running VCE component.

| Param change | AMF API | Hot? |
|--------------|---------|------|
| `FPS` | `SetProperty(AMF_VIDEO_ENCODER_FRAMERATE, AMFRate{num,den})` | yes |
| `BitrateBps` | `SetProperty(AMF_VIDEO_ENCODER_TARGET_BITRATE, b)` | yes |
| `QP` | `SetProperty(AMF_VIDEO_ENCODER_QP_I/QP_P, qp)` | yes |
| `KeyframeInterval` | `SetProperty(AMF_VIDEO_ENCODER_IDR_PERIOD, ki)` | yes |
| `Width`, `Height` | `Terminate` + `ReInit` (returns `stream::StreamError::RequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | HEVC Main10 -- `AMF_VIDEO_ENCODER_HEVC_PROFILE_MAIN_10`; requires session-start negotiation (returns `stream::StreamError::RequiresRestart`). Only an HDR session reaches it; every SDR session stays on H.264 High | no |
| Colour signalling | `AMF_VIDEO_ENCODER_OUTPUT_COLOR_PROFILE` + `_OUTPUT_TRANSFER_CHARACTERISTIC` + `_OUTPUT_COLOR_PRIMARIES` (and the `_HEVC_` equivalents): BT.709 / BT.709 / BT.709 limited range for SDR, BT.2020 / SMPTE ST 2084 / BT.2020 for HDR, `video_full_range_flag = 0` in both. Written into the SPS VUI of every keyframe access unit (MODULE_ENCODE "Colour signalling"). Not a knob | set at `Init`; changes with the HDR rebuild |
| `NetworkRTTMs`, `PacketLossPct` | Feeds `HQVBR_QVBR` quality boost | yes |