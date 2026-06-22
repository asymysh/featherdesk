# Windows Capture Add-Ons

## ⏸️ Pending Architecture Discussion

The Windows capture lineup is **not yet finalized**. Like with Windows encoders,
the capture story has more candidates than Linux or macOS and needs deliberate
choices before specs are written.

## Capture options on Windows

| API | Vendor | Status today |
|-----|--------|-------------|
| DXGI Desktop Duplication (DDup) | Microsoft / cross-vendor | ✅ Default plan — primary capture path |
| Windows.Graphics.Capture (WGC) | Microsoft / cross-vendor | ✅ Default plan — fallback for DDup edge cases |
| GDI BitBlt | Microsoft | ⚠️ Last-resort software path (already benchmarked) |
| **NvFBC for Windows** | NVIDIA | 📋 Potential add-on — direct GPU framebuffer, lower latency than DDup |
| Magnification API | Microsoft | Niche, screen-reader oriented, not for streaming |
| Mirror Drivers (legacy) | various | Deprecated since Windows 8 |

Unlike Linux where capture choices are determined by GPU + display server combination,
Windows capture choices are determined by **GPU vendor** (only NVIDIA has a proprietary
alternative) and **special edge cases** (DRM-protected content, RDP sessions,
virtual displays).

## Open questions to resolve before specs are written

1. **Is NvFBC for Windows worth shipping as an add-on?**
   - DXGI Desktop Duplication is already very low latency (~1–5ms on real GPUs)
   - NvFBC for Windows exists (same SDK as Linux NvFBC) — slightly lower latency,
     skips DWM compositor
   - Sunshine ships both, NvFBC is opt-in
   - Decision: do we mirror Sunshine's approach?

2. **How to handle DRM-protected content?**
   - DDup detects DRM content and returns black frames via
     `frame_info.ProtectedContentMaskedOut`
   - No reliable workaround at application level
   - Likely answer: detect and notify user, no add-on needed

3. **RDP / virtual display fallback?**
   - DDup returns `DXGI_ERROR_UNSUPPORTED` in RDP sessions and on virtual display
     adapters (Parsec, VMware, VirtualBox)
   - WGC works in these cases — already in default plan
   - GDI BitBlt also works as last resort
   - No additional add-on likely needed

4. **WGC variants** — WGC has multiple modes (full desktop, single window,
   per-monitor). Are these separate capture backends or a single backend with
   configuration?

## Until that discussion happens

This folder is intentionally **empty** to maintain the symmetric directory structure:

```
ADD-ON-SPECS/
├── Linux/capture/        ← 2 add-on specs (NvFBC, wlr-screencopy)
├── macOS/capture/        ← README only (SCK is the only option)
└── Windows/capture/      ← THIS README (pending discussion)
```

## Where Windows capture is currently specced

**📄 [`../WINDOWS_SPEC.md`](../WINDOWS_SPEC.md)** — covers the default-binary capture
plan:
- DXGI Desktop Duplication as primary
- WGC as fallback
- GDI BitBlt as last-resort software path
- Benchmark results from this development session (Parsec virtual display)
- `SetMaximumFrameLatency(1)` and other DDup latency optimizations
- Zero-copy D3D11 texture path to hardware encoders

The default-binary capture section is stable. **Only the add-on capture section is
pending discussion**, alongside the encoder pluggability discussion.

## When ready to spec

Likely add-on candidates (in order of probable priority):
- `NVFBC_WINDOWS_SPEC.md` — NVIDIA NvFBC for Windows (Sunshine pattern)
- *(probably none else)* — Microsoft does not have other "better than DDup" APIs

Follow the same pattern as Linux:
1. Write the spec at `ADD-ON-SPECS/Windows/capture/{NAME}_WINDOWS_SPEC.md`
2. Add a row to the Windows capture table in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
3. Implement under `internal/capture/{name}/` with a Go build tag
4. Wire the runtime probe order in `MODULE_PIPELINE.md`
