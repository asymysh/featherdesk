# Module Spec: Capture

## Overview

The Capture module is responsible for acquiring raw screen frames from the Linux display subsystem. It abstracts multiple capture backends behind a unified `Capturer` interface.

---

## Public Interface

```go
package capture

// Capturer is the contract all capture backends must satisfy.
type Capturer interface {
    // NextFrame returns the next captured frame.
    //   - Returns (frame, nil) on success.
    //   - Returns (nil, nil) when no NEW frame is available (static screen / pacing) —
    //     the caller should treat this as "skip this tick", not an error.
    //   - Returns (nil, err) on failure.
    // Frame.Data is BORROWED: valid only until the next NextFrame call. Copy to retain.
    NextFrame() (*Frame, error)

    // Close releases all resources (DRM fds, EGL contexts, subprocesses).
    Close() error
}

// Frame represents a single captured screen frame.
type Frame struct {
    Data      []byte // Raw RGBA pixel buffer (len = Width * Height * 4), BORROWED
    Width     uint32
    Height    uint32
    Timestamp uint64 // CLOCK_MONOTONIC nanoseconds, sampled at capture (canonical media clock)
}

// FBInfo is returned by DMABufCapturer.NextDMABuf for the zero-copy path.
type FBInfo struct {
    DMAFD     int    // DMA-BUF fd (caller OWNS, must close)
    Width     uint32
    Height    uint32
    Stride    uint32
    Format    uint32 // DRM fourcc
    Modifier  uint64 // tiling/compression modifier
    Timestamp uint64 // CLOCK_MONOTONIC ns, sampled at capture (REQUIRED for A/V sync)
}

// CaptureConfig holds backend-agnostic configuration.
type CaptureConfig struct {
    FPS        int             // Target capture framerate
    Backend    CaptureBackend  // Requested backend (or Auto)
    Logger     *slog.Logger    // stdlib log/slog
}

type CaptureBackend int

const (
    BackendAuto       CaptureBackend = iota // Probe and select best
    BackendKMS                              // DRM/KMS + EGL (requires root)
    BackendX11                              // ffmpeg x11grab subprocess
    BackendScreencast                       // PipeWire/Mutter D-Bus screencast
)
```

---

## Internal Architecture

### Backend: KMS (Primary Path)

```
/dev/dri/card* → DRM plane enumeration → Primary plane → Framebuffer
    → drmPrimeHandleToFD → DMA-BUF file descriptor
    → EGL: eglCreateImageKHR(EGL_LINUX_DMA_BUF_EXT)
    → GL: texture → blit to linear renderbuffer → glReadPixels
    → RGBA []byte
```

**Components:**
| Component | File | Responsibility |
|-----------|------|----------------|
| `DRMCard` | `drm.go` | DRM device discovery, plane enumeration, FB export |
| `EGLState` | `egl.go` | GBM device, EGL context, DMA-BUF import, pixel readback |
| `KMSCapturer` | `kms.go` | Orchestrates DRM+EGL per-frame, implements `Capturer` |
| `CursorState` | `cursor.go` | Cursor plane capture + alpha compositing |

**System Dependencies:**
- `libdrm` (pkg-config)
- `libgbm` (pkg-config)
- `EGL` + `GL` (link flags)
- Requires: root or `CAP_SYS_ADMIN` + DRM master

### Backend: X11/Screencast (Fallback Path)

```
Option A: ffmpeg -f x11grab -video_size WxH -framerate N -i :DISPLAY -f rawvideo -pix_fmt rgba pipe:1
Option B: screencast.py → D-Bus(Mutter) → PipeWire → GStreamer → rawvideo pipe
```

**Components:**
| Component | File | Responsibility |
|-----------|------|----------------|
| `X11Capturer` | `x11grab.go` | Subprocess management, raw frame reading from pipe |
| `screencast.py` | `screencast.py` | Mutter D-Bus session + GStreamer pipeline (GNOME only) |

**System Dependencies:**
- `ffmpeg` binary in PATH
- `xdpyinfo` for resolution detection
- Optional: Python3, `dbus`, `gi.repository`, `gst-launch-1.0` (for screencast path)

---

## Cursor Handling

The cursor plane is captured separately from the primary plane:

1. DRM plane enumeration finds the `PlaneTypeCursor` plane
2. A **separate** EGL context imports the cursor framebuffer
3. Cursor pixels (typically 64x64 RGBA with alpha) are read via `glReadPixels`
4. CPU-side alpha compositing blends cursor onto the main frame at `(crtc_x, crtc_y)`

**BlendCursor Algorithm:**
```
for each cursor pixel (cx, cy):
    if alpha == 0: skip
    if alpha == 255: direct copy
    else: standard over-compositing
        out.R = (src.R * alpha + dst.R * (255 - alpha)) / 255
        out.G = (src.G * alpha + dst.G * (255 - alpha)) / 255
        out.B = (src.B * alpha + dst.B * (255 - alpha)) / 255
```

---

## Refactoring Directives

### R-CAP-01: Extract Capturer Interface to `pkg/capture`
Move the `Capturer` interface, `Frame`, and `CaptureConfig` to a public package. Implementations stay in `internal/capture/kms/`, `internal/capture/x11/`, etc.

### R-CAP-02: Fix DMA-BUF FD Leak
In `kms.go` `NextFrame()`, the new DMA-BUF fd is assigned before error checking on `ImportDMABuf`. If import fails, the new fd is never closed. Fix: assign to `lastDMAFD` only after successful import; close new fd on error.

### R-CAP-03: Add Frame Pacing with Drop Strategy
`KMSCapturer.NextFrame()` currently runs as fast as possible. Add configurable frame pacing with the following strategy:

**Pacing Logic:**
```go
type PacedCapturer struct {
    inner     Capturer
    interval  time.Duration  // 1/FPS
    minFPS    int            // Floor: 5 FPS
    lastFrame time.Time
    dropped   uint64         // Metric: frames dropped
}

func (p *PacedCapturer) NextFrame() (*Frame, error) {
    now := time.Now()
    elapsed := now.Sub(p.lastFrame)
    
    if elapsed < p.interval {
        // Sleep until next frame is due
        time.Sleep(p.interval - elapsed)
    }
    
    p.lastFrame = time.Now()
    return p.inner.NextFrame()
}
```

**Frame Drop Logic (in pipeline, not in capturer):**
The orchestrator/pipeline decides when to drop:
```
frameStart = now()
frame = capturer.NextFrame()
encoded = encoder.Encode(convert(frame))
server.Broadcast(encoded)
frameDuration = now() - frameStart

if frameDuration > frameInterval:
    // We're behind. Calculate how many frames to skip.
    framesToSkip = min(frameDuration / frameInterval, maxSkip)
    // maxSkip ensures we never go below 5 FPS:
    // maxSkip = (targetFPS / minFPS) - 1
    // e.g., at 60fps target, maxSkip = 11 (drops to ~5fps)
    skip framesToSkip captures
```

**Key Rules:**
- Minimum floor: 5 FPS — NEVER drop below this regardless of system load
- Prefer dropping frames over accumulating latency
- A frame is either fully processed (capture → encode → broadcast) or entirely skipped
- No partial pipeline execution (e.g., capturing without encoding)

### R-CAP-04: Remove Static C Globals
The EGL blit FBO state (`read_fbo_src`, `read_fbo_dst`, etc.) uses static C variables. Refactor to pass FBO state through the `EGLState` struct to enable multiple concurrent EGL contexts (and prepare for multi-GPU).

### R-CAP-05: Eliminate Recursive Retry in X11Capturer
Replace recursive `NextFrame()` retry on process death with an iterative loop with configurable max retries and exponential backoff.

### R-CAP-06: Remove Hardcoded Developer Path
`findScreencastScript()` has a hardcoded `/home/aseem/...` path. Use `os.Executable()` relative path or embed the script.

### R-CAP-07: Unify EGL Context for Cursor
Remove the separate EGL context for cursor capture. Use a single EGL context with multiple textures/FBOs to reduce GPU resource usage.

### R-CAP-08: Add Resolution Change Detection
Detect when the display resolution changes (monitor hotplug, resolution switch) and propagate the change upstream so the encoder can reinitialize.

### R-CAP-09: Implement Frame Change Detection
When the screen is static (same `fb_id`, no page flip), skip the expensive EGL import + readback path. Return a sentinel "no change" or the cached last frame.

### R-CAP-10: Backend Auto-Selection Logic
```
if running_as_root && has_drm_card:
    use KMS
elif has_wayland && has_mutter_screencast:
    use Screencast (PipeWire)
elif has_x11 && has_ffmpeg:
    use X11Grab
else:
    error("no capture backend available")
```

### R-CAP-11: Canonical Monotonic Timestamps (ALL backends)
Every backend MUST stamp `Frame.Timestamp` / `FBInfo.Timestamp` from the shared `CLOCK_MONOTONIC` epoch in nanoseconds, at capture time. The current X11 backend uses `time.Now().UnixMilli()` (wall-clock ms — wrong domain AND wrong unit); this breaks A/V sync against any monotonic source and against audio. Fix: introduce a single `clock.Now() uint64` (monotonic ns) used by all capturers and the audio module.

### R-CAP-12: Resolution-Change Signaling
When a backend detects a resolution change, it surfaces new `Width/Height` on the next frame (and may return a one-shot `ErrResized` sentinel so the pipeline can rebuild the encoder/converter and resize the input device before the next frame). The pipeline owns the downstream orchestration (see MODULE_PIPELINE "Resolution-Change Handling").

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | Error types, struct construction, BlendCursor logic | No |
| Unit | X11Capturer argument building, resolution parsing | No |
| Integration | DRM card open, plane discovery, DMA-BUF export | Yes (DRM card) |
| Integration | Full KMS capture pipeline (1 frame) | Yes (DRM + EGL) |
| Integration | Frame pacing timing validation | Yes |
| Mock | Fake `Capturer` for downstream testing | No |

---

## Performance Targets

| Metric | KMS Backend | X11 Backend |
|--------|-------------|-------------|
| Frame acquisition | <5ms (1080p), <12ms (1440p) | <16ms (pipe read) |
| Cursor composite | <0.5ms (64x64 cursor) | N/A (built-in) |
| Memory overhead | ~24MB (1440p RGBA buffer) | ~24MB + subprocess |
| CPU usage | <3% (GPU does readback) | <5% (ffmpeg does capture) |
