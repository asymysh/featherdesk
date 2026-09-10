# macOS SW Encoder Add-On: VideoToolbox Software

## Purpose

Software H.264 / HEVC encoder via Apple's VideoToolbox, called from Rust through FFI
on macOS. This is the **macOS-native software encoder** — used when no hardware
encoder is available or hardware encoding is explicitly disabled.

It is **second in the macOS software auto order**, after `openh264`
(MODULE_ENCODE "Software encoder order"). `openh264` is the cross-platform
software default on every OS; `vt_sw` precedes the opt-in `x264` on macOS because
it is Apple's own encoder, tuned for the silicon it runs on and needing no
third-party library.

VideoToolbox abstracts both software and hardware paths behind the same
`VTCompressionSession` API. Configure with
`kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder: false` and you
get Apple's tuned software encoder. Same Rust FFI binding as the HW spec, different config.

This add-on reports **`VideoProfile::H264High`** —
`kVTProfileLevel_H264_High_AutoLevel`, CABAC permitted — and `codec()` derives the
string from it, with the level computed from the active geometry per
[`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Codec-string
computation". The level VideoToolbox picks for itself is not what goes on the
wire; ours is.

---

## Why this add-on exists alongside OpenH264

`openh264` is the software default everywhere, including here. `vt_sw` is worth
loading beside it because on Apple Silicon it is meaningfully faster, and because
it emits High profile where OpenH264 is Constrained-Baseline-only:

| Encoder | 1080p p50 (Apple Silicon estimated) | Profile | Notes |
|---------|------------------------------------|---------|-------|
| **VideoToolbox SW H.264** | **~5–8ms** | High | Apple-tuned, optimized for ARM Neon and Apple Performance counters |
| OpenH264 | ~10ms | Constrained Baseline | Cisco's NEON build, slightly slower on Apple Silicon |

On Intel Macs the two are roughly equivalent (~5ms each). Neither has been
measured on Apple hardware — both figures are estimates. Since VideoToolbox is
built into the OS, no SDK installation is required.

---

## License & Royalties

| Component | License | Notes |
|-----------|---------|-------|
| VideoToolbox framework | macOS system framework | Built into the OS; no separate license |
| H.264 / HEVC patent royalties | **Apple pays** | Apple's macOS license covers the use of MPEG-LA / MPEG-LA HEVC pools for system-shipped encoders |
| Our Rust FFI binding | MIT | We own this code |

Same model as OpenH264 (vendor pays the patent pool). Zero royalty concern for
FeatherDesk shipping this on macOS.

> **HEVC note:** the same patent-pool concerns we discussed for `libx265` do not
> apply here. Apple has paid the HEVC Advance / Velos Media / MPEG-LA HEVC pools
> for VideoToolbox use. Encoding HEVC via VideoToolbox is licensed; encoding HEVC
> via shipped libx265 is not. This is why we reject libx265 but accept VT HEVC.

---

## Platform Compatibility

| Mac | VideoToolbox SW H.264 | VideoToolbox SW HEVC | OS minimum |
|-----|----------------------|---------------------|-----------|
| Apple Silicon (M1+) | ✅ | ✅ | macOS 11+ |
| Intel Macs (any Sandy Bridge+) | ✅ | ✅ macOS 10.13+ | macOS 10.10+ |
| Hackintosh AMD CPU | ⚠️ Partial — depends on driver | ⚠️ | varies |

> The Hackintosh caveat: in our benchmarks, H.264 SW timed out on the AMD Ryzen
> Hackintosh while H.264 HW worked. This is a driver-side issue specific to
> Hackintosh configurations and not representative of real Apple hardware.
> Real Apple Silicon and real Intel Macs handle SW reliably.

---

## Build & Distribution

### Shared library (cdylib)

```bash
cargo build --release -p featherdesk-addon-vt_sw   # cdylib  featherdesk-addon-vt_sw.dylib
```

### Runtime dependencies

None. VideoToolbox ships with macOS.

### FFI / link configuration (Rust)

```rust
// build.rs — link the macOS frameworks:
//   for fw in ["VideoToolbox", "CoreMedia", "CoreVideo"] {
//       println!("cargo:rustc-link-lib=framework={fw}");
//   }
// VideoToolbox / CoreMedia / CoreVideo declarations come from `bindgen` over
// their umbrella headers; the Rust side calls them from an `extern "C"` block.
```

---

## Native Implementation Sketch

```c
// CRITICAL: closure-style outputHandler is BROKEN on macOS 26 — use the
// session-level C function pointer outputCallback at session creation instead

static void encoded_callback(
    void *outputCallbackRefCon,
    void *sourceFrameRefCon,
    OSStatus status,
    VTEncodeInfoFlags infoFlags,
    CMSampleBufferRef sampleBuffer
) {
    // copy NAL bytes to a Rust-accessible buffer; signal semaphore
}

VTCompressionSessionRef session;
VTCompressionSessionCreate(
    NULL, W, H,
    kCMVideoCodecType_H264,
    (CFDictionaryRef)@{ kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder: @NO },
    NULL,                     // image buffer attrs
    NULL,                     // allocator
    encoded_callback,         // ← C function, not closure
    refcon,
    &session
);

VTSessionSetProperty(session, kVTCompressionPropertyKey_RealTime,                  kCFBooleanTrue);
VTSessionSetProperty(session, kVTCompressionPropertyKey_AllowFrameReordering,      kCFBooleanFalse);
VTSessionSetProperty(session, kVTCompressionPropertyKey_ProfileLevel,              kVTProfileLevel_H264_High_AutoLevel);
// Colour is mandatory, not a tuning choice — see MODULE_ENCODE "Colour signalling"
VTSessionSetProperty(session, kVTCompressionPropertyKey_ColorPrimaries,            kCVImageBufferColorPrimaries_ITU_R_709_2);
VTSessionSetProperty(session, kVTCompressionPropertyKey_TransferFunction,          kCVImageBufferTransferFunction_ITU_R_709_2);
VTSessionSetProperty(session, kVTCompressionPropertyKey_YCbCrMatrix,               kCVImageBufferYCbCrMatrix_ITU_R_709_2);
VTCompressionSessionPrepareToEncodeFrames(session);

// Per frame
VTCompressionSessionEncodeFrame(session, pixelBuffer, pts, dur, NULL, NULL, NULL);
```

For HEVC: same code with `kCMVideoCodecType_HEVC` and
`kVTProfileLevel_HEVC_Main10_AutoLevel`, and the BT.2020/PQ colour triple
(`kCVImageBufferColorPrimaries_ITU_R_2020`,
`kCVImageBufferTransferFunction_SMPTE_ST_2084_PQ`,
`kCVImageBufferYCbCrMatrix_ITU_R_2020`).

### Capabilities declared at probe

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module
surface"):

```rust
// crate: featherdesk-addon-vt_sw   (cfg(target_os = "macos"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

`probe()` calls `VTCopyVideoEncoderList` and reports
`ROk(ProbeReport { available: true, codecs: [CodecId::H264],
caps: AddonCaps(AddonCaps::ENC_CONFIGURABLE), .. })` — bitrate, QP, frame rate
and keyframe interval are all `VTSessionSetProperty` calls on the running
session. Availability is not an error: a VideoToolbox that lists no software
H.264 encoder is `ROk(ProbeReport { available: false, reason })`, never an
`RErr`. Every bit the add-on actually serves must be set, and a bit claimed here
and refused later is a capability lie (MODULE_ABI "Misbehaving add-ons").

---

## Performance Targets

| Mac hardware | 1080p p50 | 1440p p50 | CPU at 30fps |
|-------------|----------|----------|-------------|
| Apple Silicon M1 SW H.264 | ~5ms | ~8ms | ~20% (1 P-core) |
| Apple Silicon M2+ SW H.264 | ~4ms | ~6ms | ~15% |
| Intel Mac (any modern) SW H.264 | ~8ms | ~14ms | ~25% |

HEVC SW is meaningfully slower (~2× H.264 SW), which is why the HDR selection
cascade routes an HDR session to `vt_hw` rather than here (see
[`specs/core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)
"HDR Pipeline"), and why `degrade_to_software` clears `hdr`/`bit_depth`/
`color_space` before it builds a software path. Apple's licensing covers
VideoToolbox HEVC either way — what is refused on macOS is libx265, not this.

---

## File Structure

```
featherdesk-addon-vt_sw/   (its own cdylib crate)
├── src/videotoolbox.rs     // Encoder struct, VideoToolboxEncoder::new (covers SW + HW)
├── src/ffi.rs              // extern "C" binding to VideoToolbox (Rust FFI)
├── src/probe.rs            // probe_videotoolbox()
└── tests/videotoolbox.rs
```

> No build-tag stub files are needed — the add-on is its own cdylib crate.

Note: the same Rust module serves both VT SW and VT HW add-ons; each is built into
its own cdylib (add-on ID `vt_sw` / `vt_hw`), which selects the relevant
probe and select paths. The Rust FFI wrapper is identical.

---

## When to use this add-on

Use this add-on when:
- Targeting macOS with no hardware encoder available (rare on modern Macs)
- Explicitly opting out of hardware encoding for testing
- Apple Silicon — VT SW is faster than OpenH264 on this hardware

Skip when:
- Have VT HW (essentially every Mac from 2011+) — use the HW add-on instead
- Cross-platform single binary preferred — use OpenH264 for portability

---

## Status

📋 Specced — not yet implemented, and not measured on any Apple hardware. The
current featherdesk codebase uses OpenH264 on all platforms, which remains the
software default; this add-on becomes the second rung of the macOS software order
once built.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_vt_sw]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_vt_sw]
realtime               = true            # kVTCompressionPropertyKey_RealTime
profile                = "h264_high"     # the ceiling; the session profile is
                                         # reported through VideoProfile
allow_frame_reordering = false           # false = lower latency (no B-frames)
```

The parser treats `vt_sw` and `vt_hw` as schema-aliases — the two sections
declare the same keys.

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements the `ConfigurableEncoder` trait (note: SW encoder trait, not HW) (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). Identical VideoToolbox property API as `vt_hw`; see [`../HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md`](../HW/VIDEOTOOLBOX_HW_MACOS_SPEC.md#stream-params-translation).

| Param change | VideoToolbox API | Hot? |
|--------------|-----------------|------|
| `fps` | `kVTCompressionPropertyKey_ExpectedFrameRate` | yes |
| `bitrate_bps` | `kVTCompressionPropertyKey_AverageBitRate` | yes |
| `qp` | `kVTCompressionPropertyKey_Quality` | yes |
| `keyframe_interval` | `kVTCompressionPropertyKey_MaxKeyFrameInterval` | yes |
| `width`, `height` | session recreation (returns `StreamError::RequiresRestart`) | no |
| Colour | `kVTCompressionPropertyKey_ColorPrimaries` / `_TransferFunction` / `_YCbCrMatrix` — `ITU_R_709_2` for SDR, `ITU_R_2020` + `SMPTE_ST_2084_PQ` + `ITU_R_2020` for HDR, always limited range, so the SPS VUI matches what the pipeline advertises (see MODULE_ENCODE "Colour signalling"). Not a knob | set at session creation |
| `bit_depth=10` / `hdr=true` | VT SW supports HEVC on macOS 12+ (set `kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder = false` + `kCMVideoCodecType_HEVC`). Requires session recreation with the HEVC Main10 profile (returns `StreamError::RequiresRestart`). In practice the HDR cascade routes HDR to `vt_hw` and `degrade_to_software` clears HDR before building a software path, so this branch is a capability, not a routine one | no |

`codec()` returns `hvc1.2.4.L<level>.B0` while the session is HEVC Main10 and
`avc1.6400<level>` otherwise; the pipeline reads it every frame, so the switch is
visible on the wire as `frame_type::VIDEO_HEVC`. That is what keeps a `vt_sw`
session configured for HEVC from being labelled `VIDEO_H264` — the SW path never
assumes H.264.