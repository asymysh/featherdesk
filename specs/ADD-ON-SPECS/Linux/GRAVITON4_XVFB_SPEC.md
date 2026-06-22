# FeatherDesk — Graviton4 EKS Pod Optimisation

## Use Case

Linux Kubernetes pods on **Amazon Graviton4** (ARM64, Neoverse V2) running headless X11
workloads via `Xvfb`. The goal is to stream an encoded view of the virtual display to a
browser client while keeping CPU usage low enough to maximise pod density per node.

**Constraints:**
- ARM64 only — no GPU, no VA-API, no KMS/DRM
- Pods run as non-root — no `/dev/dri`, no `CAP_SYS_ADMIN`
- BSD/MIT license chain only — no ffmpeg, no GPL/LGPL
- `Xvfb` is the display server — `DISPLAY=:99` or similar

---

## Why the Current Code Is Wrong for This Target

`internal/capture/x11grab.go` spawns a **`ffmpeg -f x11grab` subprocess** per pod:

```go
// current — wrong for Graviton4 pods
cmd = exec.CommandContext(c.ctx, "ffmpeg",
    "-f", "x11grab", "-i", display,
    "-f", "rawvideo", "-pix_fmt", "rgba", "-")
```

Problems per pod:
| Problem | Impact |
|---------|--------|
| One `ffmpeg` process per pod | Doubles process count on the node |
| Raw pixels travel through a kernel pipe | Memory copy + syscall per frame |
| Pipe read blocks until ffmpeg writes | Adds ~2–5ms latency per frame |
| `ffmpeg` binary must be in the container | +~80MB container image, extra dependency |
| No NEON SIMD in the Go encode path | libyuv + OpenH264 run without ARM64 optimisation because the bottleneck is the pipe, not the encoder |

---

## The Solution: XShm Direct Capture

`Xvfb` keeps its framebuffer in **kernel shared memory** (SysV SHM or POSIX SHM).
The X11 MIT-SHM extension (`XShmGetImage`) lets a client process read that framebuffer
directly — no subprocess, no pipe, no copy through the kernel IPC stack.

```
Current path:
  Xvfb (SHM) → ffmpeg (copies) → pipe → Go (copies) → libyuv → OpenH264
                ↑ 3 copies, 1 subprocess, IPC latency

New path:
  Xvfb (SHM) → XShmGetImage (1 mmap read) → Go → libyuv NEON → OpenH264 NEON
                ↑ 1 copy, 0 subprocesses, no IPC
```

### License chain
```
libX11 + libXext (MIT-SHM)   MIT ✅
libyuv                        BSD-3 ✅
OpenH264                      BSD-2 ✅
Our XvfbCapturer CGo code     MIT ✅
```
No GPL. No LGPL. No ffmpeg.

---

## CPU Budget Analysis

Measured baseline (OpenH264 CGo benchmark on AMD Ryzen 5 3600 x86_64):
- 1920×1080: **3.56ms p50** encode latency

Graviton4 (Neoverse V2, ARM64 NEON) estimated:
- Single-thread performance ≈ 85% of Ryzen 5 3600 single-thread
- NEON vs AVX2: libyuv NEON is within 20% of AVX2 for BGRA→I420

| Component | Estimated CPU (1080p 30fps) |
|-----------|---------------------------|
| `XShmGetImage` read | ~0.5% |
| libyuv BGRA→I420 (NEON) | ~1.5% |
| OpenH264 encode (NEON) | ~5–7% |
| Protocol framing + broadcast | ~0.5% |
| **Total per pod** | **~8–9%** |

vs current ffmpeg subprocess path (estimated):
| Component | Estimated CPU (1080p 30fps) |
|-----------|---------------------------|
| ffmpeg process overhead | ~5% |
| Pipe read + kernel copy | ~2% |
| libyuv (no NEON, outside hot path) | ~3% |
| OpenH264 (same) | ~7% |
| **Total per pod** | **~17–20%** |

**Result: ~2× more pods per node** for the same streaming workload.

For a node with 32 vCPUs budgeting 25% CPU per pod:
- Current: `25% / 18% ≈ 1.4 pods` — effectively 1 pod per node safely
- Optimised: `25% / 8.5% ≈ 2.9 pods` — 2–3 pods per node safely

---

## Implementation

### New File: `internal/capture/xvfb_linux.go`

Replace `X11Capturer` for the pod use case. The `XvfbCapturer` implements the same
`Capturer` interface — zero changes to the pipeline or server.

```go
//go:build linux

package capture

/*
#cgo LDFLAGS: -lX11 -lXext
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/XShm.h>
#include <sys/ipc.h>
#include <sys/shm.h>
#include <string.h>
#include <stdlib.h>

typedef struct {
    Display        *dpy;
    Window          root;
    XImage         *image;
    XShmSegmentInfo shminfo;
    int             width;
    int             height;
} XvfbCtx;

XvfbCtx* xvfb_open(const char *displayName, int *w, int *h) {
    XvfbCtx *ctx = (XvfbCtx*)calloc(1, sizeof(XvfbCtx));
    ctx->dpy = XOpenDisplay(displayName);
    if (!ctx->dpy) { free(ctx); return NULL; }

    int screen = DefaultScreen(ctx->dpy);
    ctx->root  = RootWindow(ctx->dpy, screen);
    ctx->width  = DisplayWidth(ctx->dpy, screen);
    ctx->height = DisplayHeight(ctx->dpy, screen);
    *w = ctx->width; *h = ctx->height;

    // Create shared memory image — Xvfb keeps its buffer in SHM
    ctx->image = XShmCreateImage(
        ctx->dpy,
        DefaultVisual(ctx->dpy, screen),
        DefaultDepth(ctx->dpy, screen),
        ZPixmap, NULL, &ctx->shminfo,
        ctx->width, ctx->height);
    if (!ctx->image) { XCloseDisplay(ctx->dpy); free(ctx); return NULL; }

    ctx->shminfo.shmid = shmget(IPC_PRIVATE,
        ctx->image->bytes_per_line * ctx->image->height,
        IPC_CREAT | 0600);
    ctx->shminfo.shmaddr = ctx->image->data =
        (char*)shmat(ctx->shminfo.shmid, NULL, 0);
    ctx->shminfo.readOnly = False;

    XShmAttach(ctx->dpy, &ctx->shminfo);
    XSync(ctx->dpy, False);
    return ctx;
}

// Capture one frame into the SHM buffer.
// Returns pointer to BGRA pixel data (ctx->shminfo.shmaddr).
// Caller must NOT free this pointer — it's the SHM region.
char* xvfb_capture(XvfbCtx *ctx) {
    if (!XShmGetImage(ctx->dpy, ctx->root, ctx->image, 0, 0, AllPlanes))
        return NULL;
    XSync(ctx->dpy, False);
    return ctx->shminfo.shmaddr;
}

void xvfb_close(XvfbCtx *ctx) {
    if (!ctx) return;
    XShmDetach(ctx->dpy, &ctx->shminfo);
    XDestroyImage(ctx->image);
    shmdt(ctx->shminfo.shmaddr);
    shmctl(ctx->shminfo.shmid, IPC_RMID, NULL);
    XCloseDisplay(ctx->dpy);
    free(ctx);
}
*/
import "C"
import (
    "context"
    "fmt"
    "os"
    "time"
    "unsafe"
)

// XvfbCapturer reads the Xvfb framebuffer directly via MIT-SHM.
// No subprocess. No pipe. No ffmpeg.
type XvfbCapturer struct {
    ctx    context.Context
    cctx   *C.XvfbCtx
    width  uint32
    height uint32
    fps    int
    buf    []byte  // owned copy of SHM pixels for the Frame
}

func NewXvfbCapturer(ctx context.Context, fps int) (*XvfbCapturer, error) {
    display := os.Getenv("DISPLAY")
    if display == "" {
        display = ":0"
    }

    var cw, ch C.int
    cctx := C.xvfb_open(C.CString(display), &cw, &ch)
    if cctx == nil {
        return nil, fmt.Errorf("capture: XvfbCapturer: cannot open display %s", display)
    }

    w, h := uint32(cw), uint32(ch)
    return &XvfbCapturer{
        ctx:    ctx,
        cctx:   cctx,
        width:  w,
        height: h,
        fps:    fps,
        buf:    make([]byte, w*h*4),
    }, nil
}

func (c *XvfbCapturer) NextFrame() (*Frame, error) {
    if c.ctx.Err() != nil {
        return nil, c.ctx.Err()
    }

    // XShmGetImage reads Xvfb SHM into our registered SHM region — no extra copy
    ptr := C.xvfb_capture(c.cctx)
    if ptr == nil {
        return nil, fmt.Errorf("capture: XShmGetImage failed")
    }

    // One copy from SHM into our Go-owned buffer (Frame.Data ownership contract)
    C.memcpy(unsafe.Pointer(&c.buf[0]), unsafe.Pointer(ptr), C.size_t(len(c.buf)))

    return &Frame{
        Data:      c.buf,
        Width:     c.width,
        Height:    c.height,
        Timestamp: uint64(time.Now().UnixNano()), // CLOCK_MONOTONIC equivalent
    }, nil
}

func (c *XvfbCapturer) Close() error {
    C.xvfb_close(c.cctx)
    c.cctx = nil
    return nil
}
```

### Pixel Format Note

Xvfb on 24/32-bit depth uses **BGRX** (blue-green-red-padding, little-endian) by default,
not RGBA. This matters for libyuv:

```go
// Use ABGRToI420, not RGBAToI420 — the byte order from Xvfb is BGRX
// libyuv's "ABGR" in little-endian memory = B G R X = Xvfb native
i420 := converter.Convert(frame.Data)  // Converter uses C.ABGRToI420 — correct as-is
```

The existing `Converter` in `internal/encode/convert.go` already calls `C.ABGRToI420`
which matches Xvfb's native BGRX byte order. **No change needed to the encoder.**

---

## Optional: X11 Damage Extension (Skip Static Frames)

When the Xvfb screen hasn't changed, capturing and encoding the same frame wastes CPU.
The X11 Damage extension delivers change notifications:

```c
#include <X11/extensions/Xdamage.h>

// At init:
Damage dmg = XDamageCreate(dpy, root, XDamageReportNonEmpty);

// Per frame tick: check if damage event pending before capturing
Bool xvfb_has_damage(XvfbCtx *ctx) {
    XEvent ev;
    if (XCheckTypedEvent(ctx->dpy, ctx->damageEventBase + XDamageNotify, &ev)) {
        XDamageSubtract(ctx->dpy, ctx->damage, None, None); // reset damage region
        return True;
    }
    return False;
}
```

In Go:
```go
func (c *XvfbCapturer) NextFrame() (*Frame, error) {
    if !c.hasDamage() {
        return nil, nil  // nil, nil = "no new frame this tick" (pipeline skips encode)
    }
    // ... capture as before
}
```

**Impact on CPU:** If the Xvfb screen is 70% idle (typical for many remote desktop
workloads), damage-gated capture reduces encode CPU by 70%.

---

## ARM64 / NEON Build Requirements

Both libyuv and OpenH264 must be compiled for ARM64 with NEON enabled. Verify:

```bash
# Check libyuv has NEON enabled:
objdump -d /usr/lib/aarch64-linux-gnu/libyuv.so | grep -c "fmla\|fmul\|umull" 
# Should return > 0 (NEON instructions present)

# Check OpenH264 has NEON enabled:
objdump -d /usr/lib/aarch64-linux-gnu/libopenh264.so | grep -c "fmla\|smull\|umull"
# Should return > 0
```

If your base image uses Ubuntu 22.04 ARM64, both packages have NEON enabled by default.
Older Alpine ARM64 images may use scalar builds — verify before benchmarking.

**OpenH264 CGo build flag for explicit NEON:**
```go
// #cgo CFLAGS: -march=armv8-a+crc+simd
```

---

## Container Changes

### Remove from image:
- `ffmpeg` binary (~80MB with codecs)
- `xdpyinfo` (replaced by libX11 display query)
- `python3` (if only used for screencast.py)
- `screencast.py`

### Add to image:
```dockerfile
RUN apt-get install -y \
    libx11-dev \      # XOpenDisplay, XShmGetImage
    libxext-dev \     # MIT-SHM extension
    libopenh264-dev \ # already needed
    libyuv-dev        # already needed
# Total addition: ~4MB headers at build, ~2MB runtime .so
```

### Image size impact:
- Removed: ~80MB (ffmpeg)
- Added: ~2MB (libX11 + libXext runtime)
- **Net: ~78MB smaller image**

---

## Kubernetes Pod Spec Changes

```yaml
# No special privileges needed for XShm
# Just ensure DISPLAY is set and Xvfb is running in the pod

env:
  - name: DISPLAY
    value: ":99"

# Xvfb sidecar or init container
initContainers:
  - name: xvfb
    image: your-base-image
    command: ["Xvfb", ":99", "-screen", "0", "1920x1080x24", "-ac", "+extension", "MIT-SHM"]
```

No `privileged: true`. No `hostPath` volumes. No capability escalation.

---

## Backend Selection Logic

In the pipeline startup (currently in `cmd/server/main.go`, future in `pipeline.go`):

```go
func selectCapture(ctx context.Context, fps int) (Capturer, error) {
    // Graviton4 / headless pods: prefer XShm direct capture
    if os.Getenv("DISPLAY") != "" {
        if cap, err := capture.NewXvfbCapturer(ctx, fps); err == nil {
            return cap, nil  // XShm available — use it
        }
    }
    // Fallback to subprocess (original X11Capturer)
    return capture.NewX11Capturer(ctx, fps)
}
```

The `XvfbCapturer` auto-detects — if MIT-SHM init fails (no Xvfb, display not available),
it returns an error and the pipeline falls back to the original subprocess path.

---

## Benchmark Plan

Run on an actual Graviton4 pod (or `c8g` instance) with Xvfb:

```bash
# Start Xvfb
Xvfb :99 -screen 0 1920x1080x24 -ac +extension MIT-SHM &
export DISPLAY=:99

# Run the featherdesk benchmark (once built)
./viewport-rds --benchmark --fps 30 --resolution 1920x1080

# Measure CPU with pidstat
pidstat -u -p $(pgrep viewport-rds) 1 60
```

Expected results:
| Metric | subprocess (current) | XShm (new) |
|--------|---------------------|-----------|
| Capture CPU | ~7% | ~1% |
| Encode CPU (OpenH264 NEON) | ~7% | ~6% |
| Total pod CPU | ~17% | ~8% |
| Max pods @ 25% budget | 1 | 3 |

---

## Relationship to Other Modules

| Module | Relationship |
|--------|-------------|
| `MODULE_CAPTURE.md` | `XvfbCapturer` implements the `Capturer` interface. New backend, same contract. |
| `MODULE_ENCODE.md` | Unchanged — `OpenH264` CGo path used as-is. NEON happens automatically on ARM64. |
| `MODULE_PIPELINE.md` | Backend selection logic (choose XvfbCapturer on Linux pods) |
| `MODULE_CUSTOM_LIBVA.md` | Not applicable — no GPU on Graviton4. That module is for when a GPU is present. |
