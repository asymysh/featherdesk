# macOS Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The macOS default binary contains **no encoders**. Every encoder is a separate
add-on shared library. Users drop in exactly the encoders they want.

```
specs/addons/macos/encoders/
├── SW/
│   ├── OPENH264_CGO_MACOS_SPEC.md     ← BSD-licensed Cisco SW (commercial, cross-platform)
│   ├── X264_SUBPROCESS_MACOS_SPEC.md  ← GPL-isolated x264 subprocess (home / OSS, 2× faster)
│   └── VIDEOTOOLBOX_SW_MACOS_SPEC.md  ← Apple's tuned SW (macOS-native, best on Apple Silicon)
└── HW/
    └── VIDEOTOOLBOX_HW_MACOS_SPEC.md  ← unified HW for Intel QS, AMD VCE, Apple Media Engine
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Add-on libraries to drop in |
|-----------|-----------------------|------------------------------|
| Generic Mac (commercial default) | `vt_sw` + `vt_hw` | `featherdesk-addon-vt_sw.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Apple Silicon M1+ | `vt_hw` only | `featherdesk-addon-vt_hw.dylib` |
| Intel Mac | `vt_sw` + `vt_hw` | `featherdesk-addon-vt_sw.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Cross-platform, commercial | `openh264` + `vt_hw` | `featherdesk-addon-openh264.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Cross-platform, home / OSS | `x264` + `vt_hw` | `featherdesk-addon-x264.dylib`, `featherdesk-addon-vt_hw.dylib` |

Build each add-on as its own shared library and drop the set into the add-ons
directory — there is no combined host build. For example:
```bash
go build -buildmode=c-shared -o featherdesk-addon-openh264.dylib ./internal/encode/openh264
go build -buildmode=c-shared -tags vt_hw -o featherdesk-addon-vt_hw.dylib ./internal/encode/vt
go build -buildmode=c-shared -tags vt_sw -o featherdesk-addon-vt_sw.dylib ./internal/encode/vt
```
`vt_sw` and `vt_hw` are two variants of the same `./internal/encode/vt` package,
selected by an **internal build tag at the add-on's own build step** (not host
composition); each produces its own `.dylib` with its own capability descriptor.

---

## Why so few HW add-ons on macOS

Unlike Linux (3 HW add-ons) and Windows (4 HW add-ons), macOS has **one HW encoder
API**: VideoToolbox. Apple controls the entire graphics stack from Metal up.
There is no vendor fragmentation — every Mac, whether Intel + AMD discrete,
Intel integrated, or Apple Silicon, exposes its hardware encoder through the
same `VTCompressionSession` API.

The add-on split (`vt_sw` vs `vt_hw`) is purely for packaging modularity — they
share the same CGo file, just different configuration at runtime.

---

## SW encoder choice on macOS

Three options for software H.264 encoding:

| Encoder | License | Performance on Apple Silicon | Performance on Intel Mac | When to use |
|---------|---------|-----------------------------|-------------------------|-------------|
| **VT SW** (Apple) | macOS system | Best (Apple's ARM-tuned) | Good | macOS-only deployment |
| **OpenH264** (Cisco) | BSD-2 | Slightly slower than VT SW | Equivalent to VT SW | Cross-platform commercial deployment |
| **x264** (subprocess) | GPL-2 (isolated) | 2× faster than OpenH264 | 2× faster than OpenH264 | Home / OSS / cross-platform with GPL OK |

See:
- [`SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](./SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) — macOS-native
- [`SW/OPENH264_CGO_MACOS_SPEC.md`](./SW/OPENH264_CGO_MACOS_SPEC.md) — BSD cross-platform
- [`SW/X264_SUBPROCESS_MACOS_SPEC.md`](./SW/X264_SUBPROCESS_MACOS_SPEC.md) — GPL-isolated, 2× faster

---

## Codec fallback order at runtime

When multiple encoders are loaded:

```
1. VT HW (HEVC available)?  → use HEVC HW (announce hvc1.1.6.L93.B0)
2. VT HW (H.264 available)? → use H.264 HW (announce avc1.42E01F)
3. x264 subprocess?         → use x264 (GPL builds only — 2× faster than alternatives)
4. VT SW?                   → use VT SW (macOS-native fallback)
5. OpenH264?                → use OpenH264 (cross-platform BSD fallback)
6. None?                    → fatal: no encoder add-on installed
```

No software HEVC anywhere — even VT HEVC SW is too slow for real-time streaming
(~30ms p50 at 1080p) and software HEVC has triple patent pool concerns.
