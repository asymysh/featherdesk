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
| Our Rust FFI binding | MIT | We own this code |

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
sudo setcap cap_sys_admin+p ./featherdesk
```

After that, the binary runs as a regular user.

---

## Build & Distribution

### Shared library build

```bash
cargo build --release -p featherdesk-addon-kms_egl   # cdylib → featherdesk-addon-kms_egl.so
```

### Runtime dependencies

- `libdrm.so` (system package: `libdrm-dev`)
- `libgbm.so` (system package: `libgbm-dev`)
- `libEGL.so` + `libGL.so` (ships with GPU driver, also in `libegl-dev` / `libgl-dev`)
- pkg-config (build time only)

All universally available on every Linux distro.

### FFI configuration

The bindings are generated with `bindgen` in `build.rs`, which probes the same
libraries via `pkg-config` and wraps the C headers:

```rust
// build.rs — link libdrm/gbm/egl/gl (+ -ldl) and generate bindings
pkg_config::Config::new().probe("libdrm").unwrap();
pkg_config::Config::new().probe("gbm").unwrap();
pkg_config::Config::new().probe("egl").unwrap();
pkg_config::Config::new().probe("gl").unwrap();
println!("cargo:rustc-link-lib=dl");

bindgen::Builder::default()
    .header_contents("wrapper.h", "
        #include <xf86drm.h>
        #include <xf86drmMode.h>
        #include <gbm.h>
        #include <EGL/egl.h>
        #include <EGL/eglext.h>
        #include <GL/gl.h>
        #include <GL/glext.h>
        #include <unistd.h>
        #include <fcntl.h>
        #include <drm/drm_fourcc.h>")
    .generate().unwrap();
```

**Desktop OpenGL, not GLES2.** The readback path binds `EGL_OPENGL_API` and asks
for an `EGL_OPENGL_BIT` config. GLES2 is not an option here: a `GL_TEXTURE_2D`
backed by an EGLImage imported from a scanout DMA-BUF is not
framebuffer-attachable under GLES2 on Mesa/i915 — `glCheckFramebufferStatus`
returns `GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT` (`0x8CD6`) — so there is no way to
blit it into a linear renderbuffer, and `glReadPixels` has nothing to read from.
Desktop GL accepts the attachment. That is why `pkg-config` probes `gl` rather
than `glesv2` and why the wrapper includes `<GL/gl.h>`.

---

## FFI Implementation Sketch

```c
// 1. Find a DRM card with active output
int drm_fd = open("/dev/dri/card0", O_RDWR | O_CLOEXEC);
drmSetClientCap(drm_fd, DRM_CLIENT_CAP_UNIVERSAL_PLANES, 1);

drmModePlaneRes *planes = drmModeGetPlaneResources(drm_fd);
// Find primary plane with active fb_id, get CRTC dimensions
// ... iterate planes, pick primary
// Read the plane's "rotation" property while walking it: DRM_MODE_ROTATE_0 /
// _90 / _180 / _270 map to capture::Rotation::{R0,R90,R180,R270}, which every
// Frame and FbInfo carries. The add-on REPORTS the rotation; it never rotates
// (MODULE_CAPTURE "Display rotation").

// 2. Create GBM device + EGL context (surfaceless, DESKTOP GL — not GLES2)
struct gbm_device *gbm = gbm_create_device(drm_fd);
EGLDisplay egl_dpy = eglGetPlatformDisplayEXT(EGL_PLATFORM_GBM_KHR, gbm, NULL);
eglInitialize(egl_dpy, NULL, NULL);
eglBindAPI(EGL_OPENGL_API);                 // NOT EGL_OPENGL_ES_API

EGLConfig egl_cfg;
// config_attribs carries EGL_RENDERABLE_TYPE = EGL_OPENGL_BIT
eglChooseConfig(egl_dpy, config_attribs, &egl_cfg, 1, &num_cfgs);
EGLContext egl_ctx = eglCreateContext(egl_dpy, egl_cfg, EGL_NO_CONTEXT, ctx_attribs);
eglMakeCurrent(egl_dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, egl_ctx);

// 3. Per-frame: get current framebuffer as DMA-BUF
drmModeFB2 *fb2 = drmModeGetFB2(drm_fd, plane->fb_id);
int dmabuf_fd;
drmPrimeHandleToFD(drm_fd, fb2->handles[0], DRM_CLOEXEC, &dmabuf_fd);

// 4. Path A: Zero-copy direct to HW encoder
// Hand the fd to libva / NVENC / AMF as SurfaceHandle::DmaBuf — no GPU→CPU copy
return FbInfo{ width: w, height: h, rotation, timestamp_ns,
               handle: DmaBuf{ fd: dmabuf_fd, stride, fourcc, modifier } };

// 4. Path B: CPU readback for SW encoder
EGLImageKHR egl_image = eglCreateImageKHR(egl_dpy, EGL_NO_CONTEXT,
    EGL_LINUX_DMA_BUF_EXT, NULL, image_attribs);
GLuint tex;
glGenTextures(1, &tex);
glBindTexture(GL_TEXTURE_2D, tex);
glEGLImageTargetTexture2DOES(GL_TEXTURE_2D, egl_image);

// Blit to a linear renderbuffer, then read it back through the PBO ring below.
// The attachment is the step that fails under GLES2 — see "CPU readback".
glReadPixels(0, 0, w, h, GL_BGRA, GL_UNSIGNED_BYTE, NULL);
```

---

## Two Output Paths

The capturer serves both frame paths from `capture` (`CursorCapturer` is the
third, orthogonal trait — see "Cursor Handling"):

| Trait | Method | Output | Use case |
|-----------|--------|--------|----------|
| `Capturer` (CPU readback) | `next_frame()` | `Frame` — RGBA, owned `RVec<u8>`, with `stride`, `rotation` and `timestamp_ns` | Pair with SW encoder (OpenH264) |
| `SurfaceCapturer` (zero-copy) | `next_surface()` | `FbInfo { width, height, rotation, timestamp_ns, handle: DmaBuf { fd, stride, fourcc, modifier } }` | Pair with HW encoder (libva, NVENC, Vulkan) |

The pipeline picks the right method based on what encoder add-on is paired.
`AddonCaps::SURFACE` is what tells it the zero-copy method is served at all.

### CPU readback (PBO double-buffer, mandatory)

A bare `glReadPixels` into client memory stalls the CPU until the GPU has
finished the copy — the 25 ms / 50 ms figures below are that stall. The add-on
therefore uses a **ring of two pixel-buffer objects**:

```c
// Once, at construction:
GLuint pbo[2]; glGenBuffers(2, pbo);
for (int i = 0; i < 2; i++) {
    glBindBuffer(GL_PIXEL_PACK_BUFFER, pbo[i]);
    glBufferData(GL_PIXEL_PACK_BUFFER, stride * height, NULL, GL_STREAM_READ);
}
size_t n = 0;              // frame counter
bool   primed = false;     // no previous frame to map on the very first call

// Per frame, in next_frame():
glBindBuffer(GL_PIXEL_PACK_BUFFER, pbo[n % 2]);
glReadPixels(0, 0, width, height, GL_BGRA, GL_UNSIGNED_BYTE, NULL); // ASYNC: returns now
if (!primed) { n++; primed = true; return Ok(None); }               // ONE-frame warmup
glBindBuffer(GL_PIXEL_PACK_BUFFER, pbo[(n + 1) % 2]);               // the PREVIOUS frame
void *p = glMapBufferRange(GL_PIXEL_PACK_BUFFER, 0, stride * height, GL_MAP_READ_BIT);
// copy p into the owned RVec<u8>, then:
glUnmapBuffer(GL_PIXEL_PACK_BUFFER);
n++;
```

Consequences:

- The delivered frame is **one frame interval old**. `Frame.timestamp_ns` is
  stamped when the `glReadPixels` for *that* frame was issued, not when it is
  mapped, so the timestamp stays truthful and A/V sync stays correct.
- The very first `next_frame()` after construction (and after any reconfigure)
  returns `Ok(None)` to prime the ring: there is genuinely no completed readback
  to hand over yet, which is what `Ok(None)` means. It is **not** a slow-readback
  signal — a readback that cannot keep up is settled by the pipeline's
  sustainable-rate control, never by this add-on reporting an idle screen
  (MODULE_CAPTURE `next_frame`). One dropped frame at startup.
- The stall is gone: the map hits a buffer the GPU finished a full interval ago.

---

## Performance Targets

| Path | 1080p p50 | 1440p p50 | Notes |
|------|----------:|----------:|-------|
| **DMA-BUF zero-copy** | **~0.5 ms** | **~0.5 ms** | Just acquires the fd — actual cost paid by the encoder |
| CPU readback, PBO double-buffered | ~6 ms | ~11 ms | The default SW-path cost. One frame of added latency; the DMA overlaps the next frame |
| CPU readback, synchronous `glReadPixels` (rejected) | ~25 ms | ~50 ms | The measured stall the PBO ring exists to remove — recorded so the number is not re-derived as a target |

Measured (Intel HD 630, original featherdesk integration tests):
- EGL context init: 170 ms (one-time)
- DRM card discovery: 5 ms (one-time)
- DMA-BUF fd export per frame: ~0.5 ms
- Synchronous `glReadPixels` 1440p BGRA: 50 ms per frame — the reason the
  readback path is PBO double-buffered and the reason the DMA-BUF path is the
  whole point of this add-on.

---

## Cursor Handling

The cursor is on its own DRM plane (commonly 64x64 RGBA with alpha, up to the
plane's advertised size), which the compositor programs directly — under X11 and under Wayland alike. Reading it needs nothing
this add-on does not already have: the same `CAP_SYS_ADMIN`/root the probe
already requires for `drmModeGetFB2` on the primary plane.

**Capabilities declared at probe.** A plane of type `DRM_PLANE_TYPE_CURSOR` on the
selected CRTC → `AddonCaps::CURSOR | AddonCaps::EMBED_CURSOR`. No cursor plane →
`AddonCaps::EMBED_CURSOR | AddonCaps::EMBED_CURSOR_SURF` and **not** `CURSOR`: the
compositor is drawing a software cursor into the primary plane, so the frames
already contain it and embedding is a no-op.

**`capture::CursorCapturer` (`embed_cursor = false`).**

```rust
fn next_cursor(&mut self) -> Result<Option<capture::CursorState>, StreamError>;
```

- Position, every poll, no readback: `drmModeGetPlane(card_fd, cursor_plane_id)`
  → `crtc_x` / `crtc_y`. These are the bitmap's top-left corner on the CRTC, so
  the add-on adds the current shape's hotspot back before returning `x`/`y` —
  every add-on reports the hotspot (MODULE_CAPTURE "Cursor coordinate space").
- Visibility: `plane->fb_id == 0` → `visible = false` with the last position.
- Shape, only when `fb_id` changes: `drmModeGetFB2` → `drmPrimeHandleToFD` →
  EGL import → `glReadPixels` into a buffer of the plane's **advertised** size —
  `drmModeGetFB2`'s `width`/`height`, commonly 64x64 and up to 256x256 on AMD and
  Intel CRTCs, never a hardcoded 64x64 — then the ARGB8888
  premultiplied → straight-RGBA conversion from MODULE_CAPTURE "Cursor pixel
  format". The previous PRIME fd is closed on the swap; the last one is closed on
  Drop. A `fb_id` that has not changed returns `shape: None` and costs one ioctl.
  The bitmap is returned at the OS's native size; the 128-pixel wire cap is
  applied by the host's cursor publisher.
- Hotspot: from the plane's `HOTSPOT_X`/`HOTSPOT_Y` properties when the driver
  exposes them, else `(0, 0)` — the plane position already accounts for the
  hotspot in that case, so the two are consistent.

**First call.** MODULE_CAPTURE's `next_cursor` contract requires the first call
after construction to return `Ok(Some(..))` carrying the current position, the
current visibility **and** the current bitmap (`shape: Some(..)`), even though
nothing has changed — the host has no other way to seed a joining client. The
"only when `fb_id` changes" rule above applies from the second call on.

**`embed_cursor = true`.** `next_frame` calls `capture::blend_cursor` on the
readback buffer at `(crtc_x, crtc_y)` before returning the `Frame`. There is no
surface-path equivalent — the exported DMA-BUF is the compositor's, so
`EMBED_CURSOR_SURF` is not declared when a cursor plane is in use, and a session
that resolves `"embedded"` on this add-on is paired with the software encode path.

**Failure behavior.** A failed plane query, PRIME export, EGL import or readback
returns `Err(StreamError::Backend(..))` from `next_cursor` and never from
`next_frame`: a dead cursor query must not take video down. The pipeline's
`on_cursor_error` handles it (MODULE_PIPELINE). A plane that disappears
mid-session is `visible = false`, not an error.

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-kms_egl   (cfg(target_os = "linux"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

```rust
/// The root module's `probe`. A missing prerequisite is NOT an error — it is
/// `ROk(ProbeReport { available: false, reason, .. })`. `RErr` means the probe
/// itself broke.
fn probe(&self) -> RResult<ProbeReport, AbiError> {
    // 1. Check CAP_SYS_ADMIN / root via geteuid + effective capability set
    // 2. Enumerate /dev/dri/card* devices
    // 3. For each: open, set UNIVERSAL_PLANES, find the primary plane with fb_id
    // 4. Read CRTC dimensions + refresh rate into a DisplayInfo per output:
    //      id           = the DRM connector id (the value [addon_module_kms_egl]
    //                     output_index selects among, in enumeration order)
    //      width/height = the CRTC mode in PIXELS, as scanned out (NOT upright)
    //      rotation     = the DRM plane's "rotation" property mapped to
    //                     abi::Rotation; the host transposes for R90/R270
    //      refresh_mhz  = the mode's vertical refresh in milliHertz
    //      scale_num/den = 1/1 — DRM scanout is in physical pixels
    // 5. No usable card → ROk(ProbeReport { available: false,
    //      reason: "no usable DRM card (need CAP_SYS_ADMIN and a card with a
    //               primary plane)".into(), codecs: RVec::new(),
    //      caps: AddonCaps(0), displays: RVec::new() })
    // 6. Otherwise → ROk(ProbeReport {
    //      available: true, reason: RString::new(), codecs: RVec::new(),
    //      caps: AddonCaps(AddonCaps::SURFACE           // DMA-BUF export works
    //                    | AddonCaps::CURSOR            // ONLY if the cursor plane is readable
    //                    | AddonCaps::EMBED_CURSOR      // capture::blend_cursor on the CPU readback
    //                    | AddonCaps::EMBED_CURSOR_SURF // ONLY when no cursor plane exists —
    //                                                   //   the compositor already composited
    //                    | AddonCaps::CONFIGURABLE),    // ONLY if a mode change needs no re-create
    //      // see "Cursor Handling" for which combination applies
    //      displays })
}
```

**Availability is not an error.** A missing driver, a denied permission or an
absent device is `ROk(ProbeReport { available: false, reason })`. `RErr` is
reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` means
no zero-copy path, no separate cursor and no hot parameter change — silently, with
no error and no warning.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); a capability that only
`construct()` can settle is reported by the constructed object's `caps()`, which
is authoritative and may be a strict subset of this one.

The cursor bits follow "Cursor Handling": a `DRM_PLANE_TYPE_CURSOR` plane on the
selected CRTC yields `CURSOR | EMBED_CURSOR`; its absence yields
`EMBED_CURSOR | EMBED_CURSOR_SURF` without `CURSOR`.

Pipeline probes capture (Linux, this add-on loaded):
```
NvFBC add-on AND NVIDIA proprietary?  → use NvFBC (lower latency on NVIDIA)
KMS+EGL with root?                    → use this add-on
None?                                 → fatal: no capture add-on configured
```

---

## File Structure

```
addons/capture/kms_egl/
├── kms.rs                      // KmsCapturer struct, KmsCapturer::new
├── drm.rs                      // DRM card discovery, plane enumeration
├── egl.rs                      // EGL context (desktop GL), DMA-BUF import, PBO readback
├── ffi.rs                      // Rust FFI bindings (built into the add-on cdylib)
├── cursor.rs                   // DRM cursor plane: next_cursor + blend_cursor
├── probe.rs                    // the root module's probe() -> ProbeReport
├── tests/drm.rs                // integration test (cfg(feature = "integration"))
├── tests/egl.rs                // integration test (cfg(feature = "integration"))
└── tests/kms.rs                // integration test (cfg(feature = "integration"))
```

A Go prototype exists on the `feature-libav-vp8s8` branch
(`internal/capture/{drm,egl,kms,cursor}.go`). It is not the path that branch runs
(see "Status"), so the Rust add-on lands the design, not a mechanical port.

---

## Known Issues (to fix in refactor)

| ID | Issue | Severity |
|----|-------|---------|
| TD-03 | Four file-scope C statics in the EGL readback path allow only one context per process | Medium — latent while a single pinned capture thread is the only caller; fatal on the first concurrent second context |
| TD-13 | `fps` parameter accepted but never used (no frame pacing) | Medium — orchestrator handles pacing externally |

Beyond the recorded TD rows, the blocker that stopped the Go prototype is open
work in its own right: an XR30 (10-bit XRGB) scanout framebuffer with a Y-tiled
modifier imports through EGL but is not framebuffer-attachable, so the readback
returns zeros (see "Status"). Making tiled and 10-bit scanout formats work — by
negotiating a linear/8-bit import where the driver allows one, and by falling
back to `ROk(ProbeReport { available: false, reason })` where it does not — is in
scope for this add-on, not something the port inherits solved.

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

📋 **Specced; prototyped in Go and then abandoned.** `internal/capture/{drm,egl,kms,cursor}.go`
on `feature-libav-vp8s8` is a working-shaped implementation, but it is not the
capture path that runs: commit `deb99d1` switched `cmd/server/main.go` from
`NewKMSCapturer` to `NewX11Capturer`, and `NewKMSCapturer` now has no non-test
caller. The recorded root cause is that on Intel HD 630 under GNOME Wayland the
scanout framebuffer is XR30 (10-bit XRGB) with a Y-tiled modifier: the EGL import
succeeds, but a `GL_TEXTURE_2D` backed by the resulting EGLImage is not
framebuffer-attachable (`GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT`, `0x8CD6`) and
readback returns zeros. Landing this add-on therefore means solving tiled and
10-bit scanout formats, not porting already-working code — see "Known Issues".

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_kms_egl]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.

The keys, their defaults and their domains are in MODULE_CONFIG "Schema", under
`[addon_module_kms_egl]`; this spec does not restate them.



---

## Stream Params Translation

This add-on implements `capture::ConfigurableCapturer` — and therefore sets `AddonCaps::CONFIGURABLE` at probe — only where the driver accepts a mode change without a fresh `drmModeAddFB2` (see [`specs/core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)). Where it does not, the bit stays clear and the pipeline tears the capturer down and rebuilds it. KMS+EGL captures at native display resolution; the pipeline handles scaling.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Output is native display resolution -- pipeline scales via GL blit or libyuv. No capturer change needed. | n/a (pipeline) |
| `FPS` | Pipeline pacing (capture is event-driven via `drmModePageFlip` / `drmHandleEvent`). No capturer change needed. | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | Request `DRM_FORMAT_XRGB2101010` framebuffer via `drmModeAddFB2` (driver-dependent; falls back to XRGB8888 if unsupported) | requires re-init |
| `ColorSpace` | Reported per surface metadata from `drmModeGetProperty`; pipeline annotates encoder | n/a (read-only) |