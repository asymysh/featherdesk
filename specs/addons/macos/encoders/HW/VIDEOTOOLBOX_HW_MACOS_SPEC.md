# macOS HW Encoder Add-On: VideoToolbox Hardware

## Purpose

Hardware-accelerated H.264 / HEVC encoder via Apple's VideoToolbox,
called from Rust through FFI. Routes to the appropriate hardware encoder on every
Mac platform:

| Mac hardware | Hardware encoder used |
|-------------|----------------------|
| Apple Silicon M1 | Apple Media Engine (`.gva` H.264 + HEVC) |
| Apple Silicon M1+ | Apple Media Engine (H.264 + HEVC). **No AV1 HW encode on any Apple Silicon.** |
| Intel Mac + AMD discrete GPU | AMD VCE via Apple GVA framework |
| Intel Mac integrated only | Intel Quick Sync via Apple GVA framework |

Same VideoToolbox API for all four. The driver picks the right hardware at runtime.

---

## License & Royalties

Same as VideoToolbox SW spec — Apple covers all patent pool royalties for
system-shipped encoders (MPEG-LA H.264, HEVC Advance, Velos Media, MPEG-LA HEVC,
AOMedia AV1).

| Component | License |
|-----------|---------|
| VideoToolbox framework | macOS system framework, no separate license |
| H.264 / HEVC / AV1 royalties | Apple pays |
| Our Rust FFI binding | MIT |

---

## Hardware Compatibility Matrix

| Mac | H.264 HW | HEVC HW | AV1 HW |
|-----|---------|---------|--------|
| Apple Silicon M1/M2 | ✅ | ✅ | ❌ (no AV1 HW encode on any Apple Silicon) |
| Apple Silicon M3/M4 | ✅ | ✅ | ❌ encode (M3+ has AV1 HW **decode** only) |
| Intel + AMD discrete (2016+) | ✅ AMD VCE | ✅ AMD VCE | ❌ |
| Intel integrated Skylake+ (2015+) | ✅ QSV | ✅ QSV | ❌ |
| Intel integrated Haswell (2014) | ✅ QSV | ❌ | ❌ |
| Intel integrated Sandy/Ivy Bridge (2011-12) | ✅ QSV | ❌ | ❌ |

Runtime probe via `VTCopyVideoEncoderList` returns the available encoders. The
add-on reports the codec it actually configured through `codec()`, and the
pipeline puts that string in the `config` control-stream message.

> The encoder advertises **H.264** (`avc1.*`) for every SDR session, on every
> platform, regardless of what HEVC hardware is present. **HEVC Main10**
> (`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
> H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's
> WebCodecs cannot decode it, and a codec no attached client can decode is a
> black screen, not a saving. Which HW encoder is *selected* is a separate
> question from which codec it *emits* — the probe order picks the add-on, this
> rule picks the codec.

---

## Build & Distribution

### Shared library (cdylib)

```bash
cargo build --release -p featherdesk-addon-vt_hw   # cdylib  featherdesk-addon-vt_hw.dylib
```

### Runtime dependencies

None. VideoToolbox ships with macOS.

### FFI / link configuration (Rust)

```rust
// build.rs — link the macOS frameworks (and compile any Obj-C glue):
//   for fw in ["VideoToolbox", "CoreMedia", "CoreVideo", "Metal"] {
//       println!("cargo:rustc-link-lib=framework={fw}");
//   }
// VideoToolbox / CoreMedia / CoreVideo / Metal declarations come from `bindgen`
// over their umbrella headers (or the `core-video`/`objc2` crates); the Rust
// side calls them from an `extern "C"` block.
```

---

## Native Implementation Sketch

```c
VTCompressionSessionRef session;

// H.264 HW
VTCompressionSessionCreate(
    NULL, W, H,
    kCMVideoCodecType_H264,
    (CFDictionaryRef)@{
        kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder: @YES
    },
    NULL, NULL,
    encoded_callback,
    refcon,
    &session
);
VTSessionSetProperty(session, kVTCompressionPropertyKey_RealTime,             kCFBooleanTrue);
VTSessionSetProperty(session, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);
VTSessionSetProperty(session, kVTCompressionPropertyKey_ProfileLevel,         kVTProfileLevel_H264_High_AutoLevel);
// Colour is mandatory, not a tuning choice — see MODULE_ENCODE "Colour signalling"
VTSessionSetProperty(session, kVTCompressionPropertyKey_ColorPrimaries,       kCVImageBufferColorPrimaries_ITU_R_709_2);
VTSessionSetProperty(session, kVTCompressionPropertyKey_TransferFunction,     kCVImageBufferTransferFunction_ITU_R_709_2);
VTSessionSetProperty(session, kVTCompressionPropertyKey_YCbCrMatrix,          kCVImageBufferYCbCrMatrix_ITU_R_709_2);
VTCompressionSessionPrepareToEncodeFrames(session);

// For an HDR session: kCMVideoCodecType_HEVC + kVTProfileLevel_HEVC_Main10_AutoLevel,
//   with ITU_R_2020 primaries, the SMPTE_ST_2084_PQ transfer and the ITU_R_2020 matrix
// AV1: NOT available via HW encode on any Apple Silicon (decode only on M3+)
```

### Zero-copy path from ScreenCaptureKit

```c
// SCStreamOutput delivers CMSampleBuffer with IOSurface-backed CVPixelBuffer
// Pass it directly — VT consumes IOSurface zero-copy
CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(sampleBuffer);
VTCompressionSessionEncodeFrame(session, pb, pts, dur, NULL, NULL, NULL);
```

This is the canonical macOS streaming pipeline: SCK → IOSurface → VTCompressionSession,
no CPU pixel copy at any stage. Sunshine's macOS path does exactly this.

The surface arrives upright: ScreenCaptureKit composites display rotation itself
and `sck` always reports `Rotation::R0`, so this encoder's VPP never has a
rotation to apply and never returns `StreamError::FallbackToSoftware` for one
(see [`specs/media/MODULE_CAPTURE.md`](../../../../media/MODULE_CAPTURE.md)
"Display rotation").

---

## Codec Strings (WebCodecs Config Handshake)

`codec()` is **computed**, never a constant: this add-on calls
`featherdesk_abi::codec_string(active_profile, p.width, p.height, p.fps)` after
construction and after every successful `update_stream_params`, so the advertised
profile and level always match what is actually being encoded (see
[`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Codec-string
computation"). The configured profile is `VideoProfile::H264High` for an SDR
session and `VideoProfile::HevcMain10` for an HDR one. At 1080p60 that is:

```json
{ "codec": "avc1.64002A" }      // H.264 High, Level 4.2 — every SDR session
{ "codec": "hvc1.2.4.L123.B0" } // HEVC Main10, Level 4.1 — HDR sessions only
// AV1 encode is not available on any Apple Silicon -- no av01.* string is emitted
```

A different resolution or frame rate produces a different level, so neither
string may be quoted as a constant. The server announces whichever codec it
selected; the client configures `VideoDecoder` from that string.

---

## Performance Targets

**Measured (Hackintosh AMD Ryzen 5 3600 + RX 570):**

| Encoder | Resolution | FPS | p50 |
|---------|-----------|-----|-----|
| H.264 HW (AMD VCE) | 1080p | 115 | 8.6ms |
| H.264 HW (AMD VCE) | 1440p | 71 | 13.9ms |
| HEVC HW | any | ❌ | Hackintosh driver gap |

**Estimated on real Apple hardware:**

| Mac | H.264 HW 1080p p50 | HEVC HW 1080p p50 | AV1 HW 1080p p50 |
|-----|-------------------|-------------------|-----------------|
| M1 | ~2–3ms | ~2–3ms | — |
| M2 | ~2ms | ~2ms | ~3ms |
| M3 Pro/Max | ~1.5ms | ~1.5ms | ~2.5ms |
| Intel + AMD discrete | ~5–8ms | ~5–8ms | — |
| Intel integrated Skylake+ | ~5ms | ~5ms | — |

Real Apple hardware is significantly faster than Hackintosh due to direct media
engine integration, unified memory, and no driver translation layer.

---

## VTCompressionSession macOS 26 Gotcha

`VTCompressionSessionEncodeFrame` with an inline `outputHandler:` closure (the
modern Swift-friendly API variant) **never fires its callback** on macOS 26.
Confirmed in our debugging sessions.

**Use the C function pointer `outputCallback` parameter at session creation time**
(set in `VTCompressionSessionCreate`). This is the older API that still works
reliably. Both H.264 and HEVC are affected.

Documented separately in [`../../MACOS_SPEC.md`](../../MACOS_SPEC.md) and
[`../SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](../SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md).

---

## File Structure

```
featherdesk-addon-vt_hw/   (its own cdylib crate)
├── src/videotoolbox.rs     // shared module with the vt_sw add-on
├── src/ffi.rs              // extern "C" binding to VideoToolbox (Rust FFI)
├── src/probe.rs            // probe_videotoolbox() — enumerates encoders, advertises codecs
└── tests/videotoolbox.rs
```

> No build-tag stub files are needed — the add-on is its own cdylib crate.

Shares the `videotoolbox` module with the VT SW add-on (a common dependency
crate); each variant is built into its own cdylib (`featherdesk-addon-vt_hw.dylib` /
`featherdesk-addon-vt_sw.dylib`).

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../../core/MODULE_ABI.md) "Root module
surface"):

```rust
// crate: featherdesk-addon-vt_hw   (cfg(target_os = "macos"))

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
    // 1. VTCopyVideoEncoderList → enumerate the available encoders
    // 2. Look for "*.gva" suffix entries (= hardware-accelerated)
    // 3. h264.gva present → codecs.push(CodecId::H264)
    //    hevc.gva present → codecs.push(CodecId::Hevc)  (this is what makes HDR
    //                                                    available at all)
    // 4. No .gva entry → ROk(ProbeReport { available: false,
    //      reason: "no hardware video encoder in VTCopyVideoEncoderList".into(),
    //      codecs: RVec::new(), caps: AddonCaps(0), displays: RVec::new() })
    // 5. Otherwise → ROk(ProbeReport {
    //      available: true, reason: RString::new(), codecs,
    //      caps: AddonCaps(AddonCaps::ENC_CONFIGURABLE), // bitrate/QP/fps/GOP are
    //                                                    // VTSessionSetProperty calls
    //      displays: RVec::new() })
}
```

**Availability is not an error.** A Mac whose VideoToolbox lists no `.gva`
encoder is `ROk(ProbeReport { available: false, reason })`, never an `RErr`.

**Set every capability bit this add-on actually serves.** `caps` left at `0` here
means every parameter change tears the session down and rebuilds it, silently.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); the constructed object's
`caps()` is authoritative and may be a strict subset of this one.

Pipeline probes (macOS, this add-on loaded):
```
VT HW available? → vt_hw; codec = H.264 unless Params.hdr, then HEVC Main10
Neither?         → fall through to the SW order (openh264 → vt_sw)
```

`hevc.gva`'s presence decides whether an HDR session can be entered at all — it
never decides the codec for an SDR one.

---

## When to use this add-on

Use this add-on when:
- Targeting any Mac that can run macOS 12.3+ (every M-series and most Intel)
- Want zero-copy SCK → IOSurface → VT pipeline
- AV1 HW encode: not available on any current Apple Silicon

Skip when:
- Building a SW-only test binary
- Targeting Macs older than 2011 (out of scope anyway — can't run modern macOS)

---

## Status

📋 Specced — not yet implemented, and measured only on a Hackintosh whose raw
data lives outside this repository (see "Performance Targets"). The current
featherdesk codebase has no macOS target; this add-on becomes the macOS HW path
during the platform port.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_vt_hw]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_vt_hw]
realtime               = true            # kVTCompressionPropertyKey_RealTime
profile                = "h264_high"     # the ceiling; the session profile is
                                         # H264High for SDR and HevcMain10 for HDR
                                         # (MODULE_ENCODE "Profile")
allow_frame_reordering = false           # false = lower latency (no B-frames)
```

The parser treats `vt_hw` and `vt_sw` as schema-aliases — the two sections
declare the same keys.

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.


---

## Stream Params Translation

This add-on implements the `ConfigurableHardwareEncoder` trait (see [`../../../../core/MODULE_STREAM_PARAMS.md`](../../../../core/MODULE_STREAM_PARAMS.md)). VideoToolbox has partial hot-reconfiguration support -- some properties can be set mid-session, but profile/resolution changes require full session invalidation + recreation.

| Param change | VideoToolbox API | Hot? |
|--------------|-----------------|------|
| `fps` | `kVTCompressionPropertyKey_ExpectedFrameRate` via `VTSessionSetProperty` | yes |
| `bitrate_bps` | `kVTCompressionPropertyKey_AverageBitRate` via `VTSessionSetProperty` | yes |
| `qp` | `kVTCompressionPropertyKey_Quality` via `VTSessionSetProperty` | yes |
| `keyframe_interval` | `kVTCompressionPropertyKey_MaxKeyFrameInterval` via `VTSessionSetProperty` | yes |
| `width`, `height` | `VTCompressionSessionInvalidate` + recreate session (returns `StreamError::RequiresRestart`) | no |
| Colour | `kVTCompressionPropertyKey_ColorPrimaries` / `_TransferFunction` / `_YCbCrMatrix` — `ITU_R_709_2` throughout for SDR, `ITU_R_2020` + `SMPTE_ST_2084_PQ` + `ITU_R_2020` for HDR, always limited range. VideoToolbox converts RGB→YUV inside the encoder, so this is the only place the matrix is chosen; a session that cannot be made to emit BT.709 returns `StreamError::Backend` at construction rather than shipping a mislabelled stream (see MODULE_ENCODE "Colour signalling") | set at session creation |
| `bit_depth=10` / `hdr=true` | `kVTProfileLevel_HEVC_Main10_AutoLevel` -- requires the HEVC codec + session recreation (returns `StreamError::RequiresRestart`), and is available only where `hevc.gva` is in `VTCopyVideoEncoderList`. `codec()` then reports `hvc1.2.4.L<level>.B0` | no |
| `network_rtt_ms`, `packet_loss_pct` | Used to adjust `kVTCompressionPropertyKey_AverageBitRate` headroom | yes |
| Rotation | Never applied: `sck` composites display rotation and always reports `Rotation::R0`, so every `FbInfo` handed to `encode_surface` is already upright | n/a (never rotated) |

**macOS 26 note:** Use C function pointer `outputCallback` at `VTCompressionSessionCreate` -- per-frame closure is broken on macOS 26.