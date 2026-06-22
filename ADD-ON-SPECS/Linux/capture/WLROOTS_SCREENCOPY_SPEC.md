# Linux Add-On: wlr-screencopy Capture (wlroots Wayland)

## Purpose

Optional add-on capture backend for **wlroots-based Wayland compositors** — Sway,
Hyprland, river, dwl, labwc, and others. Uses the `wlr-screencopy-unstable-v1`
Wayland protocol to receive DMA-BUF framebuffers directly from the compositor,
bypassing the PipeWire ScreenCast portal layer that GNOME and KDE require.

**Why this matters vs PipeWire ScreenCast on wlroots:**

| Method | Path | Latency |
|--------|------|---------|
| PipeWire ScreenCast (default binary) | Compositor → XDG portal → D-Bus → PipeWire → frame | ~5–10ms portal overhead |
| **wlr-screencopy** | **Compositor → Wayland protocol → DMA-BUF** | **~1–2ms, direct** |

wlroots compositors implement both protocols, but `wlr-screencopy` is the native,
direct path. Tiling-WM power users typically expect applications to support it.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| `wayland-client` | MIT | Wayland client library |
| `wlr-screencopy-unstable-v1.xml` | MIT (wlroots project) | Protocol definition |
| `wayland-scanner` | MIT | Code generator |
| Our CGo binding | MIT | We own this code |

Fully permissive. No GPL/LGPL contamination. wlroots project is BSD-friendly throughout.

---

## Compositor Compatibility

| Compositor | wlr-screencopy support |
|-----------|----------------------|
| **Sway** | ✅ |
| **Hyprland** | ✅ |
| **river** | ✅ |
| **dwl** | ✅ |
| **labwc** | ✅ |
| **Wayfire** | ✅ |
| **Cosmic (System76)** | ✅ |
| **niri** | ✅ |
| GNOME Mutter | ❌ — use PipeWire portal (default binary) |
| KDE KWin | ❌ — use PipeWire portal (default binary) |
| Weston | ⚠️ partial — check version |

**Probe at startup:** check the Wayland registry for the `zwlr_screencopy_manager_v1`
global. If present, this capture path works. If absent, fall back to the next backend.

---

## Build & Distribution

### Go build tag

```bash
go build -tags wlr_screencopy -o viewport-rds-linux-wlr-capture ./cmd/server
```

### Runtime dependencies

- Wayland session (`WAYLAND_DISPLAY` set)
- wlroots-based compositor running
- `libwayland-client.so` (universally available on Linux)

No driver requirement — works on Intel, AMD, NVIDIA (open-source driver). The protocol
is compositor-side, not GPU-side.

### CGo / protocol generation

```bash
# Generate Go bindings from the Wayland protocol XML at build time
wayland-scanner client-header \
    /usr/share/wayland-protocols/unstable/wlr-screencopy/wlr-screencopy-unstable-v1.xml \
    internal/capture/wlroots/wlr-screencopy-client-protocol.h

wayland-scanner private-code \
    /usr/share/wayland-protocols/unstable/wlr-screencopy/wlr-screencopy-unstable-v1.xml \
    internal/capture/wlroots/wlr-screencopy-protocol.c
```

```go
/*
#cgo CFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -lwayland-client

#include <wayland-client.h>
#include "wlr-screencopy-client-protocol.h"
*/
import "C"
```

---

## CGo Implementation Sketch

```c
// 1. Connect to Wayland display
struct wl_display *display = wl_display_connect(NULL);

// 2. Get registry, bind wl_output and zwlr_screencopy_manager_v1
struct wl_registry *registry = wl_display_get_registry(display);
wl_registry_add_listener(registry, &registry_listener, NULL);
wl_display_roundtrip(display);
// → state has wl_output and zwlr_screencopy_manager_v1 bound

// 3. Per-frame capture request
struct zwlr_screencopy_frame_v1 *frame =
    zwlr_screencopy_manager_v1_capture_output(manager, 0 /* overlay_cursor */, output);
zwlr_screencopy_frame_v1_add_listener(frame, &frame_listener, &state);

// 4. Listener callbacks
static void on_buffer(void *data, struct zwlr_screencopy_frame_v1 *frame,
                      uint32_t format, uint32_t width, uint32_t height, uint32_t stride) {
    // Allocate a DMA-BUF or shm buffer matching format/dimensions
    // For zero-copy to encoder: use linux_dmabuf_v1 protocol to get a DMA-BUF
}

static void on_linux_dmabuf(void *data, struct zwlr_screencopy_frame_v1 *frame,
                            uint32_t format, uint32_t width, uint32_t height) {
    // Compositor offers a DMA-BUF — preferred path for zero-copy
    // Allocate via zwp_linux_buffer_params_v1 → wl_buffer
    zwlr_screencopy_frame_v1_copy(frame, dmabuf_buffer);
}

static void on_ready(void *data, struct zwlr_screencopy_frame_v1 *frame,
                     uint32_t tv_sec_hi, uint32_t tv_sec_lo, uint32_t tv_nsec) {
    // Frame is ready in the buffer
    // For DMA-BUF: extract the fd, hand to VA-API / NVENC / Vulkan Video
    // For shm: BGRA pixels are in the mapped shm region
    state->frame_ready = true;
    state->capture_timestamp_ns = (uint64_t)tv_sec_lo * 1000000000ULL + tv_nsec;
}

// 5. Cleanup
zwlr_screencopy_frame_v1_destroy(frame);
```

---

## Output Buffer Types

wlr-screencopy supports two buffer types:

| Buffer type | Path | Use case |
|------------|------|----------|
| **`linux-dmabuf`** | **DMA-BUF fd from compositor** | **Preferred — zero-copy to VA-API / NVENC / Vulkan Video** |
| `wl_shm` | Shared memory BGRA | Fallback — CPU readback, slower |

The DMA-BUF path requires the `zwp_linux_dmabuf_v1` protocol (also wlroots-native).
If the compositor doesn't advertise it, fall back to `wl_shm`.

---

## Performance Targets

| Compositor + GPU | 1080p capture p50 | 1440p capture p50 |
|-----------------|------------------|------------------|
| Sway + AMD RDNA2 | <2ms | <3ms |
| Hyprland + NVIDIA | <2ms | <3ms |
| labwc + Intel UHD | <2ms | <3ms |

Roughly equivalent to KMS+EGL (the default with root) without needing root. Lower
latency than PipeWire portal on wlroots compositors by ~5–8ms.

---

## Probe & Selection

```go
//go:build wlr_screencopy

func ProbeWlrScreencopy() (*WlrScreencopyCapabilities, error) {
    // 1. wl_display_connect(NULL) — Wayland session check
    // 2. wl_registry → look for zwlr_screencopy_manager_v1 global
    // 3. wl_registry → look for zwp_linux_dmabuf_v1 (for zero-copy)
    // 4. Enumerate wl_outputs, return resolution per output
    // 5. Return capabilities or ErrNotWlrootsCompositor
}
```

Pipeline probes capture (Linux):
```
NvFBC available?            → use NvFBC (NVIDIA-specific)
wlr-screencopy + wlroots?   → use this add-on (Sway/Hyprland power users)
KMS+EGL with root?          → use KMS+EGL (default, universal)
PipeWire portal?            → use PipeWire (default, GNOME/KDE)
X11grab?                    → use X11grab (default, X11)
```

---

## File Structure

```
internal/capture/wlroots/
├── wlroots.go                          // Capturer struct
├── wlroots_cgo.go                      // CGo binding (build tag: wlr_screencopy)
├── wlroots_stub.go                     // No-op stub (build tag: !wlr_screencopy)
├── probe.go                            // ProbeWlrScreencopy()
├── dmabuf_export.go                    // Pass DMA-BUF fd to encoder
├── wlr-screencopy-client-protocol.h    // Generated by wayland-scanner
├── wlr-screencopy-protocol.c           // Generated
└── wlroots_test.go                     // Integration tests
```

---

## When to use this add-on

Use this add-on when:
- Deployment targets users on Sway, Hyprland, river, dwl, labwc, or other wlroots WMs
- Don't want to require root for KMS+EGL access
- Want a Wayland-native path with lower overhead than PipeWire portal
- Tiling-WM power-user community is a primary audience

Stick with default binary capture when:
- GNOME or KDE Wayland (use PipeWire portal — already included)
- X11 (use X11grab — already included)
- Root available (use KMS+EGL — already included, universal across all WMs)

---

## Status

📋 Specced — not yet built. Implementation priority: medium. Sway and Hyprland have
non-trivial user bases in the Linux gaming/streaming community, making this a
worthwhile add-on for capture parity with Sunshine.
