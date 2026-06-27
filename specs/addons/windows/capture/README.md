# Windows Capture Add-Ons

## Single capture path: DXGI Desktop Duplication

Windows has **one** capture mechanism: DXGI Desktop Duplication. Vendor-specific
capture APIs (NvFBC, AMF Display Capture) were considered and rejected after
benchmarking proved DXGI DD's raw overhead is sub-microsecond on every GPU.

The default Windows binary ships with no capture backend. Compile in the
`dxgi_dd` add-on to enable capture.

---

## Available capture add-on

| Add-on | Build tag | Spec | Status |
|--------|-----------|------|--------|
| **DXGI Desktop Duplication** | `dxgi_dd` | [`./DXGI_DD_WINDOWS_SPEC.md`](./DXGI_DD_WINDOWS_SPEC.md) | ✅ Benchmarked |

### Recommended combinations

| Deployment | Capture add-on | Encoder add-on(s) | Build command |
|------------|---------------|-------------------|---------------|
| Any GPU, maximum compat | `dxgi_dd` | `mf_hw` + `openh264` | `go build -tags "dxgi_dd,mf_hw,openh264"` |
| NVIDIA GPU, peak performance | `dxgi_dd` | `nvenc,openh264` | `go build -tags "dxgi_dd,nvenc,openh264"` |
| AMD GPU, peak performance | `dxgi_dd` | `amf,openh264` | `go build -tags "dxgi_dd,amf,openh264"` |
| Intel Arc / iGPU | `dxgi_dd` | `qsv,openh264` | `go build -tags "dxgi_dd,qsv,openh264"` |
| Home / personal (fastest SW) | `dxgi_dd` | `nvenc,amf,x264` | `go build -tags "dxgi_dd,nvenc,amf,x264"` |
| Maximum flexibility | `dxgi_dd` | `mf_hw,nvenc,amf,qsv,openh264,x264` | Probe selects best at runtime |

---

## Headless support (integrated IddCx virtual display)

The `dxgi_dd` add-on bundles a pre-signed IddCx virtual display driver. On
first launch on a machine with no physical display:

1. The server detects no DXGI outputs
2. Auto-installs the IddCx driver via `pnputil` (one-time UAC prompt)
3. Creates a virtual display via device IOCTL
4. DXGI DD captures the virtual display normally

Same approach as Sunshine/Moonlight/Parsec — production-proven for headless
Windows streaming. See [`DXGI_DD_WINDOWS_SPEC.md`](./DXGI_DD_WINDOWS_SPEC.md#headless-support-integrated-iddcx-virtual-display)
for the full flow.

---

## What was rejected

| Capture method | Why rejected |
|----------------|-------------|
| NvFBC for Windows | DXGI DD raw overhead is sub-microsecond — no measurable gain. Adds NVIDIA driver patcher concerns on GeForce. |
| AMF Display Capture | Same reason — DXGI DD is already as fast as physically possible. |
| Windows.Graphics.Capture (WGC) | Per-window capture not needed; full-desktop WGC is slower than DXGI DD. |
| GDI BitBlt | ~30–50ms, fails on hardware-accelerated content. |
| Magnification API | ~15–30ms, CPU-only, niche. |
| DirectShow / MF screen capture | Wrappers around DXGI DD. No benefit. |

---

## Runtime probe order

```
1. dxgi_dd compiled in AND active display found?     → use DXGI DD
2. dxgi_dd compiled in AND no display + admin?       → install IddCx VDD → use DXGI DD
3. dxgi_dd compiled in AND no display + no admin?    → request elevation, then continue
4. None of the above?                                 → fatal: no capture add-on installed
```

---

## Permission requirements

| Add-on | Elevation | Notes |
|--------|-----------|-------|
| DXGI DD (with display) | None | Standard user session |
| DXGI DD (headless, first run) | One-time UAC | For IddCx driver install via pnputil |
| DXGI DD (headless, after install) | None | Driver persists across reboots |
