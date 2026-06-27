# macOS Input Add-On: CGEvent

## Purpose

The `cgevent` add-on is the macOS keyboard+mouse injection backend for the core
Input module (see [`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)).
It implements `input.KeyMouseInjector` using Core Graphics event synthesis
(`CGEventPost` to `kCGHIDEventTap`).

There is no alternative on macOS — `CGEventPost` is the supported public path for
synthetic keyboard/mouse events. Touch injection has no public API on macOS, so
there is no macOS touch add-on (pen/touch from a client falls back to mouse).

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Core Graphics / ApplicationServices | Apple system framework | Linked, not redistributed |
| Our CGo binding | MIT | |

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

The pipeline must therefore advertise `cfg.Width`/`cfg.Height` in **points** to
this add-on, and the capture pipeline must agree. Conversion if needed:
`CGDisplayPixelsWide(displayID)` (pixels) vs `CGDisplayBounds(displayID).size.width`
(points) gives the per-display backing-scale factor.

### Modifier flag tracking & release-all

Modifier state (`kCGEventFlagMaskShift`/`Control`/`Alternate`/`Command`/`Help`) is
maintained from the decoded key stream. On every event — **including mouse
events**, so Cmd-click / Shift-drag / Ctrl-scroll behave correctly — the
current mask is applied via `CGEventSetFlags`.

On controller disconnect or `Resize`, the dispatcher tells this add-on to
**release all held keys + buttons**: emit a synthetic up event for each
tracked-down key and button before tearing down. Without it, autorepeat / lost
events leave "stuck modifier" state in the OS session.

### HID-usage → virtual key (kVK_*)

The add-on maps the core's neutral HID usage IDs to macOS virtual key codes
(`Carbon/HIToolbox Events.h` `kVK_*`). Generated for full coverage. Modifier
state is tracked and applied via `CGEventSetFlags` so combinations (Cmd+C, etc.)
behave correctly.

### Coordinate mapping

Stream-point coordinates (see "Coordinate space" above) map directly to the
global display point space. For the single-display target with the captured
display at origin (0,0): `globalPointX = x`, `globalPointY = y`. `Resize`
updates the width/height used for clamping. Multi-monitor is out of scope.

---

## Accessibility Permission (mandatory)

`CGEventPost` to `kCGHIDEventTap` **silently drops events** unless the process has
**Accessibility** permission (System Settings → Privacy & Security →
Accessibility). This is the single biggest macOS support issue for remote-desktop
tools, so the add-on handles it explicitly:

```c
Boolean trusted = AXIsProcessTrustedWithOptions(
    (__bridge CFDictionaryRef)@{ (__bridge id)kAXTrustedCheckOptionPrompt : @YES });
```

- At startup, `Probe`/`New` calls `AXIsProcessTrusted()`. If not trusted, it
  prompts (opens the Accessibility pane) and returns a clear error; the pipeline
  starts view-only until permission is granted.
- The error message tells the operator exactly which toggle to enable.

> **Not synthesizable:** some system sequences (e.g. the login window, certain
> Cmd+Space behaviors on newer macOS) are intercepted below `CGEventPost` and
> cannot be injected. There is no macOS equivalent of Ctrl+Alt+Del to handle.

---

## Build & Distribution

```bash
go build -tags "cgevent" -o featherdesk ./cmd/server
```

CGo config:

```go
/*
#cgo LDFLAGS: -framework ApplicationServices -framework Carbon
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"
```

For distribution the app must be **signed + notarized**, and the user grants
Accessibility once. Apple Silicon and Intel use the identical API.

---

## Constructor & Probe

```go
// internal/input/cgevent/cgevent_darwin.go  (build tag: cgevent)

// Probe returns true on macOS (the API always exists); it does NOT guarantee
// Accessibility permission — that is checked in New with an actionable error.
func Probe() bool

// New creates the injector. Returns ErrNoAccessibility if not trusted.
func New(cfg input.InjectorConfig) (input.KeyMouseInjector, error)
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| No Accessibility permission | `New` returns `ErrNoAccessibility`, prompts, opens Settings pane; view-only until granted |
| `CGEventCreate*` returns NULL | log + skip that event; do not crash |
| Unmappable HID usage | log once; drop the key |
| Permission revoked mid-session | injection silently no-ops (OS behavior); detect via a periodic `AXIsProcessTrusted` check and warn |

---

## File Structure

```
internal/input/cgevent/
├── cgevent_darwin.go     // build tag: cgevent (KeyMouseInjector impl, CGo)
├── keymap.go             // HID usage → kVK_* (generated)
├── accessibility.go      // AXIsProcessTrusted check + prompt
├── stub.go               // build tag: !cgevent (no-op, never registers)
└── cgevent_test.go
```

---

## Configuration

Reads `[addon_module_cgevent]` (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_cgevent]
prompt_accessibility = true   # auto-open the Accessibility pane if not trusted
```

If absent, defaults apply. Strictly validated only when this add-on is compiled in.

---

## Status

📋 Specced — not yet built. Implementation order: Accessibility probe/prompt →
key injection (kVK map + flag tracking) → mouse abs/rel/button → scroll.
