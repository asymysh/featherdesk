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

// Absolute mouse move (stream PIXEL in, global display POINT out — see below).
// While a button is held, post the matching *MouseDragged event so AppKit / Finder
// register the drag (kCGEventMouseMoved is NOT a drag).
CGEventType moveType = currentlyDown(button) ? draggedFor(button) : kCGEventMouseMoved;
CGPoint p = CGPointMake(globalPointX, globalPointY);
CGEventRef m = CGEventCreateMouseEvent(NULL, moveType, p, currentMouseButton());
CGEventSetFlags(m, currentFlags); // apply modifiers to MOUSE events too (Cmd-click etc.)
CGEventPost(kCGHIDEventTap, m); CFRelease(m);

// Relative mouse (pointer lock): CGEventCreateMouseEvent ALWAYS moves the cursor
// to its CGPoint argument — the delta fields only inform game-style consumers.
// The wire delta is in stream pixels, so it goes through the SAME sx/sy multiply
// as an absolute position. Fetch the current location, add, clamp to bounds.
double dxPoints = wireDx * sx, dyPoints = wireDy * sy;
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

### Coordinate space: stream pixels in, points out

`CGEventPost` consumes the **global display coordinate space measured in
points**, NOT pixels. On a Retina Mac the framebuffer is, e.g., 2880x1800
physical pixels while the global space is 1440x900 points.

The wire and the whole pipeline are in **stream pixels** (see
[`../../../core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)
"Coordinate space"). This injector — not the pipeline, not the capturer —
performs the conversion, because it is the component that knows CGEvent's units:

```objc
// Cached at construction and refreshed from a display-reconfiguration callback.
CGDirectDisplayID  did      = <the display `sck` is capturing>;
CGRect             bounds   = CGDisplayBounds(did);              // POINTS
size_t             px_w     = CGDisplayPixelsWide(did);          // PIXELS
size_t             px_h     = CGDisplayPixelsHigh(did);          // PIXELS

// resize(w, h) records the stream space; convert on every absolute event:
double sx = bounds.size.width  / (double)stream_w;   // stream px -> points
double sy = bounds.size.height / (double)stream_h;
CGPoint pt = CGPointMake(bounds.origin.x + stream_x * sx,
                         bounds.origin.y + stream_y * sy);
CGWarpMouseCursorPosition(pt);   // or the CGEventCreateMouseEvent location
```

Note that `sx`/`sy` fold **two** ratios into one multiply: the backing-scale
factor (`px_w / bounds.size.width`, 2.0 on Retina) and any stream downscale
(`stream_w / px_w`). Deriving them separately is the mistake that produces the
half-position bug.

`CGDisplayRegisterReconfigurationCallback` refreshes `bounds`/`px_*` on a mode
change, a resolution change or a display being attached; the pipeline's
resolution-change flow separately calls `resize()` with the new stream space, and
the two are independent inputs to the same multiply.

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

`resize(width, height)` is how the injector learns the stream space; the multiply
above is how it reaches the OS. For the single-display v1 target the captured
display is at origin `(0, 0)`, so `bounds.origin` contributes nothing and the
conversion collapses to the two scale factors. Multi-monitor is out of scope.

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

- At startup, `new_key_mouse` calls `AXIsProcessTrusted()`. If not trusted, it
  prompts (opens the Accessibility pane) and returns a clear error; the pipeline
  starts view-only until permission is granted.
- The error message tells the operator exactly which toggle to enable.

> **Not synthesizable:** some system sequences (e.g. the login window, certain
> Cmd+Space behaviors on newer macOS) are intercepted below `CGEventPost` and
> cannot be injected. There is no macOS equivalent of Ctrl+Alt+Del to handle.

---

## Build & Distribution

There is nothing to build separately and nothing to drop into the add-ons
directory: this path is compiled into the core `featherdesk-input` crate along
with the rest of the `enigo` default, and ships inside the app bundle.

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

## Construction

There is no `probe()` here and no `ProbeReport` to fill. This is not an add-on:
it is the macOS arm of the in-core `enigo` default, so it has no root module, no
`InputAddon` adapter and no `AddonCaps` of its own — `caps()` on the constructed
injector returns `AddonCaps(0)`, exactly as
[`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)
states for the default. Core Graphics always exists on macOS; what is not
guaranteed is Accessibility permission, and that is checked at construction with
an actionable error.

```rust
// crate: featherdesk-input (the `enigo` default's macOS backend, compiled into core)

/// Builds the macOS KeyMouseInjector and caches the display geometry the
/// coordinate conversion needs. Returns `Err(InputError::Backend(..))` naming the
/// Accessibility toggle when `AXIsProcessTrusted()` is false; the pipeline starts
/// view-only until the operator grants it.
fn new_key_mouse(cfg: input::InjectorConfig)
    -> Result<Box<dyn input::KeyMouseInjector>, input::InputError>;
```

The result is handed to `input::new_dispatcher(km, touch, gp, cfg)` at startup
step 8, alongside `gcvirtual` if that add-on loaded.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| No Accessibility permission | `new_key_mouse` returns `InputError::Backend("Accessibility permission not granted (System Settings → Privacy & Security → Accessibility)")`, prompts, opens the Settings pane; view-only until granted. `InputError`'s variant set is fixed by the AbiErr registry, so the actionable text travels in the `Backend` detail rather than in a variant of its own |
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

There is **no** `[addon_module_cgevent]` section, and the strict config decoder
would reject one: `[addon_module_*]` tables exist only for loadable add-ons, and
this path is in core. The macOS kb/mouse default is governed by `[input]` like
every other OS's default (see
[`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)) — `enabled`
is the master switch, and setting it to `false` is what makes a macOS binary
view-only. Prompting for Accessibility is not a knob: an injector that cannot
inject always says so, and always says which toggle to flip.

---

## Status

📋 Specced — not yet built. Implementation order: Accessibility probe/prompt →
key injection (kVK map + flag tracking) → mouse abs/rel/button → scroll.
