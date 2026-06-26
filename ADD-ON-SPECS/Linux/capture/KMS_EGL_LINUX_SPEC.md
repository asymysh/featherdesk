# Linux Capture Add-On: KMS+EGL DMA-BUF

## Purpose

Direct kernel-level screen capture via DRM/KMS (Direct Rendering Manager / Kernel
Mode Setting) combined with EGL for GPU-accessible framebuffer access. The capture
operates **below the display server** (X11 or Wayland — either works identically)
by reading the framebuffer that the kernel sends to display hardware.

The gold-standard Linux capture path. Lowest latency. Works on every GPU vendor.
Zero-copy via DMA-BUF when paired with a hardware encoder (libva, NVENC, Vulkan
Video, AMF).

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| `libdrm` | MIT | DRM userspace API |
| `libgbm` | MIT | Generic Buffer Management |
| `libEGL` / `libGL` | proprietary headers, vendor implementations | Ships with GPU driver |
| Linux DRM kernel subsystem | GPL-2 (kernel) | Userspace calls via ioctl — no GPL contamination of userspace binary |
| Our CGo binding | MIT | We own this code |

The DRM kernel module is GPL-2 but exposes a stable ioctl ABI to userspace.
Calling that ABI from userspace doesn't pull GPL into our binary, in the same
way calling `read(2)` doesn't. This is the standard Linux userspace pattern.

---

## Hardware & GPU Compatibility

Works on every GPU that has a DRM driver — essentially everything in 2026:

| GPU | DRM driver | KMS+EGL support |
|-----|------------|-----------------|
| Intel HD Graphics 2000+ (Sandy Bridge 2011+) | `i915` | ✅ |
| Intel Arc | `i915` / `xe` | ✅ |
| AMD GCN 1.0+ (HD 7000+, 2012+) | `amdgpu` | ✅ |
| AMD RDNA1/2/3/4 | `amdgpu` | ✅ |
| NVIDIA proprietary driver | `nvidia` (with `nvidia-drm.modeset=1`) | ⚠️ Works but quirky on multi-monitor — consider NvFBC add-on instead |
| NVIDIA open driver (Nouveau, NVK) | `nouveau` / `nvk` | ✅ |

For NVIDIA proprietary specifically, the NvFBC add-on
([`NVFBC_LINUX_SPEC.md`](./NVFBC_LINUX_SPEC.md)) is meaningfully better
(~2–3ms lower latency, no `nvidia-drm.modeset=1` quirks). KMS+EGL on NVIDIA
proprietary remains a valid fallback.

---

## Permission Requirements

**Requires `CAP_SYS_ADMIN` or root.** This is the only constraint.

Grant the capability once at install time:
```bash
sudo setcap cap_sys_admin+p ./viewport-rds
```

After that, the binary runs as a regular user.

---

## Build & Distribution

### Go build tag

```bash
go build -tags kms_egl -o viewport-rds-linux-kms ./cmd/server
```

### Runtime dependencies

- `libdrm.so` (system package: `libdrm-dev`)
- `libgbm.so` (system package: `libgbm-dev`)
- `libEGL.so` + `libGL.so` (ships with GPU driver, also in `libegl-dev` / `libgl-dev`)
- pkg-config (build time only)

All universally available on every Linux distro.

### CGo configuration

```go
/*
#cgo pkg-config: libdrm gbm egl gl
#cgo LDFLAGS: -ldl

#include <xf86drm.h>
#include <xf86drmMode.h>
#include <gbm.h>
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <GLES2/gl2ext.h>
#include <unistd.h>
#include <fcntl.h>
#include <drm/drm_fourcc.h>
*/
import "C"
```

---

## CGo Implementation Sketch

```c
// 1. Find a DRM card with active output
int drm_fd = open("/dev/dri/card0", O_RDWR | O_CLOEXEC);
drmSetClientCap(drm_fd, DRM_CLIENT_CAP_UNIVERSAL_PLANES, 1);

drmModePlaneRes *planes = drmModeGetPlaneResources(drm_fd);
// Find primary plane with active fb_id, get CRTC dimensions
// ... iterate planes, pick primary

// 2. Create GBM device + EGL context (surfaceless)
struct gbm_device *gbm = gbm_create_device(drm_fd);
EGLDisplay egl_dpy = eglGetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR, gbm, NULL);
eglInitialize(egl_dpy, NULL, NULL);

EGLConfig egl_cfg;
eglChooseConfig(egl_dpy, config_attribs, &egl_cfg, 1, &num_cfgs);
EGLContext egl_ctx = eglCreateContext(egl_dpy, egl_cfg, EGL_NO_CONTEXT, ctx_attribs);
eglMakeCurrent(egl_dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, egl_ctx);

// 3. Per-frame: get current framebuffer as DMA-BUF
drmModeFB2 *fb2 = drmModeGetFB2(drm_fd, plane->fb_id);
int dmabuf_fd;
drmPrimeHandleToFD(drm_fd, fb2->handles[0], DRM_CLOEXEC, &dmabuf_fd);

// 4. Path A: Zero-copy direct to HW encoder
// Pass dmabuf_fd to libva / NVENC / AMF — no GPU→CPU copy
return EncodedFrame{ DMAFD: dmabuf_fd, Width: w, Height: h, ... };

// 4. Path B: CPU readback for SW encoder
EGLImageKHR egl_image = eglCreateImageKHR(egl_dpy, EGL_NO_CONTEXT,
    EGL_LINUX_DMA_BUF_EXT, NULL, image_attribs);
GLuint tex;
glGenTextures(1, &tex);
glBindTexture(GL_TEXTURE_2D, tex);
glEGLImageTargetTexture2DOES(GL_TEXTURE_2D, egl_image);

// Blit to linear renderbuffer + glReadPixels
glReadPixels(0, 0, w, h, GL_RGBA, GL_UNSIGNED_BYTE, pixels);
// ~50ms at 2560×1440 — only use when no HW encoder is available
```

---

## Two Output Paths

The capturer implements both `Capturer` interfaces from `pkg/capture`:

| Interface | Method | Output | Use case |
|-----------|--------|--------|----------|
| `Capturer` (CPU readback) | `NextFrame()` | RGBA `[]byte` | Pair with SW encoder (OpenH264) |
| `DMABufCapturer` (zero-copy) | `NextDMABuf()` | `FBInfo{ DMAFD, W, H, Stride, Format, Modifier, Timestamp }` | Pair with HW encoder (libva, NVENC, Vulkan) |

The pipeline picks the right method based on what encoder add-on is paired.

---

## Performance Targets

| Path | 1080p p50 | 1440p p50 | Notes |
|------|----------|----------|-------|
| **DMA-BUF zero-copy** | **~0.5ms** | **~0.5ms** | Just acquires the fd — actual cost paid by encoder |
| CPU readback (glReadPixels) | ~25ms | ~50ms | Used only when paired with SW encoder |

Measured (Intel HD 630, original featherdesk integration tests):
- EGL context init: 170ms (one-time)
- DRM card discovery: 5ms (one-time)
- DMA-BUF fd export per frame: ~0.5ms
- glReadPixels 1440p BGRA: 50ms per frame

The DMA-BUF path is the entire point — pair this capture add-on with a HW
encoder add-on to skip the 50ms readback entirely.

---

## Cursor Handling

The cursor is on a separate DRM plane (cursor plane, ~64×64 RGBA with alpha).
The capturer reads it via a parallel EGL context and the pipeline composites
client-side via the `FrameTypeCursorUpdate` protocol frame — keeps the cursor
out of the main framebuffer for the zero-copy path.

See `MODULE_HARDWARE_ENCODE.md` "Cursor Handling in Hardware Path" for the
client-side cursor compositing approach.

---

## Probe & Selection

```go
//go:build kms_egl

func ProbeKMSEGL() (*KMSEGLCapabilities, error) {
    // 1. Check CAP_SYS_ADMIN / root via geteuid + check effective caps
    // 2. Enumerate /dev/dri/card* devices
    // 3. For each: open, set UNIVERSAL_PLANES, find primary plane with fb_id
    // 4. Get CRTC dimensions + refresh rate
    // 5. Return per-display dimensions or ErrNoUsableCard
}
```

Pipeline probes capture (Linux, this add-on compiled in):
```
NvFBC add-on AND NVIDIA proprietary?  → use NvFBC (lower latency on NVIDIA)
KMS+EGL with root?                    → use this add-on
None?                                 → fatal: no capture add-on configured
```

---

## File Structure

```
internal/capture/kms/
├── kms.go                      // KMSCapturer struct, NewKMSCapturer
├── drm.go                      // DRM card discovery, plane enumeration
├── egl.go                      // EGL context, DMA-BUF import, glReadPixels
├── kms_cgo.go                  // CGo binding (build tag: kms_egl)
├── kms_stub.go                 // No-op stub (build tag: !kms_egl)
├── cursor.go                   // Cursor plane capture
├── probe.go                    // ProbeKMSEGL()
├── drm_integration_test.go     // build tag: kms_egl,integration
├── egl_integration_test.go     // build tag: kms_egl,integration
└── kms_integration_test.go     // build tag: kms_egl,integration
```

Already exists in working form at `internal/capture/{drm,egl,kms,cursor}.go` —
refactor moves it under the `kms_egl` build tag without changing the underlying
code.

---

## Known Issues (to fix in refactor)

| ID | Issue | Severity |
|----|-------|---------|
| TD-01 | DMA-BUF fd leak on EGL import failure | High — fd exhaustion over time |
| TD-03 | Static C globals in EGL state prevent thread safety | High — silent corruption with concurrent access |
| TD-13 | `fps` parameter accepted but never used (no frame pacing) | Medium — orchestrator handles pacing externally |

See main `MODULE_CAPTURE.md` for the full refactor plan.

---

## When to use this add-on

Use this add-on when:
- Have root or `CAP_SYS_ADMIN` available
- Any GPU (universally compatible)
- Want zero-copy to a HW encoder (lowest end-to-end latency)
- Display server agnostic deployment (works on X11, Wayland GNOME/KDE/wlroots, or headless)

Skip when:
- NVIDIA proprietary driver — prefer NvFBC add-on (~2–3ms lower latency)
- No root available — no other capture add-on exists today; would need future
  XShm or PipeWire portal add-on

---

## Status

✅ **Working** — implemented today in `internal/capture/{drm,egl,kms,cursor}.go`,
verified on Intel HD 630 at 2560×1440 with measured performance numbers. The
refactor moves it to `internal/capture/kms/` under a `kms_egl` build tag without
changing the underlying capture logic.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_kms_egl]` section
of the TOML config (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is compiled into the binary; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements `stream.ConfigurableCapturer` (see [`../../../../specs/MODULE_STREAM_PARAMS.md`](../../../../specs/MODULE_STREAM_PARAMS.md)). KMS+EGL captures at native display resolution; the pipeline handles scaling.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Output is native display resolution -- pipeline scales via GL blit or libyuv. No capturer change needed. | n/a (pipeline) |
| `FPS` | Pipeline pacing (capture is event-driven via `drmModePageFlip` / `drmHandleEvent`). No capturer change needed. | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | Request `DRM_FORMAT_XRGB2101010` framebuffer via `drmModeAddFB2` (driver-dependent; falls back to XRGB8888 if unsupported) | requires re-init |
| `ColorSpace` | Reported per surface metadata from `drmModeGetProperty`; pipeline annotates encoder | n/a (read-only) |