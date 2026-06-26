# macOS HW Encoder Add-On: VideoToolbox Hardware

## Purpose

Hardware-accelerated H.264 / HEVC encoder via Apple's VideoToolbox,
called from Go through CGo. Routes to the appropriate hardware encoder on every
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
| Our CGo binding | MIT |

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
add-on advertises in `FrameTypeConfig` the codec it actually picked.

---

## Build & Distribution

### Go build tag

```bash
go build -tags vt_hw -o viewport-rds-macos-vt-hw ./cmd/server
```

### Runtime dependencies

None. VideoToolbox ships with macOS.

### CGo configuration

```go
/*
#cgo CFLAGS: -fmodules
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreVideo -framework Metal

#include <VideoToolbox/VideoToolbox.h>
#include <CoreMedia/CoreMedia.h>
#include <CoreVideo/CoreVideo.h>
#include <Metal/Metal.h>
*/
import "C"
```

---

## CGo Implementation Sketch

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
VTSessionSetProperty(session, kVTCompressionPropertyKey_ProfileLevel,         kVTProfileLevel_H264_Baseline_3_1);
VTCompressionSessionPrepareToEncodeFrames(session);

// For HEVC:  kCMVideoCodecType_HEVC + kVTProfileLevel_HEVC_Main_AutoLevel
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

---

## Codec Strings (WebCodecs Config Handshake)

```json
{ "codec": "avc1.42E01F" }    // H.264 Constrained Baseline Level 3.1
{ "codec": "hvc1.1.6.L93.B0" } // HEVC Main Profile Level 3.1
// AV1 encode not available on any Apple Silicon -- removed from codec strings
```

Server announces whichever codec it selected; client configures VideoDecoder
from this string.

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
internal/encode/vt/
├── videotoolbox.go        // shared with vt_sw add-on
├── videotoolbox_cgo.go    // CGo binding (build tag: vt_hw)
├── videotoolbox_stub.go   // (build tag: !vt_sw,!vt_hw)
├── probe.go               // ProbeVideoToolbox() — enumerates encoders, advertises codecs
└── videotoolbox_test.go
```

Same `internal/encode/vt/` package as the VT SW add-on; build tags decide
which paths compile in.

---

## Probe & Selection

```go
//go:build vt_hw

func ProbeVideoToolboxHW() (*VTHWCapabilities, error) {
    // 1. VTCopyVideoEncoderList → enumerate available encoders
    // 2. Look for "*.gva" suffix entries (= hardware-accelerated)
    // 3. Per codec: H.264, HEVC
    // 4. Return supported codecs + max resolution
}
```

Pipeline probes (macOS, this add-on compiled in):
```
VT HW supports HEVC? → pick HEVC (announce hvc1.1.6.L93.B0 in Config)
VT HW supports H.264? → pick H.264 (announce avc1.42E01F)
Neither?              → fall through to VT SW or OpenH264 CGo add-on
```

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

📋 Specced — not yet implemented. The current featherdesk codebase has no macOS
target. This add-on becomes the macOS HW path during the platform port.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_vt_hw]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements `stream.ConfigurableHardwareEncoder` (see [`../../../../specs/MODULE_STREAM_PARAMS.md`](../../../../specs/MODULE_STREAM_PARAMS.md)). VideoToolbox has partial hot-reconfiguration support -- some properties can be set mid-session, but profile/resolution changes require full session invalidation + recreation.

| Param change | VideoToolbox API | Hot? |
|--------------|-----------------|------|
| `FPS` | `kVTCompressionPropertyKey_ExpectedFrameRate` via `VTSessionSetProperty` | yes |
| `BitrateBps` | `kVTCompressionPropertyKey_AverageBitRate` via `VTSessionSetProperty` | yes |
| `QP` | `kVTCompressionPropertyKey_Quality` via `VTSessionSetProperty` | yes |
| `KeyframeInterval` | `kVTCompressionPropertyKey_MaxKeyFrameInterval` via `VTSessionSetProperty` | yes |
| `Width`, `Height` | `VTCompressionSessionInvalidate` + recreate session (returns `stream.ErrRequiresRestart`) | no |
| `BitDepth=10` / `HDR=true` | `kVTProfileLevel_HEVC_Main10_AutoLevel` -- requires HEVC codec + session recreation (returns `stream.ErrRequiresRestart`) | no |
| `NetworkRTTMs`, `PacketLossPct` | Used to adjust `kVTCompressionPropertyKey_AverageBitRate` headroom | yes |

**macOS 26 note:** Use C function pointer `outputCallback` at `VTCompressionSessionCreate` -- per-frame closure is broken on macOS 26.