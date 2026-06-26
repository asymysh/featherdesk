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
> Minimum browser requirement: **Chrome 107+, Edge, Safari 16.4+** (partial
> WebCodecs; full support Safari 26+). All three support HEVC hardware decode.

## Codec Support Matrix

| Platform | Hardware | H.264 HW | HEVC HW | AV1 HW | SW Fallback |
|----------|----------|---------|---------|--------|------------|
| Linux | Intel (VA-API) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Arc+ | OpenH264 CGo |
| Linux | AMD (VA-API) | ✅ GCN+ | ✅ Polaris+ | ✅ RDNA3+ (RX 7000+) | OpenH264 CGo |
| Linux | NVIDIA (via nvidia-vaapi-driver) | ✅ | ✅ | ❌ | OpenH264 CGo |
| Linux | No GPU | ❌ | ❌ | ❌ | OpenH264 CGo |
| Windows | NVIDIA (NVENC) | ✅ | ✅ | ✅ RTX40+ | OpenH264 CGo |
| Windows | AMD (AMF) | ✅ | ✅ | ✅ RDNA3+ (RX 7000+) | OpenH264 CGo |
| Windows | Intel (QSV) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Arc+ | OpenH264 CGo |
| Windows | No GPU | ❌ | ❌ | ❌ | OpenH264 CGo |
| macOS | Apple Silicon M1 | ✅ | ✅ | ❌ encode | OpenH264 CGo* |
| macOS | Apple Silicon M2 | ✅ | ✅ | ❌ | OpenH264 CGo* |
| macOS | Apple Silicon M3+ | ✅ | ✅ | ❌ (decode only) | OpenH264 CGo* |
| macOS | Intel + AMD discrete | ✅ | ✅ | ❌ | OpenH264 CGo* |
| macOS | Intel integrated Skylake+ | ✅ | ✅ | ❌ | OpenH264 CGo* |
| macOS | Intel integrated pre-Skylake | ✅ | ❌ | ❌ | OpenH264 CGo* |

> *macOS software fallback: VideoToolbox SW H.264 preferred over OpenH264 CGo since
> VideoToolbox is macOS-native. OpenH264 CGo is the universal fallback if VT fails.

---

## Capture — One Per Platform

The default binary has **no capture backend** on any OS; capture is always
an opt-in build-tagged add-on.

| Platform | Build tag | Mechanism | Headless support |
|----------|-----------|-----------|------------------|
| **Linux** | `kms_egl` (+ optional `nvfbc`) | KMS/DRM + EGL DMA-BUF zero-copy | Xvfb / virtual display |
| **Windows** | `dxgi_dd` | DXGI Desktop Duplication → ID3D11Texture2D | Integrated IddCx virtual display (auto-installed) |
| **macOS** | `sck` | ScreenCaptureKit → CMSampleBuffer / IOSurface | Virtual display driver |

No fallbacks in the default binary. PipeWire / X11grab / WGC / GDI / Magnification
were all considered and rejected — see per-OS capture READMEs.

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
- `FrameTypeVideoH264` (type 1) for video — slot 5 reserved (formerly VP8, rejected)
- `FrameTypeAudioPCM` (type 4) for audio
- JSON text frames for all client→server messages (input, keyframe request)

The codec in the `Config` handshake is the **full WebCodecs codec string**:
- H.264: `"avc1.42E01F"` (Constrained Baseline 3.1) — universal default
- AV1: `"av01.0.04M.08"` — RTX 40+ NVIDIA (Ada Lovelace), RDNA3+ AMD (RX 7000+), Intel Arc. **No Apple Silicon has AV1 HW encode** (M3+ has decode only).

---

## Implementation Status (current)

| Feature | Linux | Windows | macOS |
|---------|-------|---------|-------|
| Capture | ✅ KMS+EGL specced & working | ✅ DXGI DD specced & benchmarked | ✅ SCK specced & benchmarked |
| HW encode | 📋 libva/NVENC/AMF specced | ✅ NVENC/AMF/MF/QSV specced & benchmarked | 📋 VideoToolbox specced (Hackintosh measured) |
| SW encode (BSD) | 📋 OpenH264 specced | ✅ OpenH264 specced & benchmarked | 📋 OpenH264 specced |
| SW encode (GPL) | 📋 x264 subprocess specced | ✅ x264 specced & benchmarked | 📋 x264 specced |
| Audio | ⏸️ deferred | ⏸️ deferred | ⏸️ deferred |
| Input | ⏸️ deferred | ⏸️ deferred | ⏸️ deferred |
| Browser client | ✅ built | shared | shared |
