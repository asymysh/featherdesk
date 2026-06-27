# Linux Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The Linux default binary contains **no encoders**. Every encoder is a separate
add-on shared library. Users drop in exactly the encoders they want.

```
specs/addons/linux/encoders/
├── SW/
│   ├── OPENH264_CGO_LINUX_SPEC.md     ← BSD-licensed Cisco SW (commercial use)
│   └── X264_SUBPROCESS_LINUX_SPEC.md  ← GPL-isolated x264 subprocess (home / OSS, 2× faster)
└── HW/
    ├── LIBVA_LINUX_SPEC.md            ← Intel + AMD + NVIDIA via VA-API (MIT)
    ├── NVENC_LINUX_SPEC.md            ← NVIDIA direct
    └── AMF_ROCM_SPEC.md               ← AMD direct via ROCm (Apache 2.0)
```

---

## Recommended combinations

| Deployment | Recommended add-on set |
|-----------|-----------------------|
| Generic Linux server (any GPU, commercial) | `openh264` + `libva` |
| Home / personal (any GPU, fastest SW) | `x264` + `libva` |
| NVIDIA workstation (low latency priority) | `openh264` + `nvenc` |
| AMD workstation (quality priority) | `openh264` + `libva` + `amf_rocm` |
| Container / no GPU (commercial) | `openh264` only |
| Container / no GPU (home, fastest) | `x264` only |

Each add-on is a standalone shared library; drop in any combination you want.
Build them one at a time, e.g.:
```bash
go build -buildmode=c-shared -o featherdesk-addon-openh264.so ./internal/encode/openh264
```

---

## SW encoder choice: OpenH264 vs x264

Both produce H.264 — same codec, different implementations, different licenses.

| Aspect | OpenH264 (BSD, Cisco) | x264 (GPL, subprocess) |
|--------|----------------------|------------------------|
| License | BSD-2-Clause | GPL-2.0+ (isolated via ffmpeg subprocess) |
| 1080p P50 @ 12T | 7.4ms | **3.3ms** (2.2× faster) |
| 1440p P50 @ 12T | 13.4ms | **5.8ms** (2.3× faster) |
| Integration | CGo in-process | ffmpeg subprocess + pipe |
| Royalties | Cisco pays MPEG-LA | None — patent expired in most regions |
| Deployment | Commercial-safe | Home / OSS / accept GPL on subprocess |

**For commercial deployment:** ship `openh264`. The BSD license and Cisco's
royalty arrangement keep the binary fully proprietary.

**For home / personal / OSS:** ship `x264`. It's 2× faster and the GPL
contamination is isolated to the ffmpeg subprocess (your main binary stays
under your chosen license).

See [`SW/OPENH264_CGO_LINUX_SPEC.md`](./SW/OPENH264_CGO_LINUX_SPEC.md)
and [`SW/X264_SUBPROCESS_LINUX_SPEC.md`](./SW/X264_SUBPROCESS_LINUX_SPEC.md).

---

## Codec fallback order at runtime

When multiple encoders are loaded, the pipeline probes in this order:

```
1. NVENC available (hardware + add-on)?    → use NVENC
2. AMF on ROCm available?                  → use AMF (AMD-specific tuning)
3. VA-API (libva) available?               → use VA-API (default HW path)
4. x264 available (ffmpeg in PATH)?        → use x264 subprocess (GPL builds only)
5. OpenH264 CGo?                           → universal SW fallback
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

---

## Why no Vulkan Video on Linux

Vulkan Video encode was considered but rejected as of 2026:
- Mesa driver support immature on AMD and Intel
- No measurable performance advantage over VA-API on Intel/AMD
- VA-API already provides cross-vendor abstraction on Linux

Decision may be revisited when Mesa support matures.
