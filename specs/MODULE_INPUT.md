# Module Spec: Input

> # ⏸️ DEFERRED
>
> **Status:** Deferred until video capture+encode is stable across all three OSes
> (Linux, macOS, Windows).
>
> **Why:** Input is currently a Linux-only uinput implementation. Properly
> cross-platform input injection (SendInput on Windows, CGEventPost on macOS,
> uinput/libei on Linux) needs its own design pass. The current spec also
> contradicts confirmed architectural decisions (`*slog.Logger`, no custom
> Logger interface).
>
> **Not core.** This module has been removed from the CENTRAL_SPEC module map.
> The content below is preserved for reference but should not be treated as
> current architecture.
>
> **Trigger to un-defer:** Video capture+encode add-ons working end-to-end on
> Linux + macOS + Windows with the bench harness producing comparable numbers.

---

## Overview

The Input module handles remote input injection. It receives input events from the browser client (keyboard, mouse, wheel) over WebSocket, translates them from W3C web standards to Linux input event codes, and injects them into the kernel via the uinput subsystem.

---

## Public Interface

```go
package input

// InputHandler processes remote input events.
type InputHandler interface {
    // HandleRawMessage parses and injects a single input event.
    // data is a JSON-encoded message from the WebSocket client.
    // Returns the message Seq (for InputAck) and any injection error.
    HandleRawMessage(data []byte) (seq uint32, err error)

    // Resize destroys and recreates the uinput device with new ABS_X/ABS_Y
    // ranges. Called by the pipeline on a resolution change so absolute
    // coordinates keep mapping 1:1 to the stream.
    Resize(width, height int) error

    // Close destroys the virtual input device.
    Close() error
}

// InputConfig configures the virtual input device.
type InputConfig struct {
    Width  int // Screen width (for absolute mouse positioning)
    Height int // Screen height (for absolute mouse positioning)
    Logger Logger
}

// InputMessage represents a parsed input event from the client.
type InputMessage struct {
    Type   string `json:"type"`             // "key", "mousemove", "mousedown", "mouseup", "wheel"
    Seq    uint32 `json:"seq,omitempty"`    // Per-connection monotonic id; echoed in InputAck
    Event  string `json:"event,omitempty"`  // "down" or "up" (for key events)
    Code   string `json:"code,omitempty"`   // W3C KeyboardEvent.code (e.g., "KeyA")
    Button int    `json:"button,omitempty"` // Mouse button index (0=left, 1=middle, 2=right)
    X      int    `json:"x,omitempty"`      // Absolute X in STREAM coordinate space (Config dims)
    Y      int    `json:"y,omitempty"`      // Absolute Y in STREAM coordinate space (Config dims)
    DeltaY int    `json:"deltaY,omitempty"` // Wheel scroll delta
}
```

**InputAck / latency:** after injecting an event, the server (not this module) emits a `FrameTypeInputAck` carrying `Seq` + a server timestamp. The client measures input round-trip latency from it. The input module's `HandleRawMessage` returns the parsed `Seq` (or the server reads it) so the server can ack.

**Coordinate-space constraint:** `X`/`Y` are already in the stream's pixel space (the client scaled them using the latest `Config` width/height). The uinput device's `ABS_X`/`ABS_Y` range MUST equal those same dims. The pipeline creates/resizes the device to match capture dims; there is no scaling inside this module.

---

## Internal Architecture

### Virtual Device (uinput)

```
/dev/uinput → open(O_RDWR|O_CLOEXEC)
    → ioctl(UI_SET_EVBIT, EV_KEY)     // Enable key events
    → ioctl(UI_SET_EVBIT, EV_REL)     // Enable relative events (wheel)
    → ioctl(UI_SET_EVBIT, EV_ABS)     // Enable absolute events (mouse)
    → ioctl(UI_SET_KEYBIT, KEY_*)     // Register all key codes
    → ioctl(UI_SET_RELBIT, REL_WHEEL) // Register wheel axis
    → ioctl(UI_SET_ABSBIT, ABS_X/Y)  // Register absolute axes
    → write(uinput_user_dev{...})     // Device descriptor (name, ID, abs ranges)
    → ioctl(UI_DEV_CREATE)            // Finalize: device appears in /dev/input/
```

**Device Identity:**
- Name: "viewport-rds" (or configurable)
- Bus: BUS_USB (0x03)
- Vendor: 0x1234
- Product: 0x5678
- Version: 1

### Event Injection

All injection follows the pattern: `write(input_event{type, code, value})` + `write(SYN_REPORT)`

| Method | Event Type | Code | Value |
|--------|-----------|------|-------|
| `InjectKey(code, down)` | EV_KEY | Linux keycode | 1=down, 0=up |
| `InjectMouseMove(x, y)` | EV_ABS | ABS_X, ABS_Y | Pixel coordinate |
| `InjectMouseButton(btn, down)` | EV_KEY | BTN_LEFT/RIGHT/MIDDLE | 1=down, 0=up |
| `InjectWheel(delta)` | EV_REL | REL_WHEEL | +1 or -1 |

### Keymap Translation

Browser `KeyboardEvent.code` (W3C standard, physical key position) → Linux `KEY_*` scancode:

**Covered:**
- Letters: KeyA-KeyZ → KEY_A-KEY_Z (26 keys)
- Digits: Digit0-Digit9 → KEY_0-KEY_9 (10 keys)
- Function: F1-F12 → KEY_F1-KEY_F12 (12 keys)
- Modifiers: Shift/Control/Alt/Meta Left+Right (8 keys)
- Navigation: Arrow keys, Home, End, PageUp, PageDown (8 keys)
- Editing: Backspace, Tab, Enter, Escape, Delete, Insert, Space (7 keys)
- Punctuation: 10 keys
- Lock/System: CapsLock, NumLock, ScrollLock, PrintScreen, Pause, ContextMenu (6 keys)

**Missing (to be added):**
- Numpad: Numpad0-9, NumpadEnter, NumpadAdd, NumpadSubtract, etc.
- Media: AudioVolumeUp/Down, MediaPlayPause, etc.
- F13-F24
- International keys

### Mouse Button Translation

| Browser Button | Linux Code |
|----------------|-----------|
| 0 (left) | BTN_LEFT (0x110) |
| 1 (middle) | BTN_MIDDLE (0x112) |
| 2 (right) | BTN_RIGHT (0x111) |

### Message Dispatch Flow

```
WebSocket text frame (JSON)
    → ParseMessage([]byte) → *InputMessage
    → HandleMessage(device, msg):
        switch msg.Type:
            "key"       → BrowserCodeToLinux(msg.Code) → InjectKey(code, down)
            "mousemove" → InjectMouseMove(msg.X, msg.Y)
            "mousedown" → MouseButtonToLinux(msg.Button) → InjectMouseButton(btn, true)
            "mouseup"   → MouseButtonToLinux(msg.Button) → InjectMouseButton(btn, false)
            "wheel"     → normalize(msg.DeltaY) → InjectWheel(±1)
```

---

## Refactoring Directives

### R-INP-01: Surface Injection Errors
`HandleMessage` currently discards all errors from `Inject*` methods. Propagate errors back to caller and log them. A broken fd should trigger device recreation or a fatal error.

### R-INP-02: Validate Mouse Coordinates
Add bounds clamping in `InjectMouseMove`: `x = clamp(x, 0, width-1)`, `y = clamp(y, 0, height-1)`. Out-of-bounds values can confuse the kernel input layer.

### R-INP-03: Preserve Wheel Magnitude
The current normalization discards scroll magnitude (all deltas become ±1). Implement high-resolution scrolling using `REL_WHEEL_HI_RES` or scale the delta: `delta = clamp(msg.DeltaY / 120, -10, 10)`.

### R-INP-04: Complete Keymap Coverage
Add numpad keys, media keys, F13-F24, and international keys to `browserToLinux` map. Consider generating the map from the Linux input-event-codes.h header.

### R-INP-05: Remove Dead Code
- `inputEvent` struct (lines 40-46) is defined but never used
- Remove or use it in `writeEvent`

### R-INP-06: Use Typed Message Variants
Replace the flat `InputMessage` struct with a discriminated union pattern:
```go
type InputEvent interface{ inputEvent() }
type KeyEvent struct { Code string; Down bool }
type MouseMoveEvent struct { X, Y int }
type MouseButtonEvent struct { Button int; Down bool }
type WheelEvent struct { DeltaY int }
```

### R-INP-07: Dynamic Resolution Update
Add a `Resize(width, height int)` method that destroys and recreates the uinput device with new ABS_X/ABS_Y ranges. Wire this to the capture module's resolution change events.

### R-INP-08: Extract Interface to `pkg/input`
Move `InputHandler` and `InputConfig` to a public package. Keep uinput implementation in `internal/input/uinput/`.

### R-INP-09: Ioctl Error Handling in NewDevice
Check return values of all `ioctl` calls during device setup. If a capability registration fails, return a descriptive error rather than creating a partially-functional device.

### R-INP-10: Add Horizontal Wheel Support
The constant `relHWheel` is defined but never used. Add horizontal scroll injection triggered by `deltaX` in wheel events.

---

## Testing Strategy

| Level | What | Hardware Required |
|-------|------|-------------------|
| Unit | JSON parsing (all message types, invalid input) | No |
| Unit | Keymap translation (all keys, unknown keys) | No |
| Unit | Mouse button translation | No |
| Unit | Wheel normalization logic | No |
| Integration | Full device lifecycle (create, inject, close) | Yes (/dev/uinput) |
| Integration | Verify events appear in `evtest` output | Yes (/dev/uinput) |
| Mock | Fake device for server testing | No |

---

## Security Considerations

- `/dev/uinput` requires elevated permissions (root or `input` group membership)
- The virtual device can inject ANY input event into the system
- Only the designated "controller" client should have input privileges
- Consider rate-limiting injection to prevent event flooding attacks
- Consider an event type whitelist (no SYS_* or dangerous key combos by default)
