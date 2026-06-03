# Implementation Plan: Input Injection via uinput

## Phase 1: Virtual Device Setup

- [ ] Task: Implement uinput device creation (pure Go)
    - [ ] Open /dev/uinput with O_WRONLY|O_NONBLOCK
    - [ ] ioctl UI_SET_EVBIT for EV_KEY, EV_ABS, EV_REL, EV_SYN
    - [ ] ioctl UI_SET_KEYBIT for all KEY_* codes (KEY_A through KEY_Z, digits, function keys, modifiers, etc.)
    - [ ] ioctl UI_SET_KEYBIT for BTN_LEFT, BTN_RIGHT, BTN_MIDDLE
    - [ ] ioctl UI_SET_ABSBIT for ABS_X, ABS_Y with min=0, max=capture_width/height
    - [ ] ioctl UI_SET_RELBIT for REL_WHEEL, REL_HWHEEL
    - [ ] Write uinput_user_dev struct and ioctl UI_DEV_CREATE
    - [ ] Implement Destroy() for clean shutdown
- [ ] Task: Write device creation tests
    - [ ] Test open/create succeeds (requires input group or root)
    - [ ] Test destroy closes fd
    - [ ] Test ABS range matches specified dimensions
    - [ ] Build-tag gate: `//go:build integration`
- [ ] Task: Conductor - User Manual Verification 'Virtual Device Setup' (Protocol in workflow.md)

## Phase 2: Event Translation & Injection

- [ ] Task: Implement browser-to-Linux keycode mapping
    - [ ] Create map: browser code string -> Linux KEY_* constant
    - [ ] Cover: letters (KeyA-KeyZ), digits (Digit0-9), F-keys (F1-F24)
    - [ ] Cover: modifiers (ShiftLeft/Right, ControlLeft/Right, AltLeft/Right, MetaLeft/Right)
    - [ ] Cover: navigation (ArrowUp/Down/Left/Right, Home, End, PageUp/Down)
    - [ ] Cover: editing (Backspace, Tab, Enter, Escape, Delete, Insert, Space)
    - [ ] Cover: punctuation (Semicolon, Equal, Comma, Minus, Period, Slash, Backquote, BracketLeft/Right, Backslash, Quote)
    - [ ] Write unit tests for all mapped codes
- [ ] Task: Implement input event injection
    - [ ] Implement InjectKey(code uint16, down bool) - writes EV_KEY + SYN_REPORT
    - [ ] Implement InjectMouseMove(x, y int32) - writes EV_ABS for X and Y + SYN_REPORT
    - [ ] Implement InjectMouseButton(button uint16, down bool) - writes EV_KEY + SYN_REPORT
    - [ ] Implement InjectWheel(delta int32) - writes EV_REL + SYN_REPORT
    - [ ] All inject functions write input_event struct (time, type, code, value)
- [ ] Task: Write injection unit tests
    - [ ] Test input_event struct serialization (16 bytes, correct layout)
    - [ ] Test SYN_REPORT is appended after each event
    - [ ] Test coordinate scaling (browser coords -> ABS range)
- [ ] Task: Conductor - User Manual Verification 'Event Translation & Injection' (Protocol in workflow.md)

## Phase 3: WebSocket Integration

- [ ] Task: Implement JSON input message parsing
    - [ ] Define Go structs for key/mouse messages
    - [ ] Parse incoming text WebSocket frames as JSON
    - [ ] Route by "type" field: "key" -> InjectKey, "mouse" -> InjectMouse*
    - [ ] Handle "input_ping" -> send input ACK binary frame
    - [ ] Ignore unknown message types (forward compatibility)
- [ ] Task: Implement input ACK for latency measurement
    - [ ] On input with ack_id field: send back protocol frame (type=input_ack, p1=ack_id)
    - [ ] Client measures round-trip time from send to ACK receipt
- [ ] Task: Write integration tests
    - [ ] Test JSON parsing for all message types
    - [ ] Test invalid JSON doesn't crash
    - [ ] Test input ACK response is well-formed
- [ ] Task: Conductor - User Manual Verification 'WebSocket Integration' (Protocol in workflow.md)

## Phase 4: Web Client Input Capture

- [ ] Task: Implement keyboard capture in compositor.js
    - [ ] Add keydown/keyup listeners on document
    - [ ] Prevent default for all keys when canvas is focused
    - [ ] Track pressed keys in Set, release all on blur/visibilitychange
    - [ ] Send JSON text frame: {"type":"key","event":"down|up","code":"..."}
    - [ ] Include ack_id on keydown for latency measurement
- [ ] Task: Implement mouse capture in compositor.js
    - [ ] pointermove on canvas: throttle to 8ms, map coords to host resolution
    - [ ] pointerdown/pointerup on canvas: send with button index and ack_id
    - [ ] wheel on canvas: batch at 16ms, send deltaY
    - [ ] Prevent context menu on right-click
    - [ ] Focus canvas on pointerdown
- [ ] Task: Implement input latency display
    - [ ] Track pending ACKs with timestamps
    - [ ] Calculate rolling average of last 20 round-trips
    - [ ] Display in status bar: "Input X ms"
- [ ] Task: Conductor - User Manual Verification 'Web Client Input Capture' (Protocol in workflow.md)
