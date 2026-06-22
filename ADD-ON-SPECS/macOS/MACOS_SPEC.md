# FeatherDesk — macOS Platform Spec

## Overview

macOS is a secondary target for FeatherDesk. The use case is **remote control only** — not gaming, not high-fps screen sharing. Target: 30fps, <50ms total pipeline latency, H.264 compatibility everywhere.

---

## Capture

### Only One Option: ScreenCaptureKit

macOS 26 (Tahoe) removed every legacy capture API simultaneously:

| API | macOS 26 Status |
|-----|-----------------|
| **ScreenCaptureKit** | ✅ Only option |
| CGDisplayStream | ❌ Removed — compiler error |
| CGWindowListCreateImage | ❌ Removed — compiler error |
| CGDisplayCreateImage | ❌ Removed — compiler error |
| AVCaptureScreenInput | ❌ Removed — compiler error |

All four throw: *"unavailable in macOS: Please use ScreenCaptureKit instead."*

**ScreenCaptureKit minimum requirement:** macOS 12.3 (Monterey). Macs that cannot upgrade past macOS 12 are end-of-support and out of scope.

### SCK Permission Requirements

SCK requires Screen Recording permission granted through System Settings. The binary **must be in a proper app bundle** (with `Info.plist` containing a `CFBundleIdentifier`) for the permission dialog to appear. A raw CLI binary cannot request the permission.

For production deployment: the app bundle must be **code-signed with an Apple Developer ID certificate** and **notarized**. Ad-hoc signing (`codesign -s -`) is sufficient for development but macOS 26 enforces HMAC-signed TCC entries that can only be created through the legitimate permission dialog flow.

### SCK Benchmark Results (Hackintosh: AMD Ryzen 5 3600 + RX 570, macOS 26.5.1)

| Resolution | FPS | p50 | p95 | p99 |
|-----------|-----|-----|-----|-----|
| 2112×1188 (native 2x) | 89.6 | 11.9ms | 14.1ms | 14.1ms |
| 1920×1080 | 91.4 | 10.5ms | 14.1ms | 14.3ms |

**Raw CSV:** `/tmp/fd_bench/cap_sck_native.csv`, `cap_sck_1080p.csv`

> Note: These numbers are from a Hackintosh with a virtual display adapter. A real Apple Silicon Mac will be significantly faster (~2–4ms p50) due to unified memory and tighter display compositor integration.

### SCK Implementation Notes

```swift
// Minimum viable SCK stream setup
let content = try await SCShareableContent.current
let filter = SCContentFilter(display: content.displays.first!, excludingWindows: [])
let cfg = SCStreamConfiguration()
cfg.width = 1920; cfg.height = 1080
cfg.minimumFrameInterval = CMTime(value: 1, timescale: 60) // 60fps cap
cfg.pixelFormat = kCVPixelFormatType_32BGRA
cfg.showsCursor = false  // hardware path: cursor sent separately as CursorUpdate
cfg.queueDepth = 6

let stream = SCStream(filter: filter, configuration: cfg, delegate: self)
try stream.addStreamOutput(self, type: .screen, sampleHandlerQueue: captureQueue)
try await stream.startCapture()
```

Frame delivery: `SCStreamOutput.stream(_:didOutputSampleBuffer:of:)` — one call per frame on the specified queue. Frame is a `CMSampleBuffer` backed by an `IOSurface` (stays on GPU — zero-copy path is possible if the encoder can consume IOSurface directly).

### Zero-Copy Path (macOS)

The gold standard on macOS: `SCStream` → `CMSampleBuffer.imageBuffer` → `IOSurface` → `VTCompressionSession` directly. No CPU readback at any stage. This is what Sunshine does on macOS.

For the software path, `CVPixelBufferLockBaseAddress` on the IOSurface-backed buffer forces a GPU→CPU copy, same as `glReadPixels` on Linux.

---

## Video Encoding

### H.264 Hardware — The Universal Default

**VideoToolbox** abstracts all hardware encoders behind a unified API. The `com.apple.videotoolbox.videoencoder.h264.gva` encoder uses:
- Intel Quick Sync (Sandy Bridge 2011+) on Intel Macs
- AMD VCE via Apple's GVA framework on Intel Macs with AMD discrete GPU
- Apple Media Engine on Apple Silicon

**This works on every Mac that can run macOS 12.3+.** No exceptions. H.264 HW via VideoToolbox is the universal baseline.

### Support Matrix

| Mac Hardware | H.264 HW | HEVC HW | AV1 HW | Notes |
|-------------|---------|---------|--------|-------|
| **Apple Silicon M1** | ✅ | ✅ | ❌ encode | Media engine. AV1 decode only on M1. |
| **Apple Silicon M2+** | ✅ | ✅ | ✅ | AV1 hardware encode added in M2. |
| **Intel + AMD discrete (2016+)** | ✅ AMD VCE | ✅ AMD VCE | ❌ | e.g. MBP 15" 2019, iMac 27" |
| **Intel integrated only, Skylake+ (2015–16+)** | ✅ Quick Sync | ✅ Quick Sync | ❌ | MacBook Air 2017, Mac mini 2018 |
| **Intel integrated only, Haswell (2014)** | ✅ Quick Sync | ❌ | ❌ | Mac mini 2014, MBA 2013–14 |
| **Intel integrated only, Sandy/Ivy Bridge (2011–12)** | ✅ Quick Sync | ❌ | ❌ | Mac mini 2011–12 |
| **Pre-2011 (Core 2 Duo, Nvidia 320M)** | ❌ | ❌ | ❌ | Cannot run macOS 12.3+ anyway |

### Codec Decision (Remote Control Use Case)

```
Primary:  H.264 HW via VideoToolbox  ← works everywhere, browser-native decode
Optional: AV1 HW via VideoToolbox    ← M2+ only, announce via Config handshake
Skip:     HEVC HW                    ← WebCodecs HEVC support patchy in browsers
Skip:     VP8/VP9                    ← no HW encode path on macOS, not worth SW cost
```

H.264 at 30fps for remote control is entirely adequate. AV1 on M2+ is a future quality improvement, not a priority.

### VideoToolbox Benchmark Results (Hackintosh: AMD Ryzen 5 3600 + RX 570, macOS 26.5.1)

| Encoder | Resolution | FPS | p50 | p95 | p99 |
|---------|-----------|-----|-----|-----|-----|
| **H.264 HW (AMD GVA/VCE)** | 1920×1080 | **115** | 8.6ms | 8.9ms | 9.0ms |
| **H.264 HW (AMD GVA/VCE)** | 2560×1440 | **71** | 13.9ms | 14.1ms | 14.2ms |
| HEVC SW | 1920×1080 | 27 | 30.5ms | 76ms | 156ms |
| HEVC SW | 2560×1440 | 18 | 46ms | 131ms | 250ms |
| H.264 SW | any | ❌ | — | — | Hackintosh driver gap |
| HEVC HW | any | ❌ | — | — | Hackintosh driver gap |

**M1 Mac estimates (literature + extrapolation):**

| Encoder | 1080p p50 | 1440p p50 |
|---------|----------|----------|
| H.264 HW (Media Engine) | ~2–3ms | ~3–4ms |
| HEVC HW (Media Engine) | ~2–3ms | ~3–4ms |
| AV1 HW (M2+) | ~3–5ms | ~4–6ms |

**Raw CSVs:** `/tmp/fd_bench/enc_h264_hw_*.csv`, `enc_hevc_sw_*.csv`

### VideoToolbox API (C-callback style — closure API broken in macOS 26)

```swift
// Use traditional C-callback approach. The per-frame closure variant
// (VTCompressionSessionEncodeFrame with outputHandler: closure) does NOT
// fire its callback in macOS 26. Use the session-level outputCallback instead.

let vtCallback: VTCompressionOutputCallback = { refcon, _, _, _, sb in
    guard let ptr = refcon else { return }
    let ctx = Unmanaged<EncContext>.fromOpaque(ptr).takeUnretainedValue()
    // process CMSampleBuffer
    ctx.sema.signal()
}

VTCompressionSessionCreate(
    allocator: nil, width: Int32(w), height: Int32(h),
    codecType: kCMVideoCodecType_H264,
    encoderSpecification: [
        kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder: true
    ] as CFDictionary,
    imageBufferAttributes: nil,
    compressedDataAllocator: nil,
    outputCallback: vtCallback,    // ← set here, not per-frame
    refcon: ctxPtr,
    compressionSessionOut: &session
)
VTSessionSetProperty(session, key: kVTCompressionPropertyKey_RealTime, value: kCFBooleanTrue)
VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
```

---

## Audio

| API | Status | Notes |
|-----|--------|-------|
| **CoreAudio loopback** | ✅ Primary path | Capture system audio output via virtual aggregate device or tap |
| **BlackHole** | Optional | Third-party virtual audio device for loopback (common on macOS) |
| **ScreenCaptureKit audio** | ✅ Built-in | SCK can capture audio alongside video with `capturesAudio = true` — simplest path |

**Recommended:** Use SCK's built-in audio capture (`SCStreamConfiguration.capturesAudio = true`). This captures system audio automatically, no virtual audio device needed. PCM output format from SCK: Float32, 48kHz, stereo.

---

## Input Injection

| API | Status | Notes |
|-----|--------|-------|
| **CGEvent** | ✅ Primary | `CGEventCreateKeyboardEvent`, `CGEventCreateMouseEvent`. Requires Accessibility permission. |
| **IOKit HID** | Optional | Lower-level, game controller support |

```swift
// Mouse move
let event = CGEvent(mouseEventSource: nil, mouseType: .mouseMoved,
                    mouseCursorPosition: CGPoint(x: x, y: y), mouseButton: .left)
event?.post(tap: .cghidEventTap)

// Key press
let keyDown = CGEvent(keyboardEventSource: nil, virtualKey: CGKeyCode(keyCode), keyDown: true)
keyDown?.post(tap: .cghidEventTap)
```

**Requires:** Accessibility permission (System Settings → Privacy → Accessibility). Separate from Screen Recording permission.

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum macOS | **12.3 (Monterey)** — SCK minimum |
| Code signing | **Apple Developer ID** — required for SCK TCC permission |
| Notarization | Required for distribution outside Mac App Store |
| Permissions | Screen Recording + Accessibility |
| Architecture | Universal binary (arm64 + x86_64) recommended |
| App bundle | Required — raw CLI binary cannot request SCK permission |

---

## Known macOS 26 Behavioral Changes

1. **All legacy capture APIs removed** — CGDisplayStream, CGWindowListCreateImage, CGDisplayCreateImage, AVCaptureScreenInput all return compile errors
2. **TCC HMAC enforcement** — TCC database manipulation (sqlite3 injection) does not grant permissions in macOS 26; only native permission dialogs work
3. **VTCompressionSession per-frame closure broken** — the `outputHandler:` closure variant of `VTCompressionSessionEncodeFrame` never fires its callback in macOS 26; use the session-level `outputCallback` C function pointer instead
4. **SCK requires GUI session** — process must be running in the user's window server session; background daemons cannot capture even with TCC permission
