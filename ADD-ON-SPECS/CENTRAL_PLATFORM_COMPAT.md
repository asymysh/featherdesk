# FeatherDesk — Cross-Platform Compatibility

## Platform Specs

| Platform | Spec | Status |
|----------|------|--------|
| **Linux** | [`../specs/CENTRAL_SPEC.md`](../specs/CENTRAL_SPEC.md) + module specs | ✅ Working codebase |
| **Windows** | [`./Windows/WINDOWS_SPEC.md`](./Windows/WINDOWS_SPEC.md) | 📋 Specced, not built |
| **macOS** | [`./macOS/MACOS_SPEC.md`](./macOS/MACOS_SPEC.md) | 📋 Specced, not built |

---

## Codec Fallback Order (Confirmed)

**Server-side encode fallback** (tried in this order at startup based on hardware probe):

```
1. HEVC hardware   — best compression (~40% better than H.264)
                     licensing: covered by GPU/OS vendor (Intel/AMD/Apple)
                     NO software HEVC fallback (libx265 has triple patent pool exposure)

2. H.264 hardware  — hardware accelerated, no CPU cost
                     licensing: covered by GPU/OS vendor

3. H.264 software  — OpenH264 CGo, Cisco pays MPEG-LA royalties
                     licensing: zero concern, runs on any hardware
```

**Client-side decode:**
No codec negotiation needed. The server picks the best codec it can encode and
sends it in `FrameTypeConfig`. The client decodes whatever arrives.

> **Firefox is not a supported browser.** Firefox has no HEVC WebCodecs support.
> Minimum browser requirement: **Chrome 107+, Edge, Safari 14.1+**. All three
> support HEVC hardware decode. This removes the need for any client-side codec
> capability advertisement or fallback negotiation.

## Codec Support Matrix

| Platform | Hardware | H.264 HW | HEVC HW | AV1 HW | SW Fallback |
|----------|----------|---------|---------|--------|------------|
| Linux | Intel (VA-API) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Arc+ | OpenH264 CGo |
| Linux | AMD (VA-API) | ✅ GCN+ | ✅ Polaris+ | ✅ RDNA2+ | OpenH264 CGo |
| Linux | NVIDIA (via nvidia-vaapi-driver) | ✅ | ✅ | ❌ | OpenH264 CGo |
| Linux | No GPU | ❌ | ❌ | ❌ | OpenH264 CGo |
| Windows | NVIDIA (NVENC) | ✅ | ✅ | ✅ RTX40+ | OpenH264 CGo |
| Windows | AMD (AMF) | ✅ | ✅ | ✅ RDNA2+ | OpenH264 CGo |
| Windows | Intel (QSV) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Arc+ | OpenH264 CGo |
| Windows | No GPU | ❌ | ❌ | ❌ | OpenH264 CGo |
| macOS | Apple Silicon M1 | ✅ | ✅ | ❌ encode | OpenH264 CGo* |
| macOS | Apple Silicon M2+ | ✅ | ✅ | ✅ | OpenH264 CGo* |
| macOS | Intel + AMD discrete | ✅ | ✅ | ❌ | OpenH264 CGo* |
| macOS | Intel integrated Skylake+ | ✅ | ✅ | ❌ | OpenH264 CGo* |
| macOS | Intel integrated pre-Skylake | ✅ | ❌ | ❌ | OpenH264 CGo* |

> *macOS software fallback: VideoToolbox SW H.264 preferred over OpenH264 CGo since
> VideoToolbox is macOS-native. OpenH264 CGo is the universal fallback if VT fails.

---

## Capture — One Per Platform

| Platform | Primary | Fallback | Last Resort |
|----------|---------|---------|-------------|
| **Linux** | KMS/DRM + EGL → zero-copy VA-API | PipeWire ScreenCast | X11grab (ffmpeg) |
| **Windows** | DXGI Desktop Duplication | WGC | GDI BitBlt |
| **macOS** | ScreenCaptureKit | ❌ none (all others removed in macOS 26) | ❌ |

---

## Audio — One Per Platform

| Platform | API | Format |
|----------|-----|--------|
| **Linux** | PipeWire (pw-cat, monitor source) | S16LE, 48kHz, stereo |
| **Windows** | WASAPI loopback | PCM, 48kHz, stereo (auto-resampled) |
| **macOS** | SCK built-in audio (`capturesAudio = true`) | Float32, 48kHz, stereo |

---

## Input Injection — One Per Platform

| Platform | API | Notes |
|----------|-----|-------|
| **Linux** | uinput (kernel virtual device) | Requires `/dev/uinput` access |
| **Windows** | `SendInput` | No special permissions needed |
| **macOS** | `CGEvent` | Requires Accessibility permission |

---

## Protocol — Platform-Agnostic

The wire protocol (`specs/MODULE_PROTOCOL.md`) is identical on all platforms:
- 22-byte binary header (Version, Type, Sequence, Timestamp, W, H, PayloadSize)
- `FrameTypeConfig` (type 6) as handshake — carries codec string, dims, cursorMode
- `FrameTypeVideoH264` (type 1) or `FrameTypeVideoVP8` (type 5) for video
- `FrameTypeAudioPCM` (type 4) for audio
- JSON text frames for all client→server messages (input, keyframe request)

The codec in the `Config` handshake is the **full WebCodecs codec string**:
- H.264: `"avc1.42E01E"` (Constrained Baseline 3.0) — universal default
- AV1: `"av01.0.04M.08"` — M2+ Mac, RTX40+ NVIDIA, RDNA2+ AMD

---

## Feature Parity Gaps (current)

| Feature | Linux | Windows | macOS |
|---------|-------|---------|-------|
| Capture | ✅ built | 🔧 GDI only | ❌ not built |
| HW encode (zero-copy) | 📋 specced | ❌ not built | ❌ not built |
| SW encode | ✅ VP8 (should be H.264) | ❌ not built | ❌ not built |
| Audio | ✅ PipeWire | ❌ not built | ❌ not built |
| Input | ✅ uinput | ❌ not built | ❌ not built |
| Browser client | ✅ built | shared | shared |
