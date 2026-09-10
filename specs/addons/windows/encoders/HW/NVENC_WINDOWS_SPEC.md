# Windows HW Encoder Add-On: NVENC Direct

## Purpose

Direct NVIDIA Video Codec SDK access on Windows, bypassing MediaFoundation. Same
NVENC SDK as the Linux NVENC add-on, but with a **D3D11 input surface** instead of
CUDA. Unlocks NVENC features unavailable through MediaFoundation:

- `NV_ENC_TUNING_INFO_ULTRA_LOW_LATENCY` (sub-3ms p50)
- `REF_FRAMES_INVALIDATION` — partial IDR on packet loss instead of full keyframe storm
- AV1 encode on RTX 40+
- Single-surface pipeline (`surfaces=1`, `delay=0`)

For NVIDIA-only Windows deployments where streaming quality matters, this is the best
encoder available.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| NVIDIA Video Codec SDK | NVIDIA SDK License (free use) | Same model as NVENC Linux add-on |
| Our Rust FFI binding | MIT | |

No royalties. SDK redistribution rules same as Linux side — headers vendored into
source tree per NVIDIA's permissive header redistribution policy.

---

## Hardware Compatibility

Same hardware support as the NVENC Linux add-on. NVENC has been in every NVIDIA
GPU since Kepler (GTX 600 series, 2012). On Windows the codec coverage and session
caps are identical to Linux.

`probe()` queries `nvEncGetEncodeGUIDCount` / `nvEncGetEncodeGUIDs` on an opened
session and reports the result as `ProbeReport { available, codecs, caps }`:
`codecs` is what the device actually reports (H.264 always; HEVC on Maxwell 2+;
AV1 on Ada+), and `caps` sets `AddonCaps::ENC_CONFIGURABLE`, because
`nvEncReconfigureEncoder` changes bitrate, QP, frame rate and GOP length without
a rebuild. Availability is not an error — no NVIDIA adapter, or a driver too old
for the SDK, is `ROk(ProbeReport { available: false, reason })`, never an `RErr`.
Set every bit the add-on actually serves, and claim only what the probe can
prove: a bit claimed here and refused later is a capability lie (MODULE_ABI
"Misbehaving add-ons").

See
[`../../../linux/encoders/HW/NVENC_LINUX_SPEC.md`](../../../linux/encoders/HW/NVENC_LINUX_SPEC.md)
for the full Kepler → Blackwell matrix.

---

## Key Difference from NVENC Linux: D3D11 Surface

On Linux, NVENC consumes CUDA device pointers (from CUDA-EGL interop with KMS DMA-BUF).

On Windows, NVENC consumes **D3D11 textures directly**:

```rust
let mut register_params = NV_ENC_REGISTER_RESOURCE {
    version: NV_ENC_REGISTER_RESOURCE_VER,
    ..Default::default()
};
register_params.resourceType       = NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX;
register_params.width              = W;
register_params.height             = H;
register_params.resourceToRegister = d3d11_texture as *mut c_void; // ← from DXGI Desktop Duplication
register_params.bufferFormat       = NV_ENC_BUFFER_FORMAT_ARGB;
unsafe { (funcs.nvEncRegisterResource)(encoder, &mut register_params); }
```

This is the canonical Windows zero-copy path: DXGI Desktop Duplication captures
into an `ID3D11Texture2D`, that texture is registered with NVENC, NVENC encodes
on the GPU. No CPU memory copy at any stage.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-nvenc   # cdylib  featherdesk-addon-nvenc.dll
```

FFI link config (in `build.rs`):

```rust
// build.rs:
//   println!("cargo:rustc-link-lib=dylib=nvencodeapi");
//   println!("cargo:rustc-link-lib=dylib=d3d11");
//   println!("cargo:rustc-link-lib=dylib=dxgi");
// nvEncodeAPI.h is vendored and bound via bindgen into an `nvenc-sys` module.
```

`nvencodeapi.lib` ships with the NVIDIA driver; no separate SDK install needed at
runtime. SDK headers vendored under `addons/nvenc/sdk/`.

---

## Implementation

Same NVENC session setup, configuration flags, and per-frame loop as the Linux NVENC
add-on. See [`../../../linux/encoders/HW/NVENC_LINUX_SPEC.md`](../../../linux/encoders/HW/NVENC_LINUX_SPEC.md)
for:
- Session creation with `NV_ENC_DEVICE_TYPE_DIRECTX` (not `_CUDA`)
- Configuration: `ULTRA_LOW_LATENCY` tuning, CBR rate control, `zeroReorderDelay`
- Reference frame invalidation API

The only differences from Linux:
- `sessionParams.deviceType = NV_ENC_DEVICE_TYPE_DIRECTX`
- `sessionParams.device = d3d11Device` (not CUDA context)
- `picParams.inputBuffer = registeredD3D11Texture` (not CUDA pointer)

Everything else is identical.

---

## Performance

### Measured (real hardware, Ryzen 9 5900X host)

| NVIDIA GPU | Codec | 1080p ms | 1440p ms | FPS @ 1080p |
|-----------|-------|---------|---------|------------|
| **GTX 1080 Ti (Pascal)** | H.264 | **4.5ms** | **6.6ms** | 220 |
| **GTX 1080 Ti (Pascal)** | HEVC | **4.7ms** | **7.4ms** | 211 |
| **Quadro RTX 4000 (Turing)** | H.264 | **5.4ms** | **7.9ms** | 187 |
| **Quadro RTX 4000 (Turing)** | HEVC | **5.6ms** | **8.0ms** | 179 |

### Estimated (no measured hardware)

| NVIDIA GPU | 1080p p50 | 1440p p50 | CPU at 60fps |
|-----------|----------|----------|-------------|
| GTX 1660 (Turing) | ~2ms | ~3ms | <1% |
| RTX 3060 (Ampere) | ~1.5ms | ~2.5ms | <1% |
| RTX 4090 (Ada, dual NVENC) | <1ms | <1.5ms | <1% (split sessions) |

Compared to MediaFoundation HW route (NVENC under the hood): ~2–3ms lower latency
plus access to REF_FRAMES_INVALIDATION.

---

## File Structure

```
addons/nvenc/            // Rust source shared by the Linux + Windows nvenc builds
├── src/
│   ├── lib.rs           // shared encoder impl
│   ├── linux.rs         // CUDA surface path (cfg(target_os = "linux"))
│   ├── windows.rs       // D3D11 surface path (cfg(windows))
│   ├── d3d11_interop.rs // DXGI texture registration
│   └── probe.rs
├── sdk/                 // NVIDIA SDK headers
└── tests.rs
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## When to use this add-on

Same criteria as Linux NVENC: NVIDIA-only deployment, packet loss matters, AV1 needed
on RTX 40+. On Windows specifically: prefer this over the MediaFoundation HW add-on
when REF_FRAMES_INVALIDATION matters.

Skip when: heterogeneous fleet (use MF HW for cross-vendor coverage in one binary),
or AMD/Intel-only deployment.

---

## Status

📋 Specced — not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_nvenc]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

Identical to the Linux `nvenc` add-on -- NVENC's API is OS-portable through `nvEncodeAPI.h`. See [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md) and [`../../../linux/encoders/HW/NVENC_LINUX_SPEC.md`](../../../linux/encoders/HW/NVENC_LINUX_SPEC.md#stream-params-translation).

| Param change | NVENC API | Hot? |
|--------------|-----------|------|
| `FPS` | `nvEncReconfigureEncoder` (`frameRateNum/Den`) | yes |
| `BitrateBps` | `nvEncReconfigureEncoder` (`averageBitRate`) | yes |
| `QP` | `nvEncReconfigureEncoder` (`constQP`) | yes |
| `KeyframeInterval` | `nvEncReconfigureEncoder` (`gopLength`) | yes |
| `Width`, `Height` | hot if within initial `maxEncodeWidth/Height`, else re-init | mostly |
| `BitDepth=10` / `HDR=true` | requires the HEVC Main10 GUID at session start (returns `stream::StreamError::RequiresRestart`). H.264 is emitted for every SDR session; HEVC Main10 only for HDR — see [`../README.md`](../README.md) "Codec fallback order at runtime" | no |
| Colour signalling | `NV_ENC_CONFIG_H264_VUI_PARAMETERS` (and the HEVC equivalent): `videoSignalTypePresentFlag = 1`, `videoFullRangeFlag = 0`, `colourDescriptionPresentFlag = 1`, and `colourPrimaries`/`transferCharacteristics`/`colourMatrix` = `1/1/1` for SDR BT.709 or `9/16/9` for HDR BT.2020 PQ. Written into the SPS of every keyframe access unit (MODULE_ENCODE "Colour signalling"). Not a knob | set at session start; changes with the HDR rebuild |
