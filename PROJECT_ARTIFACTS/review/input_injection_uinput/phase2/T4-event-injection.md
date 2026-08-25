# Review: T4 - Implement input event injection

## Changes Made
- `InjectKey(code uint16, down bool)` — writes `EV_KEY` + `SYN_REPORT`
- `InjectMouseMove(x, y int)` — writes `EV_ABS` for X and Y + `SYN_REPORT`
- `InjectMouseButton(button uint16, down bool)` — writes `EV_KEY` + `SYN_REPORT`
- `InjectWheel(delta int32)` — writes `EV_REL` (REL_WHEEL) + `SYN_REPORT`
- All four go through a shared `writeEvent(typ, code, value)` that serializes the 24-byte `input_event` struct with a zeroed timeval (kernel fills it in)

## Files Created
- `internal/input/device.go` (same file as T1 — injection methods live alongside device creation)

## Test Results
- No test exercises these methods directly (would require a real or mocked `/dev/uinput` fd — see T2/T5, neither delivered)

## Concerns / Trade-offs
- **The injection primitive itself is correct and generic** (`InjectWheel(delta int32)` takes an arbitrary signed magnitude), but its only call site — `HandleMessage` in `protocol.go` — collapses every wheel event to a fixed `±1`:
  ```go
  case "wheel":
      delta := int32(msg.DeltaY)
      if delta > 0 { d.InjectWheel(-1) } else if delta < 0 { d.InjectWheel(1) }
  ```
  Browser `deltaY` magnitude (which varies a lot between a mouse notch scroll and a fast trackpad fling) is discarded entirely — only the sign survives. This means scroll speed cannot be controlled from the client at all; every scroll gesture, however fast, produces exactly one wheel click on the host. This is a real functional gap against the spec's "Map browser wheel deltaY to REL_WHEEL events" (FR-2), which implies a magnitude-aware mapping, not a fixed-step one.

## Verdict
CONCERNS — key/mouse-move/button injection is correct; wheel injection loses scroll magnitude at the call site.
