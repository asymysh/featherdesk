# FeatherDesk — macOS Platform Spec

## Overview

macOS is a secondary target for FeatherDesk. The use case is **remote control only** — not gaming, not high-fps screen sharing. Target: 30fps, <50ms total pipeline latency, H.264 compatibility everywhere.

---

## Capture

### Every capture backend is an add-on (pluggable architecture)

The macOS default binary contains **no capture backends**. Capture is an
add-on shared library, mirroring the Linux structure for architectural symmetry.
In practice, every real macOS deployment will load the `sck` add-on —
ScreenCaptureKit is the only supported capture path on macOS 12.3+.

```
capture/
├── SCK_MACOS_SPEC.md   ← the only macOS capture add-on
└── README.md           ← context on why no alternatives exist
```

### Add-on summary

| Add-on | Add-on ID | Spec | When to use |
|--------|-----------|------|------------|
| **ScreenCaptureKit (SCK)** | `sck` | [`capture/SCK_MACOS_SPEC.md`](./capture/SCK_MACOS_SPEC.md) | Always — the only supported macOS capture API |

### Why no other capture add-ons exist

macOS 26 (Tahoe) removed every legacy capture API simultaneously:

| API | macOS 26 Status |
|-----|-----------------|
| **ScreenCaptureKit** | ✅ Only option |
| CGDisplayStream | ❌ Removed — compiler error |
| CGWindowListCreateImage | ❌ Removed — compiler error |
| CGDisplayCreateImage | ❌ Removed — compiler error |
| AVCaptureScreenInput | ❌ Removed — compiler error |

All four throw: *"unavailable in macOS: Please use ScreenCaptureKit instead."*
**ScreenCaptureKit minimum requirement: macOS 12.3 (Monterey).** Macs that
cannot upgrade past macOS 12 are end-of-support and out of scope.

There is also no vendor fragmentation on macOS — Apple controls the entire
graphics stack from Metal up, so there's no NVIDIA/AMD/Intel-specific capture
path to add as an alternative.

### Runtime probe order

Effectively collapses to a single check:

```
1. sck loaded AND Screen Recording TCC granted? → use SCK
2. Otherwise                                     → fatal: no capture
```

Full details — permission requirements, app-bundle requirement, HMAC-signed
TCC enforcement on macOS 26, Hackintosh benchmark numbers, zero-copy
IOSurface → VideoToolbox path, and implementation sketch — are in
[`capture/SCK_MACOS_SPEC.md`](./capture/SCK_MACOS_SPEC.md).

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
| **Apple Silicon M2** | ✅ | ✅ | ❌ encode | No AV1 encode on any Apple Silicon. M2 has no AV1 decode either. |
| **Apple Silicon M3+** | ✅ | ✅ | ❌ encode | M3+ adds AV1 HW **decode** only. No Apple Silicon has AV1 HW encode. |
| **Intel + AMD discrete (2016+)** | ✅ AMD VCE | ✅ AMD VCE | ❌ | e.g. MBP 15" 2019, iMac 27" |
| **Intel integrated only, Skylake+ (2015–16+)** | ✅ Quick Sync | ✅ Quick Sync | ❌ | MacBook Air 2017, Mac mini 2018 |
| **Intel integrated only, Haswell (2014)** | ✅ Quick Sync | ❌ | ❌ | Mac mini 2014, MBA 2013–14 |
| **Intel integrated only, Sandy/Ivy Bridge (2011–12)** | ✅ Quick Sync | ❌ | ❌ | Mac mini 2011–12 |
| **Pre-2011 (Core 2 Duo, Nvidia 320M)** | ❌ | ❌ | ❌ | Cannot run macOS 12.3+ anyway |

### Every encoder is an add-on (pluggable architecture)

The macOS default binary contains **no encoders**. Every encoder is an add-on
shared library. The full set:

```
encoders/
├── SW/
│   ├── OPENH264_MACOS_SPEC.md     ← BSD-licensed Cisco SW — the software DEFAULT, cross-platform
│   ├── X264_SUBPROCESS_MACOS_SPEC.md  ← GPL-isolated x264 subprocess (opt-in, force_addon only)
│   └── VIDEOTOOLBOX_SW_MACOS_SPEC.md  ← Apple's tuned SW H.264/HEVC (macOS-native)
└── HW/
    └── VIDEOTOOLBOX_HW_MACOS_SPEC.md  ← unified HW: Intel QS + AMD VCE + Apple Media Engine
```

See [`encoders/README.md`](./encoders/README.md) for recommended combinations.

### TL;DR — recommended combinations

| Deployment | Add-ons | Why |
|-----------|---------|-----|
| Any Mac (default) | `openh264` + `vt_sw` + `vt_hw` | `openh264` is the software default on every OS; `vt_sw` is the macOS-native second rung; `vt_hw` covers every Mac |
| Apple Silicon (peak HW) | `vt_hw` only | Apple Media Engine is the entire path |
| Cross-platform binary | `openh264` + `vt_hw` | Same SW encoder as Linux + Windows (BSD) |
| Measured CPU-bound host | `x264` + `vt_hw`, plus `[encode] force_addon = "x264"` | ~2× faster SW, GPL-isolated subprocess — **opt-in only**, never auto-selected |

### Codec Decision (Confirmed)

```
Primary:   H.264 via VideoToolbox HW  ← every Mac from 2011+, universal browser decode
HDR only:  HEVC Main10 via VT HW      ← Skylake+ Intel (2015+) and all Apple Silicon;
                                          WebCodecs has no H.264 HDR profile, which is
                                          the only reason HEVC is ever selected
Future:    AV1 HW via VideoToolbox    ← no Apple Silicon has AV1 HW encode (SW only via libaom/SVT-AV1)
Skip:      VP8/VP9                    ← no HW path on macOS, not worth SW cost
```

`hevc.gva`'s presence in `VTCopyVideoEncoderList` is what makes HDR available at
all on a given Mac; when it is absent, an HDR request is refused and the session
stays SDR rather than dropping to VideoToolbox's software HEVC, which exists but
runs at ~30ms p50 at 1080p — an order of magnitude outside the latency budget.

> The encoder advertises **H.264** (`avc1.*`) for every SDR session, on every
> platform, regardless of what HEVC hardware is present. **HEVC Main10**
> (`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
> H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's
> WebCodecs cannot decode it, and a codec no attached client can decode is a
> black screen, not a saving. Which HW encoder is *selected* is a separate
> question from which codec it *emits* — the probe order picks the add-on, this
> rule picks the codec.

### Add-on probe order

The order below selects the **add-on**; the codec rule above then decides what it
emits. It is the macOS arm of
[`specs/core/MODULE_PIPELINE.md`](../../core/MODULE_PIPELINE.md) startup step 3e,
which is authoritative:

```
HW: vt_hw
     → VTCompressionSessionCreate + RequireHardwareAcceleratedVideoEncoder: true
     → All Macs from Sandy Bridge (2011+); HEVC Main10 on Skylake+/Apple Silicon

SW: openh264 → vt_sw
     → openh264 is the software default on every OS: in-process, BSD-licensed
       with Cisco carrying the MPEG-LA royalty, and able to force an IDR in place
     → vt_sw is the macOS-native second rung — Apple's own encoder, no
       third-party library
     → x264 is NOT in this order; it is reachable only via
       [encode] force_addon = "x264" (see MODULE_ENCODE "Software encoder order")
```

VideoToolbox remains the only **hardware** encoder API on macOS. On the software
tier it is second, not first: `openh264` is the cross-platform default and
`vt_sw` is preferred over the opt-in `x264` because it is macOS-native.

**No libx265, ever** (triple patent pool exposure). Apple's licensing does cover
VideoToolbox's own software HEVC, but it is ~2x the cost of H.264 SW, so the HDR
selection cascade routes an HDR session to `vt_hw`; where `hevc.gva` is absent the
request is refused and the stream stays H.264 SDR.

### VideoToolbox Benchmark Results

**Measured (Hackintosh: AMD Ryzen 5 3600 + RX 570, macOS 26.5.1):**

| Encoder | Resolution | FPS | p50 | p95 | p99 | Notes |
|---------|-----------|-----|-----|-----|-----|-------|
| **H.264 HW (AMD GVA/VCE)** | 1920×1080 | **115** | **8.6ms** | 8.9ms | 9.0ms | ✅ Measured |
| **H.264 HW (AMD GVA/VCE)** | 2560×1440 | **71** | **13.9ms** | 14.1ms | 14.2ms | ✅ Measured |
| HEVC SW | 1920×1080 | 27 | 30.5ms | 76ms | 156ms | ✅ Measured |
| HEVC SW | 2560×1440 | 18 | 46ms | 131ms | 250ms | ✅ Measured |
| H.264 SW | any | ❌ timeout | — | — | Hackintosh-specific gap only |
| HEVC HW | any | ❌ timeout | — | — | Hackintosh-specific gap only |

> **Hackintosh caveat:** H.264 SW and HEVC HW timeouts are specific to the AMD Ryzen +
> RX 570 Hackintosh configuration. On real Apple hardware these both work correctly.
> The AMD GVA driver on Hackintosh only exposes H.264 HW reliably.

**Estimated on real Apple hardware:**

| Encoder | 1080p p50 | 1440p p50 | Available on |
|---------|----------|----------|-------------|
| H.264 HW | ~2–3ms | ~3–4ms | All Macs (2011+) |
| H.264 SW | ~5–8ms | ~9–14ms | All Macs (fallback) |
| HEVC HW | ~2–3ms | ~3–4ms | Skylake+ Intel, all Apple Silicon |
| HEVC SW | ~15–25ms | ~30–50ms | All Macs (slow, avoid) |
| AV1 HW | n/a | n/a | No Apple Silicon has AV1 HW encode |

**Raw CSVs (Hackintosh):** `/tmp/fd_bench/enc_h264_hw_*.csv`, `enc_hevc_sw_*.csv`
— these live **outside this repository**. There is no macOS session in
`PROJECT_ARTIFACTS/bench_out`; every recorded session there is Windows. The
VideoToolbox and ScreenCaptureKit numbers on this page are therefore a recorded
observation on one Hackintosh, not a benchmark reproducible from this tree, and
no macOS software encode — OpenH264 or x264 — has been measured on any Apple
hardware at all.

### VideoToolbox API (C-callback style — closure API broken in macOS 26)

> **macOS 26 critical bug:** `VTCompressionSessionEncodeFrame` with an inline
> `outputHandler:` closure NEVER fires its callback. Use the session-level
> `outputCallback` C function pointer set at session creation time. This is confirmed
> and verified — the closure silently drops all encoded frames.

```swift
// CORRECT pattern for macOS 26+ (C-callback at session creation)
let vtCallback: VTCompressionOutputCallback = { refcon, _, _, _, sb in
    guard let ptr = refcon else { return }
    let ctx = Unmanaged<EncContext>.fromOpaque(ptr).takeUnretainedValue()
    // process CMSampleBuffer here
    ctx.sema.signal()
}

// H.264 hardware session
VTCompressionSessionCreate(
    allocator: nil, width: Int32(w), height: Int32(h),
    codecType: kCMVideoCodecType_H264,           // ← H.264
    encoderSpecification: [
        kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder: true
    ] as CFDictionary,
    imageBufferAttributes: nil,
    compressedDataAllocator: nil,
    outputCallback: vtCallback,    // ← callback here, NOT per-frame
    refcon: ctxPtr,
    compressionSessionOut: &h264Session
)

// HEVC hardware session (same pattern, different codecType)
VTCompressionSessionCreate(
    allocator: nil, width: Int32(w), height: Int32(h),
    codecType: kCMVideoCodecType_HEVC,           // ← HEVC/H.265
    encoderSpecification: [
        kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder: true
    ] as CFDictionary,
    imageBufferAttributes: nil,
    compressedDataAllocator: nil,
    outputCallback: vtCallback,
    refcon: ctxPtr,
    compressionSessionOut: &hevcSession
)

// Both sessions: same properties
VTSessionSetProperty(session, key: kVTCompressionPropertyKey_RealTime,
                     value: kCFBooleanTrue)
VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AllowFrameReordering,
                     value: kCFBooleanFalse)
```

### Config Handshake Codec Strings (WebCodecs)

The codec string is **computed**, never a literal: both the profile the encoder
actually configured and the level implied by the active resolution and frame rate
go into it, through `featherdesk_abi::codec_string` (see
[`specs/core/MODULE_ABI.md`](../../core/MODULE_ABI.md) "Codec-string
computation"). At 1080p60 that yields:

```json
// H.264 High — every SDR session
{ "codec": "avc1.64002A" }

// HEVC Main10 — HDR sessions only
{ "codec": "hvc1.2.4.L123.B0" }
```

A different geometry or frame rate produces a different level, so neither string
may be quoted as a constant anywhere. The server advertises whichever codec it
selected in the `config` control-stream message; the client configures
`VideoDecoder` from that string — never hardcoded.

---

## Audio + Input

⏸️ **Deferred.** The macOS audio (CoreAudio / SCK built-in capture) and input
(CGEvent) sections have been deliberately removed from this document to keep the
focus on the capture and encode pipeline.

> **Input note:** macOS keyboard/mouse is **not** an add-on — it is provided by
> the core's built-in **`enigo` default** `KeyMouseInjector`, whose macOS backend
> **is CGEvent**. The former standalone `cgevent` add-on is retired/subsumed into
> that default (see [`input/CGEVENT_MACOS_SPEC.md`](./input/CGEVENT_MACOS_SPEC.md)).
> Only the `gcvirtual` gamepad add-on remains an input add-on on macOS.
>
> **Coordinates:** the wire and the whole pipeline are in stream **pixels**, while
> `CGEventPost` consumes **points**. The pixels→points conversion happens inside
> the injector, from geometry it queries from CoreGraphics — nothing upstream
> pre-adjusts for the backing scale, and `sck` never reports points. See
> [`input/CGEVENT_MACOS_SPEC.md`](./input/CGEVENT_MACOS_SPEC.md) "Coordinate
> space".

When we resume work on audio and input, the existing core specs remain authoritative:
- [`specs/media/MODULE_AUDIO.md`](../../media/MODULE_AUDIO.md)
- [`specs/interaction/MODULE_INPUT.md`](../../interaction/MODULE_INPUT.md)

This platform spec will be updated with macOS-specific details (SCK `capturesAudio`,
CGEvent + Accessibility permission, keymap coverage) at that point.

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum macOS | **12.3 (Monterey)** — SCK minimum |
| Code signing | **Apple Developer ID** — required for SCK TCC permission |
| Notarization | Required for distribution outside Mac App Store |
| Library validation | **Must be disabled** — `com.apple.security.cs.disable-library-validation`. The hardened runtime otherwise refuses to `dlopen` any `.dylib` not signed by the app's own Team ID, which is every third-party add-on |
| Permissions | Screen Recording (input permissions deferred with audio/input sections) |
| Architecture | Universal binary (arm64 + x86_64) recommended |
| App bundle | Required — raw CLI binary cannot request SCK permission |

### Add-ons and the hardened runtime

Notarization requires the hardened runtime, and the hardened runtime enables
*library validation*: `dlopen` of a `.dylib` signed by a different Team ID fails
with a code-signature error, and the loader's default is to skip it with a `WARN`
([`../../CENTRAL_SPEC.md`](../../CENTRAL_SPEC.md) "Pluggable Architecture").
Without the entitlement the entire add-on model is inert on macOS — every
capture, encoder, audio and input add-on a user drops into the add-ons directory
silently fails to load, and the symptom looks like a configuration problem.

`FeatherDesk.entitlements`:

```xml
<key>com.apple.security.cs.disable-library-validation</key>
<true/>
```

```bash
codesign --force --options runtime \
         --entitlements FeatherDesk.entitlements \
         --sign "Developer ID Application: …" FeatherDesk.app
```

**Add-ons must still be signed.** On Apple Silicon every `.dylib` needs at least
an ad-hoc signature (`codesign -s - featherdesk-addon-<id>.dylib`) — arm64
refuses to map unsigned code regardless of this entitlement. Add-on authors
shipping macOS builds sign them, including for local use.

**The security consequence, in the same breath as the fix.** The FeatherDesk
process holds Screen Recording and (with input add-ons) Accessibility TCC grants.
This entitlement lets anything writable in the add-ons directory run inside it and
inherit those grants. That is the same trust boundary CENTRAL_SPEC states for
`[addons] dir` on every OS — on macOS it additionally means the app's TCC grants.
The default install therefore keeps the add-ons directory inside the bundle
(`FeatherDesk.app/Contents/MacOS/addons/`, which is where the documented default
— `addons/` next to the running executable — resolves to), and pointing
`[addons] dir` at a world-writable path is documented as a privilege-escalation
vector.

---

## Known macOS 26 Behavioral Changes

1. **All legacy capture APIs removed** — CGDisplayStream, CGWindowListCreateImage, CGDisplayCreateImage, AVCaptureScreenInput all return compile errors
2. **TCC HMAC enforcement** — TCC database manipulation (sqlite3 injection) does not grant permissions in macOS 26; only native permission dialogs work
3. **VTCompressionSession per-frame closure broken** — the `outputHandler:` closure variant of `VTCompressionSessionEncodeFrame` never fires its callback in macOS 26; use the session-level `outputCallback` C function pointer instead
4. **SCK requires GUI session** — process must be running in the user's window server session; background daemons cannot capture even with TCC permission
