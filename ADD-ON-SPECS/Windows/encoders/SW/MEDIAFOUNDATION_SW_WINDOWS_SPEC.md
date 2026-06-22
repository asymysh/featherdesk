# Windows SW Encoder Add-On: MediaFoundation Software

## Purpose

Software H.264 / HEVC encoder via Microsoft's MediaFoundation `h264_mf` / `hevc_mf`
encoder transforms, run in pure software mode (no D3D11 device manager attached).

Why this exists as a separate add-on from MediaFoundation HW: by deliberately
omitting the D3D11 device manager during MFT setup, the system selects the
**software MFT** rather than any registered hardware MFT. This is the
"Microsoft-native SW path" — useful on ARM Windows where OpenH264 NEON
performance varies, and as a guaranteed-available fallback on x86 Windows
when third-party DLLs aren't desired.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| MediaFoundation framework | Windows system framework | Built into the OS, no separate license |
| H.264 / HEVC patent royalties | **Microsoft pays** | Microsoft's Windows license covers system-shipped encoders |
| Our CGo binding | MIT | We own this code |

No DLL distribution concern (unlike OpenH264 which ships Cisco's DLL). No patent
royalty concern.

---

## Platform Compatibility

| Windows version | h264_mf SW | hevc_mf SW | Notes |
|----------------|-----------|-----------|-------|
| Windows 7 | ⚠️ | ❌ | h264_mf available but old API; not recommended |
| Windows 8.1 | ✅ | ❌ | HEVC SW added in Win 10 |
| Windows 10 1607+ | ✅ | ✅ | Full support |
| Windows 11 | ✅ | ✅ | Full support including HDR codepaths |
| Windows ARM (Snapdragon) | ✅ | ✅ | NEON-tuned by Microsoft for ARM |

---

## Build & Distribution

### Go build tag

```bash
go build -tags mf_sw -o viewport-rds-windows-mf-sw.exe ./cmd/server
```

### Runtime dependencies

None beyond the Windows SDK. Statically linked Windows APIs.

### CGo configuration

```go
/*
#cgo CFLAGS:
#cgo LDFLAGS: -lmf -lmfplat -lmfuuid -lwmcodecdspuuid -lole32 -loleaut32

#define COBJMACROS
#include <windows.h>
#include <mfapi.h>
#include <mfidl.h>
#include <codecapi.h>
#include <wmcodecdsp.h>
*/
import "C"
```

---

## CGo Implementation Sketch

```c
// Initialise MF (once at startup)
MFStartup(MF_VERSION, MFSTARTUP_FULL);

// Enumerate H.264 software encoders explicitly
IMFActivate **activates = NULL;
UINT32 numActivates = 0;
MFT_REGISTER_TYPE_INFO outInfo = { MFMediaType_Video, MFVideoFormat_H264 };
MFTEnumEx(
    MFT_CATEGORY_VIDEO_ENCODER,
    MFT_ENUM_FLAG_SYNCMFT | MFT_ENUM_FLAG_LOCALMFT,   // SW-only — exclude HARDWARE flag
    NULL,
    &outInfo,
    &activates,
    &numActivates
);

// Pick the first software-only MFT
IMFTransform *encoder = NULL;
activates[0]->lpVtbl->ActivateObject(activates[0], &IID_IMFTransform, (void**)&encoder);

// Set input media type (NV12)
IMFMediaType *inType;
MFCreateMediaType(&inType);
inType->lpVtbl->SetGUID(inType, &MF_MT_MAJOR_TYPE, &MFMediaType_Video);
inType->lpVtbl->SetGUID(inType, &MF_MT_SUBTYPE,    &MFVideoFormat_NV12);
MFSetAttributeSize(inType, &MF_MT_FRAME_SIZE, W, H);
MFSetAttributeRatio(inType, &MF_MT_FRAME_RATE, fps, 1);
encoder->lpVtbl->SetInputType(encoder, 0, inType, 0);

// Set output media type (H.264) with low-latency tuning
IMFMediaType *outType;
MFCreateMediaType(&outType);
outType->lpVtbl->SetGUID(outType, &MF_MT_MAJOR_TYPE, &MFMediaType_Video);
outType->lpVtbl->SetGUID(outType, &MF_MT_SUBTYPE,    &MFVideoFormat_H264);
outType->lpVtbl->SetUINT32(outType, &MF_MT_AVG_BITRATE, bitrate);
outType->lpVtbl->SetUINT32(outType, &MF_MT_MPEG2_PROFILE, eAVEncH264VProfile_Base);
MFSetAttributeSize(outType, &MF_MT_FRAME_SIZE, W, H);
MFSetAttributeRatio(outType, &MF_MT_FRAME_RATE, fps, 1);
encoder->lpVtbl->SetOutputType(encoder, 0, outType, 0);

// Configure for low-latency mode via ICodecAPI
ICodecAPI *codecAPI;
encoder->lpVtbl->QueryInterface(encoder, &IID_ICodecAPI, (void**)&codecAPI);
VARIANT v = { .vt = VT_UI4, .ulVal = eAVEncCommonRateControlMode_CBR };
codecAPI->lpVtbl->SetValue(codecAPI, &CODECAPI_AVEncCommonRateControlMode, &v);
v.ulVal = 1; codecAPI->lpVtbl->SetValue(codecAPI, &CODECAPI_AVLowLatencyMode, &v);

// Begin streaming
encoder->lpVtbl->ProcessMessage(encoder, MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);

// Per frame: ProcessInput → ProcessOutput cycle
```

For HEVC: replace `MFVideoFormat_H264` with `MFVideoFormat_HEVC` and the profile
constant accordingly.

---

## Performance Targets (Benchmarked)

From our benchmark session (Ryzen 9 5900X, Windows 11):

| Encoder | Resolution | FPS | p50 | Notes |
|---------|-----------|-----|-----|-------|
| MF SW (`h264_mf`, display_remoting scenario) | 1080p | 201 | ~5ms | Measured |
| MF SW (`h264_mf`, display_remoting scenario) | 1440p | 115 | ~8.7ms | Measured |
| Reference: OpenH264 (same machine, estimated) | 1080p | ~225 | ~4–8ms | Approximate |
| Reference: libx264 ultrafast | 1080p | 226 | 4.3ms | GPL-2 |

MF SW is competitive with OpenH264 on x86. On ARM Windows it's typically faster
than OpenH264's NEON build because Microsoft has tuned it specifically for
Snapdragon and other ARM SoCs.

---

## When to use this add-on

Use this add-on when:
- ARM Windows (Snapdragon Copilot+ PCs) — Microsoft's MF SW is NEON-tuned
- Want a SW path with no third-party DLL distribution (vs OpenH264's libopenh264.dll)
- Need a Windows-Built-In guaranteed-available encoder

Skip when:
- Cross-platform consistency matters more (use OpenH264 CGo, same code as Linux + macOS)
- Have a HW encoder add-on installed (use HW path instead)

---

## Why split from MF HW

This add-on **explicitly omits** the D3D11 device manager attachment. The HW
sibling spec adds the device manager to enable hardware MFT routing.

Same MediaFoundation API, different runtime configuration. We split them because:
1. Modularity — each add-on has a single clear purpose
2. ARM SW use case stands on its own merit and doesn't need D3D11 setup overhead
3. Build size — SW path can be a smaller binary without D3D11 interop code

See [`../HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md`](../HW/MEDIAFOUNDATION_HW_WINDOWS_SPEC.md)
for the sibling hardware path.

---

## File Structure

```
internal/encode/mediafoundation/
├── mediafoundation.go          // shared types
├── mf_sw_cgo.go                // build tag: mf_sw
├── mf_hw_cgo.go                // build tag: mf_hw  (sibling spec)
├── mf_stub.go                  // build tag: !mf_sw,!mf_hw
├── probe.go                    // ProbeMFSoftware() / ProbeMFHardware()
└── mf_test.go
```

---

## Status

📋 Specced — not yet implemented.
