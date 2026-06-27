# macOS Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The macOS default binary contains **no encoders**. Every encoder is a separate
build-tagged add-on. Users compile in exactly the encoders they want.

```
ADD-ON-SPECS/macOS/encoders/
├── SW/
│   ├── OPENH264_CGO_MACOS_SPEC.md     ← BSD-licensed Cisco SW (commercial, cross-platform)
│   ├── X264_SUBPROCESS_MACOS_SPEC.md  ← GPL-isolated x264 subprocess (home / OSS, 2× faster)
│   └── VIDEOTOOLBOX_SW_MACOS_SPEC.md  ← Apple's tuned SW (macOS-native, best on Apple Silicon)
└── HW/
    └── VIDEOTOOLBOX_HW_MACOS_SPEC.md  ← unified HW for Intel QS, AMD VCE, Apple Media Engine
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Binary |
|-----------|-----------------------|--------|
| Generic Mac (commercial default) | `vt_sw` + `vt_hw` | `featherdesk-macos-default` |
| Apple Silicon M1+ | `vt_hw` only | `featherdesk-macos-arm64` |
| Intel Mac | `vt_sw` + `vt_hw` | `featherdesk-macos-x86_64` |
| Cross-platform binary, commercial | `openh264` + `vt_hw` | `featherdesk-macos-cross-bsd` |
| Cross-platform binary, home / OSS | `x264` + `vt_hw` | `featherdesk-macos-cross-gpl` |

The build tags compose:
```bash
go build -tags "vt_sw,vt_hw,openh264" -o featherdesk-macos ./cmd/server
```

---

## Why so few HW add-ons on macOS

Unlike Linux (3 HW add-ons) and Windows (4 HW add-ons), macOS has **one HW encoder
API**: VideoToolbox. Apple controls the entire graphics stack from Metal up.
There is no vendor fragmentation — every Mac, whether Intel + AMD discrete,
Intel integrated, or Apple Silicon, exposes its hardware encoder through the
same `VTCompressionSession` API.

The add-on split (`vt_sw` vs `vt_hw`) is purely for build modularity — they
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

When multiple encoders are compiled in:

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
