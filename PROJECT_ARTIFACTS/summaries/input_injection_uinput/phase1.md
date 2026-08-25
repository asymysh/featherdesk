# Phase 1: Virtual Device Setup - Summary

## T1 - Implement uinput device creation (pure Go)
- **Commit:** d5d6d9b
- **Changes:** `Device` type opens `/dev/uinput`, sets `EV_KEY`/`EV_REL`/`EV_ABS`/`EV_SYN` bits, registers all 256 keycodes + mouse buttons + `ABS_X`/`ABS_Y` (bounded to capture dimensions) + `REL_WHEEL`/`REL_HWHEEL`, writes `uinput_user_dev`, calls `UI_DEV_CREATE`. `Close()` calls `UI_DEV_DESTROY`.
- **Files:** `internal/input/device.go`
- **Why:** Establishes the virtual keyboard/mouse the rest of the track injects events into.

## T2 - Write device creation tests
- **Commit:** N/A — not implemented
- **Changes:** None. No hardware-gated test file exists for `NewDevice`/`Close`/ABS-range verification.
- **Files:** None
- **Why:** N/A — task not delivered; see review for detail.
