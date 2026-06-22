# FeatherDesk — Linux Platform Spec

## Overview

Linux is the **primary target** for FeatherDesk. The original codebase was written for
Linux (Intel HD 630, KMS/DRM, VA-API). All five original tracks are implemented and
working on Linux. This spec documents the confirmed implementation state and the
hardware encoder path still to be built.

---

## Capture

### Backend Selection

```
Root / CAP_SYS_ADMIN available AND DRM card found?
    YES → KMS/DRM + EGL              (primary — lowest latency)
    NO  → Is Wayland + Mutter?
              YES → PipeWire ScreenCast  (Wayland fallback)
              NO  → X11grab via ffmpeg   (X11 fallback)
```

### KMS/DRM + EGL (Primary — requires root or CAP_SYS_ADMIN)

Direct kernel framebuffer capture. No display server involvement.

```
/dev/dri/card* → DRM plane enumeration → primary plane framebuffer
    → drmPrimeHandleToFD → DMA-BUF fd
    → EGL: eglCreateImageKHR(EGL_LINUX_DMA_BUF_EXT)
    → GL: texture → blit to linear renderbuffer → glReadPixels → BGRA []byte
```

**Benchmark (Intel HD 630, 2560×1440):**
- EGL context init: 170ms (one-time)
- glReadPixels per frame: **50ms** at 1440p — this is the software path cost
- With hardware encode (DMA-BUF direct to VA-API): eliminates glReadPixels entirely

**Dependencies:** `libdrm`, `libgbm`, `libEGL`, `libGL`

**Key known issues (to fix in refactor):**
- DMA-BUF fd leak on EGL import failure (TD-01)
- Static C globals prevent thread safety (TD-03)
- fps parameter unused — no frame pacing (TD-13)

### PipeWire ScreenCast (Wayland fallback)

D-Bus → Mutter ScreenCast API → PipeWire node → GStreamer → raw RGBA pipe.
GNOME-specific. No root required.

### X11grab via ffmpeg subprocess (X11 fallback)

```bash
ffmpeg -f x11grab -video_size WxH -framerate N -i $DISPLAY -f rawvideo -pix_fmt rgba -
```

Current default for development. **Subprocess overhead: ~5% CPU per pod.**
Target: replace with XShm direct capture (no subprocess) for production deployments.

---

## Video Encoding

### VA-API is the Single Hardware Interface on Linux

Unlike Windows (NVENC / AMF / QSV — three separate vendor APIs) or macOS (VideoToolbox),
**Linux uses VA-API for all GPU hardware encoding regardless of vendor.** Intel, AMD,
and NVIDIA all expose their hardware encoders through the same `libva` interface:

| GPU vendor | Linux VA-API driver | Underlying hardware |
|-----------|-------------------|---------------------|
| Intel | `iHD` (Skylake+) / `i965` (older) | Intel Quick Sync |
| AMD | Mesa `radeonsi` | AMD VCE / VCN |
| NVIDIA | `nvidia-vaapi-driver` *(unofficial)* | NVENC wrapped behind VA-API |

One libva CGo binary handles all three. At startup `vaQueryConfigEntrypoints` returns
what the installed GPU and driver support — no vendor branching in application code.

> **NVIDIA note:** `nvidia-vaapi-driver` is an unofficial community wrapper. It works
> well but is not supported by NVIDIA. Intel and AMD VA-API support is first-party.

### Confirmed Fallback Order

```
1. HEVC hardware  (VAProfileHEVCMain)
     → Intel Skylake+, AMD Polaris+, NVIDIA via wrapper
     → Config codec string: "hvc1.1.6.L93.B0"
     → Status: 📋 Specced — MODULE_CUSTOM_LIBVA.md

2. H.264 hardware (VAProfileH264Baseline)
     → Intel Sandy Bridge+, AMD GCN+, NVIDIA via wrapper
     → Config codec string: "avc1.42E01E"
     → Status: 📋 Specced — MODULE_CUSTOM_LIBVA.md

3. H.264 software (OpenH264 CGo)
     → No GPU present, or GPU has no VA-API encode support
     → Works on every machine including no-GPU ARM/x86 (Graviton etc.)
     → Config codec string: "avc1.42E01E"
     → Status: ✅ Working — current default
```

**No software HEVC.** libx265 has triple HEVC patent pool exposure (MPEG-LA, HEVC
Advance, Velos Media). If HEVC hardware is unavailable, fall straight to H.264.

### GPU Encode Capability Matrix

| GPU | Driver | H.264 HW | HEVC HW | AV1 HW |
|-----|--------|---------|---------|--------|
| Intel Sandy Bridge–Broadwell (2011–2015) | `i965` | ✅ | ❌ | ❌ |
| Intel Skylake–Ice Lake (2015–2019) | `iHD` | ✅ | ✅ | ❌ |
| Intel Tiger Lake / Xe / Arc (2020+) | `iHD` | ✅ | ✅ 10-bit | ✅ Arc+ |
| AMD GCN / RX 400+ (2016+) | Mesa | ✅ | ✅ | ❌ |
| AMD RDNA2 / RX 6000+ (2020+) | Mesa | ✅ | ✅ 10-bit | ✅ |
| AMD RDNA3 / RX 7000+ (2022+) | Mesa | ✅ | ✅ 10-bit | ✅ |
| NVIDIA *(via nvidia-vaapi-driver)* | unofficial | ✅ | ✅ | ❌ |
| No GPU / CPU-only | — | ❌ | ❌ | ❌ → OpenH264 SW |

### Software Fallback — OpenH264 CGo

**Status: ✅ Working. Current default (hardware path not yet built).**

Same CGo file as Windows and macOS. No subprocess. No ffmpeg. BSD-2 licensed.

| Resolution | FPS ceiling | p50 latency | CPU (1 core) |
|-----------|------------|------------|-------------|
| 1920×1080 | ~125 fps | ~8ms | ~25% |
| 2560×1440 | ~65 fps | ~15ms | ~25% |

**File:** `internal/encode/openh264.go`

### Hardware Path — libva CGo

**Status: 📋 Specced in `specs/MODULE_CUSTOM_LIBVA.md`. Not yet built.**

No ffmpeg. `libva` MIT licensed. Direct CGo. Zero-copy path when combined with
KMS DMA-BUF capture:

```
KMS DMA-BUF fd → vaCreateSurfaces (VASurfaceAttribExternalBuffers)
    → vaBeginPicture / vaRenderPicture / vaEndPicture
    → vaSyncSurface
    → vaMapBuffer → H.264 or HEVC NAL units (Annex B)
    → vaUnmapBuffer
```

GPU↔CPU copies: **1** (~30KB compressed NALs vs ~36MB raw pixels in software path)

| GPU | H.264 1080p p50 | HEVC 1080p p50 | CPU at 60fps |
|-----|----------------|----------------|-------------|
| Intel Skylake / iHD | <3ms | <3ms | <2% |
| AMD RDNA2 / Mesa | <4ms | <4ms | <3% |
| NVIDIA / vaapi-driver | <3ms | <3ms | <3% |

**Dependencies:** `libva` (MIT), `libva-drm` (MIT), `libdrm` (MIT)  
**Full implementation plan:** `specs/MODULE_CUSTOM_LIBVA.md`

---

## Audio

**Status: ✅ Working.**

| API | Status | Notes |
|-----|--------|-------|
| **PipeWire `pw-cat`** | ✅ Primary | Monitor source capture, 48kHz stereo S16LE |
| ALSA loopback | Fallback | `arecord` with loopback module |
| PulseAudio `parec` | Fallback | If PipeWire is unavailable |

PipeWire source auto-detected via `pactl list short sources` — selects first
`.monitor` source (system audio loopback). 20ms chunks (960 samples × 2ch × 16-bit = 3840 bytes).

**File:** `internal/audio/capture.go`

**Known issues to fix:**
- Race condition on `cmd`/`stdout` fields (TD-08)
- SIGKILL instead of SIGTERM (R-AUD-02)
- Fixed 2s reconnect delay — needs exponential backoff (R-AUD-03)

---

## Input Injection

**Status: ✅ Working.**

Linux kernel uinput subsystem. Virtual keyboard + absolute mouse. No root required
(user must be in `input` group, or `/dev/uinput` must be world-writable).

```
JSON text WebSocket → ParseMessage → BrowserCodeToLinux keymap
    → write(input_event{EV_KEY/EV_ABS/EV_REL}) + SYN_REPORT → /dev/uinput
```

**File:** `internal/input/device.go`

**Key known issues to fix:**
- Hardcoded 2560×1440 resolution for input device (TD-05 / TD-27)
- All inject errors silently discarded (TD-11)
- Wheel magnitude discarded — normalized to ±1 (R-INP-03)
- No horizontal scroll support (R-INP-10)

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum kernel | 4.15+ (DRM universal planes, VA-API modern drivers) |
| KMS capture | `CAP_SYS_ADMIN` or root — `sudo setcap cap_sys_admin+p ./viewport-rds` |
| VA-API encode | Intel: `intel-media-va-driver` or `i965-va-driver`; AMD: `mesa-va-drivers` |
| uinput | User in `input` group or `chmod a+rw /dev/uinput` |
| PipeWire | `pipewire` daemon running (standard on modern desktops) |
| ffmpeg | Only needed for X11grab fallback capture — not for encode |

---

## Benchmark Hardware (Original Development)

All featherdesk integration tests were run on:

| Component | Spec |
|-----------|------|
| CPU | Intel Core i7-7700K (Kaby Lake) |
| GPU / Encoder | Intel HD Graphics 630 — VA-API Quick Sync |
| Display | 2560×1440 @ 165Hz |
| OS | Linux (KMS/DRM, Mesa EGL) |

Verified results from `review/kms_capture_software_encode/`:
- DRM card: `/dev/dri/card1`, 2560×1440 @ 165Hz, plane ID 34
- EGL context init: 170ms
- glReadPixels (1440p BGRA): **50ms per frame**
- Frame pacing (30fps): 5 frames in 170ms ✅

---

## Current vs Target State

| Component | Current code | Target (refactor) |
|-----------|-------------|------------------|
| Capture | KMS+EGL (working) + X11grab subprocess | KMS+EGL + XShm direct (no subprocess) |
| SW encode | VP8 libvpx → **OpenH264 CGo** ✅ | OpenH264 CGo |
| HW encode | ffmpeg pipe → h264_vaapi (CPU copies) | libva CGo direct (zero-copy, no ffmpeg) |
| Audio | PipeWire pw-cat ✅ | PipeWire pw-cat (+ fix race/backoff) |
| Input | uinput ✅ | uinput (+ Resize(), error surfacing) |
| Protocol | 17-byte header | 22-byte header v1 (versioned, sequenced) |
