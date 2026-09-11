# Linux Capture Add-Ons

## Default binary capture: NONE

Mirroring the encoder architecture: the default Linux binary ships with **zero
capture backends**. Every capture method is an opt-in add-on shared library. Users
compose the deployment they need by dropping in capture add-on(s) + encoder add-on(s).

This keeps the default binary tiny, makes licensing and dependency surface
explicit per deployment, and lets a single host binary serve wildly different
environments (rootful KMS+EGL on bare metal, NvFBC on an NVIDIA GPU server,
no-root `wl_screencopy` on a headless Wayland host) without `#ifdef` spaghetti.

---

## Available capture add-ons

| Add-on | Add-on ID | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **KMS+EGL DMA-BUF** | `kms_egl` | [`./KMS_EGL_LINUX_SPEC.md`](./KMS_EGL_LINUX_SPEC.md) | Universal default — every GPU, any display server, requires `CAP_SYS_ADMIN` | 📋 Specced — Go prototype abandoned on tiled 10-bit scanout formats (see spec "Status") |
| **NvFBC** | `nvfbc` | [`./NVFBC_LINUX_SPEC.md`](./NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver, X11 only — ~2–3ms lower latency than KMS+EGL on NVIDIA, official path | 📋 Specced |
| **wlr-screencopy** | `wl_screencopy` | [`./WL_SCREENCOPY_LINUX_SPEC.md`](./WL_SCREENCOPY_LINUX_SPEC.md) | **No root.** Headless / nested Wayland, or any host that cannot grant `CAP_SYS_ADMIN`. wlroots-family compositors only; cannot deliver a separate cursor | 📋 Specced, not measured |
| **Portal ScreenCast** | `pw_portal` | [`./PW_PORTAL_LINUX_SPEC.md`](./PW_PORTAL_LINUX_SPEC.md) | **No root.** The only path on GNOME/KDE Wayland. Costs an interactive consent prompt on first launch (restore token afterwards) | 📋 Specced, not measured |

### Recommended add-on combinations

| Deployment | Capture add-on | Encoder add-on(s) |
|------------|---------------|-------------------|
| Bare-metal Linux server, any GPU | `kms_egl` | `libva` (HW) + `openh264` (SW fallback) |
| Headless Wayland, no forceable connector | `wl_screencopy` (wlroots) or `pw_portal` (Mutter/KWin) | `libva` + `openh264` |
| Host where `CAP_SYS_ADMIN` is not grantable | `wl_screencopy`, else `pw_portal` | `libva` + `openh264` |
| NVIDIA proprietary GPU host | `kms_egl` + `nvfbc` | `nvenc` + `openh264` |
| AMD ROCm workstation | `kms_egl` | `libva` + `amf_rocm` + `openh264` |
| Intel Arc on Linux | `kms_egl` | `libva` + `openh264` (libva handles Arc via iHD driver) |

Build each add-on once as its own cdylib and drop the resulting
`featherdesk-addon-<id>.so` files into the add-ons directory — e.g.
`cargo build --release -p featherdesk-addon-kms_egl` (cdylib → `featherdesk-addon-kms_egl.so`).
The host loads whatever it finds there; the same host binary serves every row.

KMS+EGL is the only add-on in the default recommended set because it's the only
universally compatible capture backend that works without proprietary SDKs.

---

## What does NOT exist as a capture add-on (and why)

| Hardware / scenario | Why no add-on |
|---------------------|--------------|
| Intel HD / Iris / UHD / Arc | No proprietary capture API exists. KMS+EGL handles all of it. |
| AMD GCN / RDNA | No proprietary capture API exists. KMS+EGL handles all of it. |
| NVIDIA open driver (Nouveau / NVK) | KMS+EGL works well here; NvFBC requires the proprietary driver. |
| X11-specific (XShm, XComposite/XDamage) | KMS+EGL works on X11 too, and no X11-only deployment needs a no-root path that the two Wayland add-ons do not already cover better. Still rejected. |
| `vkms` (kernel virtual KMS) | Produces DRM planes with no hardware, which looks like a headless answer for `kms_egl` — but it exports no accelerated, DMA-BUF-importable framebuffer worth encoding. Evaluated and rejected; use `wl_screencopy` instead. |
| Xvfb + `kms_egl` | **Does not work and never did.** Xvfb renders into main memory and never touches DRM, so there is no scanout buffer to import. Earlier revisions of `PLATFORM_COMPAT.md` claimed this configuration worked; that claim was wrong and is corrected. |

> **No-root capture is now IN scope** (`wl_screencopy`, `pw_portal`), because
> `kms_egl` structurally cannot serve two real deployments: a host where
> `CAP_SYS_ADMIN` is not grantable, and headless Wayland with no forceable
> connector. This does **not** reopen containerized deployment — FeatherDesk is
> not a containerized application (`PROJECT_ARTIFACTS/GAP_TRIAGE.md`, closed
> finding C1). *No-root capture* and *run in Docker* are separate claims; only
> the first changed.

---

## Runtime capture probe order

When multiple capture add-ons are loaded, the pipeline
selects in this priority order:

```
1. nvfbc loaded AND NVIDIA proprietary driver present AND X11?      → use NvFBC
2. kms_egl loaded AND root / CAP_SYS_ADMIN AND a CRTC with a mode?  → use KMS+EGL
3. wl_screencopy loaded AND a wlroots-family compositor?            → use wlr-screencopy
4. pw_portal loaded AND a portal ScreenCast session is granted?     → use Portal
5. None of the above?                                                → fatal: no usable capture
```

The first available capture wins. Drop in only what you need. A backend whose
prerequisite is missing reports `ProbeReport { available: false, reason }` — that
is not an error, and the next candidate is tried (`MODULE_ABI` "Root module
surface"). Under Wayland `nvfbc` is always unavailable; `kms_egl` is the whole
Linux path wherever it is available, reading the pointer from the DRM cursor
plane on X11 and Wayland alike.

Step 2's **CRTC condition** is part of `kms_egl.probe()`: a host that has the
capability but no active scanout (headless with no forced connector, or an
in-memory X server) reports `available: false` with a reason naming it, so the
order falls through to a no-root add-on rather than selecting a capturer that
would return empty frames.

Steps 3-4 are ordered by cost, not by compatibility: `wl_screencopy` needs no
consent prompt, so it is tried first even though `pw_portal` works on more
compositors. On GNOME and KDE Wayland step 3 is always unavailable and step 4 is
the only no-root path.

---

## When ready to add a new capture backend

1. Write the spec at `specs/addons/linux/capture/{NAME}_LINUX_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the capture index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Follow CENTRAL_SPEC "Where to register a new add-on" steps 4–7 for the code
   side (root module, `AddonCaps`, cdylib path, probe-order wiring).
