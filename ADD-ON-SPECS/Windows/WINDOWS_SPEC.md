# FeatherDesk — Windows Platform Spec

## Overview

Windows is a primary target for FeatherDesk. The use case covers both **remote control** and **gaming/high-fps streaming**. Target: 60fps at 1080p/1440p, <20ms total pipeline latency, H.264 as the default codec.

---

## Capture

### Every capture backend is an add-on (pluggable architecture)

The Windows default binary contains **no capture backends**. Every capture path
is a build-tagged add-on, mirroring the Linux and macOS structure. Users
compile in exactly the capture method(s) they need.

```
capture/
├── DXGI_DD_WINDOWS_SPEC.md       ← default recommended add-on, all GPU vendors
├── NVFBC_WINDOWS_SPEC.md         ← NVIDIA proprietary, lowest-latency on NVIDIA
├── AMF_CAPTURE_WINDOWS_SPEC.md   ← AMD proprietary, native zero-copy with AMF encoder
└── README.md                     ← runtime probe order + recommended combinations
```

### Add-on summary

| Add-on | Build tag | Hardware | Spec | When to use |
|--------|-----------|----------|------|------------|
| **DXGI Desktop Duplication** | `dxgi_dd` | Any GPU (WDDM 1.2+, Win 8+) | [`capture/DXGI_DD_WINDOWS_SPEC.md`](./capture/DXGI_DD_WINDOWS_SPEC.md) | Universal default — ~2–4ms, no elevation |
| **NvFBC for Windows** | `nvfbc_win` | NVIDIA proprietary driver | [`capture/NVFBC_WINDOWS_SPEC.md`](./capture/NVFBC_WINDOWS_SPEC.md) | ~50% lower latency than DXGI DD on NVIDIA; pairs with NVENC |
| **AMD AMF Display Capture** | `amf_capture` | AMD Polaris+ (Adrenalin 21.5+) | [`capture/AMF_CAPTURE_WINDOWS_SPEC.md`](./capture/AMF_CAPTURE_WINDOWS_SPEC.md) | Same AMFContext as `amf` encoder; native zero-copy; Apache 2.0 |

All three produce D3D11-backed surfaces (ID3D11Texture2D), so they're all
compatible with every Windows HW encoder add-on (MF HW, NVENC, AMF, QSV).
Vendor-specific add-ons simply integrate more tightly with their matching
encoder.

### What was rejected

| API | Why rejected |
|-----|-------------|
| Windows.Graphics.Capture (WGC) | Only advantage was per-window capture, which is out of scope. Full-desktop WGC is slower than DXGI DD. |
| GDI BitBlt | ~30–50ms, misses hardware-accelerated content. Benchmarked at 16.7ms p50 on Parsec virtual display — but fails on real DirectX apps. |
| Magnification API | ~15–30ms, CPU-only. Niche. |
| DirectShow / MF screen capture | Wrappers around DXGI DD. No benefit. |

### Runtime probe order

```
1. nvfbc_win compiled in AND NVIDIA GPU present AND probe succeeds?  → use NvFBC
2. amf_capture compiled in AND AMD GPU present?                       → use AMF Display Capture
3. dxgi_dd compiled in?                                                → use DXGI DD (universal)
4. None of the above?                                                  → fatal: no capture
```

See [`capture/README.md`](./capture/README.md) for recommended add-on
combinations and detailed rationale.

### Historical benchmark (GDI only — real DXGI DD pending hardware bench)

| Variant | FPS | p50 | p95 | p99 |
|---------|-----|-----|-----|-----|
| GDI bitblt_only | 58.0 | 16.7ms | 20.0ms | 32.6ms |
| GDI bitblt_getdib | 53.9 | 16.9ms | 31.9ms | 35.8ms |

> Tested on AMD Ryzen 9 5900X, Parsec virtual display, Windows 11. GDI
> numbers are worst-case baseline only — DXGI DD expected: 1–5ms p50 on
> real hardware. Full benchmark pass with NVIDIA + AMD GPUs pending.

---

## Video Encoding

### Every encoder is an add-on (pluggable architecture)

The Windows default binary contains **no encoders**. Every encoder — software and
hardware — is a build-tagged add-on. Users compile in exactly the encoders they
want. The full set:

```
encoders/
├── SW/
│   ├── OPENH264_CGO_WINDOWS_SPEC.md           ← cross-platform SW (same code as Linux + macOS)
│   └── MEDIAFOUNDATION_SW_WINDOWS_SPEC.md     ← Windows-native SW (no third-party DLL)
└── HW/
    ├── MEDIAFOUNDATION_HW_WINDOWS_SPEC.md     ← cross-vendor HW (NVIDIA + AMD + Intel + Qualcomm)
    ├── NVENC_WINDOWS_SPEC.md                  ← NVIDIA direct (REF_FRAMES_INVALIDATION)
    ├── AMF_WINDOWS_SPEC.md                    ← AMD direct (Pre-Analysis, Apache 2.0)
    ├── QSV_WINDOWS_SPEC.md                    ← Intel direct via oneVPL (covers Arc)
    └── VULKAN_VIDEO_WINDOWS_SPEC.md           ← cross-vendor royalty-free, future-facing
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
| Generic Windows (any GPU) | `openh264` + `mf_hw` |
| ARM Snapdragon | `mf_sw` + `mf_hw` |
| NVIDIA-only | `openh264` + `nvenc` |
| AMD-only | `openh264` + `amf` |
| Intel-only | `openh264` + `qsv` |

---

## Audio + Input

⏸️ **Deferred.** The Windows audio (WASAPI loopback) and input (SendInput, ViGEmBus,
InjectSyntheticPointerInput) sections have been deliberately removed from this
document to keep the focus on the capture and encode pipeline.

When we resume work on audio and input, the existing core specs remain authoritative:
- [`specs/MODULE_AUDIO.md`](../../specs/MODULE_AUDIO.md)
- [`specs/MODULE_INPUT.md`](../../specs/MODULE_INPUT.md)

This platform spec will be updated with Windows-specific details (WASAPI loopback,
SendInput, optional ViGEmBus for gamepads) at that point.

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
| Minimum Windows | **Windows 10 1803** (WGC minimum; DXGI works on Windows 8+) |
| DirectX | **DirectX 11.1+** for DXGI Desktop Duplication |
| DXGI on RDP | ❌ DXGI DDup returns `DXGI_ERROR_UNSUPPORTED` in RDP sessions → fall back to WGC/GDI |
| DXGI on virtual adapters | ❌ Same — Parsec, VMware, VirtualBox display adapters block DDup |
| Admin rights | Not required for capture or encoding. DXGI DDup works as standard user. |
| Driver | Any GPU driver from the last 5 years supports DDup |
