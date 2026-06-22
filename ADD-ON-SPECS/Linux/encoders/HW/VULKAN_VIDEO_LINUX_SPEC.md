# Linux Add-On: Vulkan Video

## Purpose

Optional add-on binary using the **Vulkan Video extensions** (`VK_KHR_video_encode_h264`,
`VK_KHR_video_encode_h265`) for cross-vendor hardware encoding. This is the
emerging standard that could eventually replace VA-API + NVENC + AMF with a single
royalty-free interface that works on Intel, AMD, and NVIDIA GPUs.

**Status today (2026):** Vulkan Video encode extensions are ratified, NVIDIA driver
support is production-stable, Intel and AMD Mesa drivers are catching up. **Not yet
mature enough to be the primary path** for production streaming, but worth shipping
as an add-on for forward-looking users and for benchmarking.

| Vendor | Vulkan Video encode status | When primary-ready |
|--------|---------------------------|-------------------|
| NVIDIA | ✅ Production stable (driver 525+) | Now |
| AMD Mesa (RADV) | 🔧 H.264 stable, HEVC maturing | 2026–2027 |
| Intel Mesa (ANV) | 🔧 H.264 working, HEVC partial | 2026–2027 |

---

## Why Vulkan Video matters

| | VA-API | NVENC | AMF | **Vulkan Video** |
|-|--------|-------|-----|------------------|
| Cross-vendor (1 API) | ✅ but NVIDIA via wrapper | ❌ NVIDIA only | ❌ AMD only | ✅ **truly all 3** |
| First-party on all vendors | Intel/AMD only | NVIDIA only | AMD only | **all 3** |
| Royalty-free spec | ✅ (Khronos) | ✅ (NVIDIA) | ✅ (AMD) | ✅ (Khronos) |
| Surface model | DMA-BUF | CUDA / D3D11 | Vulkan / D3D11 | **VkImage native** |
| Zero-copy from compositor | DMA-BUF import | platform-specific | platform-specific | **native VkImage** |
| Cross-platform | Linux only | Linux + Windows | Windows + (limited) Linux | **Linux + Windows + Android** |
| Maturity (2026) | high | high | high | medium |

Vulkan Video is the long-term winning bet. We ship it as an add-on now so when driver
maturity catches up, we just promote it to the default — no architectural changes
needed.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Vulkan SDK (LunarG) | Apache 2.0 | Headers, validation layers |
| `libvulkan` (loader) | Apache 2.0 | Driver-shipped on all distros |
| Vendor driver implementations | various proprietary | Ships with NVIDIA / Mesa / Intel drivers |
| Our CGo binding | MIT | We own this code |

Fully permissive. Vulkan is Khronos-governed and free of royalty obligations.

---

## Hardware & Driver Requirements

### Required Vulkan extensions

- `VK_KHR_video_queue` (core to all video operations)
- `VK_KHR_video_encode_queue`
- `VK_KHR_video_encode_h264` and/or `VK_KHR_video_encode_h265`
- `VK_KHR_synchronization2`
- `VK_KHR_external_memory_fd` (for DMA-BUF import from capture)

### Vendor support matrix

| GPU | Driver | H.264 encode | HEVC encode | AV1 encode |
|-----|--------|-------------|-------------|-----------|
| NVIDIA Pascal+ | 525+ proprietary | ✅ stable | ✅ stable | ✅ Ada+ |
| AMD RDNA1+ | Mesa 24.0+ (RADV) | ✅ stable | 🔧 maturing | ❌ planned |
| AMD Vega/Polaris | Mesa 24.0+ (RADV) | ⚠️ partial | ❌ | ❌ |
| Intel Skylake+ | Mesa 24.0+ (ANV) | ✅ stable | 🔧 partial | ❌ |
| Intel Arc | Mesa 24.0+ (ANV) | ✅ stable | 🔧 partial | 🔧 in progress |

---

## Build & Distribution

### Go build tag

```bash
go build -tags vulkan_video -o viewport-rds-linux-vulkan ./cmd/server
```

### Runtime dependencies

- Vulkan loader (`libvulkan.so.1`) — shipped on all Linux distros
- Any vendor's Vulkan driver:
  - NVIDIA proprietary 525+
  - Mesa 24.0+ for AMD RADV
  - Mesa 24.0+ for Intel ANV

### CGo configuration

```go
/*
#cgo CFLAGS: -I/usr/include/vulkan
#cgo LDFLAGS: -lvulkan

#define VK_USE_PLATFORM_XLIB_KHR
#include <vulkan/vulkan.h>
#include <vulkan/vulkan_video_codec_h264std.h>
#include <vulkan/vulkan_video_codec_h264std_encode.h>
#include <vulkan/vulkan_video_codec_h265std.h>
#include <vulkan/vulkan_video_codec_h265std_encode.h>
*/
import "C"
```

---

## CGo Implementation Sketch

```c
// 1. Vulkan instance + device with video queues
VkApplicationInfo appInfo = { .apiVersion = VK_API_VERSION_1_3 };
VkInstance instance;
vkCreateInstance(&createInfo, NULL, &instance);

// Pick physical device with video encode queue family
VkPhysicalDevice physical;
uint32_t encodeQueueFamily = findVideoEncodeQueueFamily(physical);

const char* extensions[] = {
    VK_KHR_VIDEO_QUEUE_EXTENSION_NAME,
    VK_KHR_VIDEO_ENCODE_QUEUE_EXTENSION_NAME,
    VK_KHR_VIDEO_ENCODE_H264_EXTENSION_NAME,
    VK_KHR_SYNCHRONIZATION_2_EXTENSION_NAME,
    VK_KHR_EXTERNAL_MEMORY_FD_EXTENSION_NAME,
};
VkDevice device;
vkCreateDevice(physical, &deviceCreateInfo, NULL, &device);

// 2. Video session for H.264 encode
VkVideoProfileInfoKHR profile = {
    .videoCodecOperation = VK_VIDEO_CODEC_OPERATION_ENCODE_H264_BIT_KHR,
    .chromaSubsampling = VK_VIDEO_CHROMA_SUBSAMPLING_420_BIT_KHR,
    .lumaBitDepth = VK_VIDEO_COMPONENT_BIT_DEPTH_8_BIT_KHR,
    .chromaBitDepth = VK_VIDEO_COMPONENT_BIT_DEPTH_8_BIT_KHR,
};
VkVideoSessionCreateInfoKHR sessionInfo = {
    .queueFamilyIndex = encodeQueueFamily,
    .pVideoProfile = &profile,
    .pictureFormat = VK_FORMAT_G8_B8R8_2PLANE_420_UNORM,  // NV12
    .maxCodedExtent = { w, h },
    .maxDpbSlots = 2,
    .maxActiveReferencePictures = 1,
};
VkVideoSessionKHR session;
vkCreateVideoSessionKHR(device, &sessionInfo, NULL, &session);

// 3. Per-frame: command buffer with video begin → encode → end
VkVideoBeginCodingInfoKHR beginInfo = { ... };
vkCmdBeginVideoCodingKHR(cmdBuf, &beginInfo);

VkVideoEncodeInfoKHR encodeInfo = {
    .pNext = &h264EncodeInfo,            // H.264 specific parameters
    .dstBuffer = bitstreamBuffer,
    .dstBufferOffset = 0,
    .dstBufferRange = bufferSize,
    .srcPictureResource = { .imageViewBinding = inputView },
};
vkCmdEncodeVideoKHR(cmdBuf, &encodeInfo);

vkCmdEndVideoCodingKHR(cmdBuf, &endInfo);
vkQueueSubmit(encodeQueue, ...);

// 4. Read encoded output from bitstream buffer
vkMapMemory(device, bitstreamMemory, 0, bufferSize, 0, &mapped);
// mapped → NAL units
```

### DMA-BUF import (zero-copy from KMS capture)

```c
VkImportMemoryFdInfoKHR importFd = {
    .handleType = VK_EXTERNAL_MEMORY_HANDLE_TYPE_DMA_BUF_BIT_EXT,
    .fd = dmaBufFd,
};
VkMemoryAllocateInfo allocInfo = {
    .pNext = &importFd,
    .allocationSize = fbSize,
    .memoryTypeIndex = compatibleMemoryType,
};
VkDeviceMemory importedMem;
vkAllocateMemory(device, &allocInfo, NULL, &importedMem);

// Bind to VkImage that becomes the encoder input
vkBindImageMemory(device, inputImage, importedMem, 0);
```

This is the cleanest DMA-BUF import story of any encode API — Vulkan's external memory
extension is purpose-built for it.

---

## Performance Targets

| GPU class | H.264 1080p p50 | HEVC 1080p p50 | CPU at 60fps |
|-----------|----------------|----------------|-------------|
| NVIDIA RTX 3060 | <2ms | <2ms | <1% |
| AMD RX 6700 (RADV) | <4ms (matures) | n/a yet | <2% |
| Intel Arc A380 (ANV) | <3ms | n/a yet | <2% |

Roughly equivalent to vendor-native APIs (NVENC / VA-API) once drivers fully mature.
The win is **one binary for all three vendors**, not raw speed.

---

## Probe & Selection

```go
//go:build vulkan_video

func ProbeVulkanVideo() (*VulkanVideoCapabilities, error) {
    // 1. vkCreateInstance + enumerate physical devices
    // 2. For each: check VK_KHR_video_encode_queue support
    // 3. Query VK_KHR_video_encode_h264 / _h265 capabilities
    // 4. Return per-codec max resolution and supported profiles
}
```

Pipeline probes:
```
NVENC?               → other add-on
AMF-ROCm?            → other add-on
Vulkan Video?        → use if H.264 encode supported AND driver mature
                       (vendor maturity check at startup)
VA-API (default)?    → use VA-API (the proven path)
OpenH264 (default)?  → SW fallback
```

---

## File Structure

```
internal/hwencode/vulkan/
├── vulkan.go             // Encoder struct, NewVulkanEncoder
├── vulkan_cgo.go         // CGo binding (build tag: vulkan_video)
├── vulkan_stub.go        // No-op stub (build tag: !vulkan_video)
├── probe.go              // ProbeVulkanVideo()
├── dmabuf_import.go      // DMA-BUF → VkImage import
└── vulkan_test.go        // Integration tests
```

---

## When to use this add-on

Use this add-on when:
- Wanting one binary that works on Intel + AMD + NVIDIA without three SDKs
- Forward-looking deployment betting on Vulkan Video as the future standard
- NVIDIA hardware specifically (most mature today)
- Cross-vendor consistency matters more than peak per-vendor performance

Stick with default VA-API binary when:
- Production deployment today where stability matters most
- AMD or Intel where Vulkan Video drivers are still maturing
- Want the smallest binary

---

## Why this is an add-on, not the default

Vulkan Video is the **right long-term answer** but driver maturity in 2026 is uneven:
- NVIDIA: production ready ✅
- AMD: H.264 stable, HEVC catching up 🔧
- Intel: H.264 working, HEVC partial 🔧

Promoting it to default would break HEVC on AMD/Intel for users who otherwise have
working VA-API HEVC today. By shipping as an add-on, we:
- Let NVIDIA users opt into a fully cross-vendor binary
- Keep AMD/Intel users on the proven Mesa VA-API path
- Have a clean upgrade path: when AMD/Intel Vulkan Video matures, promote to default
  and deprecate the per-vendor add-ons

---

## Status

📋 Specced — not yet built. Implementation priority: medium. Build after NVENC
(higher immediate value for NVIDIA users) and before AMF (lower demand than Mesa
VA-API on AMD).
