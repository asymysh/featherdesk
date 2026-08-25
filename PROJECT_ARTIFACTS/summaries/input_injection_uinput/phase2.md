# Phase 2: Event Translation & Injection - Summary

## T3 - Implement browser-to-Linux keycode mapping
- **Commit:** d5d6d9b
- **Changes:** `browserToLinux` map covering letters, digits, F1-F12, modifiers, navigation, editing, punctuation, plus extras (CapsLock/NumLock/ScrollLock/PrintScreen/Pause/ContextMenu). F13-F24 (specced) are missing.
- **Files:** `internal/input/keymap.go`
- **Why:** Translates browser `KeyboardEvent.code` strings into Linux `KEY_*` constants for injection.

## T4 - Implement input event injection
- **Commit:** d5d6d9b
- **Changes:** `InjectKey`/`InjectMouseMove`/`InjectMouseButton`/`InjectWheel`, each writing the appropriate `input_event` + `SYN_REPORT`. The wheel call site (in `protocol.go`, T6) collapses `deltaY` magnitude to a fixed ±1 step.
- **Files:** `internal/input/device.go`
- **Why:** The actual mechanism that turns a parsed input message into a kernel-level input event.

## T5 - Write injection unit tests
- **Commit:** N/A — not implemented
- **Changes:** None. No test covers `input_event` serialization, SYN_REPORT sequencing, or (as specced, though misplaced relative to where the feature landed) coordinate scaling.
- **Files:** None
- **Why:** N/A — task not delivered; see review for detail.
