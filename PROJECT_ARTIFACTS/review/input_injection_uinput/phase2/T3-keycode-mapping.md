# Review: T3 - Implement browser-to-Linux keycode mapping

## Changes Made
- `browserToLinux` map in `keymap.go` covers: all 26 letters, digits 0-9, F1-F12, all 8 modifier keys (Shift/Control/Alt/Meta × Left/Right), navigation (arrows, Home, End, PageUp/Down), editing (Backspace, Tab, Enter, Escape, Delete, Insert, Space), punctuation (Semicolon, Equal, Comma, Minus, Period, Slash, Backquote, BracketLeft/Right, Backslash, Quote), plus extras beyond the plan (CapsLock, NumLock, ScrollLock, PrintScreen, Pause, ContextMenu)
- `BrowserCodeToLinux(code string) (uint16, bool)` — clean lookup with an `ok` bool for unmapped codes

## Files Created
- `internal/input/keymap.go`

## Test Results
- `TestBrowserCodeToLinux` in `input_test.go` spot-checks 6 codes (KeyA, Space, Enter, ArrowUp, ShiftLeft, F1) plus the unknown-code `ok=false` path — all pass
- No exhaustive test of every mapped entry, but the spot-check covers each category

## Concerns / Trade-offs
- **The plan explicitly requires "F1-F24"; only F1-F12 are mapped.** Confirmed by grep — zero F13-F24 entries exist in `keymap.go`. Extended keyboards and some browsers' `KeyboardEvent.code` do emit F13-F24 for those physical keys; this mapping silently drops them (falls through the "unknown code" `ok=false` path in `HandleMessage`, which just returns without injecting anything — no crash, but no F13-F24 input is possible).

## Verdict
CONCERNS — functional for the standard 104/105-key layout, but does not meet the plan's explicitly stated F1-F24 requirement.
