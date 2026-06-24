# Windows Capture Add-Ons

## Default binary capture: NONE

Mirroring the Linux and macOS architecture: the default Windows binary ships
with **zero capture backends**. Every capture method is an opt-in build-tagged
add-on. Users compose the binary they need by choosing capture add-on(s) +
encoder add-on(s).

---

## Available capture add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **DXGI Desktop Duplication** | `dxgi_dd` | [`./DXGI_DD_WINDOWS_SPEC.md`](./DXGI_DD_WINDOWS_SPEC.md) | Universal default — every GPU, ~2–4ms, no elevation needed | 📋 Specced |
| **NvFBC for Windows** | `nvfbc_win` | [`./NVFBC_WINDOWS_SPEC.md`](./NVFBC_WINDOWS_SPEC.md) | NVIDIA GPU — ~50% lower latency than DXGI DD, pairs with NVENC for full zero-copy pipeline | 📋 Specced |
| **AMD AMF Display Capture** | `amf_capture` | [`./AMF_CAPTURE_WINDOWS_SPEC.md`](./AMF_CAPTURE_WINDOWS_SPEC.md) | AMD GPU — same AMFContext as `amf` encoder, native zero-copy, Apache 2.0 | 📋 Specced |

### Recommended add-on combinations

| Deployment | Capture add-on(s) | Encoder add-on(s) | Build command |
|------------|------------------|-------------------|---------------|
| Any GPU, maximum compat | `dxgi_dd` | `mf_hw` + `openh264` | `go build -tags "dxgi_dd,mf_hw,openh264"` |
| NVIDIA GPU, peak performance | `dxgi_dd,nvfbc_win` | `nvenc,openh264` | `go build -tags "dxgi_dd,nvfbc_win,nvenc,openh264"` |
| AMD GPU, peak performance | `dxgi_dd,amf_capture` | `amf,openh264` | `go build -tags "dxgi_dd,amf_capture,amf,openh264"` |
| Intel Arc / iGPU | `dxgi_dd` | `qsv,openh264` | `go build -tags "dxgi_dd,qsv,openh264"` |
| Multi-vendor (NVIDIA + AMD) | `dxgi_dd,nvfbc_win,amf_capture` | `nvenc,amf,openh264` | `go build -tags "dxgi_dd,nvfbc_win,amf_capture,nvenc,amf,openh264"` |
| Everything (maximum flexibility) | `dxgi_dd,nvfbc_win,amf_capture` | `mf_hw,nvenc,amf,qsv,openh264` | Probe selects best at runtime |

DXGI DD is included in every recommended set because it's the universal
fallback that works on every GPU vendor.

---

## What does NOT exist as a capture add-on (and why)

| API / scenario | Why no add-on |
|----------------|--------------|
| Windows.Graphics.Capture (WGC) | Per-window capture was the only advantage over DXGI DD; per-window is out of scope. Full-desktop WGC is slower than DXGI DD. |
| GDI BitBlt | ~30–50ms latency, misses hardware-accelerated content (DirectX, Chrome, games). Skip. |
| Magnification API | ~15–30ms, CPU-only. Niche use (UAC dialogs). Skip. |
| DirectShow / MF screen capture | Wrappers around DXGI DD anyway. Skip. |
| Mirror Drivers | Deprecated since Windows 8. Skip. |
| Intel-specific capture | Intel has no proprietary capture API on Windows. DXGI DD + QSV is the entire path. |

---

## Runtime capture probe order

When multiple capture add-ons are compiled into the same binary, the pipeline
selects in this priority order:

```
1. nvfbc_win compiled in AND NVIDIA GPU present AND probe succeeds?  → use NvFBC
2. amf_capture compiled in AND AMD GPU present?                       → use AMF Display Capture
3. dxgi_dd compiled in?                                                → use DXGI DD (universal)
4. None of the above?                                                  → fatal: no usable capture
```

The first available capture wins. Vendor-specific add-ons are preferred
because they integrate more tightly with their matching encoder add-on
(NvFBC → NVENC, AMF capture → AMF encode). DXGI DD is the universal fallback
that works with every encoder.

### Override via config

The `[capture]` section in the TOML config allows forcing a specific add-on:

```toml
[capture]
mode = "forced"
force_addon = "dxgi_dd"    # bypass vendor-specific probes
```

---

## Zero-copy surface path summary

| Capture add-on | Output type | Compatible HW encoders (zero-copy) |
|---------------|-------------|-----------------------------------|
| DXGI DD | `ID3D11Texture2D` | MF HW, NVENC, AMF, QSV (all accept D3D11 textures) |
| NvFBC | `ID3D11Texture2D` or `CUdeviceptr` | NVENC (primary), MF HW, AMF, QSV via D3D11 |
| AMF Capture | `AMFSurface` (wraps D3D11 texture) | AMF (native, same context), MF HW, NVENC, QSV via D3D11 |

All three capture add-ons produce D3D11-backed surfaces, so they're all
compatible with every Windows HW encoder. The vendor-specific add-ons just
do it with less overhead when paired with their matching encoder.

---

## Permission requirements comparison

| Add-on | Elevation | Notes |
|--------|-----------|-------|
| DXGI DD | None | Needs active desktop session (no headless without virtual display) |
| NvFBC | None | GeForce requires patched driver (Quadro/Tesla work officially) |
| AMF Capture | None | Needs AMD GPU with Adrenalin 21.5+ |

No Windows capture add-on requires elevation. Compare to Linux where
KMS+EGL requires `CAP_SYS_ADMIN`.

---

## When ready to add a new capture backend

1. Write the spec at `ADD-ON-SPECS/Windows/capture/{NAME}_WINDOWS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the capture index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/capture/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
