# FeatherDesk — Cross-Platform Compatibility

## Platform Specs

| Platform | Spec | Status |
|----------|------|--------|
| **Linux** | [`../specs/CENTRAL_SPEC.md`](../specs/CENTRAL_SPEC.md) + module specs | ✅ Working codebase |
| **Windows** | [`./Windows/WINDOWS_SPEC.md`](./Windows/WINDOWS_SPEC.md) | 📋 Specced, not built |
| **macOS** | [`./macOS/MACOS_SPEC.md`](./macOS/MACOS_SPEC.md) | 📋 Specced, not built |

---

## Codec Support — The Practical Matrix

The rule: **H.264 hardware encoding everywhere. AV1 hardware on M2+ Mac and RTX40+/RDNA3+ GPU as a quality upgrade. Everything else is noise for a remote desktop product.**

| Platform | Hardware | H.264 HW | HEVC HW | AV1 HW | Default |
|----------|----------|---------|---------|--------|---------|
| Linux | Intel (VA-API) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Alder Lake+ | H.264 |
| Linux | AMD (VA-API) | ✅ GCN+ | ✅ Polaris+ | ✅ RDNA2+ | H.264 |
| Linux | NVIDIA (NVENC) | ✅ Kepler+ | ✅ Maxwell+ | ✅ Ada+ | H.264 |
| Linux | No GPU | ❌ | ❌ | ❌ | SW (OpenH264) |
| Windows | NVIDIA | ✅ | ✅ | ✅ RTX40+ | H.264 |
| Windows | AMD | ✅ | ✅ | ✅ RDNA2+ | H.264 |
| Windows | Intel | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ 12th gen+ | H.264 |
| Windows | No GPU | ❌ | ❌ | ❌ | SW (libx264) |
| macOS | **Apple Silicon M1** | ✅ | ✅ | ❌ | H.264 |
| macOS | **Apple Silicon M2+** | ✅ | ✅ | **✅** | H.264 (AV1 opt-in) |
| macOS | Intel + AMD discrete | ✅ AMD VCE | ✅ | ❌ | H.264 |
| macOS | Intel integrated only | ✅ Quick Sync | ✅ Skylake+ | ❌ | H.264 |

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
