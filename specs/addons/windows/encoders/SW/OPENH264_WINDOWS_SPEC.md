# Windows SW Encoder Add-On: OpenH264 (Rust FFI)

## Purpose

Software H.264 encoder via Cisco's OpenH264 library on Windows. **Identical Rust
code to the Linux and macOS OpenH264 add-ons** — one of FeatherDesk's main
cross-platform consistency points.

**This is the software default.** When the Windows machine has no GPU at all
(rare, mostly VMs and headless test machines), when no vendor HW encoder add-on
is installed, or when a HW encoder degrades mid-session, this is the encoder that
runs — `[encode] mode = "auto"` reaches it without any operator action. It is the
default rather than the faster `x264` because it is in-process, has no external
binary to find or lose, and can force an IDR in place for the cost of one flag on
the next frame (see MODULE_ENCODE "Software encoder order").

This encoder emits **Constrained Baseline** only
(`VideoProfile::H264ConstrainedBaseline`) — OpenH264's encoder supports no other
profile (its *decoder* does). `codec()` therefore returns `avc1.42E0LL` with `LL`
computed from the active resolution and frame rate per
[`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Codec-string
computation"; it is never a constant. There is no `profile` knob: an encoder that
can emit one profile does not get a key to choose among three.

---

## License & Royalties

| Component | License | Notes |
|-----------|---------|-------|
| OpenH264 library | BSD-2 | Permissive |
| MPEG-LA H.264 royalties | **Cisco pays** | Same on every platform |
| Our Rust FFI binding | MIT | Same Rust source as Linux + macOS |

Zero royalty concern — see `specs/addons/linux/encoders/SW/OPENH264_LINUX_SPEC.md` for the
full licensing background. This applies identically on Windows.

---

## Hardware Compatibility

**Any CPU.** Pure software.

| Architecture | Performance |
|-------------|------------|
| x86_64 (Intel/AMD desktop & laptop) | ~4–8ms p50 @ 1080p |
| ARM64 (Snapdragon Copilot+ PCs) | ~8–12ms p50 @ 1080p (NEON path) |

Same encoder, same code on both x86 and ARM Windows.

---

## Build & Distribution

### Build (shared library)

```bash
cargo build --release -p featherdesk-addon-openh264   # cdylib  featherdesk-addon-openh264.dll
```

The add-on ID matches the Linux add-on ID. Same encoder, same Rust source.

### Runtime dependencies

- `openh264-X.X.X-win64.dll` (Cisco distributes prebuilt DLLs)
- Must be in `%PATH%` or alongside the .exe

The Windows binary needs the OpenH264 DLL shipped alongside it. Cisco's official
prebuilt DLLs are downloadable from the OpenH264 GitHub releases. Their license
permits redistribution.

### FFI configuration (Windows)

```rust
// build.rs:
//   println!("cargo:rustc-link-search=native=openh264/win64");
//   println!("cargo:rustc-link-lib=dylib=openh264");
// OpenH264 headers (wels/codec_api.h, wels/codec_app_def.h) are vendored and
// bound via bindgen into an `openh264-sys` module.
```

The OpenH264 SDK headers and import library are vendored in the source tree under
`addons/openh264/openh264/` (Cisco's license permits this for binary
distribution).

---

## Implementation

Identical to the Linux spec. See
[`linux/encoders/SW/OPENH264_LINUX_SPEC.md`](../../../linux/encoders/SW/OPENH264_LINUX_SPEC.md)
for:
- FFI session setup
- Per-frame encode loop
- Force-keyframe path
- IDR-on-demand configuration

The Rust code is the same across Linux + Windows + macOS. Only the `build.rs`
link directives differ for finding the OpenH264 library at link time.

### Chroma

This add-on accepts `Subsampling::I420` only — Constrained Baseline is 4:2:0 by
definition. A `YuvFrame` carrying `I422` or `I444` returns
`stream::StreamError::ChromaUnsupported`, and the chroma negotiation in
[`specs/core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)
falls the session back to 4:2:0 before it ever reaches `encode`.

### Capabilities declared at probe

`probe()` returns `ROk(ProbeReport { available: true, codecs: [CodecId::H264],
caps: AddonCaps(AddonCaps::ENC_CONFIGURABLE), .. })` — bitrate, QP, frame rate
and keyframe interval all change through `SetOption` on the running encoder, so
the hot path never needs a rebuild for them. See
[`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module
surface": availability is not an error (a missing DLL is
`ROk(ProbeReport { available: false, reason })`, never an `RErr`), every bit the
add-on actually serves must be set, and a bit claimed here and refused later is a
capability lie.

---

## Performance (Benchmarked)

Measured on AMD Ryzen 9 5900X (12C/24T), Windows 11, Cisco OpenH264 v2.4.1.

### OpenH264 thread scaling — 1920x1080

| Threads/Slices | P50 | FPS | Notes |
|---------------|-----|-----|-------|
| 1 | 23.4ms | 42 | Single-threaded baseline |
| 2 | 13.1ms | 77 | 1.8x scaling |
| **4** | **7.9ms** | **125** | **Sweet spot — best perf/thread** |
| 8 | 7.5ms | 131 | Barely faster than 4T |
| 12 | 7.4ms | 138 | Saturated — slice parallelism ceiling |

### OpenH264 vs x264 — side by side

| Threads | OpenH264 (BSD) ms | OpenH264 FPS | x264 (GPL) ms | x264 FPS | x264 speedup |
|---------|-------------------|-------------|---------------|---------|-------------|
| 1 | 23.4 | 42 | 10.6 | 94 | 2.2x |
| 2 | 13.1 | 77 | 5.6 | 179 | 2.3x |
| 4 | 7.9 | 125 | 4.3 | 234 | 1.9x |
| 8 | 7.5 | 131 | 3.4 | 294 | 2.2x |
| 12 | 7.4 | 138 | 3.3 | 302 | 2.2x |

### 2560x1440

| Config | P50 | FPS |
|--------|-----|-----|
| OpenH264 4T | 13.7ms | 73 |
| OpenH264 12T | 13.4ms | 74 |

**x264 is consistently ~2x faster**, and is still not the default: its only
mechanism for an on-demand IDR is killing and respawning its ffmpeg child.
OpenH264's advantages are that it needs no external binary, that BSD + Cisco
royalty coverage keeps the host binary fully proprietary, and that a forced
keyframe costs one flag.

### Target use case

| Scenario | Recommended encoder |
|----------|-------------------|
| Any deployment, no measurement taken | **OpenH264** — the software default |
| Cross-platform consistency | **OpenH264** (identical code on all OSes) |
| Frequent joins / lossy links (many forced IDRs) | **OpenH264** (in-place IDR; x264 respawns a process for each one) |
| Measured CPU-bound host, GPL acceptable | **x264**, forced with `[encode] force_addon = "x264"` (3.3ms vs 7.4ms at 12T) |

---

## File Structure

```
addons/openh264/
├── src/
│   ├── lib.rs               // shared with Linux + macOS (Rust FFI binding)
│   └── probe.rs
├── openh264/                // vendored OpenH264 SDK (headers + import libs)
│   ├── include/wels/*.h
│   ├── win64/openh264.lib   // Windows MSVC import library
│   └── win64/openh264-X.dll // Cisco's official prebuilt DLL
├── build.rs                 // link config for openh264
└── tests.rs
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

The vendored `openh264/win64/openh264-X.dll` ships alongside the .exe at install
time. The build copies it from `addons/openh264/openh264/win64/`
into the install directory.

---

## When to use this add-on

Always ship it. It is the software default and the target `degrade_to_software`
falls back to, and startup step 3f treats "a HW encoder loaded with no SW
fallback add-on" as a configuration error. Specifically:
- A guaranteed SW H.264 path that doesn't depend on Windows version (works on
  Windows 10 and 11, both x86 and ARM)
- Cross-platform binary consistency (same encoder used on every OS)
- Container deployments where MediaFoundation isn't reliable

Ship `x264` **in addition** — never instead — on a host that has been measured as
CPU-bound, and select it with `[encode] force_addon = "x264"`.

---

## Status

📋 Specced — implementation exists today as the default SW encoder in the core
encode crate. Refactor moves it to `addons/openh264/` and builds it as a cdylib,
with Windows-specific DLL vendoring added.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_openh264]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements the `encode::ConfigurableEncoder` trait (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). All updates flow through `update_stream_params(p: stream::Params)`.

| Param change | OpenH264 API | Hot? |
|--------------|--------------|------|
| `FPS` | `ISVCEncoder::SetOption(ENCODER_OPTION_FRAME_RATE, &fps)` | yes |
| `BitrateBps` | `ISVCEncoder::SetOption(ENCODER_OPTION_BITRATE, &b)` | yes |
| `QP` | `ENCODER_OPTION_SVC_ENCODE_PARAM_EXT` | yes |
| `KeyframeInterval` | `param.uiIntraPeriod` | yes |
| Colour signalling | `SEncParamExt.sSpatialLayers[0]` VUI fields: `bVideoSignalTypePresent = true`, `bColorDescriptionPresent = true`, `bFullRange = false`, `uiColorPrimaries = 1`, `uiTransferCharacteristics = 1`, `uiColorMatrix = 1` — BT.709 limited range, written into the SPS of every keyframe access unit (see MODULE_ENCODE "Colour signalling"). Not a knob | set at `Initialize` |
| `Width`, `Height` | teardown + `Initialize` (returns `stream::StreamError::RequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | rejected with `stream::StreamError::HdrUnsupported` — Constrained Baseline is 8-bit, and no SW encoder emits HEVC Main10. An HDR session runs on a HW encoder's HEVC Main10 (`mf_hw`/`nvenc`/`amf`/`qsv`); the pipeline sends `{"type":"hdr_unavailable","reason":"no_hevc_encoder"}` when none is loaded. This add-on never sees 10-bit params on the degrade path either: `degrade_to_software` resets `hdr`/`bit_depth`/`color_space` **before** it builds the software path | n/a |
