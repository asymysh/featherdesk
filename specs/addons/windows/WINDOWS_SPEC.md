# FeatherDesk — Windows Platform Spec

## Overview

Windows is a primary target for FeatherDesk. The use case covers both **remote control** and **gaming/high-fps streaming**. Target: 60fps at 1080p/1440p, H.264 as the default codec, and <20ms total pipeline latency **on the video path** — the figure this spec budgets against. End-to-end motion-to-photon with `[audio] enabled` (the default) is ~45ms, because audio-master A/V sync slaves the video clock to the audio clock; see [`../../media/MODULE_AUDIO.md`](../../media/MODULE_AUDIO.md).

---

## Capture

### Single capture path: DXGI Desktop Duplication

Windows uses **one** capture mechanism — DXGI Desktop Duplication — for all
GPU vendors and all deployment scenarios (with or without a physical display).

DXGI Desktop Duplication is the single Windows capture path for maintenance
reasons, not measured ones: it is the only vendor-neutral API that yields an
`ID3D11Texture2D` directly consumable zero-copy by MF HW, NVENC, AMF and QSV,
so one add-on serves every GPU. Vendor-specific capture APIs (NvFBC, AMF
Display Capture) are **deferred, not rejected on evidence** — no DXGI
acquisition has been measured yet (the bench tool's DXGI backend is a stub
pending COM bindings, and every recorded session reports it unavailable), so
there is no number to compare them against. Revisit if a measured DXGI
acquisition turns out to be a material share of the frame budget.

```
capture/
├── DXGI_DD_WINDOWS_SPEC.md       ← the only capture add-on
└── README.md                     ← runtime details, headless setup
```

### Add-on summary

| Add-on | Add-on ID | Hardware | Spec | Headless support |
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
| NvFBC for Windows | Deferred, not rejected on evidence — one vendor-neutral add-on covers every GPU, and no measured DXGI acquisition cost exists to compare against. Would also add NVIDIA driver-patcher concerns on GeForce. |
| AMF Display Capture | Deferred for the same maintenance reason — a second AMD-only capture path for an unquantified gain. |
| GDI BitBlt | ~30–50ms, misses hardware-accelerated content (DirectX games, modern apps). |
| Magnification API | ~15–30ms, CPU-only. Niche. |
| DirectShow / MF screen capture | Wrappers around DXGI DD. No benefit. |

### Runtime probe order

```
1. dxgi_dd loaded AND active display found?                   → use DXGI DD
2. dxgi_dd loaded AND no display + admin?                     → install IddCx VDD → use DXGI DD
3. dxgi_dd loaded AND no display + no admin?                  → prompt for elevation, then continue
4. None of the above?                                          → fatal: no capture add-on installed
```

### Capture cost: not yet measured

No DXGI acquisition has been benchmarked. The bench tool's DXGI backend is a
stub pending COM bindings, and every recorded session in
`PROJECT_ARTIFACTS/bench_out` reports `{"backend":"dxgi","available":false}` —
the bench machine's display is a Parsec virtual adapter, which blocks Desktop
Duplication. The one thing that is structural rather than measured: a *blocking*
`AcquireNextFrame` cannot return sooner than the display's refresh interval
(~16.7 ms at 60 Hz), because there is no new frame before then. The polling cost
on top of that is what remains unquantified.

---

## Video Encoding

### Every encoder is an add-on (pluggable architecture)

The Windows default binary contains **no encoders**. Every encoder — software and
hardware — is an add-on shared library. Users drop in exactly the encoders they
want. The full set:

```
encoders/
├── SW/
│   ├── OPENH264_WINDOWS_SPEC.md           ← BSD-licensed Cisco SW (commercial use)
│   └── X264_SUBPROCESS_WINDOWS_SPEC.md        ← GPL-isolated x264 subprocess (opt-in, force_addon only)
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
| Generic Windows, any licence | `dxgi_dd` + `openh264` + `mf_hw` |
| Measured CPU-bound host (x264 opt-in) | `dxgi_dd` + `x264` + `mf_hw`, with `[encode] force_addon = "x264"` |
| ARM Snapdragon | `dxgi_dd` + `openh264` + `mf_hw` (OpenH264 has NEON path) |
| NVIDIA-only | `dxgi_dd` + `openh264` + `nvenc` |
| AMD-only | `dxgi_dd` + `openh264` + `amf` |
| Intel-only | `dxgi_dd` + `openh264` + `qsv` |

---

## Audio + Input

⏸️ **Deferred.** The Windows audio (WASAPI loopback) section has been
deliberately removed from this document to keep the focus on the capture and
encode pipeline.

**Keyboard/mouse input is NOT deferred and NOT an add-on.** The default
kb/mouse injector ships **in core** via the `enigo` crate, whose Windows
backend is `SendInput`. It is chosen because `SendInput` is anti-cheat-safe,
the same approach Sunshine uses. So a default Windows build is fully
controllable (not view-only) with no input add-on installed.

The input add-ons are extensions/overrides of that in-core default:
- `interception` — opt-in, power-user override (kernel filter driver; reaches
  elevated windows + the Secure Attention Sequence). See its spec for the
  anti-cheat-risk warning.
- `win_touch` (`InjectTouchInput`) — touch injection (`enigo` covers neither
  touch nor gamepad).
- `vigem` (ViGEmBus) — virtual gamepad.

When we resume work on audio, the existing core specs remain authoritative:
- [`specs/media/MODULE_AUDIO.md`](../../media/MODULE_AUDIO.md)
- [`specs/interaction/MODULE_INPUT.md`](../../interaction/MODULE_INPUT.md)

This platform spec will be updated with Windows-specific audio details (WASAPI
loopback) at that point.

---

## Benchmark Tool

A working benchmark binary exists for Windows in `cmd/benchmark/`. It benchmarks:
- GDI capture (bitblt_only and bitblt_getdib variants)
- DXGI Desktop Duplication (stub — pending COM bindings)
- System probing (CPU, GPU, display, ffmpeg encoder availability)

Results stored in SQLite (`featherdesk_bench.db`) + raw per-frame CSVs.

**Build:** `cargo build --release -p featherdesk-bench` (no native/FFI dependencies required)

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
| Process DPI awareness | **`PER_MONITOR_AWARE_V2`, mandatory** — see below. Both `dxgi_dd` and `win_touch` depend on it |
| Long paths | The app manifest sets `longPathAware`; file-transfer folders are opened `\\?\`-prefixed (see [`specs/interaction/MODULE_FILETRANSFER.md`](../../interaction/MODULE_FILETRANSFER.md)) |

### DPI awareness

`PER_MONITOR_AWARE_V2` is process-global and may be set only once, before any
DPI-dependent call. `dxgi_dd` needs it for physical surface dimensions and
`win_touch` needs it for physical injection coordinates, so neither add-on may
set it — the host entry point owns it, at
[`specs/core/MODULE_PIPELINE.md`](../../core/MODULE_PIPELINE.md) startup step 0,
and declares it twice:

- The **application manifest** embedded in `featherdesk.exe`, which is effective
  before any code runs, and which also carries `longPathAware`:

  ```xml
  <windowsSettings xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">
    <dpiAwareness>PerMonitorV2</dpiAwareness>
    <longPathAware>true</longPathAware>
  </windowsSettings>
  ```

- `main()` additionally calls
  `SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)` as
  its first statement, for hosts started from a build that lost the manifest. It
  returns `ERROR_ACCESS_DENIED` when the manifest already applied it — that error
  is expected and ignored.

Without it, `IDXGIOutput::GetDesc` reports virtualized logical dimensions, the
advertised `config` width/height do not match the panel, and every absolute
pointer coordinate lands short by the scale factor. `dxgi_dd.probe()` verifies
the context and reports `available: false` rather than capturing at the wrong
size.
