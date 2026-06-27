# FeatherDesk — Windows Platform Spec

## Overview

Windows is a primary target for FeatherDesk. The use case covers both **remote control** and **gaming/high-fps streaming**. Target: 60fps at 1080p/1440p, <20ms total pipeline latency, H.264 as the default codec.

---

## Capture

### Single capture path: DXGI Desktop Duplication

Windows uses **one** capture mechanism — DXGI Desktop Duplication — for all
GPU vendors and all deployment scenarios (with or without a physical display).

Benchmarking proved DXGI DD's raw capture overhead is **sub-microsecond** on
both NVIDIA and AMD GPUs. Vendor-specific capture APIs (NvFBC, AMF Display
Capture) were considered and rejected — they cannot improve on near-zero
overhead, and adding them would double maintenance for no measurable benefit.

```
capture/
├── DXGI_DD_WINDOWS_SPEC.md       ← the only capture add-on
└── README.md                     ← runtime details, headless setup
```

### Add-on summary

| Add-on | Build tag | Hardware | Spec | Headless support |
|--------|-----------|----------|------|------------------|
| **DXGI Desktop Duplication** | `dxgi_dd` | Any GPU (WDDM 1.2+, Win 8+) | [`capture/DXGI_DD_WINDOWS_SPEC.md`](./capture/DXGI_DD_WINDOWS_SPEC.md) | Integrated IddCx virtual display driver auto-installs on first launch when no physical display detected |

Output: `ID3D11Texture2D` — directly consumable by every Windows HW encoder
(MF HW, NVENC, AMF, QSV) with zero-copy.

### Headless support (integrated)

On machines with no physical display (servers, headless workstations, VMs),
the `dxgi_dd` add-on bundles a **pre-signed IddCx virtual display driver**
and auto-installs it on first launch via `pnputil` (one-time UAC). After
install, the virtual display appears as a normal DXGI output and DXGI DD
captures it like any physical monitor.

See [`capture/DXGI_DD_WINDOWS_SPEC.md`](./capture/DXGI_DD_WINDOWS_SPEC.md#headless-support-integrated-iddcx-virtual-display)
for the full auto-install flow.

### What was rejected

| API | Why rejected |
|-----|-------------|
| Windows.Graphics.Capture (WGC) | Only advantage was per-window capture, which is out of scope. Full-desktop WGC is slower than DXGI DD. |
| NvFBC for Windows | DXGI DD overhead already sub-microsecond — no measurable gain. Would add NVIDIA driver patcher concerns on GeForce. |
| AMF Display Capture | Same reason — no measurable improvement over DXGI DD for full-desktop capture. |
| GDI BitBlt | ~30–50ms, misses hardware-accelerated content (DirectX games, modern apps). |
| Magnification API | ~15–30ms, CPU-only. Niche. |
| DirectShow / MF screen capture | Wrappers around DXGI DD. No benefit. |

### Runtime probe order

```
1. dxgi_dd compiled in AND active display found?              → use DXGI DD
2. dxgi_dd compiled in AND no display + admin?                → install IddCx VDD → use DXGI DD
3. dxgi_dd compiled in AND no display + no admin?             → prompt for elevation, then continue
4. None of the above?                                          → fatal: no capture add-on installed
```

### Measured benchmark (GTX 1080 Ti + RX 6800 XT)

| GPU | Display | Test | P50 | P95 | P99 |
|-----|---------|------|-----|-----|-----|
| GTX 1080 Ti | Real 60Hz | Blocking (vsync wait) | 16.4ms | 17.4ms | 18.1ms |
| GTX 1080 Ti | Real 60Hz | **Polling (raw overhead)** | **<0.001ms** | **<0.001ms** | 0.5ms |
| RX 6800 XT | Dummy HDMI | Blocking (vsync wait) | 16.5ms | 17.5ms | 18.2ms |
| RX 6800 XT | Dummy HDMI | **Polling (raw overhead)** | **<0.001ms** | **<0.001ms** | <0.001ms |

The blocking latency (~16.4ms) is purely the 60Hz refresh interval —
unavoidable for any frame-based capture. Raw acquisition overhead is
effectively zero.

---

## Video Encoding

### Every encoder is an add-on (pluggable architecture)

The Windows default binary contains **no encoders**. Every encoder — software and
hardware — is a build-tagged add-on. Users compile in exactly the encoders they
want. The full set:

```
encoders/
├── SW/
│   ├── OPENH264_CGO_WINDOWS_SPEC.md           ← BSD-licensed Cisco SW (commercial use)
│   └── X264_SUBPROCESS_WINDOWS_SPEC.md        ← GPL-isolated x264 subprocess (home / OSS, 2× faster)
└── HW/
    ├── MEDIAFOUNDATION_HW_WINDOWS_SPEC.md     ← cross-vendor HW (NVIDIA + AMD + Intel + Qualcomm)
    ├── NVENC_WINDOWS_SPEC.md                  ← NVIDIA direct
    ├── AMF_WINDOWS_SPEC.md                    ← AMD direct (Apache 2.0)
    └── QSV_WINDOWS_SPEC.md                    ← Intel direct via oneVPL (covers Arc)
```

See [`encoders/README.md`](./encoders/README.md) for recommended combinations,
runtime probe order, and rationale.

### Why MF HW is the recommended cross-vendor default

Unlike Linux (where VA-API uniformly covers Intel + AMD + NVIDIA), Windows
historically fragmented per-vendor. **MediaFoundation Hardware Transform** is the
closest equivalent: a single Microsoft API that routes to whatever vendor MFT is
registered. For most deployments, `mf_hw` alone is sufficient.

For peak performance or vendor-specific features (NVENC's
`REF_FRAMES_INVALIDATION`, AMF's Pre-Analysis), add the vendor-direct add-on
alongside MF HW.

### TL;DR — recommended combinations

| Deployment | Add-ons |
|-----------|---------|
| Generic Windows, commercial | `dxgi_dd` + `openh264` + `mf_hw` |
| Generic Windows, home / OSS | `dxgi_dd` + `x264` + `mf_hw` |
| ARM Snapdragon | `dxgi_dd` + `openh264` + `mf_hw` (OpenH264 has NEON path) |
| NVIDIA-only | `dxgi_dd` + `openh264` + `nvenc` |
| AMD-only | `dxgi_dd` + `openh264` + `amf` |
| Intel-only | `dxgi_dd` + `openh264` + `qsv` |

---

## Audio + Input

⏸️ **Deferred.** The Windows audio (WASAPI loopback) and input (Interception
driver, `win_touch` InjectTouchInput, ViGEmBus) sections have been deliberately
removed from this document to keep the focus on the capture and encode pipeline.

When we resume work on audio and input, the existing core specs remain authoritative:
- [`specs/media/MODULE_AUDIO.md`](../../media/MODULE_AUDIO.md)
- [`specs/interaction/MODULE_INPUT.md`](../../interaction/MODULE_INPUT.md)

This platform spec will be updated with Windows-specific details (WASAPI loopback,
Interception driver, `win_touch`, optional ViGEmBus for gamepads) at that point.

---

## Benchmark Tool

A working benchmark binary exists for Windows in `cmd/benchmark/`. It benchmarks:
- GDI capture (bitblt_only and bitblt_getdib variants)
- DXGI Desktop Duplication (stub — pending COM bindings)
- System probing (CPU, GPU, display, ffmpeg encoder availability)

Results stored in SQLite (`featherdesk_bench.db`) + raw per-frame CSVs.

**Build:** `go build -o bench.exe ./cmd/benchmark/` (pure Go, no CGo required)

**First run results on this machine (Ryzen 9 5900X, Windows 11, virtual display):**
```
GDI bitblt_only:    fps=58.0  p50=16.7ms  p95=20.0ms  p99=32.6ms
GDI bitblt_getdib:  fps=53.9  p50=16.9ms  p95=31.9ms  p99=35.8ms
DXGI:               UNAVAILABLE (Parsec virtual display, expected)
```

---

## Deployment Requirements

| Requirement | Details |
|-------------|---------|
| Minimum Windows | **Windows 8 + WDDM 1.2** (DXGI DD minimum). Windows 10/11 all supported. |
| DirectX | **DirectX 11.1+** for DXGI Desktop Duplication |
| DXGI on RDP | ⚠️ DXGI DDup returns `DXGI_ERROR_UNSUPPORTED` in RDP sessions → DXGI DD add-on auto-installs IddCx virtual display driver to bypass (see [`capture/DXGI_DD_WINDOWS_SPEC.md`](./capture/DXGI_DD_WINDOWS_SPEC.md#headless-support-integrated-iddcx-virtual-display)) |
| DXGI on virtual adapters | ⚠️ Same — Parsec, VMware, VirtualBox display adapters block DDup. IddCx VDD bypass applies the same way. |
| Admin rights | Not required for normal capture. **One-time UAC** required for IddCx VDD install on first headless launch. |
| Driver | Any GPU driver from the last 5 years supports DDup |
