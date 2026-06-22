# Linux Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The Linux default binary contains **no encoders**. Every encoder is a separate
build-tagged add-on. Users compile in exactly the encoders they want.

```
ADD-ON-SPECS/Linux/encoders/
├── SW/
│   └── OPENH264_CGO_LINUX_SPEC.md    ← cross-platform SW H.264 (Cisco, BSD-2)
└── HW/
    ├── LIBVA_LINUX_SPEC.md            ← Intel + AMD + NVIDIA via VA-API (MIT)
    ├── NVENC_LINUX_SPEC.md            ← NVIDIA direct (REF_FRAMES_INVALIDATION)
    ├── AMF_ROCM_SPEC.md               ← AMD direct via ROCm (Apache 2.0)
    └── VULKAN_VIDEO_LINUX_SPEC.md     ← cross-vendor royalty-free, future-facing
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Binary |
|-----------|-----------------------|--------|
| Generic Linux server (any GPU) | `openh264` + `libva` | `viewport-rds-linux-default` |
| NVIDIA workstation (low latency priority) | `openh264` + `nvenc` | `viewport-rds-linux-nvenc` |
| AMD workstation (quality priority) | `openh264` + `libva` + `amf_rocm` | `viewport-rds-linux-amf` |
| Container / no GPU | `openh264` only | `viewport-rds-linux-cpu` |
| Forward-looking cross-vendor | `openh264` + `vulkan_video` | `viewport-rds-linux-vulkan` |

The build tags compose; users can stack any combination:
```bash
go build -tags "openh264,libva,nvenc,vulkan_video" -o viewport-rds-linux-full ./cmd/server
```

---

## Codec fallback order at runtime

When multiple encoders are compiled in, the pipeline probes in this order:

```
1. NVENC available (hardware + add-on)?    → use NVENC (lowest latency, REF_FRAMES_INVALIDATION)
2. AMF on ROCm available?                  → use AMF (AMD-specific tuning)
3. Vulkan Video mature on this GPU?        → use Vulkan Video (cross-vendor)
4. VA-API (libva) available?               → use VA-API (default HW path)
5. OpenH264 CGo?                           → SW fallback
6. None?                                   → fatal: no encoder add-on installed
```

For each available encoder, the pipeline then picks codec by preference:
```
HEVC HW → H.264 HW → H.264 SW
```

No software HEVC anywhere — libx265's triple patent pool exposure is rejected.

---

## Why no add-on for Intel on Linux

Intel Quick Sync (and Arc) on Linux is exposed through VA-API exclusively. There is
no separate Intel SDK on Linux — `iHD` and `i965` drivers register through libva.
The `libva` add-on covers Intel completely.

This is unlike Windows, where Intel uses oneVPL/QSV as its own SDK.
