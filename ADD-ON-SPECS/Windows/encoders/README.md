# Windows Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The default Windows binary contains zero encoders. Each encoder is a separate
Go build-tagged add-on. Mix and match exactly what you need.

```
encoders/
├── SW/
│   ├── OPENH264_CGO_WINDOWS_SPEC.md          ← BSD-licensed Cisco SW (commercial use)
│   └── X264_SUBPROCESS_WINDOWS_SPEC.md       ← GPL-isolated x264 subprocess (home / OSS, 2× faster)
└── HW/
    ├── MEDIAFOUNDATION_HW_WINDOWS_SPEC.md    ← cross-vendor HW via MFT routing (NVIDIA + AMD + Intel + Qualcomm)
    ├── NVENC_WINDOWS_SPEC.md                 ← NVIDIA direct
    ├── AMF_WINDOWS_SPEC.md                   ← AMD direct (Apache 2.0)
    └── QSV_WINDOWS_SPEC.md                   ← Intel direct via oneVPL (covers Arc)
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Binary |
|-----------|-----------------------|--------|
| Generic Windows (any GPU, commercial) | `openh264` + `mf_hw` | `viewport-rds-windows-default` |
| Home / personal (any GPU, fastest SW) | `x264` + `mf_hw` | `viewport-rds-windows-home` |
| NVIDIA-only (low latency priority) | `openh264` + `nvenc` | `viewport-rds-windows-nvenc` |
| AMD-only (quality priority) | `openh264` + `amf` | `viewport-rds-windows-amf` |
| Intel-only (low power) | `openh264` + `qsv` | `viewport-rds-windows-qsv` |
| Maximum flexibility | `openh264,x264,mf_hw,nvenc,amf,qsv` | `viewport-rds-windows-full` |

The build tags compose; stack any combination:
```bash
go build -tags "openh264,mf_hw,nvenc" -o viewport-rds-windows-full ./cmd/server
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
| Bundled | ~1 MB DLL | Requires ffmpeg in PATH or bundled |

**For commercial deployment:** ship `openh264`. The BSD license and Cisco's
royalty arrangement keep the binary fully proprietary.

**For home / personal / OSS:** ship `x264`. It's 2× faster and the GPL
contamination is isolated to the ffmpeg subprocess (your main binary stays
under your chosen license).

See [`SW/OPENH264_CGO_WINDOWS_SPEC.md`](./SW/OPENH264_CGO_WINDOWS_SPEC.md)
and [`SW/X264_SUBPROCESS_WINDOWS_SPEC.md`](./SW/X264_SUBPROCESS_WINDOWS_SPEC.md).

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
4. MF HW (any vendor MFT registered)?        → use MF HW (cross-vendor default)
5. x264 available (ffmpeg in PATH)?          → use x264 subprocess (GPL builds only)
6. OpenH264 CGo?                             → universal SW fallback
7. None?                                     → fatal: no encoder add-on installed
```

For each available encoder, the pipeline then picks codec by preference:
```
HEVC HW → H.264 HW → H.264 SW
```

No software HEVC — libx265's triple patent pool exposure is rejected.

---

## Why no Vulkan Video on Windows

Vulkan Video encode was considered but rejected as of 2026:
- Driver support immature on AMD and Intel
- No measurable performance advantage over vendor-direct SDKs
- MediaFoundation HW already provides cross-vendor abstraction with mature drivers

Decision may be revisited when AMD/Intel driver support matures.

---

## Why no MediaFoundation SW

MediaFoundation's H.264 software MFT cannot be forced when any hardware MFT is
registered — Windows always picks HW when available. On any deployment machine
with a GPU, MF SW is unreachable. Use `openh264` or `x264` for the SW path.
