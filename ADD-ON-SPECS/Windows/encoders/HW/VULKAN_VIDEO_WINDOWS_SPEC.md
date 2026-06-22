# Windows HW Encoder Add-On: Vulkan Video

## Purpose

Cross-vendor hardware encoding via the **Vulkan Video extensions**
(`VK_KHR_video_encode_h264`, `VK_KHR_video_encode_h265`). Same code as the Linux
Vulkan Video add-on — the Vulkan API is identical across operating systems.

This is the long-term winning bet for cross-vendor Windows encoding. It works on
NVIDIA, AMD, and Intel GPUs through their respective Vulkan drivers — one binary,
one CGo binding, all three vendors covered.

---

## Why Vulkan Video matters on Windows

Windows historically had no equivalent of VA-API. The fragmentation cost is real:
write three CGo bindings (NVENC, AMF, QSV) or pick MediaFoundation and accept its
overhead. Vulkan Video changes this:

| | MF HW | NVENC | AMF | QSV | **Vulkan Video** |
|-|-------|-------|-----|-----|------------------|
| Cross-vendor in one binary | ✅ via MFT routing | ❌ | ❌ | ❌ | ✅ **first-party from all 3** |
| Vendor-specific tuning | partial | full | full | full | **standard Khronos params** |
| Latency floor | ~5–8ms | ~1–3ms | ~3–5ms | ~3–5ms | ~2–5ms (NVIDIA mature) |
| Driver maturity (2026) | high | high | high | high | medium (varies by vendor) |
| Royalty status | MS pays | NVIDIA pays | AMD pays | Intel pays | Khronos: standard, free |

Vulkan Video's main current weakness is **uneven driver maturity** — NVIDIA is
production-ready on Windows, AMD and Intel are catching up.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Vulkan SDK (LunarG) | Apache 2.0 | Headers, loader, validation layers |
| `vulkan-1.dll` (loader) | Apache 2.0 | Ships with Windows or vendor driver |
| Vendor driver implementations | proprietary | Ships with the GPU driver |
| Our CGo binding | MIT | Same code as Linux Vulkan Video add-on |

Fully permissive across the stack.

---

## Hardware & Driver Compatibility on Windows

| Vendor | Driver | H.264 encode | HEVC encode | AV1 encode |
|--------|--------|-------------|-------------|-----------|
| NVIDIA | 525+ | ✅ stable | ✅ stable | ✅ Ada+ |
| AMD | Adrenalin 23.x+ | ✅ stable | 🔧 maturing | ❌ planned |
| Intel | 31.0.x+ (Arc) | ✅ stable | 🔧 partial | 🔧 in progress |
| Intel UHD (older) | varies | ⚠️ limited | ❌ | ❌ |

Runtime probe: check Vulkan instance for `VK_KHR_video_encode_queue` and the
per-codec extensions. Fall through to MF HW if Vulkan Video is unavailable or
the vendor driver reports immaturity.

---

## Build & Distribution

```bash
go build -tags vulkan_video -o viewport-rds-windows-vulkan.exe ./cmd/server
```

CGo config:

```go
/*
#cgo CFLAGS: -I${SRCDIR}/vulkan/include
#cgo LDFLAGS: -lvulkan-1

#define VK_USE_PLATFORM_WIN32_KHR
#include <vulkan/vulkan.h>
#include <vulkan/vulkan_video_codec_h264std.h>
#include <vulkan/vulkan_video_codec_h264std_encode.h>
#include <vulkan/vulkan_video_codec_h265std.h>
#include <vulkan/vulkan_video_codec_h265std_encode.h>
*/
import "C"
```

Vulkan SDK headers vendored under `internal/encode/vulkan/vulkan/`. Loader DLL
(`vulkan-1.dll`) ships with Windows and every modern GPU driver.

---

## Implementation

Same Vulkan Video pipeline as the Linux Vulkan Video add-on:
- `vkCreateInstance` + Vulkan 1.3 device with video encode queue family
- `vkCreateVideoSessionKHR` for H.264 or H.265
- Per-frame: `vkCmdBeginVideoCodingKHR` → `vkCmdEncodeVideoKHR` → `vkCmdEndVideoCodingKHR`
- Read encoded bytes from bitstream buffer

See [`../../../Linux/encoders/HW/VULKAN_VIDEO_LINUX_SPEC.md`](../../../Linux/encoders/HW/VULKAN_VIDEO_LINUX_SPEC.md)
for the full CGo sketch — the Windows code is byte-for-byte identical except for:
- `VK_USE_PLATFORM_WIN32_KHR` instead of `VK_USE_PLATFORM_XLIB_KHR`
- DXGI texture import via `VK_KHR_external_memory_win32` instead of DMA-BUF import

### DXGI Desktop Duplication → Vulkan zero-copy

```c
// Import D3D11 texture into Vulkan via the Win32 external memory extension
VkImportMemoryWin32HandleInfoKHR importWin32 = {
    .sType = VK_STRUCTURE_TYPE_IMPORT_MEMORY_WIN32_HANDLE_INFO_KHR,
    .handleType = VK_EXTERNAL_MEMORY_HANDLE_TYPE_D3D11_TEXTURE_BIT,
    .handle = sharedTextureHandle,   // from IDXGIResource::GetSharedHandle
};
VkMemoryAllocateInfo allocInfo = {
    .pNext = &importWin32,
    // ...
};
VkDeviceMemory importedMem;
vkAllocateMemory(device, &allocInfo, NULL, &importedMem);
vkBindImageMemory(device, inputImage, importedMem, 0);
```

This is the Windows-side equivalent of Linux DMA-BUF import.

---

## Performance Targets

| GPU | H.264 1080p p50 | HEVC 1080p p50 | CPU at 60fps |
|-----|----------------|----------------|-------------|
| NVIDIA RTX 3060 (via Vulkan) | <2ms | <2ms | <1% |
| AMD RX 6700 (RADV/Adrenalin) | <4ms | maturing | <2% |
| Intel Arc A380 | <3ms | maturing | <2% |

Once drivers fully mature across all vendors, Vulkan Video matches vendor-native
APIs in latency and quality. Today (2026) NVIDIA is the safe bet; AMD/Intel are
catching up.

---

## File Structure

```
internal/encode/vulkan/
├── vulkan.go                   // shared with Linux Vulkan Video
├── vulkan_cgo_common.go        // shared CGo (Vulkan API is cross-platform)
├── vulkan_dmabuf_linux.go      // build tag: vulkan_video,linux
├── vulkan_d3d11_windows.go     // build tag: vulkan_video,windows
├── vulkan_stub.go              // build tag: !vulkan_video
├── probe.go
├── vulkan/                     // Vulkan SDK headers (Apache 2.0)
└── vulkan_test.go
```

The Go code is shared with the Linux Vulkan Video add-on; only the platform-specific
external memory import path differs (DMA-BUF on Linux, D3D11 texture handle on Windows).

---

## When to use this add-on

Use this add-on when:
- Want one binary for cross-vendor coverage (alternative to MF HW)
- NVIDIA-dominant deployment (NVIDIA Vulkan Video is the most mature)
- Forward-looking: betting on Vulkan Video as the future of cross-vendor encoding
- Don't want to ship per-vendor SDKs

Skip when:
- AMD or Intel-dominant deployment today (drivers still maturing in 2026)
- Production stability matters most — prefer MF HW or vendor-direct SDKs

---

## Status

📋 Specced — not yet implemented. Lower implementation priority than MF HW
(more universal coverage today) and NVENC/AMF/QSV (peak performance per vendor).
Promote to higher priority once AMD + Intel Vulkan Video drivers reach NVIDIA's
maturity level.
