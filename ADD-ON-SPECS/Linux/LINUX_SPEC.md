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

## Video Encoding — Confirmed Codec Targets

| Codec | Path | Status |
|-------|------|--------|
| **H.264 Baseline** | OpenH264 CGo (SW) | ✅ Working — default |
| **H.264** | libva CGo HW (Intel/AMD/NVIDIA) | 📋 Specced — MODULE_CUSTOM_LIBVA.md |
| **HEVC Main** | libva CGo HW (`VAProfileHEVCMain`) | 📋 Specced — same module |
| AV1 | libva CGo HW (Intel Arc, AMD RDNA2+) | 📋 Future |

---

## Software Encoding — OpenH264 CGo

**Status: ✅ Working. Default encoder.**

Same CGo file as Windows and macOS (cross-platform). No ffmpeg. No subprocess.
License: BSD-2.

```
I420Frame → CGo → WelsCreateSVCEncoder → EncodeFrame → SFrameBSInfo → NAL units
```

| Resolution | FPS ceiling | p50 latency | CPU (1 core) |
|-----------|------------|------------|-------------|
| 1920×1080 | ~125 fps | ~8ms | ~25% |
| 2560×1440 | ~65 fps | ~15ms | ~25% |

- `CAMERA_VIDEO_REAL_TIME` usage type
- Single-thread (`iMultipleThreadIdc=1`)
- No B-frames, on-demand IDR only
- Profile: H.264 Baseline (browser-native, maximum compatibility)

**File:** `internal/encode/openh264.go`

---

## Hardware Encoding — libva CGo (Direct VA-API)

**Status: 📋 Specced in `specs/MODULE_CUSTOM_LIBVA.md`. Not yet built.**

No ffmpeg. `libva` is MIT licensed. Calls VA-API directly from Go via CGo.

### GPU Compatibility

| GPU | Driver | H.264 HW | HEVC HW | AV1 HW |
|-----|--------|---------|---------|--------|
| Intel Sandy Bridge–Broadwell (2011–2015) | `i965` | ✅ | ❌ | ❌ |
| Intel Skylake–Ice Lake (2015–2019) | `iHD` | ✅ | ✅ | ❌ |
| Intel Tiger Lake / Xe / Arc (2020+) | `iHD` | ✅ | ✅ 10-bit | ✅ Arc |
| AMD GCN / RX 400+ (2016+) | Mesa `radeonsi` | ✅ | ✅ | ❌ |
| AMD RDNA2 / RX 6000+ (2020+) | Mesa | ✅ | ✅ 10-bit | ✅ |
| AMD RDNA3 / RX 7000+ (2022+) | Mesa | ✅ | ✅ 10-bit | ✅ |
| NVIDIA (unofficial) | `nvidia-vaapi-driver` | ✅ wraps NVENC | ✅ | ❌ |

**The same binary works on all of the above.** At startup, `vaQueryConfigEntrypoints`
reports what the GPU supports. The pipeline selects accordingly.

### VA-API Pipeline (zero-copy path)

```
KMS DMA-BUF fd → vaCreateSurfaces (VASurfaceAttribExternalBuffers)
    → vaBeginPicture / vaRenderPicture / vaEndPicture
    → vaSyncSurface
    → vaMapBuffer → H.264 or HEVC NAL units (Annex B)
    → vaUnmapBuffer
```

GPU↔CPU copies: **1** (compressed output only, ~30KB/frame vs ~36MB software path)

### Expected Performance (from MODULE_CUSTOM_LIBVA.md targets)

| GPU class | H.264 1080p p50 | HEVC 1080p p50 | CPU |
|-----------|----------------|----------------|-----|
| Intel Skylake / iHD | <3ms | <3ms | <2% |
| AMD RDNA2 | <4ms | <4ms | <3% |
| NVIDIA via wrapper | <3ms | <3ms | <3% |

### Implementation Reference

See `specs/MODULE_CUSTOM_LIBVA.md` for:
- Full 6-phase implementation plan
- Complete CGo preamble with all required libva functions
- SPS/PPS serialization strategy (copy from libva-utils `h264encode.c`, MIT)
- Surface ring buffer design
- Format negotiation (XRGB8888 → NV12 GPU-side conversion when needed)

**Dependencies:** `libva` (MIT), `libva-drm` (MIT), `libdrm` (MIT)

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
