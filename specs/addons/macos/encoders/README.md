# macOS Encoder Add-Ons

## Pluggable architecture: every encoder is an opt-in add-on

The macOS default binary contains **no encoders**. Every encoder is a separate
add-on shared library. Users drop in exactly the encoders they want.

```
specs/addons/macos/encoders/
├── SW/
│   ├── OPENH264_MACOS_SPEC.md     ← BSD-licensed Cisco SW — the software DEFAULT, cross-platform
│   ├── X264_SUBPROCESS_MACOS_SPEC.md  ← GPL-isolated x264 subprocess (opt-in, force_addon only)
│   └── VIDEOTOOLBOX_SW_MACOS_SPEC.md  ← Apple's tuned SW (macOS-native, best on Apple Silicon)
└── HW/
    └── VIDEOTOOLBOX_HW_MACOS_SPEC.md  ← unified HW for Intel QS, AMD VCE, Apple Media Engine
```

---

## Recommended combinations

| Deployment | Recommended add-on set | Add-on libraries to drop in |
|-----------|-----------------------|------------------------------|
| Generic Mac (default) | `openh264` + `vt_sw` + `vt_hw` | `featherdesk-addon-openh264.dylib`, `featherdesk-addon-vt_sw.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Apple Silicon M1+ | `vt_hw` only | `featherdesk-addon-vt_hw.dylib` |
| Intel Mac | `openh264` + `vt_sw` + `vt_hw` | `featherdesk-addon-openh264.dylib`, `featherdesk-addon-vt_sw.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Cross-platform | `openh264` + `vt_hw` | `featherdesk-addon-openh264.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Measured CPU-bound host (x264 opt-in) | `x264` + `vt_hw` | `featherdesk-addon-x264.dylib`, `featherdesk-addon-vt_hw.dylib`, plus `[encode] force_addon = "x264"` |

Build each add-on as its own shared library and drop the set into the add-ons
directory — there is no combined host build. For example:
```bash
cargo build --release -p featherdesk-addon-openh264   # cdylib  featherdesk-addon-openh264.dylib
cargo build --release -p featherdesk-addon-vt_hw   # cdylib  featherdesk-addon-vt_hw.dylib
cargo build --release -p featherdesk-addon-vt_sw   # cdylib  featherdesk-addon-vt_sw.dylib
```
`vt_sw` and `vt_hw` are two variants built from the same `addons/encode/vt` crate,
selected by an **internal Cargo feature at the add-on's own build step** (not host
composition); each produces its own `.dylib` with its own capability descriptor.

---

## Why so few HW add-ons on macOS

Unlike Linux (3 HW add-ons) and Windows (4 HW add-ons), macOS has **one HW encoder
API**: VideoToolbox. Apple controls the entire graphics stack from Metal up.
There is no vendor fragmentation — every Mac, whether Intel + AMD discrete,
Intel integrated, or Apple Silicon, exposes its hardware encoder through the
same `VTCompressionSession` API.

The add-on split (`vt_sw` vs `vt_hw`) is purely for packaging modularity — they
share the same Rust FFI module, just different configuration at runtime.

---

## SW encoder choice: OpenH264 is the default, x264 is opt-in

Three software H.264 encoders exist on macOS; two of them are in the auto order.

| Encoder | License | Selected by | Profile | On Apple Silicon | Forcing an IDR |
|---------|---------|-------------|---------|------------------|----------------|
| **OpenH264** (Cisco) | BSD-2 | `[encode] mode = "auto"` — first | Constrained Baseline (its encoder emits no other) | Slightly slower than VT SW | one flag on the next `encode` call |
| **VT SW** (Apple) | macOS system | `[encode] mode = "auto"` — second | High | Best (Apple's ARM-tuned) | session property, in place |
| **x264** (subprocess) | GPL-2 (isolated) | `[encode] force_addon = "x264"` only | High / High 4:2:2 / High 4:4:4 | ~2× faster than OpenH264 | **kill + respawn the ffmpeg child** — no cheaper path exists |

**`openh264` is the default** on every deployment, macOS included. It is
in-process, has no external binary to find or lose, and — the property that
decides it — can force an IDR in place, for the cost of one flag on the next
frame.

**`vt_sw` is second** because it is Apple's own encoder, tuned for the silicon it
runs on and needing no third-party library. It precedes `x264` on macOS for the
same reason.

**`x264` is ~2× faster on a CPU-bound host and is worth forcing where that has
been measured.** It is not the default, because its only mechanism for an
on-demand keyframe is respawning the ffmpeg child: every client join, every gap
recovery and every resolution change costs a process restart and a discontinuity
in the Annex B stream. See MODULE_ENCODE "Software encoder order".

None of the three has been measured on Apple hardware — the numbers above are
carried over from the Windows benchmark of the same cross-platform code.

See:
- [`SW/OPENH264_MACOS_SPEC.md`](./SW/OPENH264_MACOS_SPEC.md) — the software default, BSD, cross-platform
- [`SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md`](./SW/VIDEOTOOLBOX_SW_MACOS_SPEC.md) — macOS-native, second in the auto order
- [`SW/X264_SUBPROCESS_MACOS_SPEC.md`](./SW/X264_SUBPROCESS_MACOS_SPEC.md) — GPL-isolated, opt-in only

---

## Add-on probe order at runtime

When multiple encoders are loaded, the pipeline probes in this order (the macOS
arm of MODULE_PIPELINE startup step 3e — the order is per-OS, and `vt_hw`/`vt_sw`
are the only VideoToolbox paths anywhere):

```
1. VT HW available?  → use vt_hw
2. OpenH264?         → the software default
3. VT SW?            → macOS-native SW, second in the auto order
4. None?             → fatal: no encoder add-on installed

x264 is NOT in this order — it is reachable only via
[encode] force_addon = "x264" (see "SW encoder choice" above).
```

Which add-on is *selected* is a separate question from which codec it *emits*:

> The encoder advertises **H.264** (`avc1.*`) for every SDR session, on every
> platform, regardless of what HEVC hardware is present. **HEVC Main10**
> (`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
> H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's
> WebCodecs cannot decode it, and a codec no attached client can decode is a
> black screen, not a saving. Which HW encoder is *selected* is a separate
> question from which codec it *emits* — the probe order picks the add-on, this
> rule picks the codec.

The codec string that reaches the client is **computed** from the configured
profile and the active geometry, never quoted as a constant — see
[`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Codec-string
computation".

**No libx265, ever** — software HEVC outside VideoToolbox carries triple patent
pool exposure. VideoToolbox's own SW HEVC is licensed by Apple but too slow for
real-time streaming (~30ms p50 at 1080p), so HDR runs on `vt_hw` and is refused
outright where `hevc.gva` is absent.
