# Windows Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The default Windows binary contains zero encoders. Each encoder is a separate
add-on shared library. Mix and match exactly what you need by dropping the
libraries you want into the add-ons directory.

```
encoders/
├── SW/
│   ├── OPENH264_WINDOWS_SPEC.md          ← BSD-licensed Cisco SW (commercial use)
│   └── X264_SUBPROCESS_WINDOWS_SPEC.md       ← GPL-isolated x264 subprocess (opt-in, force_addon only)
└── HW/
    ├── MEDIAFOUNDATION_HW_WINDOWS_SPEC.md    ← cross-vendor HW via MFT routing (NVIDIA + AMD + Intel + Qualcomm)
    ├── NVENC_WINDOWS_SPEC.md                 ← NVIDIA direct
    ├── AMF_WINDOWS_SPEC.md                   ← AMD direct (Apache 2.0)
    └── QSV_WINDOWS_SPEC.md                   ← Intel direct via oneVPL (covers Arc)
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Add-on libraries to drop in |
|-----------|-----------------------|------------------------------|
| Generic Windows (any GPU, any licence) | `openh264` + `mf_hw` | `featherdesk-addon-{openh264,mf_hw}.dll` |
| Measured CPU-bound host (x264 opt-in) | `x264` + `mf_hw` | `featherdesk-addon-{x264,mf_hw}.dll`, plus `[encode] force_addon = "x264"` |
| NVIDIA-only (low latency priority) | `openh264` + `nvenc` | `featherdesk-addon-{openh264,nvenc}.dll` |
| AMD-only (quality priority) | `openh264` + `amf` | `featherdesk-addon-{openh264,amf}.dll` |
| Intel-only (low power) | `openh264` + `qsv` | `featherdesk-addon-{openh264,qsv}.dll` |
| Maximum flexibility | `openh264,x264,mf_hw,nvenc,amf,qsv` | drop in all six; probe selects best at runtime (`x264` only when forced) |

Build each add-on separately as a cdylib and drop the resulting `.dll`
into the add-ons directory; stack any combination by dropping in more libraries:
```bash
cargo build --release -p featherdesk-addon-mf_hw   # cdylib  featherdesk-addon-mf_hw.dll
```

---

## SW encoder choice: OpenH264 is the default, x264 is opt-in

Both produce H.264 — same codec, different implementations, different licenses.

| Aspect | OpenH264 (BSD, Cisco) | x264 (GPL, subprocess) |
|--------|----------------------|------------------------|
| Selected by | `[encode] mode = "auto"` — the software default | `[encode] force_addon = "x264"` only |
| License | BSD-2-Clause | GPL-2.0+ (isolated via ffmpeg subprocess) |
| Profile | Constrained Baseline (its encoder emits no other) | High / High 4:2:2 / High 4:4:4 |
| 1080p P50 @ 12T | 7.4ms | **3.3ms** (2.2× faster) |
| 1440p P50 @ 12T | 13.4ms | **5.5ms** (2.4× faster) |
| Forcing an IDR | one flag on the next `encode` call | **kill + respawn the ffmpeg child** — no cheaper path exists |
| Integration | Rust FFI in-process | ffmpeg subprocess + pipe |
| Royalties | Cisco pays MPEG-LA | None — patent expired in most regions |
| Bundled | ~1 MB DLL | Requires ffmpeg in PATH or bundled |

**`openh264` is the default** on every deployment. It is in-process, has no
external binary to find or lose, and — the property that decides it — can force
an IDR in place, for the cost of one flag on the next frame.

**`x264` is ~2× faster on a CPU-bound host and is worth forcing where that has
been measured.** It is not the default, because its only mechanism for an
on-demand keyframe is respawning the ffmpeg child: every client join, every gap
recovery and every resolution change costs a process restart and a discontinuity
in the Annex B stream. See MODULE_ENCODE "Software encoder order".

See [`SW/OPENH264_WINDOWS_SPEC.md`](./SW/OPENH264_WINDOWS_SPEC.md)
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

One Rust FFI binding (`mf_hw`), one binary, all vendors covered. The trade-off is
~2–4ms higher latency than vendor-direct SDKs (NVENC / AMF / QSV) and lack of
vendor-specific features (REF_FRAMES_INVALIDATION, AMF PA).

**For standard remote-desktop deployment, ship `mf_hw` and skip the per-vendor SDKs.**
For peak performance / vendor-specific features, ship the vendor SDK as an additional
add-on.

---

## Codec fallback order at runtime

When multiple encoders are loaded, the pipeline probes in this order (the
Windows arm of MODULE_PIPELINE startup step 3e — the order is per-OS, and
`libva` is not a Windows path):

```
1. NVENC available (hardware + add-on)?      → use NVENC
2. AMF available?                            → use AMF
3. QSV available?                            → use QSV
4. MF HW (any vendor MFT registered)?        → use MF HW (cross-vendor default)
5. OpenH264 (Rust FFI)?                      → universal SW fallback
6. None?                                     → fatal: no encoder add-on installed

x264 is NOT in this order — it is reachable only via
[encode] force_addon = "x264" (see "SW encoder choice" above).
```

Vendor-specific SDKs precede the generic abstraction: `nvenc`/`amf`/`qsv` before
`mf_hw`.

Which add-on is *selected* is a separate question from which codec it *emits*:

> The encoder advertises **H.264** (`avc1.*`) for every SDR session, on every
> platform, regardless of what HEVC hardware is present. **HEVC Main10**
> (`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
> H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's
> WebCodecs cannot decode it, and a codec no attached client can decode is a
> black screen, not a saving. Which HW encoder is *selected* is a separate
> question from which codec it *emits* — the probe order picks the add-on, this
> rule picks the codec.

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
with a GPU, MF SW is unreachable. `openh264` is the SW path (`x264` where it has
been forced).
