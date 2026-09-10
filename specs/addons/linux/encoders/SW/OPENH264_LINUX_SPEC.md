# Linux SW Encoder Add-On: OpenH264 (Rust FFI)

## Purpose

Software H.264 Constrained Baseline encoder via Cisco's OpenH264 library, called
through Rust FFI. The **software default on every OS** — the same Rust crate
compiles on Linux, Windows, and macOS without changes.

When the system has no GPU at all (containers, headless ARM, Graviton-class instances,
broken drivers, etc.), this is the encoder that runs. It is the default rather
than the faster `x264` subprocess because it is in-process, has no external binary
to find or lose, and can force an IDR in place for the cost of one flag on the
next frame ([`MODULE_ENCODE.md`](../../../../media/MODULE_ENCODE.md) "Software
encoder order").

---

## License & Royalties

| Component | License | Notes |
|-----------|---------|-------|
| OpenH264 library (Cisco) | **BSD-2-Clause** | Permissive, embeddable |
| MPEG-LA H.264 patent royalties | **Cisco pays** | Cisco operates the binary distribution and pays MPEG-LA on behalf of all users |
| Our Rust FFI binding | MIT | We own this code |

This is the entire reason OpenH264 exists as a project — Cisco wants free H.264 in
WebRTC and pays the patent pool so downstream users don't have to. Zero royalty
concern for FeatherDesk shipping this.

---

## Hardware Compatibility

**Any CPU.** Pure software. No GPU required.

| Architecture | SIMD path | 1080p p50 |
|-------------|-----------|----------|
| x86_64 (Intel/AMD) | SSE4.2 / AVX2 | ~8ms |
| ARM64 (Apple Silicon native, Graviton, Snapdragon Linux) | NEON | ~10ms |
| ARM64 (Rosetta-translated) | — | Avoid; use VideoToolbox on macOS |

OpenH264 has tuned assembly for both x86_64 and ARM64 NEON. Verified working on
both architectures.

---

## Build & Distribution

### Shared library build

```bash
cargo build --release -p featherdesk-addon-openh264   # cdylib → featherdesk-addon-openh264.so
```

### Runtime dependencies

- `libopenh264.so.X` (system package: `libopenh264-dev` on Debian/Ubuntu, `openh264-devel` on RHEL/Fedora)
- pkg-config (build time only)

Most distros ship OpenH264 prebuilt. If absent, building from source is straightforward
(MIT-licensed NASM assembler required for SIMD optimisations).

### FFI configuration

The bindings are generated with `bindgen` in `build.rs`, which probes `openh264`
via `pkg-config` and wraps the C headers:

```rust
// build.rs
pkg_config::Config::new().probe("openh264").unwrap();

bindgen::Builder::default()
    .header_contents("wrapper.h", "
        #include <wels/codec_api.h>
        #include <wels/codec_app_def.h>
        #include <wels/codec_def.h>
        #include <stdlib.h>
        #include <string.h>")
    .generate().unwrap();
```

> **macOS 26 note (for reference, irrelevant on Linux):** when building on macOS,
> a different pointer-to-vtable indirection is needed than on Linux. The macOS
> spec covers this. On Linux the bindings are direct as shown above.

---

## FFI Implementation Sketch

```c
// Session setup
ISVCEncoder *enc = NULL;
WelsCreateSVCEncoder(&enc);

SEncParamExt p;
memset(&p, 0, sizeof(p));
enc->GetDefaultParams(enc, &p);
p.iUsageType         = CAMERA_VIDEO_REAL_TIME;
p.iPicWidth          = W;
p.iPicHeight         = H;
p.fMaxFrameRate      = fps;
p.iRCMode            = RC_OFF_MODE;        // fixed-QP (lowest latency)
p.iMultipleThreadIdc = 1;                  // single-threaded
p.iSpatialLayerNum   = 1;
p.sSpatialLayers[0].iVideoWidth = W;
p.sSpatialLayers[0].iVideoHeight = H;
enc->InitializeExt(enc, &p);

// Per-frame
SSourcePicture pic;
memset(&pic, 0, sizeof(pic));
pic.iColorFormat = videoFormatI420;
pic.iPicWidth = W; pic.iPicHeight = H;
pic.iStride[0] = W; pic.iStride[1] = W/2; pic.iStride[2] = W/2;
pic.pData[0] = yPlane; pic.pData[1] = uPlane; pic.pData[2] = vPlane;

SFrameBSInfo info;
memset(&info, 0, sizeof(info));
enc->EncodeFrame(enc, &pic, &info);

// info.sLayerInfo[i].pBsBuf contains the Annex B NAL units
```

Force keyframe (IDR-on-demand) -- use the dedicated vtable method, NOT SetOption:
```c
// ForceIntraFrame(encoder, bIDR) -- forces the NEXT frame to be IDR (one-shot).
// Do NOT use ENCODER_OPTION_IDR_INTERVAL (that sets periodic IDR interval --
// setting it to 1 makes EVERY frame an IDR, destroying compression).
(*enc)->ForceIntraFrame(enc, true);
```

---

## Performance (Benchmarked)

Verified results from previous benchmark sessions:

| Platform | Resolution | FPS | p50 | p95 | CPU (1 core) |
|----------|-----------|-----|-----|-----|-------------|
| AMD Ryzen 9 5900X (Windows reference) | 1080p | ~226 | 4.3ms | 6.0ms | ~25% |
| AMD Ryzen 5 3600 (macOS Hackintosh) | 1080p | 280 | 3.6ms | 3.7ms | ~25% |
| Intel HD 630 (original Linux dev) | 1080p | ~125 | ~8ms | — | ~25% |

Within budget for 30fps remote control on every machine tested. Slower than libx264
ultrafast, which is the trade the default accepts: no GPL, no ffmpeg subprocess,
and an in-place forced IDR instead of a process respawn.

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-openh264   (the add-on's cdylib)

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

`probe` dlopens `libopenh264.so`, creates and destroys a test encoder to confirm
it is functional, and reads its version and maximum resolution. It reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(),
    codecs: RVec::from(vec![CodecId::H264]),
    caps: AddonCaps(AddonCaps::ENC_CONFIGURABLE), // SetOption changes bitrate, QP,
                                                  //   frame rate and GOP length
                                                  //   without a rebuild
    displays: RVec::new(),
})
```

**Availability is not an error.** A missing or unloadable `libopenh264.so` is
`ROk(ProbeReport { available: false, reason: "libopenh264.so not found" })`,
never an `RErr`. `RErr` is reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` would
cost this add-on every hot parameter change — silently, with no error and no
warning; the pipeline would tear it down and rebuild it for a bitrate change.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); the constructed object's
`caps()` is authoritative and may be a strict subset of this one.

**Profile.** This encoder emits **Constrained Baseline** only
(`VideoProfile::H264ConstrainedBaseline`) — OpenH264's encoder supports no other
profile. `codec()` therefore returns `avc1.42E0LL` with `LL` computed per
MODULE_ABI "Codec-string computation", never a constant.

Pipeline probes (Linux, with this add-on loaded):
```
NVENC / AMF-ROCm / libva HW add-ons available? → use HW
None available?                                 → use OpenH264 (this add-on) — the SW default
This add-on not loaded either?                  → fatal: no encoder
```

`x264` is not in that ladder: it is opt-in, reached only by
`[encode] force_addon = "x264"` (MODULE_ENCODE "Software encoder order").

---

## File Structure

```
addons/encode/openh264/
├── openh264.rs           // Encoder struct, OpenH264Encoder::new
├── ffi.rs                // Rust FFI bindings (built into the add-on cdylib)
├── probe.rs              // the root module's probe() -> ProbeReport
└── tests.rs              // Unit + benchmark tests
```

---

## When to use this add-on

Ship this add-on when:
- Any deployment that may fall back to software — it is the SW default, so
  leaving it out means a host with no working HW encoder has no encoder at all
- Container deployments (no GPU passthrough)
- ARM Linux (Graviton, Ampere) — Cisco's NEON build works well
- Cross-platform single SW codepath wanted (same encoder on Linux + Windows)

Skip when:
- Always have a GPU and a HW encoder add-on installed, and a software fallback is
  not wanted at all (startup step 3f warns that the HW add-on has no SW fallback)
- On macOS — `vt_sw` precedes it in the auto order (Apple-tuned for ARM, faster on
  Apple Silicon)

---

## Status

✅ **Working** — implemented today (Go) as the default SW encoder
(`internal/encode/openh264.go`) on the `feature-libav-vp8s8` branch. The Rust
rewrite lands it as `addons/encode/openh264/`, built as the `openh264` add-on
cdylib, keeping the same encode logic.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_openh264]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements `encode::ConfigurableEncoder` and sets `AddonCaps::ENC_CONFIGURABLE` at probe (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). All updates flow through `update_stream_params(p: stream::Params)`.

| Param change | OpenH264 API | Hot? |
|--------------|--------------|------|
| `FPS` | `ISVCEncoder::SetOption(ENCODER_OPTION_FRAME_RATE, &fps)` | yes |
| `BitrateBps` | `ISVCEncoder::SetOption(ENCODER_OPTION_BITRATE, &b)` | yes |
| `QP` | `ISVCEncoder::SetOption(ENCODER_OPTION_SVC_ENCODE_PARAM_EXT, &param)` | yes |
| `KeyframeInterval` | `param.uiIntraPeriod` via `ENCODER_OPTION_SVC_ENCODE_PARAM_EXT` | yes |
| `Width`, `Height` | requires teardown + `Initialize` (returns `StreamError::RequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | rejected with `StreamError::HdrUnsupported` (OpenH264 is 8-bit only -- pipeline switches to HEVC encoder) | n/a |
