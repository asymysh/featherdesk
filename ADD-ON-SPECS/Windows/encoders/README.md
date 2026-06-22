# Windows Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The Windows default binary contains **no encoders**. Every encoder is a separate
build-tagged add-on. Users compile in exactly the encoders they want.

```
ADD-ON-SPECS/Windows/encoders/
├── SW/
│   ├── OPENH264_CGO_WINDOWS_SPEC.md          ← cross-platform SW (same code as Linux + macOS)
│   └── MEDIAFOUNDATION_SW_WINDOWS_SPEC.md    ← Windows-native SW (no third-party DLLs)
└── HW/
    ├── MEDIAFOUNDATION_HW_WINDOWS_SPEC.md    ← cross-vendor HW via MFT routing (NVIDIA + AMD + Intel + Qualcomm)
    ├── NVENC_WINDOWS_SPEC.md                 ← NVIDIA direct (REF_FRAMES_INVALIDATION)
    ├── AMF_WINDOWS_SPEC.md                   ← AMD direct (Pre-Analysis, Apache 2.0)
    ├── QSV_WINDOWS_SPEC.md                   ← Intel direct via oneVPL (covers Arc)
    └── VULKAN_VIDEO_WINDOWS_SPEC.md          ← cross-vendor royalty-free, future-facing
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Binary |
|-----------|-----------------------|--------|
| Generic Windows (any GPU) | `openh264` + `mf_hw` | `viewport-rds-windows-default` |
| ARM Snapdragon (Copilot+ PC) | `mf_sw` + `mf_hw` | `viewport-rds-windows-arm64` |
| NVIDIA-only (low latency priority) | `openh264` + `nvenc` | `viewport-rds-windows-nvenc` |
| AMD-only (quality priority) | `openh264` + `amf` | `viewport-rds-windows-amf` |
| Intel-only (low power) | `openh264` + `qsv` | `viewport-rds-windows-qsv` |
| Cross-vendor + forward-looking | `openh264` + `vulkan_video` | `viewport-rds-windows-vulkan` |

The build tags compose; stack any combination:
```bash
go build -tags "openh264,mf_hw,nvenc" -o viewport-rds-windows-full ./cmd/server
```

---

## MediaFoundation HW is the recommended cross-vendor default

Unlike Linux (where VA-API is the universal HW abstraction), Windows historically
fragmented per-vendor. **MediaFoundation HW** is the closest equivalent — a single
Microsoft API that routes to vendor MFTs:

| Vendor | MF HW routes to |
|--------|----------------|
| NVIDIA Kepler+ | NVENC (via NVIDIA's MFT) |
| AMD GCN+ | AMF (via AMD's MFT) |
| Intel Sandy Bridge+ | Quick Sync (via Intel's MFT) |
| Qualcomm Snapdragon | Qualcomm HW encoder |

One CGo binding (`mf_hw`), one binary, all vendors covered. The trade-off is
~2–4ms higher latency than vendor-direct SDKs (NVENC / AMF / QSV) and lack of
vendor-specific features (REF_FRAMES_INVALIDATION, AMF PA).

**For standard remote-desktop deployment, ship `mf_hw` and skip the per-vendor SDKs.**
For peak performance / vendor-specific features, ship the vendor SDK as an additional
add-on.

---

## Codec fallback order at runtime

When multiple encoders are compiled in, the pipeline probes in this order:

```
1. NVENC available (hardware + add-on)?      → use NVENC
2. AMF available?                            → use AMF
3. QSV available?                            → use QSV
4. Vulkan Video mature on this GPU?          → use Vulkan Video
5. MF HW (any vendor MFT registered)?        → use MF HW (cross-vendor default)
6. MF SW?                                    → use MF SW (Windows-built-in fallback)
7. OpenH264 CGo?                             → cross-platform SW fallback
8. None?                                     → fatal: no encoder add-on installed
```

For each available encoder, the pipeline then picks codec by preference:
```
HEVC HW → H.264 HW → H.264 SW
```

No software HEVC — libx265's triple patent pool exposure is rejected.

---

## Why split MF into SW and HW add-ons

MediaFoundation supports both software and hardware modes through the same
`IMFTransform` interface. We split them into separate add-ons for **modularity**:

- `mf_sw` build tag: enables `MFTEnumEx` with `MFT_ENUM_FLAG_LOCALMFT` (SW only),
  no D3D11 setup, smaller binary
- `mf_hw` build tag: enables `MFTEnumEx` with `MFT_ENUM_FLAG_HARDWARE`, requires
  D3D11 device manager, brings in DXGI interop

Each add-on does one thing. Users opt into what they need.
