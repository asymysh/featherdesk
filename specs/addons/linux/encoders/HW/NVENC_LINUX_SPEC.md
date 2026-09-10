# Linux Add-On: NVENC Direct (NVIDIA Video Codec SDK)

## Purpose

Optional add-on binary for **NVIDIA GPUs on Linux** that calls NVIDIA's NVENC hardware
encoder directly via the official NVIDIA Video Codec SDK. Replaces the default VA-API
path (which on NVIDIA uses the unofficial `nvidia-vaapi-driver` wrapper) with the
official, full-featured API.

**Why this exists:** the VA-API → `nvidia-vaapi-driver` wrapper does not expose
NVENC-specific features. Direct NVENC unlocks:

| Feature | VA-API wrapper | NVENC direct |
|---------|---------------|--------------|
| H.264 / HEVC encode | ✅ | ✅ |
| AV1 encode (Ada Lovelace+) | partial | ✅ |
| `REF_FRAMES_INVALIDATION` | ❌ | ✅ — **biggest streaming win** |
| `NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY` | ❌ | ✅ |
| Single-surface pipeline (`surfaces=1`, `delay=0`) | ❌ | ✅ |
| CUDA device interop (zero-copy from CUDA buffers) | ❌ | ✅ |
| Vendor support | community | official NVIDIA |

`REF_FRAMES_INVALIDATION` alone justifies a separate binary. On packet loss, NVENC can
invalidate only the affected reference frames and re-encode the dependent macroblocks
instead of forcing a full IDR keyframe. This is the single biggest latency/bandwidth
advantage NVIDIA has over Intel and AMD for streaming. The VA-API wrapper cannot
surface this control.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Video Codec SDK headers | NVIDIA Software License (free use) | Cannot redistribute headers separately; users install SDK |
| `libnvidia-encode` (driver-shipped) | proprietary NVIDIA | Ships with the NVIDIA driver |
| Our Rust FFI binding | MIT | We own this code |

No royalties. No GPL/LGPL contamination. The NVIDIA SDK license is "free to use, can't
redistribute the SDK as a standalone package." This matches the pattern of every
production product using NVENC (OBS, Sunshine, ffmpeg).

---

## Hardware Compatibility

NVENC is in every NVIDIA GPU from **Kepler (GTX 600 series, 2012) onward**.

| GPU generation | H.264 | HEVC | AV1 | Concurrent sessions |
|----------------|-------|------|-----|---------------------|
| Kepler (GTX 600–700) | ✅ | ❌ | ❌ | 2 (consumer cap) |
| Maxwell 1 (GTX 750) | ✅ | ❌ | ❌ | 2 |
| Maxwell 2 (GTX 900) | ✅ | ✅ 8-bit | ❌ | 2 |
| Pascal (GTX 1000) | ✅ | ✅ 10-bit | ❌ | 2 |
| Volta / Turing (GTX 1600, RTX 2000) | ✅ | ✅ | ❌ | 3 |
| Ampere (RTX 3000) | ✅ | ✅ | ❌ | 3 |
| Ada Lovelace (RTX 40-series — 4070/4080/4090) | ✅ | ✅ | ✅ | 3+ (dual NVENC on 4090) |
| Blackwell (RTX 5000) | ✅ | ✅ | ✅ | 3+ |

Consumer cards historically capped at 2-3 concurrent sessions. Quadro / Tesla / Data
Center cards have unlimited sessions. For remote desktop (one viewer = one session),
the cap rarely matters.

---

## Build & Distribution

### Shared library build

```bash
cargo build --release -p featherdesk-addon-nvenc   # cdylib → featherdesk-addon-nvenc.so
```

The `nvenc` add-on cdylib is built from the `addons/encode/nvenc/` crate.
The NVIDIA SDK dependency is linked into that library only — the host binary never
links it.

### Runtime dependencies

Users installing this binary need:
- NVIDIA proprietary driver 470+ (libnvidia-encode.so ships with it)
- NVIDIA Video Codec SDK 12+ headers at build time only (not at runtime)

### FFI configuration

The bindings are generated with `bindgen` in `build.rs`, which adds the CUDA +
SDK include paths, links the driver libraries, and wraps the headers:

```rust
// build.rs
println!("cargo:rustc-link-search=native=/usr/lib/x86_64-linux-gnu");
println!("cargo:rustc-link-lib=nvidia-encode");
println!("cargo:rustc-link-lib=cuda");
println!("cargo:rustc-link-lib=dl");

bindgen::Builder::default()
    .clang_args(["-I/usr/local/cuda/include", "-Isdk"])
    .header_contents("wrapper.h", "
        #include <cuda.h>
        #include <nvEncodeAPI.h>")
    .generate().unwrap();
```

The SDK headers (`nvEncodeAPI.h`, `cuda.h`) are checked into the source tree under
`addons/encode/nvenc/sdk/` — NVIDIA's SDK license permits redistributing the headers
inside an application source tree (this is what ffmpeg, Sunshine, OBS all do).

---

## FFI Implementation Sketch

```c
// Encode session setup (one-time at startup)
NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS sessionParams = { 0 };
sessionParams.version = NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS_VER;
sessionParams.deviceType = NV_ENC_DEVICE_TYPE_CUDA;
sessionParams.device = cuContext;
sessionParams.apiVersion = NVENCAPI_VERSION;

NV_ENCODE_API_FUNCTION_LIST funcs = { NV_ENCODE_API_FUNCTION_LIST_VER };
NvEncodeAPICreateInstance(&funcs);
funcs.nvEncOpenEncodeSessionEx(&sessionParams, &encoder);

// Configure for low-latency streaming
NV_ENC_INITIALIZE_PARAMS initParams = { NV_ENC_INITIALIZE_PARAMS_VER };
initParams.encodeGUID  = NV_ENC_CODEC_H264_GUID;
initParams.presetGUID  = NV_ENC_PRESET_P1_GUID;       // P1 = fastest
initParams.tuningInfo  = NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY;
initParams.encodeWidth = w;
initParams.encodeHeight = h;
initParams.frameRateNum = fps;
initParams.frameRateDen = 1;
initParams.enablePTD = 1;

NV_ENC_CONFIG cfg = { NV_ENC_CONFIG_VER };
funcs.nvEncGetEncodePresetConfigEx(encoder, encodeGUID, presetGUID, tuning, &presetCfg);
memcpy(&cfg, &presetCfg.presetCfg, sizeof(cfg));
cfg.gopLength = NVENC_INFINITE_GOPLENGTH;             // IDR on demand only
cfg.frameIntervalP = 1;                                // no B-frames
cfg.rcParams.rateControlMode = NV_ENC_PARAMS_RC_CBR;
cfg.rcParams.averageBitRate = bitrate;
cfg.rcParams.disableBadapt = 1;
cfg.rcParams.zeroReorderDelay = 1;                     // critical for low latency
cfg.rcParams.enableLookahead = 0;

// Enable REF_FRAMES_INVALIDATION
cfg.rcParams.enableNonRefP = 1;
initParams.encodeConfig = &cfg;

funcs.nvEncInitializeEncoder(encoder, &initParams);

// Per-frame encode (CUDA surface input → bitstream output)
NV_ENC_PIC_PARAMS picParams = { NV_ENC_PIC_PARAMS_VER };
picParams.pictureStruct = NV_ENC_PIC_STRUCT_FRAME;
picParams.inputBuffer   = registeredSurface;
picParams.bufferFmt     = NV_ENC_BUFFER_FORMAT_NV12;
picParams.outputBitstream = bitstreamBuffer;
picParams.inputWidth    = w;
picParams.inputHeight   = h;
funcs.nvEncEncodePicture(encoder, &picParams);

// Read encoded output
NV_ENC_LOCK_BITSTREAM lockParams = { NV_ENC_LOCK_BITSTREAM_VER };
lockParams.outputBitstream = bitstreamBuffer;
funcs.nvEncLockBitstream(encoder, &lockParams);
// lockParams.bitstreamBufferPtr / bitstreamSizeInBytes = NALs
funcs.nvEncUnlockBitstream(encoder, bitstreamBuffer);
```

### Reference frame invalidation (NVENC-only superpower)

```c
// On client packet-loss report: invalidate specific reference frames
// rather than forcing a full IDR
NV_ENC_INVALIDATE_REFERENCE_FRAMES invalidate = { 0 };
invalidate.invalidRefFrameTimeStamp = lostFrameTimestamp;
funcs.nvEncInvalidateRefFrames(encoder, &invalidate);
```

This is the kernel of low-latency streaming on NVIDIA. No full keyframe needed on loss.

---

## Performance Targets

| GPU class | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| GTX 1660 (Turing) | <2ms | <3ms | <1% |
| RTX 3060 (Ampere) | <1.5ms | <2.5ms | <1% |
| RTX 4090 (Ada) | <1ms | <1.5ms | <1% (dual NVENC) |

Compared to VA-API wrapper on the same GPUs: roughly 2× lower latency due to direct
API access and the lack of wrapper translation layer.

---

## Capture Integration

NVENC accepts inputs as:
- **CUDA arrays / device pointers** (zero-copy from CUDA-based capture)
- **D3D11 textures** (Windows only)
- **Vulkan VkImage** (cross-platform, Linux applies)
- System memory NV12 / YUV420 (fallback, requires CPU upload)

The Linux capture options for zero-copy to NVENC:

| Capture source | Zero-copy to NVENC? |
|---------------|---------------------|
| KMS DMA-BUF | Via Vulkan import (best) or CUDA EGL interop |
| X11 / XShm | CPU upload only (~5ms overhead at 1440p) |
| PipeWire | Possible via EGL → CUDA interop |

For the zero-copy path, the cleanest route is:
```
KMS DMA-BUF → CUDA EGL interop (cuGraphicsEGLRegisterImage)
            → CUDA array → NVENC NV_ENC_BUFFER_FORMAT_NV12
```

Documented in `MODULE_HARDWARE_ENCODE.md` as one of the supported zero-copy paths.

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-nvenc   (cfg(target_os = "linux"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

```rust
fn probe(&self) -> RResult<ProbeReport, AbiError> {
    // 1. dlopen libnvidia-encode.so
    // 2. NvEncodeAPICreateInstance
    // 3. nvEncGetEncodeGUIDCount / nvEncGetEncodeGUIDs on an opened session
    // 4. Query max width/height per codec
    // 5. No NVIDIA driver/GPU, or a driver too old for the SDK → ROk(ProbeReport {
    //      available: false,
    //      reason: "no NVIDIA adapter, or the driver predates Video Codec SDK
    //               <n>".into(),
    //      codecs: RVec::new(), caps: AddonCaps(0), displays: RVec::new() })
    // 6. Otherwise → ROk(ProbeReport {
    //      available: true, reason: RString::new(),
    //      codecs: <what the device reports: H264 always; Hevc on Maxwell 2+;
    //               Av1 on Ada+>,
    //      caps: AddonCaps(AddonCaps::ENC_CONFIGURABLE), // nvEncReconfigureEncoder
    //                                                    //   changes bitrate, QP,
    //                                                    //   frame rate and GOP
    //                                                    //   without a rebuild
    //      displays: RVec::new() })
}
```

**Availability is not an error.** No NVIDIA adapter, or a driver too old for the
SDK, is `ROk(ProbeReport { available: false, reason })`, never an `RErr`. `RErr`
is reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` means
no hot parameter change — silently, with no error and no warning.

**Only claim what this call can prove.** `codecs` is what the *device* reports,
not the union of what NVENC supports somewhere; a bit or a codec claimed here and
refused later is a capability lie (MODULE_ABI "Misbehaving add-ons"), and the
constructed object's `caps()` is authoritative.

Pipeline probes encoders in this order on Linux (`MODULE_PIPELINE` startup
step 3e):
```
HW:  nvenc (this add-on)  →  amf_rocm  →  libva
SW:  openh264             →  (x264 only via [encode] force_addon = "x264")
```

---

## File Structure

```
addons/encode/nvenc/
├── nvenc.rs              // Encoder struct, NvencEncoder::new
├── ffi.rs                // Rust FFI bindings, cfg(target_os = "linux") (built into the add-on cdylib)
├── probe.rs              // the root module's probe() -> ProbeReport
├── cuda_interop.rs       // KMS DMA-BUF → CUDA array import
├── sdk/                  // NVIDIA SDK headers (redistributable per NVIDIA license)
│   ├── nvEncodeAPI.h
│   └── cuda.h
└── tests.rs              // Integration tests (cfg(feature = "integration"))
```

---

## Testing

| Test | Hardware |
|------|---------|
| `test_probe_nvenc` | NVIDIA GPU + driver |
| `test_encode_h264` | NVIDIA GPU |
| `test_encode_hevc` | Maxwell 2+ |
| `test_encode_av1` | Ada Lovelace+ |
| `test_ref_frame_invalidation` | NVIDIA GPU + simulated packet loss |
| `bench_encode_1080p60` | NVIDIA GPU |
| `bench_encode_1440p60` | NVIDIA GPU |

All gated behind `cfg(feature = "integration")`.

---

## When to use this add-on

Use this add-on when:
- Target deployment has NVIDIA GPUs
- Streaming over lossy networks where REF_FRAMES_INVALIDATION matters
- Need AV1 hardware encode on RTX 40+
- Need the absolute minimum latency NVIDIA can produce

Stick with default VA-API binary when:
- Heterogeneous fleet (mix of Intel/AMD/NVIDIA) where one binary is simpler
- LAN-only deployment (loss rarely happens, REF_FRAMES_INVALIDATION less important)
- Container size matters (NVIDIA SDK adds ~50MB)

---

## Status

📋 Specced — not yet built. Implementation order: probe → session init → per-frame
encode → reference frame invalidation → CUDA interop zero-copy.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_nvenc]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.

The keys, their defaults and their domains are in MODULE_CONFIG "Schema", under
`[addon_module_nvenc]`; this spec does not restate them.


---

## Stream Params Translation

This add-on implements `hwencode::ConfigurableHardwareEncoder` and sets `AddonCaps::ENC_CONFIGURABLE` at probe (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). NVENC supports fully-hot reconfiguration via `nvEncReconfigureEncoder` for everything except resolution changes that cross the IDR boundary.

| Param change | NVENC API | Hot? |
|--------------|-----------|------|
| `FPS` | `NV_ENC_RECONFIGURE_PARAMS.reInitEncodeParams.frameRateNum/Den` + `nvEncReconfigureEncoder` | yes |
| `BitrateBps` | `rcParams.averageBitRate` + `nvEncReconfigureEncoder` | yes |
| `QP` | `rcParams.constQP` (requires `rateControlMode == NV_ENC_PARAMS_RC_CONSTQP`) + `nvEncReconfigureEncoder` | yes |
| `KeyframeInterval` | `rcParams.gopLength` + `nvEncReconfigureEncoder` | yes |
| `Width`, `Height` | `nvEncReconfigureEncoder` with `forceIDR=1, resetEncoder=1` -- hot for downscale, requires re-init for upscale past initial `maxEncodeWidth/Height` | mostly |
| `BitDepth=10` / `HDR=true` | Requires HEVC codec (`NV_ENC_CODEC_HEVC_GUID`) + `NV_ENC_PROFILE_HEVC_MAIN10_GUID`; pipeline negotiates codec at session start, NOT mid-stream -- returns `StreamError::RequiresRestart` if toggled later | no |
| `NetworkRTTMs`, `PacketLossPct` | feeds `rcParams.lowDelayKeyFrameScale` + intra-refresh wave width | yes |

**Sizing hint:** allocate `maxEncodeWidth/Height` to the largest display dimension at session creation to keep resolution-downscale hot.
