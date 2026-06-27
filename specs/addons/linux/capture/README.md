# Linux Capture Add-Ons

## Default binary capture: NONE

Mirroring the encoder architecture: the default Linux binary ships with **zero
capture backends**. Every capture method is an opt-in build-tagged add-on. Users
compose the binary they need by choosing capture add-on(s) + encoder add-on(s).

This keeps the default binary tiny, makes licensing and dependency surface
explicit per deployment, and lets a single source tree produce binaries for
wildly different environments (rootful KMS+EGL on bare metal, NvFBC on an
NVIDIA GPU server, future no-root XShm for a kiosk) without `#ifdef` spaghetti.

---

## Available capture add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **KMS+EGL DMA-BUF** | `kms_egl` | [`./KMS_EGL_LINUX_SPEC.md`](./KMS_EGL_LINUX_SPEC.md) | Universal default — every GPU, any display server, requires `CAP_SYS_ADMIN` | ✅ Working |
| **NvFBC** | `nvfbc` | [`./NVFBC_LINUX_SPEC.md`](./NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver — ~2–3ms lower latency than KMS+EGL on NVIDIA, official path | 📋 Specced |

### Recommended add-on combinations

| Deployment | Capture add-on | Encoder add-on(s) | Build command |
|------------|---------------|-------------------|---------------|
| Bare-metal Linux server, any GPU | `kms_egl` | `libva` (HW) + `openh264` (SW fallback) | `go build -tags "kms_egl,libva,openh264"` |
| NVIDIA proprietary GPU host | `kms_egl,nvfbc` | `nvenc` + `openh264` | `go build -tags "kms_egl,nvfbc,nvenc,openh264"` |
| AMD ROCm workstation | `kms_egl` | `libva,amf,openh264` | `go build -tags "kms_egl,libva,amf,openh264"` |
| Intel Arc on Linux | `kms_egl` | `libva,openh264` | `go build -tags "kms_egl,libva,openh264"` (libva handles Arc via iHD driver) |

KMS+EGL is the only add-on in the default recommended set because it's the only
universally compatible capture backend that works without proprietary SDKs.

---

## What does NOT exist as a capture add-on (and why)

| Hardware / scenario | Why no add-on |
|---------------------|--------------|
| Intel HD / Iris / UHD / Arc | No proprietary capture API exists. KMS+EGL handles all of it. |
| AMD GCN / RDNA | No proprietary capture API exists. KMS+EGL handles all of it. |
| NVIDIA open driver (Nouveau / NVK) | KMS+EGL works well here; NvFBC requires the proprietary driver. |
| Wayland-specific (wlr-screencopy, PipeWire portal) | KMS+EGL works on Wayland without needing compositor cooperation. Rejected — marginal benefit only for the no-root case we're not targeting. |
| X11-specific (XShm, XComposite/XDamage) | KMS+EGL works on X11 too. Rejected — same reason. |

> **No-root fallbacks remain out of scope.** If a deployment needs to run without
> root, that requirement will be addressed when it comes up — likely as a future
> XShm (X11) or PipeWire portal (Wayland) add-on.

---

## Runtime capture probe order

When multiple capture add-ons are compiled into the same binary, the pipeline
selects in this priority order:

```
1. nvfbc compiled in AND NVIDIA proprietary driver present?  → use NvFBC
2. kms_egl compiled in AND root / CAP_SYS_ADMIN?             → use KMS+EGL
3. None of the above?                                         → fatal: no usable capture
```

The first available capture wins. Compile with only what you need.

---

## When ready to add a new capture backend

1. Write the spec at `specs/addons/linux/capture/{NAME}_LINUX_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the capture index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/capture/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
