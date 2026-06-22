# Linux Capture Add-Ons

## When the default binary is sufficient

The default FeatherDesk Linux binary ships three capture backends covering most
deployments — see [`../LINUX_SPEC.md`](../LINUX_SPEC.md):

| Backend | When used | Status |
|---------|----------|--------|
| **KMS+EGL + DMA-BUF** | Root or `CAP_SYS_ADMIN` available; primary path | ✅ Default |
| **PipeWire ScreenCast** | Wayland (GNOME / KDE), no root | ✅ Default |
| **X11grab via ffmpeg** | X11 fallback, no root | ✅ Default |

For Intel GPUs, AMD GPUs, NVIDIA open-source driver (Nouveau / NVK), and GNOME/KDE
Wayland desktops, the default binary is optimal. **No add-on needed.**

---

## When an add-on capture backend matters

| Hardware / environment | Best capture | Why add-on |
|-----------------------|--------------|-----------|
| **NVIDIA proprietary driver** | NvFBC | Direct GPU framebuffer access, ~2–3ms lower latency than KMS+EGL on the proprietary stack. Skips the compositor entirely. |
| **wlroots Wayland compositor** (Sway, Hyprland, river, dwl, labwc) | wlr-screencopy | Direct DMA-BUF export from the compositor without the PipeWire portal indirection. |

## Available capture add-ons

| Add-on | Spec | Hardware / compositor | Status |
|--------|------|---------------------|--------|
| **NvFBC** | [`./NVFBC_LINUX_SPEC.md`](./NVFBC_LINUX_SPEC.md) | NVIDIA proprietary driver | 📋 Specced |
| **wlr-screencopy** | [`./WLROOTS_SCREENCOPY_SPEC.md`](./WLROOTS_SCREENCOPY_SPEC.md) | wlroots-based Wayland compositors | 📋 Specced |

---

## What does NOT exist as a separate add-on (and why)

| Hardware | Why no add-on |
|----------|--------------|
| Intel HD / Iris / UHD / Arc | Intel does not have a proprietary capture API. KMS+EGL via i915 + Mesa is the entire path. |
| AMD GCN / RDNA | AMD does not have a proprietary capture API. KMS+EGL via amdgpu + Mesa is the entire path. |
| GNOME / KDE Wayland | PipeWire ScreenCast portal is the supported path on these desktops. wlr-screencopy is not implemented in Mutter or KWin. |

KMS+EGL is genuinely the best capture on Intel and AMD. The only meaningful capture
alternative on Linux is NVIDIA's NvFBC, which is why we ship it as an add-on rather
than baking it into the default binary (it requires the proprietary driver and a
patcher on consumer cards).

---

## Runtime capture probe order

The pipeline checks at startup, in this order:

```
1. NvFBC add-on compiled in AND NVIDIA proprietary driver present?  → use NvFBC
2. wlr-screencopy add-on compiled in AND wlroots compositor running? → use wlr-screencopy
3. Root / CAP_SYS_ADMIN AND DRM card found?                          → use KMS+EGL
4. Wayland (any compositor) AND PipeWire portal available?           → use PipeWire portal
5. X11 DISPLAY set AND ffmpeg in PATH?                               → use X11grab
6. None of the above?                                                → fatal error
```

The first available capture wins. User controls priority by choosing which add-ons
to install — the binary itself does not need command-line flags.

---

## When ready to add a new capture backend

Follow the same pattern as encoders:

1. Write the spec at `ADD-ON-SPECS/Linux/capture/{NAME}_SPEC.md`
2. Add a row to the table above
3. Add a row to the capture index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/capture/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
