# Windows Encoder Add-Ons

## ⏸️ Pending Architecture Discussion

The Windows encoder lineup is **not yet finalized**. Windows has the most fragmented
vendor encoder API landscape of any platform — there is no equivalent to Linux's VA-API
or macOS's VideoToolbox that uniformly covers all GPU vendors:

| Vendor | Windows API | Notes |
|--------|------------|-------|
| NVIDIA | NVENC (NVIDIA Video Codec SDK) | Same SDK as Linux NVENC add-on |
| AMD | AMF (Advanced Media Framework) | First-class on Windows, mature |
| Intel | QSV via oneVPL / Intel Media SDK | MIT licensed, modern path |
| Any | MediaFoundation (`h264_mf` etc.) | Windows-native, also used for ARM (Qualcomm) software fallback |
| Any | DXGI + custom D3D11 → Vulkan Video | Cross-vendor future-facing |

Unlike Linux, where VA-API is a clean default and add-ons cover edge cases, **on Windows
every major encoder is potentially its own add-on**. The pluggability strategy needs to
be decided before writing specs.

## Open questions to resolve before specs are written

1. **What is the "default binary" encoder on Windows?**
   - There is no Windows equivalent of VA-API that covers Intel + AMD + NVIDIA.
   - Options: ship MediaFoundation as universal default + every vendor as add-on, or
     ship all three vendor SDKs in the default binary and pick at runtime.

2. **Software fallback?**
   - OpenH264 CGo works (same as Linux/macOS), already benchmarked.
   - MediaFoundation `h264_mf` is Windows-native but ~10% slower per our benchmark.
   - Decision: stick with OpenH264 CGo cross-platform, or use MF on Windows?

3. **D3D11 zero-copy interop**
   - All three vendor APIs accept D3D11 textures as input — zero-copy from DXGI
     Desktop Duplication is achievable with all of them.
   - Should the D3D11 interop layer be shared or duplicated per add-on?

4. **Same binary or separate binaries per vendor?**
   - NVIDIA SDK adds ~50MB to the binary.
   - AMF SDK is smaller.
   - oneVPL is small.
   - All three could compile into one binary, unlike Linux where add-ons are split.

## Until that discussion happens

This folder is intentionally **empty** to maintain the symmetric directory structure:

```
ADD-ON-SPECS/
├── Linux/encoders/       ← 3 add-on specs (NVENC, AMF, Vulkan)
├── macOS/encoders/       ← README only (no add-ons needed — VideoToolbox is everything)
└── Windows/encoders/     ← THIS README (pending architecture discussion)
```

## Where Windows is currently partially specced

**📄 [`../WINDOWS_SPEC.md`](../WINDOWS_SPEC.md)** — covers:
- DXGI Desktop Duplication capture (primary)
- WGC fallback for cases DXGI can't handle
- GDI BitBlt benchmark results from this development session
- Audio (WASAPI loopback)
- Input (SendInput, ViGEmBus)

The capture, audio, and input sections are stable. **Only the encoder section is
pending the pluggability discussion.**

## When ready to spec

Once the architecture is decided, follow the Linux add-on pattern:

1. Write each encoder spec at `ADD-ON-SPECS/Windows/encoders/{NAME}_WINDOWS_SPEC.md`
2. Add rows to the Windows table in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
3. Implement under `internal/hwencode/{name}/` with a Go build tag
4. Wire the runtime probe order in `MODULE_PIPELINE.md`

Likely add-on candidates (in order of probable priority):
- `NVENC_WINDOWS_SPEC.md` — NVIDIA direct (same SDK as Linux NVENC, different surface model: D3D11 instead of CUDA)
- `AMF_WINDOWS_SPEC.md` — AMD Windows AMF (mature, Apache 2.0 SDK)
- `QSV_WINDOWS_SPEC.md` — Intel oneVPL (MIT, modern Intel path)
- `MEDIAFOUNDATION_SPEC.md` — `h264_mf` for ARM/Qualcomm fallback
