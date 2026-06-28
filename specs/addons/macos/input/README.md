# macOS Input Add-Ons

## Default binary input: kb/mouse via the in-core `enigo` default

macOS keyboard + mouse injection is **built into core** via the default `enigo`
`KeyMouseInjector` — and **`enigo`'s macOS backend IS CGEvent**. So out of the
box (with `[input] enabled = true`) the macOS binary already injects keyboard +
mouse; no add-on is required. The standalone **`cgevent` add-on is retired /
subsumed** into this `enigo` default — there is no separate `cgevent` shared
library anymore. (The CGEvent technical details are kept in
[`./CGEVENT_MACOS_SPEC.md`](./CGEVENT_MACOS_SPEC.md) as documentation of how the
in-core default works on macOS.)

The only macOS **input add-on** that remains is `gcvirtual` (gamepad) — `enigo`
has no gamepad path, so virtual-controller injection stays an opt-in add-on
shared library.

---

## Available input add-ons

| Add-on | Add-on ID | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| _kb/mouse (CGEvent)_ | _core `enigo` default_ | [`./CGEVENT_MACOS_SPEC.md`](./CGEVENT_MACOS_SPEC.md) | Always — built into core; **not** a loadable add-on (the former `cgevent` add-on is retired/subsumed) | ✅ In core |
| **GCVirtualController** | `gcvirtual` | [`./GCVIRTUAL_MACOS_SPEC.md`](./GCVIRTUAL_MACOS_SPEC.md) | Browser gamepad redirection (macOS 14+) — virtual controller via the Game Controller framework | 📋 Specced |

### Recommended combinations

| Deployment | Input | Add-on libraries to drop in |
|------------|-------|------------------------------|
| Any interactive Mac | core `enigo` default (kb/mouse) | `featherdesk-addon-sck.dylib`, `featherdesk-addon-vt_hw.dylib` |
| Casual gaming with gamepad (macOS 14+) | core `enigo` default + `gcvirtual` | `featherdesk-addon-sck.dylib`, `featherdesk-addon-vt_hw.dylib`, `featherdesk-addon-gcvirtual.dylib` |
| View-only monitoring (no injection) | `[input] enabled = false` | `featherdesk-addon-sck.dylib`, `featherdesk-addon-vt_hw.dylib` |

`gcvirtual` only reaches apps using Apple's GameController framework. SDL2-based
games that read IOKit HID directly do **not** see the virtual controller — this
is a real limitation, document it in user-facing release notes.

---

## Permission requirements

The in-core `enigo` default calls `CGEventPost`, which macOS gates behind
**Accessibility** permission. The app must be:

- **signed + notarized**, and
- granted **Accessibility** in **System Settings → Privacy & Security →
  Accessibility** (one-time, per machine).

Until Accessibility is granted, injected events are silently swallowed by the
OS — the spec covers detecting and surfacing this state to the operator.

---

## No touch on macOS

There is **no public touch-injection API** on macOS — `UITouch` is iOS-only with
no AppKit equivalent. macOS input is keyboard + mouse only; touch clients fall
back to mouse emulation. There's also no vendor fragmentation to add backends for.

---

## Runtime input registration

Keyboard + mouse come from core; only gamepad is add-on-gated:

```
1. [input] enabled AND Accessibility granted?   → core enigo default registers keyboard + mouse (CGEvent)
2. gcvirtual loaded AND macOS 14+?              → register gamepad injection
3. Neither?                                      → view-only: no input capabilities
```

`gcvirtual` is independent of the core `enigo` default — it adds gamepad on top,
though a typical gaming deployment uses both.

---

## When ready to add a new input backend

1. Write the spec at `specs/addons/macos/input/{NAME}_MACOS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Build as a cdylib from `addons/input/{name}/`
5. Wire the capability registration in `MODULE_PIPELINE.md`
