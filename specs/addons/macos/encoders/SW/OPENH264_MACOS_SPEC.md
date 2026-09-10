# macOS Software Encoder Add-On: OpenH264 (Cisco, BSD)

## Purpose

Cisco's OpenH264 H.264 software encoder via a direct Rust FFI binding.
**Identical Rust code to the Linux and Windows OpenH264 add-ons** — one of
FeatherDesk's main cross-platform consistency points.

**This is the software default**, on macOS as on every other OS. When no
VideoToolbox hardware encoder is available, or a hardware encoder degrades
mid-session, `[encode] mode = "auto"` reaches this add-on first, without any
operator action. It is the default rather than the macOS-native `vt_sw` or the
faster `x264` because it is in-process, has no external binary to find or lose,
and can force an IDR in place for the cost of one flag on the next frame (see
MODULE_ENCODE "Software encoder order"). `vt_sw` follows it in the auto order;
`x264` is opt-in only.

This encoder emits **Constrained Baseline** only
(`VideoProfile::H264ConstrainedBaseline`) — OpenH264's encoder supports no other
profile (its *decoder* does). `codec()` therefore returns `avc1.42E0LL` with `LL`
computed from the active resolution and frame rate per
[`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Codec-string
computation"; it is never a constant. There is no `profile` knob: an encoder that
can emit one profile does not get a key to choose among three.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| libopenh264 | BSD-2-Clause | Cisco pays MPEG-LA royalties on behalf of all users |
| Cisco prebuilt binary | BSD-2-Clause | Downloaded from `ciscobinary.openh264.org` |
| Our Rust FFI binding | MIT | In-process, no GPL contamination |

**Key advantage over x264:** No GPL. The main binary stays fully proprietary.
Cisco's royalty arrangement means no H.264 patent fees for the user.

---

## Performance (Benchmarked on Windows, Ryzen 9 5900X)

Cross-platform encoder — same performance characteristics on macOS:

### 1920x1080

| Threads | OpenH264 ms | OpenH264 FPS | x264 ms | x264 FPS | x264 speedup |
|---------|------------|-------------|---------|---------|-------------|
| 1 | 23.4 | 42 | 10.6 | 94 | 2.2x |
| 2 | 13.1 | 77 | 5.6 | 179 | 2.3x |
| **4** | **7.9** | **125** | **4.3** | **234** | **1.9x** |
| 8 | 7.5 | 131 | 3.5 | 285 | 2.2x |
| 12 | 7.4 | 138 | 3.3 | 302 | 2.2x |

### 2560x1440

| Threads | OpenH264 ms | OpenH264 FPS |
|---------|------------|-------------|
| 4 | 13.7 | 73 |
| 12 | 13.4 | 74 |

**Thread scaling saturates at 4 threads** — slice-based parallelism with
diminishing returns beyond 4 slices. On Apple Silicon (M1 = 8 cores,
M2 Pro = 12 cores), 4 threads is the practical ceiling.

---

## Build & Distribution

### Shared library (cdylib)

```bash
cargo build --release -p featherdesk-addon-openh264   # cdylib  featherdesk-addon-openh264.dylib
```

### Runtime dependency

Cisco prebuilt binary downloaded from `ciscobinary.openh264.org`:
```bash
curl -O http://ciscobinary.openh264.org/libopenh264-2.4.1-mac-arm64.dylib.bz2
bunzip2 libopenh264-2.4.1-mac-arm64.dylib.bz2
```

### FFI / link configuration (Rust)

```rust
// build.rs — point the linker at the vendored libopenh264:
//   println!("cargo:rustc-link-search=native=vendor/openh264");
//   println!("cargo:rustc-link-lib=dylib=openh264");
// FFI declarations for <wels/codec_api.h> are generated with `bindgen`
// (include path vendor/openh264/include); the Rust side calls the
// `ISVCEncoder` vtable through an `extern "C"` block.
```

Both the add-on `.dylib` and the vendored `libopenh264.dylib` it links must be
signed — ad-hoc at minimum on Apple Silicon — and the host app needs the
library-validation entitlement before either can be `dlopen`ed. See
[`../../MACOS_SPEC.md`](../../MACOS_SPEC.md) "Add-ons and the hardened runtime".

### Chroma

This add-on accepts `Subsampling::I420` only — Constrained Baseline is 4:2:0 by
definition. A `YuvFrame` carrying `I422` or `I444` returns
`stream::StreamError::ChromaUnsupported`, and the chroma negotiation in
[`specs/core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)
falls the session back to 4:2:0 before it ever reaches `encode`.

### Capabilities declared at probe

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module
surface"):

```rust
// crate: featherdesk-addon-openh264   (cfg(target_os = "macos"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

`probe()` returns `ROk(ProbeReport { available: true, codecs: [CodecId::H264],
caps: AddonCaps(AddonCaps::ENC_CONFIGURABLE), .. })` — bitrate, QP, frame rate
and keyframe interval all change through `SetOption` on the running encoder, so
the hot path never needs a rebuild for them. Availability is not an error: a
missing or unloadable `libopenh264.dylib` is
`ROk(ProbeReport { available: false, reason })`, never an `RErr`. Every bit the
add-on actually serves must be set, and a bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons").

---

## When to use this add-on

Always, unless something specific argues otherwise — it is the software default
and needs no configuration to be reached.

| Scenario | OpenH264 | x264 (opt-in) | VideoToolbox |
|----------|----------|---------------|--------------|
| Commercial deployment | ✅ the default; BSD safe | ❌ GPL | ✅ `vt_hw` for hardware |
| Cross-platform binary consistency | ✅ same code all OSes | ✅ same | ❌ macOS-only |
| No GPU / headless Mac | ✅ first in the SW order | needs `force_addon` | `vt_sw` is second in the SW order |
| Apple Silicon M1+ | ✅ still the default | ~2× faster, but each forced IDR is a process respawn | `vt_hw` is the fast path; `vt_sw` beats OpenH264 on ARM |
| Measured CPU-bound host | worth comparing against | ✅ `[encode] force_addon = "x264"` | — |

---

## Status

📋 **Specced.** Benchmarked on Windows (same cross-platform code); **not measured
on any Apple hardware**, Hackintosh included — a single-frame 3.56ms P50 reading
at 1080p was taken on a Hackintosh in a prior session, which is not a sustained
encode and is not reproducible from this repository.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_openh264]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_openh264]
threads      = 0                  # 0 = auto (min(cpu_count, 4) — saturates at 4)
slice_mode   = "fixed"            # "single" (1 slice) | "fixed" (N slices = N threads)
```

Those two keys are the whole section. There is deliberately **no** `profile` key:
this encoder can emit exactly one profile, so offering a choice among three would
be offering a lie.

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements the `ConfigurableEncoder` trait (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). All updates flow through `update_stream_params(p: stream::Params)`.

| Param change | OpenH264 API | Hot? |
|--------------|--------------|------|
| `fps` | `ISVCEncoder::SetOption(ENCODER_OPTION_FRAME_RATE, &fps)` | yes |
| `bitrate_bps` | `ISVCEncoder::SetOption(ENCODER_OPTION_BITRATE, &b)` | yes |
| `qp` | `ENCODER_OPTION_SVC_ENCODE_PARAM_EXT` | yes |
| `keyframe_interval` | `param.uiIntraPeriod` | yes |
| `width`, `height` | teardown + `Initialize` (returns `StreamError::RequiresRestart`) | no |
| Colour signalling | `SEncParamExt.sSpatialLayers[0]` VUI fields: `bVideoSignalTypePresent = true`, `bColorDescriptionPresent = true`, `bFullRange = false`, `uiColorPrimaries = 1`, `uiTransferCharacteristics = 1`, `uiColorMatrix = 1` — BT.709 limited range, written into the SPS of every keyframe access unit (see MODULE_ENCODE "Colour signalling"). Not a knob | set at `Initialize` |
| `bit_depth=10` / `hdr=true` | rejected with `StreamError::HdrUnsupported` — Constrained Baseline is 8-bit and this encoder emits H.264 only. An HDR session runs on `vt_hw`'s HEVC Main10; the pipeline sends `{"type":"hdr_unavailable","reason":"no_hevc_encoder"}` when no HW encoder is loaded. This add-on never sees 10-bit params on the degrade path either: `degrade_to_software` resets `hdr`/`bit_depth`/`color_space` **before** it builds the software path | n/a |
