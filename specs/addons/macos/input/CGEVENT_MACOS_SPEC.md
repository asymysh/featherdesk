# macOS Input: CGEvent (the in-core `enigo` default)

> **RETIRED as a standalone add-on — subsumed by the core `enigo` default.**
> There is no separate `cgevent` add-on anymore. `enigo`'s macOS backend **IS**
> CGEvent, and `enigo` is built into core as the default `KeyMouseInjector` on
> every OS (see [`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)).
> This document is kept as a reference for **how the macOS kb/mouse default works
> under the hood** — the CGEvent / Core Graphics technical details below describe
> `enigo`'s macOS code path, not a loadable shared library.

## Purpose

CGEvent is the macOS keyboard+mouse injection path used by the core's built-in
`enigo` default `KeyMouseInjector` (see
[`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)).
It synthesizes events through Core Graphics (`CGEventPost` to `kCGHIDEventTap`).

There is no alternative on macOS — `CGEventPost` is the supported public path for
synthetic keyboard/mouse events. Touch injection has no public API on macOS, so
there is no macOS touch add-on (pen/touch from a client falls back to mouse).

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Core Graphics / ApplicationServices | Apple system framework | Linked, not redistributed |
| Our Rust FFI binding (`objc2` / system framework) | MIT | |

---

## How It Works

```c
#include <ApplicationServices/ApplicationServices.h>

// Key: map HID usage → macOS virtual key code (kVK_*), then:
CGEventRef e = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)vk, down /*true=down*/);
CGEventSetFlags(e, currentFlags); // tracked modifier mask, see below
CGEventPost(kCGHIDEventTap, e);
CFRelease(e);

// Absolute mouse move (stream pixel → global display POINT, NOT pixel — see below).
// While a button is held, post the matching *MouseDragged event so AppKit / Finder
// register the drag (kCGEventMouseMoved is NOT a drag).
CGEventType moveType = currentlyDown(button) ? draggedFor(button) : kCGEventMouseMoved;
CGPoint p = CGPointMake(globalPointX, globalPointY);
CGEventRef m = CGEventCreateMouseEvent(NULL, moveType, p, currentMouseButton());
CGEventSetFlags(m, currentFlags); // apply modifiers to MOUSE events too (Cmd-click etc.)
CGEventPost(kCGHIDEventTap, m); CFRelease(m);

// Relative mouse (pointer lock): CGEventCreateMouseEvent ALWAYS moves the cursor
// to its CGPoint argument — the delta fields only inform game-style consumers.
// Fetch the current location, add the delta, clamp to display bounds.
CGEventRef snap = CGEventCreate(NULL);
CGPoint cur = CGEventGetLocation(snap); CFRelease(snap);
CGPoint next = CGPointMake(clamp(cur.x + dxPoints, 0, screenPointsW - 1),
                           clamp(cur.y + dyPoints, 0, screenPointsH - 1));
CGEventRef m = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, next, kCGMouseButtonLeft);
CGEventSetIntegerValueField(m, kCGMouseEventDeltaX, dxPoints);
CGEventSetIntegerValueField(m, kCGMouseEventDeltaY, dyPoints);
CGEventPost(kCGHIDEventTap, m); CFRelease(m);

// Buttons: Down/Up types per index (left/right/other), and OtherMouse* needs
// CGEventSetIntegerValueField(kCGMouseEventButtonNumber, n).

// Scroll. Wire is W3C (positive Dy = scroll DOWN). macOS scroll events are
// positive = scroll UP. NEGATE before posting.
CGScrollEventUnit unit = (wireUnit == 0) ? kCGScrollEventUnitPixel : kCGScrollEventUnitLine;
int32_t macDy = -wireDy, macDx = -wireDx;
// Page mode (wireUnit==2): translate as 3 lines per page (W3C deltaMode page is rare).
if (wireUnit == 2) { unit = kCGScrollEventUnitLine; macDy *= 3; macDx *= 3; }
CGEventRef s = CGEventCreateScrollWheelEvent(NULL, unit, 2, macDy, macDx);
CGEventPost(kCGHIDEventTap, s); CFRelease(s);
```

### Coordinate space: points, not pixels

`CGEventPost` consumes the **global display coordinate space measured in
points**, NOT pixels. On a Retina Mac the framebuffer is, e.g., 2880x1800
physical pixels but the global space is 1440x900 points. Using raw pixel
coordinates lands the cursor at half the intended position on every Retina
display.

The pipeline must therefore advertise `cfg.width`/`cfg.height` in **points** to
the `enigo` default, and the capture pipeline must agree. Conversion if needed:
`CGDisplayPixelsWide(displayID)` (pixels) vs `CGDisplayBounds(displayID).size.width`
(points) gives the per-display backing-scale factor.

### Modifier flag tracking & release-all

Modifier state (`kCGEventFlagMaskShift`/`Control`/`Alternate`/`Command`/`Help`) is
maintained from the decoded key stream. On every event — **including mouse
events**, so Cmd-click / Shift-drag / Ctrl-scroll behave correctly — the
current mask is applied via `CGEventSetFlags`.

On controller disconnect or `resize`, the dispatcher tells the injector to
**release all held keys + buttons**: emit a synthetic up event for each
tracked-down key and button before tearing down. Without it, autorepeat / lost
events leave "stuck modifier" state in the OS session.

### HID-usage → virtual key (kVK_*)

The `enigo` default maps the core's neutral HID usage IDs to macOS virtual key codes
(`Carbon/HIToolbox Events.h` `kVK_*`). Generated for full coverage. Modifier
state is tracked and applied via `CGEventSetFlags` so combinations (Cmd+C, etc.)
behave correctly.

### Coordinate mapping

Stream-point coordinates (see "Coordinate space" above) map directly to the
global display point space. For the single-display target with the captured
display at origin (0,0): `globalPointX = x`, `globalPointY = y`. `resize`
updates the width/height used for clamping. Multi-monitor is out of scope.

---

## Accessibility Permission (mandatory)

`CGEventPost` to `kCGHIDEventTap` **silently drops events** unless the process has
**Accessibility** permission (System Settings → Privacy & Security →
Accessibility). This is the single biggest macOS support issue for remote-desktop
tools, so the `enigo` default handles it explicitly:

```c
Boolean trusted = AXIsProcessTrustedWithOptions(
    (__bridge CFDictionaryRef)@{ (__bridge id)kAXTrustedCheckOptionPrompt : @YES });
```

- At startup, `probe`/`new` calls `AXIsProcessTrusted()`. If not trusted, it
  prompts (opens the Accessibility pane) and returns a clear error; the pipeline
  starts view-only until permission is granted.
- The error message tells the operator exactly which toggle to enable.

> **Not synthesizable:** some system sequences (e.g. the login window, certain
> Cmd+Space behaviors on newer macOS) are intercepted below `CGEventPost` and
> cannot be injected. There is no macOS equivalent of Ctrl+Alt+Del to handle.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-cgevent   # cdylib  featherdesk-addon-cgevent.dylib
```

Framework link config (Rust FFI):

```rust
// build.rs — link the macOS system frameworks:
//   println!("cargo:rustc-link-lib=framework=ApplicationServices");
//   println!("cargo:rustc-link-lib=framework=Carbon");
// The CGEvent / Core Graphics declarations come from the `objc2` /
// `core-graphics` crates (or `bindgen` over
// <ApplicationServices/ApplicationServices.h>). `enigo` itself pulls these in;
// the macOS backend is compiled directly into the core `featherdesk-input` crate.
```

For distribution the app must be **signed + notarized**, and the user grants
Accessibility once. Apple Silicon and Intel use the identical API.

---

## Constructor & Probe

```rust
// crate: featherdesk-input (the `enigo` default's macOS backend, compiled into core)

/// probe returns true on macOS (the API always exists); it does NOT guarantee
/// Accessibility permission — that is checked in `new` with an actionable error.
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// new creates the injector. Returns Err(InputError::NoAccessibility) if not trusted.
fn new(&self, cfg: InjectorConfig) -> Result<Box<dyn KeyMouseInjector>, InputError>;
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| No Accessibility permission | `new` returns `InputError::NoAccessibility`, prompts, opens Settings pane; view-only until granted |
| `CGEventCreate*` returns NULL | log + skip that event; do not crash |
| Unmappable HID usage | log once; drop the key |
| Permission revoked mid-session | injection silently no-ops (OS behavior); detect via a periodic `AXIsProcessTrusted` check and warn |

---

## File Structure

```
featherdesk-input/src/macos/   (the `enigo` default's macOS backend, compiled into core)
├── cgevent.rs            // KeyMouseInjector impl, Rust FFI
├── keymap.rs             // HID usage → kVK_* (generated)
├── accessibility.rs      // AXIsProcessTrusted check + prompt
└── tests.rs
```

> There is no separate `cgevent` shared library — the macOS kb/mouse path is
> compiled into the core `featherdesk-input` crate as the `enigo` default backend.

---

## Configuration

Reads `[addon_module_cgevent]` (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_cgevent]
prompt_accessibility = true   # auto-open the Accessibility pane if not trusted
```

If absent, defaults apply. Folded into the core input config now that the macOS
kb/mouse path is the in-core `enigo` default (no separate add-on to load).

---

## Status

📋 Specced — not yet built. Implementation order: Accessibility probe/prompt →
key injection (kVK map + flag tracking) → mouse abs/rel/button → scroll.
