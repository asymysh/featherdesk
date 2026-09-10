# macOS Capture Add-Ons

## Default binary capture: NONE

Mirroring the Linux and encoder architecture: the default macOS binary ships
with **zero capture backends**. Capture is an opt-in add-on shared library. Build
the `sck` library with `cargo build --release -p featherdesk-addon-sck`
and drop it into the add-ons directory to enable ScreenCaptureKit.

In practice, **every** real macOS deployment will load the SCK add-on —
it's the only supported capture path on macOS 12.3+. The pluggable structure
exists for architectural symmetry across platforms, not because there's a
realistic choice to make.

---

## Available capture add-ons

| Add-on | Add-on ID | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **ScreenCaptureKit (SCK)** | `sck` | [`./SCK_MACOS_SPEC.md`](./SCK_MACOS_SPEC.md) | Always — the only supported macOS capture API | 📋 Specced; Hackintosh-benchmarked |

### Recommended combinations

Build each add-on as its own shared library and drop the set into the add-ons directory:

| Deployment | Capture add-on | Encoder add-on(s) | Add-on libraries to drop in |
|------------|---------------|-------------------|------------------------------|
| Apple Silicon Mac | `sck` | `vt_hw` (HEVC + H.264 via Apple Media Engine) | `featherdesk-addon-sck.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Intel Mac | `sck` | `vt_hw,vt_sw` (HW preferred, VT-SW fallback for legacy Intel) | `featherdesk-addon-sck.dylib`, `featherdesk-addon-vt_hw.dylib`, `featherdesk-addon-vt_sw.dylib` |
| Universal macOS binary | `sck` | `vt_hw,vt_sw,openh264` (max compatibility + SW fallback) | `featherdesk-addon-sck.dylib`, `featherdesk-addon-vt_hw.dylib`, `featherdesk-addon-vt_sw.dylib`, `featherdesk-addon-openh264.dylib` |

---

## Why no other capture add-ons exist on macOS

macOS 26 (Tahoe) removed every legacy capture API simultaneously:

| API | macOS 26 Status |
|-----|-----------------|
| **ScreenCaptureKit** | ✅ Only option |
| CGDisplayStream | ❌ Removed — compiler error |
| CGWindowListCreateImage | ❌ Removed — compiler error |
| CGDisplayCreateImage | ❌ Removed — compiler error |
| AVCaptureScreenInput | ❌ Removed — compiler error |

All four legacy APIs throw: *"unavailable in macOS: Please use ScreenCaptureKit instead."*

There is also no vendor fragmentation on macOS — Apple controls the entire
graphics stack from Metal up, so there's no NVIDIA/AMD/Intel-specific capture
path to add as an alternative.

---

## Runtime capture probe order

With only one capture add-on possible, the probe collapses to:

```
1. sck loaded AND Screen Recording TCC granted? → use SCK
2. Otherwise                                    → fatal: no usable capture
```

`sck` declares `AddonCaps::EMBED_CURSOR | EMBED_CURSOR_SURF` and **not**
`AddonCaps::CURSOR`, so macOS always resolves `cursorMode = "embedded"`:
ScreenCaptureKit composites the pointer before the sample buffer is delivered.
Setting `[capture] cursor_mode = "separate"` makes `sck` ineligible and, with no
other macOS capture add-on, fails startup — see
[`./SCK_MACOS_SPEC.md`](./SCK_MACOS_SPEC.md) "Cursor Handling".

---

## If a separate capture is ever needed

Extremely unlikely given Apple's consolidation onto SCK, but if some future
workload requires it (e.g., a hypothetical pre-compositor frame access API):

1. Write the spec at `specs/addons/macos/capture/{NAME}_MACOS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the capture index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Follow CENTRAL_SPEC "Where to register a new add-on" steps 4–7 for the code
   side (root module, `AddonCaps`, cdylib path, probe-order wiring).
