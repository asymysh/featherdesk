# macOS Input Add-On: CGEvent

## Purpose

The `cgevent` add-on is the macOS keyboard+mouse injection backend for the core
Input module (see [`specs/MODULE_INPUT.md`](../../../../specs/MODULE_INPUT.md)).
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
// carry modifier flags so shortcuts work:
CGEventSetFlags(e, currentFlags); // kCGEventFlagMaskShift/Control/Alternate/Command
CGEventPost(kCGHIDEventTap, e);
CFRelease(e);

// Absolute mouse move (stream pixel → global display point):
CGPoint p = CGPointMake(globalX, globalY);
CGEventRef m = CGEventCreateMouseEvent(NULL, kCGEventMouseMoved, p, kCGMouseButtonLeft);
CGEventPost(kCGHIDEventTap, m); CFRelease(m);

// Relative mouse (pointer lock): post mouseMoved with delta fields set:
CGEventSetIntegerValueField(m, kCGMouseEventDeltaX, dx);
CGEventSetIntegerValueField(m, kCGMouseEventDeltaY, dy);

// Buttons: kCGEventLeftMouseDown/Up, RightMouse*, OtherMouse* (with button number)
// Scroll: CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2, dy, dx)
```

### HID-usage → virtual key (kVK_*)

The add-on maps the core's neutral HID usage IDs to macOS virtual key codes
(`Carbon/HIToolbox Events.h` `kVK_*`). Generated for full coverage. Modifier
state is tracked and applied via `CGEventSetFlags` so combinations (Cmd+C, etc.)
behave correctly.

### Coordinate mapping

Stream-pixel coordinates map to the global display coordinate space. For the
single-display target, `globalX = x`, `globalY = y` when the captured display is
the main display at origin (0,0). `Resize` updates the width/height used for
clamping. (Multi-monitor is out of scope.)

### Modifier & flag handling

`CGEventCreateKeyboardEvent` alone does not set modifier masks; the add-on
maintains the live modifier set (which of Shift/Control/Option/Command are held)
from the decoded key stream and applies it to every event via `CGEventSetFlags`.

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
go build -tags "cgevent" -o viewport-rds-macos ./cmd/server
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

Reads `[addon_module_cgevent]` (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_cgevent]
prompt_accessibility = true   # auto-open the Accessibility pane if not trusted
```

If absent, defaults apply. Strictly validated only when this add-on is compiled in.

---

## Status

📋 Specced — not yet built. Implementation order: Accessibility probe/prompt →
key injection (kVK map + flag tracking) → mouse abs/rel/button → scroll.
