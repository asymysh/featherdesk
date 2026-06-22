# macOS Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The macOS default binary contains **no encoders**. Every encoder is a separate
build-tagged add-on. Users compile in exactly the encoders they want.

```
ADD-ON-SPECS/macOS/encoders/
├── SW/
│   └── VIDEOTOOLBOX_SW_MACOS_SPEC.md   ← Apple's tuned SW H.264/HEVC (preferred on Apple Silicon)
│   └── (OpenH264 CGo also works — see Linux/SW spec)
└── HW/
    └── VIDEOTOOLBOX_HW_MACOS_SPEC.md   ← unified HW for Intel QS, AMD VCE, Apple Media Engine
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Binary |
|-----------|-----------------------|--------|
| Generic Mac (any year) | `vt_sw` + `vt_hw` | `viewport-rds-macos-default` |
| Apple Silicon M1+ | `vt_sw` + `vt_hw` | `viewport-rds-macos-arm64` |
| Intel Mac | `vt_sw` + `vt_hw` | `viewport-rds-macos-x86_64` |
| Cross-platform binary parity | `openh264` instead of `vt_sw` | `viewport-rds-macos-universal-codec` |

The build tags compose:
```bash
go build -tags "vt_sw,vt_hw" -o viewport-rds-macos ./cmd/server
```

---

## Why so few add-ons on macOS

Unlike Linux (4 HW add-ons) and Windows (5 HW add-ons), macOS has **one HW encoder API**:
VideoToolbox. Apple controls the entire graphics stack from Metal up. There is no
vendor fragmentation — every Mac, whether Intel + AMD discrete, Intel integrated,
or Apple Silicon, exposes its hardware encoder through the same `VTCompressionSession`
API.

The add-on split (`vt_sw` vs `vt_hw`) is purely for build modularity — they share
the same CGo file, just different configuration at runtime.

---

## OpenH264 CGo on macOS

OpenH264 CGo works on macOS too (we verified during benchmark sessions). It can
be used as the SW encoder on macOS instead of VT SW for cross-platform consistency
(same Go code as Linux + Windows). However:

- On Apple Silicon: VT SW is ~30% faster than OpenH264 CGo (Apple's ARM tuning is better)
- On Intel Macs: VT SW and OpenH264 CGo are roughly equivalent

The macOS-preferred SW path is VT SW. OpenH264 CGo is a valid alternative when
cross-platform binary consistency matters more than peak performance. See
[`../../Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md`](../../Linux/encoders/SW/OPENH264_CGO_LINUX_SPEC.md)
— the same spec applies on macOS with the documented CGo pointer indirection
adjustments.

---

## Codec fallback order at runtime

When both `vt_hw` and `vt_sw` are compiled in:

```
1. VT HW (HEVC available)?  → use HEVC HW (announce hvc1.1.6.L93.B0)
2. VT HW (H.264 available)? → use H.264 HW (announce avc1.42E01E)
3. VT SW (H.264)?           → use H.264 SW
4. None?                    → fatal: no encoder add-on installed
```

No software HEVC — VT HEVC SW exists but is too slow for real-time streaming
(~30ms p50 at 1080p), and software HEVC has triple patent pool concerns when
shipped (though Apple's framework license covers VT HEVC SW, the encoder is too
slow to be useful anyway).
