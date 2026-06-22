# macOS Capture Add-Ons

## No separate capture add-ons are needed on macOS.

macOS 26 (Tahoe) removed every legacy capture API simultaneously, leaving exactly one
supported path: **ScreenCaptureKit (SCK)**. There is no vendor-specific alternative
because there is no vendor fragmentation on macOS — Apple controls the entire graphics
stack from Metal up.

| API | macOS 26 Status |
|-----|-----------------|
| **ScreenCaptureKit (SCK)** | ✅ Only option |
| CGDisplayStream | ❌ Removed — compiler error |
| CGWindowListCreateImage | ❌ Removed — compiler error |
| CGDisplayCreateImage | ❌ Removed — compiler error |
| AVCaptureScreenInput | ❌ Removed — compiler error |

All four legacy APIs return: *"unavailable in macOS: Please use ScreenCaptureKit instead."*

## Why this folder exists

This folder is kept **empty** intentionally to maintain a symmetric directory structure
across all platforms:

```
ADD-ON-SPECS/
├── Linux/capture/        ← 2 add-on specs (NvFBC, wlr-screencopy)
├── macOS/capture/        ← this README only (SCK is the only option)
└── Windows/capture/      ← pending architecture discussion
```

This way every implementor knows the capture spec location for every OS follows the
same path pattern: `ADD-ON-SPECS/{Platform}/capture/`.

## Where SCK is specced

The full ScreenCaptureKit capture spec lives in the platform spec:

**📄 [`../MACOS_SPEC.md`](../MACOS_SPEC.md)** — see the "Capture" section for:
- ScreenCaptureKit-only environment on macOS 26
- Minimum macOS requirement (12.3 Monterey for SCK)
- TCC permission requirements + signed app bundle requirement
- macOS 26 TCC HMAC enforcement (sqlite injection no longer grants permissions)
- Benchmark results: 89–91 fps at ~11ms p50 (Hackintosh measured)
- Zero-copy IOSurface → VideoToolbox path
- Implementation example with `SCStream` + `SCStreamConfiguration`

## If a separate capture is ever needed on macOS

Extremely unlikely given Apple's consolidation onto SCK, but in case some future
workload requires it (e.g., a hypothetical pre-compositor frame access API Apple ships):

1. Write the spec at `ADD-ON-SPECS/macOS/capture/{NAME}_MACOS_SPEC.md`
2. Add a row to the macOS capture table in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
3. Implement under `internal/capture/{name}/` with a Go build tag
4. Wire the runtime probe order in `MODULE_PIPELINE.md`

Until then, this folder remains intentionally empty.
