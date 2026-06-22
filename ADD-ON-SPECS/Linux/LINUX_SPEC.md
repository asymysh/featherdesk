# FeatherDesk — Linux Platform Spec

## Overview

Linux is the **primary target** for FeatherDesk. The original codebase was written for
Linux (Intel HD 630, KMS/DRM, VA-API). All five original tracks are implemented and
working on Linux. This spec documents the confirmed implementation state and the
hardware encoder path still to be built.

---

## Capture

### Default Binary: KMS+EGL only

The default Linux binary uses a single capture backend:

```
Root or CAP_SYS_ADMIN available AND DRM card found?
    YES → KMS+EGL with DMA-BUF zero-copy
    NO  → fatal: insufficient permissions
```

KMS+EGL operates at the kernel/DRM level below the display server, so it works on
**X11, Wayland (GNOME/KDE/wlroots), or no display server at all** — display server
choice is irrelevant. The constraint is `CAP_SYS_ADMIN` only.

```
DRM card → drmPrimeHandleToFD → DMA-BUF fd → encoder (zero CPU pixel copies)
```

Latency: ~0.5ms — gold standard, nothing beats it.

**No no-root fallback paths in the default binary.** If a deployment needs to run
without root, that requirement will be addressed when it comes up. Until then,
users grant the capability once at install:

```bash
sudo setcap cap_sys_admin+p ./viewport-rds
```

### Optional Capture Add-On

| Add-on | Hardware | Spec | Why opt in |
|--------|---------|------|-----------|
| **NvFBC** | NVIDIA proprietary driver | [`capture/NVFBC_LINUX_SPEC.md`](./capture/NVFBC_LINUX_SPEC.md) | ~2–3ms lower than KMS+EGL on NVIDIA proprietary stack; official NVIDIA path; pairs with NVENC encoder for full zero-copy GPU-resident pipeline |

NvFBC is the only capture path that beats KMS+EGL on any hardware — and only on
NVIDIA, where KMS+EGL has historically been finicky with the proprietary driver.

**Intel and AMD do not need capture add-ons** — neither vendor has a proprietary
capture API on Linux.

See [`capture/README.md`](./capture/README.md) for the runtime probe order and the
documented reasoning for why other paths (wlr-screencopy, XShm, etc.) were considered
and rejected.

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

### Every encoder is an add-on (pluggable architecture)

The Linux default binary contains **no encoders**. Every encoder — software and
hardware — is a build-tagged add-on. Users compile in exactly the encoders they
want. The full set:

```
encoders/
├── SW/
│   └── OPENH264_CGO_LINUX_SPEC.md    ← cross-platform SW H.264 (Cisco, BSD-2)
└── HW/
    ├── LIBVA_LINUX_SPEC.md            ← Intel + AMD + NVIDIA via VA-API (MIT)
    ├── NVENC_LINUX_SPEC.md            ← NVIDIA direct (REF_FRAMES_INVALIDATION)
    ├── AMF_ROCM_SPEC.md               ← AMD direct via ROCm (Apache 2.0)
    └── VULKAN_VIDEO_LINUX_SPEC.md     ← cross-vendor royalty-free, future-facing
```

See [`encoders/README.md`](./encoders/README.md) for recommended combinations,
runtime probe order, and rationale.

### TL;DR — recommended combinations

| Deployment | Add-ons | Why |
|-----------|---------|-----|
| Generic Linux server | `openh264` + `libva` | Universal coverage, smallest add-on set |
| NVIDIA workstation | `openh264` + `nvenc` | REF_FRAMES_INVALIDATION for lossy networks |
| AMD workstation | `openh264` + `libva` + `amf_rocm` | AMD-specific tuning + universal fallback |
| Container / no GPU | `openh264` only | SW-only, smallest binary |

### How vendor APIs map to Linux

| GPU vendor | LIBVA covers | Vendor-direct add-on available |
|-----------|--------------|-------------------------------|
| Intel | ✅ first-party (`iHD` / `i965`) | — (no separate Intel SDK on Linux) |
| AMD | ✅ first-party (Mesa `radeonsi`) | `amf_rocm` for AMF tuning |
| NVIDIA | ⚠️ via unofficial `nvidia-vaapi-driver` | `nvenc` recommended for NVIDIA deployments |

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

## Audio + Input

⏸️ **Deferred.** The audio and input subsystems are working in the current codebase
(PipeWire `pw-cat` for audio loopback, `uinput` for keyboard/mouse injection) but
their platform-spec sections have been deliberately removed from this document to
keep the focus on the capture and encode pipeline.

When we resume work on audio and input, the existing core specs remain authoritative:
- [`specs/MODULE_AUDIO.md`](../../specs/MODULE_AUDIO.md)
- [`specs/MODULE_INPUT.md`](../../specs/MODULE_INPUT.md)

This platform spec will be updated with Linux-specific details (PipeWire backend
selection, uinput permissions, keymap coverage) at that point.

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum kernel | 4.15+ (DRM universal planes, VA-API modern drivers) |
| KMS capture | `CAP_SYS_ADMIN` or root — `sudo setcap cap_sys_admin+p ./viewport-rds` |
| VA-API encode | Intel: `intel-media-va-driver` or `i965-va-driver`; AMD: `mesa-va-drivers` |

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

## Current vs Target State (Capture + Encode)

| Component | Current code | Target (refactor) |
|-----------|-------------|------------------|
| Capture | KMS+EGL (working) + X11grab subprocess | KMS+EGL only (no fallback in default binary) |
| SW encode | VP8 libvpx → **OpenH264 CGo** ✅ | OpenH264 CGo |
| HW encode | ffmpeg pipe → h264_vaapi (CPU copies) | libva CGo direct (zero-copy, no ffmpeg) |
| Protocol | 17-byte header | 22-byte header v1 (versioned, sequenced) |

(Audio and input current-vs-target deferred — see Audio + Input section above.)
