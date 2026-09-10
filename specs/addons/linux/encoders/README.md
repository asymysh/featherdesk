# Linux Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The Linux default binary contains **no encoders**. Every encoder is a separate
add-on shared library. Users drop in exactly the encoders they want.

```
specs/addons/linux/encoders/
├── SW/
│   ├── OPENH264_LINUX_SPEC.md     ← BSD-licensed Cisco SW (the software default)
│   └── X264_SUBPROCESS_LINUX_SPEC.md  ← GPL-isolated x264 subprocess (opt-in, 2× faster)
└── HW/
    ├── LIBVA_LINUX_SPEC.md            ← Intel + AMD + NVIDIA via VA-API (MIT)
    ├── NVENC_LINUX_SPEC.md            ← NVIDIA direct
    └── AMF_ROCM_SPEC.md               ← AMD direct via ROCm (Apache 2.0)
```

---

## Recommended combinations

| Deployment | Recommended add-on set |
|-----------|-----------------------|
| Generic Linux server (any GPU) | `openh264` + `libva` |
| Measured CPU-bound host, GPL acceptable | `openh264` + `x264` + `libva`, with `[encode] force_addon = "x264"` |
| NVIDIA workstation (low latency priority) | `openh264` + `nvenc` |
| AMD workstation (quality priority) | `openh264` + `libva` + `amf_rocm` |
| Container / no GPU | `openh264` only |

Ship `openh264` in every row that can fall back to software: it is the SW default,
and a host with no working HW encoder and no `openh264` has no encoder at all.
`x264` is an addition to that set, never a replacement for it — forcing it while
it is unavailable is a startup failure with nothing left to fall through to.

Each add-on is a standalone shared library; drop in any combination you want.
Build them one at a time, e.g.:
```bash
cargo build --release -p featherdesk-addon-openh264   # cdylib → featherdesk-addon-openh264.so
```

---

## SW encoder choice: OpenH264 vs x264

Both produce H.264 — same codec, different implementations, different licenses.

| Aspect | OpenH264 (BSD, Cisco) | x264 (GPL, subprocess) |
|--------|----------------------|------------------------|
| License | BSD-2-Clause | GPL-2.0+ (isolated via ffmpeg subprocess) |
| 1080p P50 @ 12T | 7.4ms | **3.3ms** (2.2× faster) |
| 1440p P50 @ 12T | 13.4ms | **5.8ms** (2.3× faster) |
| Integration | Rust FFI in-process | ffmpeg subprocess + pipe |
| Forced IDR | one flag on the next `encode` call | kill + respawn the child (~100 ms, a few dropped frames) |
| `ENC_CONFIGURABLE` | yes — `SetOption` retunes in place | no — every parameter change respawns the child |
| Profile | Constrained Baseline only | High / High 4:2:2 / High 4:4:4 |
| Royalties | Cisco pays MPEG-LA | None — patent expired in most regions |
| Selection | the SW default, in the auto order | opt-in via `[encode] force_addon = "x264"` |

**`openh264` is the software default** on every OS: in-process, BSD-licensed with
Cisco carrying the MPEG-LA royalty, no external binary to find or lose, and — the
property that decides it — it forces an IDR in place for the cost of one flag on
the next frame.

**`x264` is ~2x faster on a CPU-bound host and is worth forcing where that has
been MEASURED**; it is not the default, because its only IDR mechanism is
respawning the ffmpeg child (see
[`MODULE_ENCODE.md`](../../../media/MODULE_ENCODE.md) "Software encoder order").
The GPL contamination is isolated to that subprocess, so the main binary stays
under your chosen license either way.

See [`SW/OPENH264_LINUX_SPEC.md`](./SW/OPENH264_LINUX_SPEC.md)
and [`SW/X264_SUBPROCESS_LINUX_SPEC.md`](./SW/X264_SUBPROCESS_LINUX_SPEC.md).

---

## Codec fallback order at runtime

When multiple encoders are loaded, the pipeline probes in this order:

```
1. NVENC available (hardware + add-on)?    → use NVENC
2. AMF on ROCm available?                  → use AMF (AMD-specific tuning)
3. VA-API (libva) available?               → use VA-API (default HW path)
4. OpenH264 (Rust FFI)?                     → universal SW default
5. None?                                   → fatal: no encoder add-on installed
```

`x264` is **not** in this ladder. It is opt-in: reached only by
`[encode] force_addon = "x264"`, which overrides the SW order entirely.

The encoder advertises **H.264** (`avc1.*`) for every SDR session, on every
platform, regardless of what HEVC hardware is present. **HEVC Main10**
(`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's
WebCodecs cannot decode it, and a codec no attached client can decode is a
black screen, not a saving. Which HW encoder is *selected* is a separate
question from which codec it *emits* — the probe order picks the add-on, this
rule picks the codec.

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
