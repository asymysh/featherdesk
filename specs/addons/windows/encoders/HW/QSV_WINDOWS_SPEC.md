# Windows HW Encoder Add-On: Intel Quick Sync (oneVPL)

## Purpose

Direct Intel hardware encoding on Windows via the modern **oneVPL** (Intel Video
Processing Library) SDK. Covers integrated Intel graphics (Sandy Bridge through Arrow
Lake) and discrete Intel Arc GPUs through a single API.

Bypasses MediaFoundation to unlock QSV-specific tuning:
- Low-power mode (`low_power = 1`) — runs on Intel's dedicated low-power encode silicon
- `vcm = 1` (video conferencing mode for H.264) — sub-3ms latency
- Direct rate-control tuning beyond MF defaults
- AV1 encode on Arc with full QSV control

**Intel Arc is NOT a separate add-on** — same oneVPL API, better hardware (adds AV1
encode, more concurrent sessions). This one add-on covers both Intel iGPUs and Arc.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| oneVPL (libvpl) | **MIT** ✅ | Genuinely permissive |
| Intel Media SDK (legacy) | MIT (now archived) | Predecessor of oneVPL; we use the newer SDK |
| Our Rust FFI binding | MIT | We own this code |

No royalties. SDK and headers can be vendored and redistributed without restriction.

---

## Hardware Compatibility

| Intel GPU | H.264 | HEVC 8-bit | HEVC 10-bit | AV1 |
|-----------|-------|-----------|-------------|-----|
| Sandy Bridge (2nd gen, 2011) | ✅ | ❌ | ❌ | ❌ |
| Ivy Bridge–Haswell (3rd–4th gen) | ✅ | ❌ | ❌ | ❌ |
| Broadwell (5th gen, 2014) | ✅ | ❌ | ❌ | ❌ |
| Skylake–Coffee Lake (6th–9th gen) | ✅ | ✅ | ✅ Kaby Lake+ | ❌ |
| Ice Lake (10th gen) | ✅ | ✅ | ✅ | ❌ |
| Tiger Lake (11th gen) | ✅ | ✅ | ✅ | ❌ |
| Alder Lake–Raptor Lake (12th–13th gen) | ✅ | ✅ | ✅ | ❌ |
| **Arc A-series (DG2)** | ✅ | ✅ | ✅ | ✅ |
| **Battlemage / Lunar Lake+** | ✅ | ✅ | ✅ | ✅ |

Runtime probe via `MFXLoad` + `MFXEnumImplementations` returns the available
encoder + supported codecs.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-qsv   # cdylib  featherdesk-addon-qsv.dll
```

FFI link config (in `build.rs`):

```rust
// build.rs:
//   println!("cargo:rustc-link-lib=dylib=vpl");
//   println!("cargo:rustc-link-lib=dylib=d3d11");
//   println!("cargo:rustc-link-lib=dylib=dxgi");
// oneVPL headers (mfx.h / mfxstructures.h / mfxvideo.h) are vendored and bound
// via bindgen into a `vpl-sys` module.
```

`vpl.lib` is the static import library from the **oneVPL SDK** (not the
graphics driver -- the driver ships the runtime DLL `libmfx-gen.dll`).
SDK headers vendored under `addons/qsv/vpl/`.

---

## FFI Implementation Sketch

```rust
// Raw FFI to oneVPL (a `vpl-sys`-style binding).
// 1. Initialise oneVPL with D3D11 device
let loader: mfxLoader = unsafe { MFXLoad() };
let cfg: mfxConfig = unsafe { MFXCreateConfig(loader) };
// Filter for hardware impl with H.264 encoder + D3D11 surfaces
// ... configure mfxConfig filters

let mut session: mfxSession = std::ptr::null_mut();
unsafe { MFXCreateSession(loader, 0, &mut session); }

let hdl = d3d11_device as mfxHDL;
unsafe { MFXVideoCORE_SetHandle(session, MFX_HANDLE_D3D11_DEVICE, hdl); }

// 2. Configure encoder for low latency
let mut enc_params = mfxVideoParam::default();
enc_params.mfx.CodecId           = MFX_CODEC_AVC;
enc_params.mfx.RateControlMethod = MFX_RATECONTROL_CBR as u16;
enc_params.mfx.TargetKbps        = (bitrate / 1000) as u16;
enc_params.mfx.FrameInfo.Width   = W;
enc_params.mfx.FrameInfo.Height  = H;
enc_params.mfx.FrameInfo.FourCC  = MFX_FOURCC_NV12;
enc_params.mfx.FrameInfo.FrameRateExtN = fps;
enc_params.mfx.FrameInfo.FrameRateExtD = 1;
enc_params.mfx.GopRefDist        = 1;            // no B-frames
enc_params.AsyncDepth            = 1;            // critical for low latency
enc_params.IOPattern             = MFX_IOPATTERN_IN_VIDEO_MEMORY as u16;
enc_params.mfx.LowPower          = MFX_CODINGOPTION_ON as u16;

// H.264-specific: video conferencing mode
let mut co2 = mfxExtCodingOption2::default();
co2.Header.BufferId = MFX_EXTBUFF_CODING_OPTION2;
co2.Header.BufferSz = std::mem::size_of::<mfxExtCodingOption2>() as u32;
co2.LookAheadDepth = 0;
co2.MaxFrameSize   = 0;
let mut ext_bufs: [*mut mfxExtBuffer; 1] = [&mut co2.Header];
enc_params.NumExtParam = 1;
enc_params.ExtParam    = ext_bufs.as_mut_ptr();

unsafe { MFXVideoENCODE_Init(session, &mut enc_params); }

// 3. Per frame: wrap D3D11 texture as mfxFrameSurface1
let mut surface = mfxFrameSurface1::default();
surface.Data.MemId = d3d11_texture as mfxMemId;   // shared via D3D11 device

let mut bs = mfxBitstream::default();
let mut sync: mfxSyncPoint = std::ptr::null_mut();
unsafe {
    MFXVideoENCODE_EncodeFrameAsync(session, std::ptr::null_mut(), &mut surface, &mut bs, &mut sync);
    MFXVideoCORE_SyncOperation(session, sync, INFINITE);
}
// bs.Data = encoded H.264 NALs
```

---

## Performance Targets

| Intel GPU | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| UHD 630 (Coffee Lake, low_power) | ~5ms | ~8ms | <2% |
| Iris Xe (Tiger Lake) | ~3ms | ~5ms | <2% |
| Arc A380 | ~3ms | ~4ms | <1% |
| Arc A770 | ~2ms | ~3ms | <1% (multi-stream capable) |

Low-power mode runs the encode on Intel's dedicated low-power encode silicon (LPE),
keeping the main GPU available for other tasks. This is one of Intel's biggest
advantages over NVIDIA / AMD for thin clients.

---

## File Structure

```
addons/qsv/          // Windows-only — Linux Intel uses libva
├── src/
│   ├── lib.rs       // encoder impl (Rust FFI to oneVPL)
│   ├── d3d11_interop.rs
│   └── probe.rs
├── vpl/             // oneVPL SDK headers (MIT)
└── tests.rs
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

> Note: there is no Linux QSV add-on because Intel Quick Sync on Linux is exposed
> through VA-API, fully covered by the LIBVA Linux add-on. On Windows, QSV needs
> its own SDK path (oneVPL) — hence this Windows-only add-on.

---

## When to use this add-on

Use this add-on when:
- Intel-only deployment (especially Arc)
- Need low-power encoding for thin clients / always-on devices
- Need AV1 encode on Arc with full tuning control

Skip when:
- Heterogeneous fleet (use MF HW for cross-vendor in one binary)
- NVIDIA or AMD deployment

---

## Status

📋 Specced — not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_qsv]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements the `stream::ConfigurableHardwareEncoder` trait (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). Intel oneVPL (QSV) supports `MFXVideoENCODE_Reset` for hot reconfiguration of some parameters, but resolution and profile changes require full session teardown.

| Param change | oneVPL/QSV API | Hot? |
|--------------|---------------|------|
| `FPS` | `mfxVideoParam.mfx.FrameInfo.FrameRateExtN/D` + `MFXVideoENCODE_Reset` | yes |
| `BitrateBps` | `mfxVideoParam.mfx.TargetKbps` + `MFXVideoENCODE_Reset` | yes |
| `QP` | `mfxVideoParam.mfx.QPI/QPP/QPB` (CQP) or `mfxExtCodingOption.ICQQuality` (ICQ) + `MFXVideoENCODE_Reset` | yes |
| `KeyframeInterval` | `mfxVideoParam.mfx.GopPicSize` + `MFXVideoENCODE_Reset` | yes |
| `Width`, `Height` | `MFXVideoENCODE_Close` + re-alloc surfaces + `MFXVideoENCODE_Init` (returns `stream::Error::RequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | HEVC Main10 profile via `MFX_PROFILE_HEVC_MAIN10`; requires full session recreation (returns `stream::Error::RequiresRestart`) | no |