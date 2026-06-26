# macOS SW Encoder Add-On: VideoToolbox Software

## Purpose

Software H.264 / HEVC encoder via Apple's VideoToolbox, called from Go through CGo
on macOS. This is the **macOS-native software fallback** — used when no GPU is
available, no hardware MFT is registered, or hardware encoding is explicitly disabled.

VideoToolbox abstracts both software and hardware paths behind the same
`VTCompressionSession` API. Configure with
`kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder: false` and you
get Apple's tuned software encoder. Same CGo binding as the HW spec, different config.

---

## Why use this over OpenH264 CGo on macOS

OpenH264 CGo works on macOS too (we verified during benchmark sessions), but
VideoToolbox SW is preferable when targeting Apple Silicon:

| Encoder | 1080p p50 (Apple Silicon estimated) | Notes |
|---------|------------------------------------|-------|
| **VideoToolbox SW H.264** | **~5–8ms** | Apple-tuned, optimized for ARM Neon and Apple Performance counters |
| OpenH264 CGo | ~10ms | Cisco's NEON build, slightly slower on Apple Silicon |

On Intel Macs the two are roughly equivalent (~5ms each). On Apple Silicon, VT SW
wins meaningfully. Since VideoToolbox is built into the OS, no SDK installation
is required.

---

## License & Royalties

| Component | License | Notes |
|-----------|---------|-------|
| VideoToolbox framework | macOS system framework | Built into the OS; no separate license |
| H.264 / HEVC patent royalties | **Apple pays** | Apple's macOS license covers the use of MPEG-LA / MPEG-LA HEVC pools for system-shipped encoders |
| Our CGo binding | MIT | We own this code |

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

### Go build tag

```bash
go build -tags vt_sw -o viewport-rds-macos-vt-sw ./cmd/server
```

### Runtime dependencies

None. VideoToolbox ships with macOS.

### CGo configuration

```go
/*
#cgo CFLAGS: -fmodules
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreVideo

#include <VideoToolbox/VideoToolbox.h>
#include <CoreMedia/CoreMedia.h>
#include <CoreVideo/CoreVideo.h>
*/
import "C"
```

---

## CGo Implementation Sketch

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
    // copy NAL bytes to Go-accessible buffer; signal semaphore
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
VTSessionSetProperty(session, kVTCompressionPropertyKey_ProfileLevel,              kVTProfileLevel_H264_Baseline_3_1);
VTCompressionSessionPrepareToEncodeFrames(session);

// Per frame
VTCompressionSessionEncodeFrame(session, pixelBuffer, pts, dur, NULL, NULL, NULL);
```

For HEVC: same code with `kCMVideoCodecType_HEVC` and `kVTProfileLevel_HEVC_Main_AutoLevel`.

---

## Performance Targets

| Mac hardware | 1080p p50 | 1440p p50 | CPU at 30fps |
|-------------|----------|----------|-------------|
| Apple Silicon M1 SW H.264 | ~5ms | ~8ms | ~20% (1 P-core) |
| Apple Silicon M2+ SW H.264 | ~4ms | ~6ms | ~15% |
| Intel Mac (any modern) SW H.264 | ~8ms | ~14ms | ~25% |

HEVC SW is meaningfully slower (~2× H.264 SW). Avoid HEVC SW for real-time streaming
— if HEVC is requested and no HW path is available, fall back to H.264 SW instead.

---

## File Structure

```
internal/encode/videotoolbox/
├── videotoolbox.go        // Encoder struct, NewVideoToolboxEncoder (covers SW + HW)
├── videotoolbox_cgo.go    // CGo binding (build tag: vt_sw OR vt_hw)
├── videotoolbox_stub.go   // No-op stub (build tag: !vt_sw,!vt_hw)
├── probe.go               // ProbeVideoToolbox()
└── videotoolbox_test.go
```

Note: the same Go file serves both VT SW and VT HW add-ons. The build tags
(`vt_sw`, `vt_hw`) enable the relevant probe and select paths at compile time.
The CGo wrapper is identical.

---

## When to use this add-on

Use this add-on when:
- Targeting macOS with no hardware encoder available (rare on modern Macs)
- Explicitly opting out of hardware encoding for testing
- Apple Silicon — VT SW is faster than OpenH264 CGo on this hardware

Skip when:
- Have VT HW (essentially every Mac from 2011+) — use the HW add-on instead
- Cross-platform single binary preferred — use OpenH264 CGo for portability

---

## Status

📋 Specced — not yet implemented. The current featherdesk codebase uses OpenH264
CGo on all platforms. This add-on will be the macOS-preferred SW encoder once built.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_vt_sw]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary`;` unknown
keys in this section will cause startup to fail.

