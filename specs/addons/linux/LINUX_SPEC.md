# FeatherDesk — Linux Platform Spec

## Overview

Linux is the **primary target** for FeatherDesk, and the only platform with a
working end-to-end reference codebase — the Go implementation on
`feature-libav-vp8s8`. What runs there is not what this branch specifies: capture
runs through `x11grab.go` + `screencast.py`, not `kms_egl`, and input runs through
`/dev/uinput`, not the specced in-core `enigo` default. This spec documents the
target architecture and, where the two differ, says which is which
([`PLATFORM_COMPAT.md`](../../PLATFORM_COMPAT.md) "Implementation Status").

---

## Capture

### Every capture backend is an add-on (pluggable architecture)

The Linux default binary contains **no capture backends**. Every capture path is
an add-on shared library, mirroring the encoder architecture. Users drop in
exactly the capture method(s) they need.

```
capture/
├── KMS_EGL_LINUX_SPEC.md   ← default recommended add-on, universal GPU coverage
│                          (specced; the Go prototype was abandoned — see its "Status")
├── NVFBC_LINUX_SPEC.md     ← NVIDIA proprietary, lower-latency alternative
└── README.md               ← runtime probe order + recommended combinations
```

### Add-on summary

| Add-on | Add-on ID | Hardware | Spec | When to use |
|--------|-----------|----------|------|------------|
| **KMS+EGL DMA-BUF** | `kms_egl` | Any GPU, any display server | [`capture/KMS_EGL_LINUX_SPEC.md`](./capture/KMS_EGL_LINUX_SPEC.md) | Universal default — requires `CAP_SYS_ADMIN` **and a real KMS CRTC** |
| **NvFBC** | `nvfbc` | NVIDIA proprietary driver, X11 only | [`capture/NVFBC_LINUX_SPEC.md`](./capture/NVFBC_LINUX_SPEC.md) | ~2–3ms lower than KMS+EGL on NVIDIA proprietary; official NVIDIA path; pairs with NVENC encoder for full zero-copy GPU-resident pipeline |
| **wlr-screencopy** | `wl_screencopy` | Any GPU; wlroots-family compositor | [`capture/WL_SCREENCOPY_LINUX_SPEC.md`](./capture/WL_SCREENCOPY_LINUX_SPEC.md) | **No root.** Headless/nested Wayland, or any deployment that cannot grant `CAP_SYS_ADMIN` |
| **Portal ScreenCast** | `pw_portal` | Any GPU; any compositor with xdg-desktop-portal | [`capture/PW_PORTAL_LINUX_SPEC.md`](./capture/PW_PORTAL_LINUX_SPEC.md) | **No root.** GNOME/KDE Wayland where `wl_screencopy` is unavailable; costs an interactive consent prompt |

KMS+EGL operates at the kernel/DRM level below the display server, so it is
display-server **agnostic** — X11 and Wayland (GNOME/KDE/wlroots) alike. It is
**not** display-server *optional*: it captures DRM/KMS **scanout**, so it needs a
CRTC with a mode set and something rendering to it. Its two constraints are
therefore `CAP_SYS_ADMIN`:

```bash
sudo setcap cap_sys_admin+p ./featherdesk
```

…and a real CRTC. **Xvfb does not satisfy the second** — it renders into main
memory and never touches DRM, so `kms_egl` on an Xvfb-only host captures
nothing. A headless `kms_egl` host needs a connected or force-enabled connector
(e.g. `video=HDMI-A-1:1920x1080e`) plus a compositor rendering to it. See
[`../../PLATFORM_COMPAT.md`](../../PLATFORM_COMPAT.md) "Headless on Linux".

NvFBC is the only capture path that beats KMS+EGL on any hardware — and only on
NVIDIA under X11, where KMS+EGL has historically been finicky with the
proprietary driver. Intel and AMD do not need capture add-ons; neither vendor has
a proprietary capture API on Linux, so KMS+EGL is the entire path.

`kms_egl` and `nvfbc` report the pointer separately — `kms_egl` from the DRM
cursor plane (X11 and Wayland alike), `nvfbc` from XFixes — and both can embed it
instead when the session resolves `cursorMode = "embedded"`. The two no-root
add-ons differ from each other here, and it is the sharpest distinction between
them:

- **`pw_portal` can** — portal `cursor_mode = Metadata` delivers pointer position
  and the cursor bitmap as PipeWire buffer metadata, so it declares `CURSOR` when
  the backend grants that mode. It is fixed for the session's life, because it is
  a `SelectSources` argument rather than a runtime switch.
- **`wl_screencopy` cannot** — no wlroots protocol reports the pointer to a
  screencopy client at all, so it never declares `CURSOR`, always resolves
  `cursorMode = "embedded"`, and is **ineligible** under
  `[capture] cursor_mode = "separate"`: the pipeline skips it at selection rather
  than streaming a pointerless desktop (MODULE_CAPTURE "Cursor delivery",
  MODULE_PIPELINE step 3d).

Each spec's "Cursor Handling" section is normative for which capability bits it
declares.

**Two no-root paths now exist** (`wl_screencopy`, `pw_portal`), and they are
add-ons like every other backend — dropping them in changes nothing about the
default. They exist for two deployments `kms_egl` structurally cannot serve:
a host where `CAP_SYS_ADMIN` is not grantable, and headless Wayland with no
forceable connector. Neither is a container story — FeatherDesk is not a
containerized application (see `PROJECT_ARTIFACTS/GAP_TRIAGE.md`, closed finding
C1); *no-root capture* and *run in Docker* are separate claims and only the first
is in scope. Both add-ons are slower than `kms_egl` and neither is chosen ahead
of it when it is available.

### Runtime probe order

```
1. nvfbc loaded AND NVIDIA proprietary driver present AND X11?      → use NvFBC
2. kms_egl loaded AND root / CAP_SYS_ADMIN AND a CRTC with a mode?  → use KMS+EGL
3. wl_screencopy loaded AND a wlroots-family compositor?            → use wlr-screencopy
4. pw_portal loaded AND a portal ScreenCast session is granted?     → use Portal
5. None of the above?                                                → fatal: no capture
```

Step 2's CRTC condition is part of `kms_egl.probe()`: a host with the capability
but no active scanout reports `available: false` with a `reason` naming it, so
the order falls through to a no-root add-on instead of selecting a capturer that
would return empty frames.

NvFBC is an X11-only API, so under Wayland it reports
`ProbeReport { available: false, reason }` — not an error — and `kms_egl` is the
whole Linux path there.

The first available capture wins. See [`capture/README.md`](./capture/README.md)
for recommended add-on combinations and the documented reasoning for which other
paths were considered and rejected — XShm, X11grab and `vkms` remain rejected;
wlr-screencopy and the PipeWire portal do **not**, and are the two no-root
add-ons above.

---

## Video Encoding

### Every encoder is an add-on (pluggable architecture)

The Linux default binary contains **no encoders**. Every encoder — software and
hardware — is an add-on shared library. Users drop in exactly the encoders they
want. The full set:

```
encoders/
├── SW/
│   ├── OPENH264_LINUX_SPEC.md     ← BSD-licensed Cisco SW (the software default)
│   └── X264_SUBPROCESS_LINUX_SPEC.md  ← GPL-isolated x264 subprocess (opt-in, 2× faster)
└── HW/
    ├── LIBVA_LINUX_SPEC.md            ← Intel + AMD + NVIDIA via VA-API (MIT)
    ├── NVENC_LINUX_SPEC.md            ← NVIDIA direct
    └── AMF_ROCM_SPEC.md               ← AMD direct via ROCm (Apache 2.0)
```

See [`encoders/README.md`](./encoders/README.md) for recommended combinations,
runtime probe order, and rationale.

### TL;DR — recommended combinations

| Deployment | Add-ons | Why |
|-----------|---------|-----|
| Generic Linux server | `openh264` + `libva` | Universal coverage, smallest BSD add-on set |
| Measured CPU-bound host, GPL acceptable | `openh264` + `x264` + `libva`, forced with `[encode] force_addon = "x264"` | 2× faster SW path where it has been measured; `openh264` stays as the fallback |
| NVIDIA workstation | `openh264` + `nvenc` | Vendor-specific NVIDIA tuning |
| AMD workstation | `openh264` + `libva` + `amf_rocm` | AMD-specific tuning + universal fallback |
| Container / no GPU | `openh264` only | SW-only BSD, smallest binary |

### How vendor APIs map to Linux

| GPU vendor | LIBVA covers | Vendor-direct add-on available |
|-----------|--------------|-------------------------------|
| Intel | ✅ first-party (`iHD` / `i965`) | — (no separate Intel SDK on Linux) |
| AMD | ✅ first-party (Mesa `radeonsi`) | `amf_rocm` for AMF tuning |
| NVIDIA | ⚠️ via unofficial `nvidia-vaapi-driver` | `nvenc` recommended for NVIDIA deployments |

### Confirmed Fallback Order

The order selects an **add-on**, not a codec — which codec the selected add-on
emits is a separate rule, below.

```
1. HW: nvenc  →  amf_rocm  →  libva
     → nvenc:    NVIDIA proprietary driver
     → amf_rocm: AMD, AMF runtime over ROCm/Vulkan
     → libva:    Intel Sandy Bridge+, AMD GCN+, NVIDIA via the vaapi wrapper
     → Config codec string: computed per MODULE_ABI (H.264 High; avc1.64002A at 1080p60)
     → Status: 📋 Specced — encoders/HW/{NVENC,AMF_ROCM,LIBVA}_*.md

2. SW: openh264
     → No GPU present, or no HW add-on probed available
     → Works on every machine including no-GPU ARM/x86 (Graviton etc.)
     → Config codec string: computed per MODULE_ABI (H.264 Constrained Baseline;
       avc1.42E02A at 1080p60)
     → Status: ✅ Working — the software default

   x264 is NOT in this order. It is opt-in, reached only by
   [encode] force_addon = "x264" (MODULE_ENCODE "Software encoder order").
```

Vendor-specific SDKs precede the generic abstraction: `nvenc`/`amf_rocm` before
`libva`. The authoritative statement of the order is
[`MODULE_PIPELINE.md`](../../core/MODULE_PIPELINE.md) startup step 3e; this is a
restatement of the Linux row.

The encoder advertises **H.264** (`avc1.*`) for every SDR session, on every
platform, regardless of what HEVC hardware is present. **HEVC Main10**
(`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's
WebCodecs cannot decode it, and a codec no attached client can decode is a
black screen, not a saving. Which HW encoder is *selected* is a separate
question from which codec it *emits* — the probe order picks the add-on, this
rule picks the codec. On Linux the HDR path is `libva`'s `VAProfileHEVCMain10`
(Intel Skylake+, AMD Polaris+, NVIDIA via the wrapper); `hevc.gva`-class support
is what makes HDR available at all.

**No software HEVC.** libx265 has triple HEVC patent pool exposure (MPEG-LA, HEVC
Advance, Velos Media). If HEVC hardware is unavailable, an HDR session falls back
to SDR H.264 rather than to a software HEVC encoder.

### GPU Encode Capability Matrix

| GPU | Driver | H.264 HW | HEVC HW | AV1 HW |
|-----|--------|---------|---------|--------|
| Intel Sandy Bridge–Broadwell (2011–2015) | `i965` | ✅ | ❌ | ❌ |
| Intel Skylake–Ice Lake (2015–2019) | `iHD` | ✅ | ✅ | ❌ |
| Intel Tiger Lake / Xe / Arc (2020+) | `iHD` | ✅ | ✅ 10-bit | ✅ Arc+ |
| AMD GCN / RX 400+ (2016+) | Mesa | ✅ | ✅ | ❌ |
| AMD RDNA2 / RX 6000+ (2020+) | Mesa | ✅ | ✅ 10-bit | ❌ (decode only) |
| AMD RDNA3 / RX 7000+ (2022+) | Mesa | ✅ | ✅ 10-bit | ✅ |
| NVIDIA *(via nvidia-vaapi-driver)* | unofficial | ✅ | ✅ | ❌ |
| No GPU / CPU-only | — | ❌ | ❌ | ❌ → OpenH264 SW |

### Software Fallback — OpenH264 (Rust FFI)

**Status: ✅ Working. The software default (hardware path not yet built).** It
runs at up to 30 fps (see [`CENTRAL_SPEC.md`](../../CENTRAL_SPEC.md)
"Motion-to-photon budget"); the pipeline's sustainable-rate control lowers the
advertised fps to what the host actually sustains rather than advertising 60 and
delivering 20.

Same Rust crate as Windows and macOS. No subprocess. No ffmpeg. BSD-2 licensed.

| Resolution | FPS ceiling | p50 latency | CPU (1 core) |
|-----------|------------|------------|-------------|
| 1920×1080 | ~125 fps | ~8ms | ~25% |
| 2560×1440 | ~65 fps | ~15ms | ~25% |

These are encode-only figures. On the CPU-readback path the end-to-end rate is
capture-bound, not encode-bound — the PBO readback costs ~6 ms at 1080p and
~11 ms at 1440p ([`capture/KMS_EGL_LINUX_SPEC.md`](./capture/KMS_EGL_LINUX_SPEC.md)
"CPU readback"), so the sustainable end-to-end rates are ~70 fps and ~38 fps
respectively, and the pipeline's sustainable-rate control settles on whichever the
host actually reaches.

**Crate:** `addons/encode/openh264/` (cdylib → `featherdesk-addon-openh264`)

### Hardware Path — libva (Rust FFI)

**Status: 📋 Specced in `encoders/HW/LIBVA_LINUX_SPEC.md`. Not yet built.**

No ffmpeg. `libva` MIT licensed. Direct Rust FFI. Zero-copy path when combined with
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
**Full implementation plan:** `encoders/HW/LIBVA_LINUX_SPEC.md`

---

## Audio + Input

**Input is no longer deferred.** Keyboard + mouse injection is **built into core**
via the default `enigo` `KeyMouseInjector` (Linux backend: XTEST/libei), so a
default binary is not view-only for kb/mouse. The **`uinput` add-on is an opt-in
override** of that `enigo` default for power users — a kernel-level injector
(kernel `/dev/uinput`, X11 + Wayland + console, plus gamepad force-feedback). See
[`input/UINPUT_LINUX_SPEC.md`](./input/UINPUT_LINUX_SPEC.md).

**Audio design is LOCKED, implementation deferred** behind the video trigger.
The Linux backend is the `pipewire` add-on — **native libpipewire** monitor
capture (the old `pw-cat` subprocess is gone), Pulse/ALSA fallback. Host→client
only; Opus or PCM codec. See [`audio/PIPEWIRE_LINUX_SPEC.md`](./audio/PIPEWIRE_LINUX_SPEC.md).

The core specs remain authoritative:
- [`specs/media/MODULE_AUDIO.md`](../../media/MODULE_AUDIO.md)
- [`specs/interaction/MODULE_INPUT.md`](../../interaction/MODULE_INPUT.md)

This platform spec will be updated with Linux-specific details (PipeWire backend
selection, uinput permissions, keymap coverage) at that point.

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum kernel | 4.15+ (DRM universal planes, VA-API modern drivers) |
| KMS capture | `CAP_SYS_ADMIN` or root — `sudo setcap cap_sys_admin+p ./featherdesk` |
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
- Synchronous `glReadPixels` (1440p BGRA): **50ms per frame** — the measured
  stall the add-on's PBO ring exists to remove
- Frame pacing (30fps): 5 frames in 170ms ✅

---

## Current vs Target State (Capture + Encode)

| Component | Current code | Target (refactor) |
|-----------|-------------|------------------|
| Capture | X11grab + PipeWire screencast (KMS+EGL prototyped, then abandoned on tiled 10-bit scanout) | `kms_egl` add-on only (X11grab deleted) |
| SW encode (default) | VP8 libvpx → **OpenH264 (Rust FFI)** ✅ | `openh264` add-on (BSD, Cisco) — the software default |
| SW encode (opt-in) | — | `x264` subprocess add-on (GPL-isolated, 2× faster; `force_addon` only) |
| HW encode | ffmpeg pipe → h264_vaapi (CPU copies) | `libva` add-on (Rust FFI direct, zero-copy, no ffmpeg) |
| HW encode (vendor-specific) | — | `nvenc`, `amf_rocm` add-ons |
| Protocol | 17-byte header | 22-byte header v1 (versioned, sequenced) |

(Audio and input current-vs-target deferred — see Audio + Input section above.)
