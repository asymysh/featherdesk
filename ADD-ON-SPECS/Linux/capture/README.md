# Linux Capture Add-Ons

## Default binary capture: KMS+EGL only

The default FeatherDesk Linux binary uses a single capture backend:

**KMS+EGL with DMA-BUF zero-copy** — see [`../LINUX_SPEC.md`](../LINUX_SPEC.md).

| Aspect | Detail |
|--------|--------|
| Requires | Root or `CAP_SYS_ADMIN` (`setcap cap_sys_admin+p ./viewport-rds`) |
| Display server | Works on X11, Wayland (GNOME/KDE/wlroots), or no display server |
| Path | DRM card → DMA-BUF fd → encoder (zero CPU pixel copies) |
| Latency | ~0.5ms — gold standard, nothing beats it |

KMS+EGL operates at the kernel/DRM level **below the display server**, so it works
identically on X11 and Wayland regardless of compositor. The only constraint is
`CAP_SYS_ADMIN`.

> **No-root fallbacks are out of scope for now.** If a deployment needs to run without
> root, that requirement will be addressed when it comes up — likely by adding XShm
> for X11 and/or PipeWire portal for Wayland as a future enhancement. Until then, the
> default binary requires `CAP_SYS_ADMIN` and uses KMS+EGL exclusively.

---

## Available capture add-ons

| Add-on | Spec | When to use | Status |
|--------|------|------------|--------|
| **NvFBC** | [`./NVFBC_LINUX_SPEC.md`](./NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver — ~2–3ms lower latency than KMS+EGL on NVIDIA, official path | 📋 Specced |

NvFBC is the only capture method that beats KMS+EGL on any hardware. It does so only
on NVIDIA's proprietary driver stack, where KMS+EGL has historically been finicky
(multi-monitor edge cases, `nvidia-drm.modeset=1` requirement). NvFBC bypasses all
of that with direct GPU framebuffer access.

---

## What does NOT exist as a capture add-on (and why)

| Hardware / scenario | Why no add-on |
|---------------------|--------------|
| Intel HD / Iris / UHD / Arc | No proprietary capture API exists. KMS+EGL is the entire path. |
| AMD GCN / RDNA | No proprietary capture API exists. KMS+EGL is the entire path. |
| NVIDIA open driver (Nouveau / NVK) | KMS+EGL works well here; NvFBC requires the proprietary driver. |
| Wayland-specific paths (wlr-screencopy, PipeWire portal) | KMS+EGL works on Wayland without needing the compositor's cooperation. Considered and rejected — marginal benefit for the no-root case which we're not targeting. |
| X11-specific paths (XShm, XComposite/XDamage) | KMS+EGL works on X11 too. Considered and rejected — same reason. |

---

## Runtime capture probe order

```
1. NvFBC add-on compiled in AND NVIDIA proprietary driver present? → use NvFBC
2. KMS+EGL with root / CAP_SYS_ADMIN?                              → use KMS+EGL (default)
3. None of the above?                                              → fatal error: insufficient permissions
```

The first available capture wins. Users on NVIDIA proprietary opt into the NvFBC
binary if they want maximum performance; everyone else uses the default binary's
KMS+EGL path.

---

## When ready to add a new capture backend

Follow the same pattern as encoders:

1. Write the spec at `ADD-ON-SPECS/Linux/capture/{NAME}_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the capture index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/capture/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
