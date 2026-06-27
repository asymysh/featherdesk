# Specification: Input Injection via uinput

## Overview

Add keyboard and mouse input injection so the browser client can control the host machine. Input events arrive as JSON text frames over WebSocket, get translated to Linux input event codes, and are injected via virtual uinput devices.

## Dependencies

- Track 1 (KMS capture + software encode + WebSocket viewer) must be complete
- Existing WebSocket text frame handling from Track 1

## Functional Requirements

### FR-1: Virtual Device Creation
- Create virtual keyboard via /dev/uinput (all standard KEY_* codes)
- Create virtual mouse with absolute positioning (ABS_X, ABS_Y)
- Register mouse buttons: BTN_LEFT, BTN_RIGHT, BTN_MIDDLE
- Register mouse wheel: REL_WHEEL, REL_HWHEEL
- Set ABS resolution to match capture dimensions
- Clean destroy (UI_DEV_DESTROY) on shutdown

### FR-2: Input Event Translation
- Map browser `event.code` strings (e.g., "KeyA", "ShiftLeft", "ArrowUp") to Linux KEY_* constants
- Map browser mouse coordinates (viewport-relative) to absolute uinput coordinates
- Map browser mouse buttons (0, 1, 2) to BTN_LEFT, BTN_MIDDLE, BTN_RIGHT
- Map browser wheel deltaY to REL_WHEEL events

### FR-3: Event Injection
- Write input_event structs to uinput fd followed by SYN_REPORT
- Key events: EV_KEY with value 1 (down) or 0 (up)
- Mouse move: EV_ABS with ABS_X and ABS_Y
- Mouse buttons: EV_KEY with BTN_* and value 1/0
- Mouse wheel: EV_REL with REL_WHEEL
- All events followed by EV_SYN / SYN_REPORT

### FR-4: Input Protocol (JSON Text Frames)
- Key: `{"type":"key","event":"down|up","code":"KeyA"}`
- Mouse move: `{"type":"mouse","event":"move","x":100,"y":200}`
- Mouse button: `{"type":"mouse","event":"down|up","button":0}`
- Mouse wheel: `{"type":"mouse","event":"wheel","dy":-120}`
- Input ACK: server sends back acknowledgment for latency measurement

### FR-5: Web Client Input Capture
- Capture keydown/keyup on document (prevent defaults)
- Capture pointermove, pointerdown, pointerup on canvas
- Capture wheel events on canvas
- Track pressed keys, release all on blur/visibility change
- Coordinate mapping: client viewport -> host screen resolution
- Mouse move throttle (~8ms minimum between sends)

## Non-Functional Requirements

- Input injection latency: <1ms (write to fd is synchronous)
- No cgo required (pure Go, just ioctl syscalls)
- Server process needs: `input` group membership or CAP_DAC_OVERRIDE

## Acceptance Criteria

1. Typing in browser appears as keystrokes on host desktop
2. Mouse movement in browser moves host cursor to correct position
3. Mouse clicks and scroll work correctly
4. Input ACK round-trip measurable in client UI
5. All keys released when browser loses focus
6. Server exits cleanly (destroys virtual devices)

## Out of Scope

- Role-based access control (Track 5)
- Gamepad input
- Touch/pen input
- Clipboard injection
