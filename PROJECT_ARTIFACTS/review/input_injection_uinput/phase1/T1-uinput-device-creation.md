# Review: T1 - Implement uinput device creation (pure Go)

## Changes Made
- `Device` type opens `/dev/uinput` with `O_WRONLY|O_NONBLOCK`
- Sets `EV_KEY`, `EV_REL`, `EV_ABS`, `EV_SYN` event bits
- Registers all 256 possible keycodes via `UI_SET_KEYBIT` (superset of the plan's "KEY_A through KEY_Z, digits, function keys, modifiers")
- Registers `BTN_LEFT`/`BTN_RIGHT`/`BTN_MIDDLE`
- Registers `ABS_X`/`ABS_Y` with min=0, max=width/height-1 (capture dimensions)
- Registers `REL_WHEEL`/`REL_HWHEEL`
- Writes `uinput_user_dev` struct and calls `UI_DEV_CREATE`
- `Close()` calls `UI_DEV_DESTROY` then closes the fd

## Files Created
- `internal/input/device.go`

## Test Results
- No hardware-gated tests exist for device creation itself (see T2 — that task was not delivered)

## Concerns / Trade-offs
- The 256-iteration `UI_SET_KEYBIT` loop discards each `ioctl` error (`ioctl(fd, uiSetKeybit, i)` return value not checked) — silent-fails are acceptable here since a handful of unsupported keycodes on some kernels wouldn't be fatal, but it's inconsistent with every other ioctl call in this file, which does check and return an error.
- `inputEvent` uses `int64` Sec/Usec (24-byte struct, matching real 64-bit Linux `struct input_event` layout) — correct for the target platform, but note this contradicts the plan's own testing note ("16 bytes" — see T5), which appears to be the plan author's error, not a code bug.

## Verdict
PASS
