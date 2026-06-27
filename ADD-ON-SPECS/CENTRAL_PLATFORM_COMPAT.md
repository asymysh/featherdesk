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
advertises it in the `config` control-stream message. The client decodes
whatever arrives.

> **Minimum browser: Chrome 107+, Edge 98+, Firefox 130+, Safari 18.2+** (the
> WebCodecs + WebTransport intersection — see [`../specs/MODULE_CLIENT.md`](../specs/MODULE_CLIENT.md)).
> **HEVC caveat:** Chrome/Edge/Safari decode HEVC; Firefox's WebCodecs does
> **not**. A host that selects HEVC (e.g. for HDR) is decodable only by
> Chromium/WebKit clients — Firefox clients need an H.264 stream (the universal
> default), so HDR is effectively Chromium/WebKit-only.

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

## Audio — One Per Platform (host→client only; 🔒 design locked, ⏸️ impl deferred)

Same pluggable, zero-by-default pattern as capture/input — a per-OS build-tagged
**capture** add-on normalizes the OS device format to the canonical 48 kHz /
stereo / S16LE, and a separate **codec** build tag (`opus`, else raw PCM) sets the
wire format (advertised in the `config` message). **No subprocess** (`pw-cat`
gone), no driver, no mic. Realtime, **audio-master** A/V sync. See
[`../specs/MODULE_AUDIO.md`](../specs/MODULE_AUDIO.md).

| Platform | Build tag | Capture mechanism | Spec |
|----------|-----------|-------------------|------|
| **Linux** | `pipewire` | PipeWire monitor (native libpipewire; Pulse/ALSA fallback) | [`Linux/audio/PIPEWIRE_LINUX_SPEC.md`](./Linux/audio/PIPEWIRE_LINUX_SPEC.md) |
| **Windows** | `wasapi` | WASAPI loopback (default render endpoint) | [`Windows/audio/WASAPI_WINDOWS_SPEC.md`](./Windows/audio/WASAPI_WINDOWS_SPEC.md) |
| **macOS** | `sck_audio` | ScreenCaptureKit audio on the shared `sck` stream (macOS 13+) | [`macOS/audio/SCK_AUDIO_MACOS_SPEC.md`](./macOS/audio/SCK_AUDIO_MACOS_SPEC.md) |

Codec: `opus` (BSD libopus, in-process; FEC/PLC; ~96–128 kbps) — **recommended** —
or raw S16LE PCM (1.536 Mbps, no concealment) when `opus` isn't compiled.

---

## Input Injection — One Per Platform

| Platform | API | Notes |
|----------|-----|-------|
| **Linux** | uinput (kernel virtual device) | Requires `/dev/uinput` access |
| **Windows** | Interception filter driver (+ `SendSAS` for Ctrl+Alt+Del) | Injects **below UIPI** (reaches elevated apps), unlike `SendInput`. Requires the Interception driver installed (LGPL, dynamically linked). |
| **macOS** | `CGEvent` | Requires Accessibility permission |

---

## Protocol — Platform-Agnostic

The wire protocol (`../specs/MODULE_PROTOCOL.md`) is identical on all platforms:
- 22-byte media `FrameHeader` (Version, Type, Sequence, Timestamp, W, H, PayloadSize) — media channels only (datagram fragment 0 + bootstrap stream)
- `config` JSON message on the **control stream** as handshake — carries codec string, dims, cursorMode (the binary type-6 Config frame is retired)
- `FrameTypeVideoH264` (type 1) for video — slot 5 reserved (formerly VP8, rejected)
- `FrameTypeAudioPCM` (type 4) for audio (deferred)
- **Binary** `[u16 RecLen]`-prefixed input records on the **input stream** (C→S); JSON on the **control stream** for keyframe/resize/etc. — input is NOT JSON

The codec in the `config` message is the **full WebCodecs codec string**:
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
