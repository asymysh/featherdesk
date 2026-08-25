# User Stories: Input & Control

QA acceptance-criteria stories covering keyboard/mouse injection, scroll,
gamepad, and input-latency acknowledgement, plus two regression-guard stories
for real bugs found in the prior (Go) web client implementation. Source specs:
`specs/interaction/MODULE_INPUT.md`, `specs/CENTRAL_SPEC.md`, plus
`PROJECT_ARTIFACTS/summaries/input_injection_uinput/phase4.md`.

---

## US-INP-1: Keyboard/mouse input is injected by default via `enigo`

**As a** controller (the client holding the control role)
**I want** my keyboard and mouse actions to be injected into the host's OS
**So that** I can actually operate the remote desktop, not just view it

**Acceptance Criteria:**
- Given `[input] enabled = true` and no kernel add-on (`interception`/`uinput`) is loaded, When a `KeyEvent` (Type `0x10`) record arrives on the input stream, Then the built-in `enigo` injector maps its HID usage to the platform keycode and injects a press/release.
- Given the client is not the designated controller, When it sends input-stream records, Then the server drops them at the server before they reach the dispatcher — only the controller's frames are injected.
- Given a record's declared length doesn't match the expected size for its Type, When the dispatcher validates it, Then the whole frame is rejected before any field is read or any injection occurs.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Overview", "Dispatch Flow", "Security Considerations"

---

## US-INP-2: `[input] enabled = false` forces view-only mode

**As a** host user
**I want** to be able to disable all input injection regardless of which injector add-ons are loaded
**So that** I can offer a pure screen-share without ever risking remote control

**Acceptance Criteria:**
- Given `[input] enabled = false`, When a client sends any input record, Then the server passes it to a no-op stub or omits the dispatcher entirely — no OS-level injection occurs, even if a kernel add-on like `uinput` or `interception` is loaded.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Configuration", "Public Interface" (`new_dispatcher` view-only note)

---

## US-INP-3: Absolute mouse movement is clamped to the stream's coordinate space

**As a** controller
**I want** my pointer position on the viewer to map correctly onto the host's screen
**So that** clicks land exactly where I expect, even after a resolution change

**Acceptance Criteria:**
- Given a `MouseMoveAbs` record (Type `0x20`) with X/Y in stream-pixel space, When the dispatcher processes it, Then X is clamped to `[0, width-1]` and Y to `[0, height-1]` before injection.
- Given the stream's dimensions change (resolution change), When the pipeline calls `Dispatcher::resize`, Then the injector's absolute-coordinate range is updated to match, so injected positions stay correct without any add-on-internal scaling.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Wire Format" (MouseMoveAbs), "Coordinate-space constraint", "Dispatch Flow" (clamp step)

---

## US-INP-4: Relative mouse movement supports pointer-lock / FPS-style control

**As a** controller playing a game or using an app that needs relative mouse input
**I want** relative mouse deltas to be injected directly
**So that** pointer-lock-based interactions (camera look, precision drawing) work correctly

**Acceptance Criteria:**
- Given `[input] relative_mouse = true`, When a `MouseMoveRel` record (Type `0x21`, `i16 Dx`/`Dy`) arrives, Then the injector applies the relative delta via `inject_pointer_rel` rather than treating it as an absolute position.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Wire Format" (MouseMoveRel), "Configuration"

---

## US-INP-5: Mouse button clicks inject with correct button mapping

**As a** controller
**I want** left/middle/right/back/forward clicks to map correctly to host mouse buttons
**So that** context menus, drag operations, and browser back/forward all behave as expected

**Acceptance Criteria:**
- Given a `MouseButton` record (Type `0x22`) with a W3C button index (0=left, 1=middle, 2=right, 3=back, 4=forward) and a down/up flag, When the dispatcher processes it, Then `inject_button` is called with that exact button index and down state.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Wire Format" (MouseButton), "Public Interface" (`inject_button`)

---

## US-INP-6: Scroll wheel input translates correctly across pixel/line/page units

**As a** controller
**I want** my scroll gestures to translate correctly regardless of the host OS's native scroll convention
**So that** scrolling feels natural rather than inverted or the wrong speed

**Acceptance Criteria:**
- Given a `Scroll` record (Type `0x23`) with high-resolution `i16 Dx`/`Dy` in W3C sign convention (positive = down/right), When the injector prepares to inject it, Then it negates Dx and Dy before injection, since every host scroll API (Windows `WHEEL_DELTA`, Linux `REL_WHEEL`, macOS `kCGScrollEventDelta*`) uses the opposite sign convention.
- Given `Unit=0` (pixel), When injecting, Then the OS's pixel/high-res scroll path is used directly.
- Given `Unit=1` (line), When injecting, Then the translation is `lines * 120` for Windows `WHEEL_DELTA`, `lines` for Linux `REL_WHEEL`, and `lines * 10` (px) for macOS line mode.
- Given `Unit=2` (page), When injecting, Then it translates as 3 lines per page on every OS.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Wire Format" (Scroll record, sign convention, unit translation)

---

## US-INP-7: Gamepad events are routed when a gamepad injector is loaded, dropped gracefully otherwise

**As a** controller using a connected gamepad
**I want** my gamepad input to reach the host when a gamepad add-on is loaded
**So that** I can play games remotely, without the server crashing or misbehaving when no gamepad add-on exists

**Acceptance Criteria:**
- Given a `GamepadInjector` add-on (`vigem`/`gcvirtual`/`uinput`) is loaded, When a `GamepadState`/`GamepadConnect`/`GamepadDisconnect` record (Types `0x40`-`0x42`) arrives, Then the dispatcher routes it to the loaded gamepad injector.
- Given no `GamepadInjector` is loaded, When the same record types arrive, Then they are dropped without error (no crash, no injection attempt).
- Given a `player`-role (co-op) client sends a gamepad record with a local gamepad index, When the server processes it, Then it remaps the local index to that client's assigned global slot before injection.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Dispatch Flow" (gamepad routing, co-op remap note)

---

## US-INP-8: Input latency is measurable via an acknowledgement round-trip

**As a** controller
**I want** to see how much latency exists between my input and the host acknowledging it
**So that** I can judge whether my connection is suitable for latency-sensitive use (e.g. gaming)

**Acceptance Criteria:**
- Given the dispatcher successfully injects an input record, When the server processes it, Then it sends a 13-byte `InputAck` (`[Type=14][Seq u32][RecvTimestampNs u64]`), length-prefixed `[u16 RecLen=13]`, back on the input stream.
- Given the `Seq` in the ack matches the `Seq` the client sent, When the client receives the ack, Then it can compute round-trip input latency from the timestamps.

**Validated by:** specs/interaction/MODULE_INPUT.md — "Public Interface" (InputAck / latency), "Wire Format" (Shared 6-byte record header, Seq echoed in InputAck)

---

## US-INP-9: A key held down when the browser tab loses focus must not stay stuck on the host

**As a** viewer/controller
**I want** any key I'm holding down to be released on the host if my browser tab loses focus or becomes hidden
**So that** the host never ends up with a phantom stuck key (e.g. a held Shift or W) after I alt-tab away or switch tabs

**Acceptance Criteria:**
- Given a key is currently held down (a `KeyEvent` down record was sent but no matching up record yet), When the browser tab fires a `blur` or `visibilitychange` (hidden) event, Then the client sends up-events for every currently-held key before/as focus is lost.
- Given the client-side pressed-key state is tracked (e.g. a `Set` of currently-down keys), When a blur/visibilitychange fires, Then that tracked state is used to determine exactly which keys need synthetic release events — not just the single most recent key.
- Given the controller slot changes hands (disconnect, takeover) without the client having sent all up-events, When the server detects the change, Then `Dispatcher::release_all` injects an up-event for every key/button/touch still held in the server-side pressed-set, as a second line of defense.

**Validated by:** specs/client/MODULE_WEB_CLIENT.md — **R-CLI-12** "Held-Input Release on Focus Loss" (client-side `heldKeys`/`heldButtons` Sets + `releaseAllHeld()` on `blur` and `visibilitychange`), and specs/interaction/MODULE_INPUT.md — `Dispatcher::release_all` (server-side second line of defense on controller-slot release/seizure or `Drop`). R-CLI-14 additionally orders `releaseAllHeld()` *first* in teardown, while the transport is still open.

> Was flagged `GAP` in the first pass — the client-side half genuinely had no owning spec. Closed by R-CLI-12/R-CLI-14. Both layers of the story's acceptance criteria now have an owner.
**Regression guard:** PROJECT_ARTIFACTS/summaries/input_injection_uinput/phase4.md — T9 documents that the shipped web client's keyboard capture had no pressed-keys `Set` and no `blur`/`visibilitychange` handler, so a key held down when the tab lost focus stayed stuck pressed on the host.

---

## US-INP-10: Scroll-wheel speed/magnitude is preserved end-to-end, not collapsed to a fixed step

**As a** controller
**I want** a fast, large scroll gesture to scroll further/faster than a small one on the host
**So that** scrolling feels proportional and responsive rather than uniformly slow regardless of gesture speed

**Acceptance Criteria:**
- Given the client captures a `wheel` event with a `deltaY` magnitude, When it constructs the `Scroll` record, Then it encodes the actual high-resolution `i16 Dy` magnitude from the browser event — not a fixed/quantized step size.
- Given a large `deltaY` (fast scroll) vs. a small `deltaY` (slow scroll), When both are injected on the host, Then the resulting host-side scroll amount is proportionally larger for the fast gesture, matching the wire magnitude (after the required sign negation).
- Given the client may batch multiple wheel samples per animation frame, When it sends them, Then the specced batching/coalescing window is honored rather than sending unconditionally on every event (which would defeat any smoothing without fixing the magnitude-collapse defect).

**Validated by:** specs/interaction/MODULE_INPUT.md — "Wire Format" (Scroll record: `i16 Dy` high-res magnitude, sign convention)
**Regression guard:** PROJECT_ARTIFACTS/summaries/input_injection_uinput/phase4.md — T10 documents that the shipped web client's `wheel` handler sent `deltaY` unconditionally with no batching, and (per the broader review of that code path) scroll magnitude was collapsed to a fixed step rather than preserving the gesture's actual speed.
