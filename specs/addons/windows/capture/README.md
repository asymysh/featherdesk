# Windows Capture Add-Ons

## Single capture path: DXGI Desktop Duplication

Windows has **one** capture mechanism: DXGI Desktop Duplication. It is the only
vendor-neutral API that yields an `ID3D11Texture2D` directly consumable zero-copy
by every Windows HW encoder, so one add-on serves every GPU. Vendor-specific
capture APIs (NvFBC, AMF Display Capture) are deferred on that maintenance
argument, not on evidence: no DXGI acquisition has been measured yet.

The default Windows binary ships with no capture backend. Drop in the
`dxgi_dd` add-on shared library to enable capture.

---

## Available capture add-on

| Add-on | Add-on ID | Spec | Status |
|--------|-----------|------|--------|
| **DXGI Desktop Duplication** | `dxgi_dd` | [`./DXGI_DD_WINDOWS_SPEC.md`](./DXGI_DD_WINDOWS_SPEC.md) | 📋 Specced — DXGI acquisition not yet measured (bench backend is a stub) |

### Recommended combinations

| Deployment | Capture add-on | Encoder add-on(s) | Add-on libraries to drop in |
|------------|---------------|-------------------|------------------------------|
| Any GPU, maximum compat | `dxgi_dd` | `mf_hw` + `openh264` | `featherdesk-addon-{dxgi_dd,mf_hw,openh264}.dll` |
| NVIDIA GPU, peak performance | `dxgi_dd` | `nvenc,openh264` | `featherdesk-addon-{dxgi_dd,nvenc,openh264}.dll` |
| AMD GPU, peak performance | `dxgi_dd` | `amf,openh264` | `featherdesk-addon-{dxgi_dd,amf,openh264}.dll` |
| Intel Arc / iGPU | `dxgi_dd` | `qsv,openh264` | `featherdesk-addon-{dxgi_dd,qsv,openh264}.dll` |
| Measured CPU-bound host | `dxgi_dd` | `nvenc,amf,x264` | `featherdesk-addon-{dxgi_dd,nvenc,amf,x264}.dll` — `x264` is opt-in: `[encode] force_addon = "x264"` |
| Maximum flexibility | `dxgi_dd` | `mf_hw,nvenc,amf,qsv,openh264,x264` | drop in all; probe selects best at runtime (`x264` only when forced) |

Build each add-on separately as a cdylib and drop the resulting `.dll`
into the add-ons directory, e.g.:
```bash
cargo build --release -p featherdesk-addon-dxgi_dd   # cdylib  featherdesk-addon-dxgi_dd.dll
```

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
| NvFBC for Windows | Deferred, not rejected on evidence — one vendor-neutral add-on covers every GPU, and no measured DXGI acquisition cost exists to compare against. Adds NVIDIA driver-patcher concerns on GeForce. |
| AMF Display Capture | Deferred for the same maintenance reason — a second AMD-only capture path for an unquantified gain. |
| Windows.Graphics.Capture (WGC) | Per-window capture not needed; full-desktop WGC is slower than DXGI DD. |
| GDI BitBlt | ~30–50ms, fails on hardware-accelerated content. |
| Magnification API | ~15–30ms, CPU-only, niche. |
| DirectShow / MF screen capture | Wrappers around DXGI DD. No benefit. |

---

## Runtime probe order

```
1. dxgi_dd loaded AND active display found?          → use DXGI DD
2. dxgi_dd loaded AND no display + admin?            → install IddCx VDD → use DXGI DD
3. dxgi_dd loaded AND no display + no admin?         → request elevation, then continue
4. None of the above?                                 → fatal: no capture add-on installed
```

---

## Permission requirements

| Add-on | Elevation | Notes |
|--------|-----------|-------|
| DXGI DD (with display) | None | Standard user session |
| DXGI DD (headless, first run) | One-time UAC | For IddCx driver install via pnputil |
| DXGI DD (headless, after install) | None | Driver persists across reboots |
