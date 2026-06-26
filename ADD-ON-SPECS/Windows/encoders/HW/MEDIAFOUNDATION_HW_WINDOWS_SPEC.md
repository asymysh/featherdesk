# Windows HW Encoder Add-On: MediaFoundation Hardware

## Purpose

Hardware-accelerated H.264 / HEVC encoder via Microsoft's MediaFoundation. This is
**the closest thing to VA-API on Windows** — a single Microsoft-provided API that
transparently routes to whichever vendor hardware MFT (Media Foundation Transform)
is registered:

| Hardware | MFT registered by | Underlying encoder |
|----------|-------------------|--------------------|
| NVIDIA Kepler+ | NVIDIA driver | NVENC |
| AMD GCN+ | AMD driver | AMF |
| Intel Sandy Bridge+ | Intel driver | Quick Sync |
| Intel Arc | Intel driver | Quick Sync (with AV1) |
| Qualcomm Snapdragon (ARM) | Qualcomm driver | Snapdragon HW encoder |
| Microsoft Basic Display | Microsoft | falls back to software |

One CGo binding handles all of them — same way `libva` handles Intel + AMD + NVIDIA
on Linux. The vendor SDKs (NVENC, AMF, QSV) are still worth shipping as separate
add-ons for users who want vendor-specific low-latency features
(`REF_FRAMES_INVALIDATION`, AMF Pre-Analysis, QSV tuning), but for default cross-vendor
HW encoding, MF is enough.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| MediaFoundation framework | Windows system framework | Built into the OS |
| H.264 / HEVC patent royalties | **Microsoft pays** for system-shipped encoder; **vendors pay** for their HW MFTs | Both layers are licensed |
| Our CGo binding | MIT | We own this code |

No royalty concern. No SDK to ship. The vendor's driver brings the hardware path.

---

## Hardware Compatibility (HW MFT route)

| Vendor | H.264 HW MFT | HEVC HW MFT | AV1 HW MFT |
|--------|-------------|------------|-----------|
| NVIDIA Kepler+ (driver 320+) | ✅ | ✅ Maxwell 2+ | ✅ Ada Lovelace+ |
| AMD GCN+ (driver 16.x+) | ✅ | ✅ | ✅ RDNA3+ (RX 7000+) |
| Intel Sandy Bridge+ | ✅ | ✅ Skylake+ | ✅ Arc+ |
| Qualcomm Snapdragon | ✅ | ✅ | ❌ |

Runtime probe via `MFTEnumEx` with `MFT_ENUM_FLAG_HARDWARE` returns the available
hardware MFTs. If none, this add-on falls through to the next encoder (or fails
gracefully so the SW add-on takes over).

---

## Build & Distribution

### Go build tag

```bash
go build -tags mf_hw -o viewport-rds-windows-mf-hw.exe ./cmd/server
```

### Runtime dependencies

None beyond a vendor GPU driver. Windows 10+ assumed.

### CGo configuration

```go
/*
#cgo LDFLAGS: -lmf -lmfplat -lmfuuid -lwmcodecdspuuid -ld3d11 -ldxgi -lole32 -loleaut32

#define COBJMACROS
#include <windows.h>
#include <mfapi.h>
#include <mfidl.h>
#include <codecapi.h>
#include <wmcodecdsp.h>
#include <d3d11.h>
#include <dxgi1_2.h>
*/
import "C"
```

---

## CGo Implementation Sketch (HW path)

```c
// 1. Create D3D11 device (used for both DXGI Desktop Duplication capture and MF encode)
ID3D11Device *d3dDevice = NULL;
ID3D11DeviceContext *d3dCtx = NULL;
D3D11CreateDevice(NULL, D3D_DRIVER_TYPE_HARDWARE, NULL,
    D3D11_CREATE_DEVICE_VIDEO_SUPPORT | D3D11_CREATE_DEVICE_BGRA_SUPPORT,
    NULL, 0, D3D11_SDK_VERSION, &d3dDevice, NULL, &d3dCtx);

// 2. Wrap as IMFDXGIDeviceManager so MF can share the D3D11 device
UINT resetToken = 0;
IMFDXGIDeviceManager *deviceManager = NULL;
MFCreateDXGIDeviceManager(&resetToken, &deviceManager);
deviceManager->lpVtbl->ResetDevice(deviceManager, (IUnknown*)d3dDevice, resetToken);

// 3. Enumerate HARDWARE H.264 encoder MFTs
IMFActivate **activates = NULL;
UINT32 numActivates = 0;
MFT_REGISTER_TYPE_INFO outInfo = { MFMediaType_Video, MFVideoFormat_H264 };
MFTEnumEx(
    MFT_CATEGORY_VIDEO_ENCODER,
    MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_ASYNCMFT | MFT_ENUM_FLAG_SORTANDFILTER,
    NULL, &outInfo, &activates, &numActivates
);

// activates[0] is now (typically) the vendor HW MFT — NVENC / AMF / QSV / Qualcomm
IMFTransform *encoder = NULL;
activates[0]->lpVtbl->ActivateObject(activates[0], &IID_IMFTransform, (void**)&encoder);

// 4. Attach the D3D11 device manager so the MFT uses the GPU
encoder->lpVtbl->ProcessMessage(encoder, MFT_MESSAGE_SET_D3D_MANAGER, (ULONG_PTR)deviceManager);

// 5. Set input + output media types (same as SW spec, but with D3D11 surface as input)
// ... SetInputType / SetOutputType with codec config

// 6. Per frame:
//    DXGI Desktop Duplication produces an ID3D11Texture2D
//    Wrap as IMFSample via MFCreateVideoSampleFromSurface
//    encoder->ProcessInput(0, sample, 0);
//    encoder->ProcessOutput(...);
```

### Zero-copy DXGI Desktop Duplication → MF

DXGI Desktop Duplication returns `IDXGIResource` which can be queried for an
`ID3D11Texture2D`. That texture is passed to MF without any CPU copy:

```c
ID3D11Texture2D *captureTex = ...;  // from DXGI Desktop Duplication

// Wrap as DXGI surface and create IMFSample
IDXGISurface *surface;
captureTex->lpVtbl->QueryInterface(captureTex, &IID_IDXGISurface, (void**)&surface);

IMFSample *sample;
MFCreateVideoSampleFromSurface((IUnknown*)surface, &sample);
sample->lpVtbl->SetSampleTime(sample, ptsHundredsOfNanos);
sample->lpVtbl->SetSampleDuration(sample, durHundredsOfNanos);

encoder->lpVtbl->ProcessInput(encoder, 0, sample, 0);
```

This is the canonical zero-copy GPU-resident streaming pipeline on Windows.

---

## Performance Targets

| GPU | H.264 1080p p50 | HEVC 1080p p50 | CPU at 60fps |
|-----|----------------|----------------|-------------|
| NVIDIA RTX 3060 (NVENC via MF) | ~3–5ms | ~3–5ms | <2% |
| AMD RX 6700 (AMF via MF) | ~4–6ms | ~4–6ms | <2% |
| Intel Arc A380 (QSV via MF) | ~3–5ms | ~3–5ms | <2% |
| Intel UHD 630 (QSV via MF) | ~5–8ms | ~5–8ms | <3% |
| Qualcomm Snapdragon X | ~5–8ms | ~5–8ms | <3% |

MF adds ~2–4ms overhead vs direct vendor SDK access. For remote desktop the budget
absorbs this easily; for sub-frame gaming-grade streaming, the vendor add-ons (NVENC,
AMF, QSV direct) are still worth installing.

---

## When to use this add-on

Use this add-on when:
- Want one binary that gets hardware encoding on any Windows GPU
- Targeting heterogeneous fleets (Intel + AMD + NVIDIA mixed)
- Targeting ARM Windows (Snapdragon) — MF is the only HW path there
- Don't want to bundle vendor SDKs (NVENC, AMF, QSV each add ~30–50MB)

Skip / prefer vendor add-ons when:
- Targeting NVIDIA-only deployments where `REF_FRAMES_INVALIDATION` matters (NVENC)
- Targeting AMD-only deployments where Pre-Analysis quality matters (AMF)
- Need sub-3ms p50 latency consistently

---

## File Structure

```
internal/encode/mediafoundation/
├── mediafoundation.go
├── mf_hw_cgo.go             // build tag: mf_hw
├── d3d11_interop.go         // DXGI texture → IMFSample wrapping
├── probe.go
└── mf_test.go
```

---

## Status

📋 Specced — not yet implemented. Highest-priority Windows HW add-on (covers most
hardware out of the box).
