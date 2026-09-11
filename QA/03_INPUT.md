# QA 03 — Input: keyboard, mouse, touch, gamepad

Matching user stories: `PROJECT_ARTIFACTS/user_stories/04_input_control.md`.

Input is where "it basically works" hides the most defects, because a wrong key
or a 10 px offset still looks like a working system. Test on a **real desktop
with real applications**, not a test page.

## Keyboard

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-INP-1 | Type the full alphabet, digits and punctuation into a host text editor | Every character arrives correctly | `MODULE_INPUT` HID contract | ☐ |
| QA-INP-2 | **F1–F24** | All twenty-four work. F13–F24 are the ones that silently failed before — test them explicitly | TD-37 | ☐ |
| QA-INP-3 | Modifiers: L/R Ctrl, Shift, Alt, Meta independently | Each is distinguishable host-side; right-vs-left is preserved | `MODULE_INPUT` coverage list | ☐ |
| QA-INP-4 | Navigation, editing, numpad, lock and media keys | All present and correct | `MODULE_INPUT` coverage list | ☐ |
| QA-INP-5 | Chords: Ctrl+C, Ctrl+Shift+T, Alt+Tab, Super | Reach the host application (Alt+Tab and Super may be captured by the browser unless in a pointer-lock/keyboard-lock mode — record which) | `MODULE_INPUT` | ☐ |
| QA-INP-6 | **Ctrl+Alt+Del** with the `interception` add-on (Windows) | Reaches the **system**, via `send_sas()`, once per chord | `MODULE_INPUT` "Ctrl+Alt+Del Chord Detection" | ☐ |
| QA-INP-7 | Ctrl+Alt+Del **without** `SECURE_ATTENTION` (default `enigo`) | Chord swallowed, one log line, and the constituent keys are **not** injected as ordinary keys. Delivering Ctrl+Alt+Del to the foreground app instead of the system would be worse than doing nothing | `MODULE_INPUT` step 3b–3d | ☐ |
| QA-INP-8 | Hold a key, then switch browser tabs / minimise / alt-tab away | The key is released on the host. Nothing stays held down | TD-35; US-INP-9 | ☐ |
| QA-INP-9 | Hold a key, then have the controller slot taken over | Same — held input released on slot change, before the new controller's first record | TD-35; US-MC-10 | ☐ |
| QA-INP-10 | Hold a key, then kill the client's network (both carriers) | Released on session timeout | QA-CON-11 | ☐ |
| QA-INP-11 | **Host keyboard layout ≠ client layout** (e.g. host AZERTY, client QWERTY) | Record the actual behaviour. HID usages are *physical positions*, so characters follow the **host** layout. This is a known limitation with no v1 mitigation — the check exists so the behaviour is documented rather than discovered by a user | `MODULE_INPUT` HID contract; `GAP_TRIAGE.md` "Never triaged" (keyboard layout + IME + Unicode text entry) | ☐ |

## Pointer

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-INP-12 | Absolute pointer accuracy at all four corners and centre | Lands exactly. No systematic offset | `MODULE_INPUT` coordinate rule | ☐ |
| QA-INP-13 | Same, with the stream **scaled** (client window ≠ host native) | Still exact. Scaling errors show up here first | `MODULE_INPUT` | ☐ |
| QA-INP-14 | Same, on a **DPI-scaled** host display (Windows 150 %) | Still exact — this is what `PER_MONITOR_AWARE_V2` exists for | `DXGI_DD_WINDOWS_SPEC` | ☐ |
| QA-INP-15 | Same, after a mid-session resolution change | Input rescales with it (`Dispatcher::resize`), no offset creeps in | TD-05 / TD-27 | ☐ |
| QA-INP-16 | Left / right / middle / back / forward buttons | All map correctly | `MODULE_INPUT` | ☐ |
| QA-INP-17 | Pointer lock (relative mode) in a 3D application or game | Smooth relative motion, no clamping at screen edges, no pointer escaping | R-CLI-04 | ☐ |
| QA-INP-18 | **Scroll magnitude**: a mouse notch vs a fast trackpad fling | Distinguishable on the host — magnitude preserved, not collapsed to ±1. Test pixel, line and page units | TD-38 | ☐ |
| QA-INP-19 | Scroll direction on all three OSes | Positive wire `Dy` scrolls content down and `Dx` right, consistently | `MODULE_INPUT` testing table | ☐ |
| QA-INP-20 | High-polling-rate mouse (1000 Hz) moved rapidly | Rate limit + `mousemove` coalescing keep the transport sane; motion still feels smooth | TD-36 | ☐ |

## Touch

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-INP-21 | Single tap, drag, and two-finger gesture from a touch client | Correct contacts land on the host (Windows `win_touch`, Linux `uinput`) | `MODULE_INPUT` touch records | ☐ |
| QA-INP-22 | Lift all fingers while the tab loses focus mid-gesture | All live contacts released; nothing stuck | `release_all` incl. touch ids | ☐ |

## Gamepad

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-INP-23 | Connect a controller in the browser; a host game sees it | Buttons and axes correct, no drift, no inverted axis | `MODULE_GAMEPAD` | ☐ |
| QA-INP-24 | Disconnect and reconnect mid-session | Connect/disconnect records handled; the virtual pad appears and disappears cleanly | `MODULE_GAMEPAD` | ☐ |
| QA-INP-25 | Rumble from a host game | Reaches the browser gamepad; respects `[gamepad] allow_rumble = false` | US-INP-11 | ☐ |
| QA-INP-26 | Co-op: `role = "player"` clients on slots 1..3 | Each drives only its own slot; a player cannot send keyboard, mouse, clipboard, file transfer or parameter changes | `MODULE_SERVER` "Role gate table" | ☐ |
| QA-INP-27 | `gcvirtual` on macOS with a **non**-GameController-framework game | Documented limitation: only GameController-framework apps see the pad. Confirm the behaviour is what the spec says rather than a silent no-op | `GCVIRTUAL_MACOS_SPEC` | ☐ |

## Latency and gating

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-INP-28 | Input latency shown in the HUD | A real measured number from `InputAck` round-trip, updating live and plausible for the link | R-CLI-13; TD-32 | ☐ |
| QA-INP-29 | `[input] enabled = false` | View-only: no keyboard, mouse, touch or gamepad reaches the host, even for the controller | `MODULE_INPUT` config | ☐ |
| QA-INP-30 | A **viewer** attempts to open the input stream (`0x01`) | Stream reset at stream scope with `PROTOCOL_ERROR`; the session survives | `MODULE_SERVER` role gate row 1 | ☐ |
| QA-INP-31 | Malformed input record (wrong length for its type, out-of-range field) | Rejected before injection; nothing reaches the kernel input layer; the session is not crashed | `MODULE_INPUT` "Binary bounds" | ☐ |
| QA-INP-32 | Type `0x50` (reserved for webcam) sent by a client | Dropped by current binaries without error | `MODULE_INPUT` type ranges | ☐ |
