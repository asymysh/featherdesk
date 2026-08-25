# Review: T10 - Implement mouse capture in compositor.js

## Changes Made
- `pointermove` on canvas: maps client coordinates to host resolution with letterbox-aware scaling (`scaleX`/`scaleY`/`Math.max` for uniform scale, centered offset subtraction), then clamps to `[0, canvas.width-1]` / `[0, canvas.height-1]` — more robust than the plan's brief description implies
- `pointerdown`/`pointerup` on canvas: `preventDefault`, `setPointerCapture`/`releasePointerCapture`, sends `{type, button: e.button}`
- `wheel` on canvas: `preventDefault: false` listener option set correctly (`{passive:false}` to allow `preventDefault`), sends `{type:"wheel", deltaY: e.deltaY}`
- `contextmenu` prevented on the canvas (stops the browser's right-click menu from appearing over a right-click drag)

## Files Created
- `cmd/server/client/compositor.js` (modified)

## Test Results
No automated coverage (browser-side; consistent with the rest of this phase).

## Concerns / Trade-offs
- **Plan requires "throttle to 8ms" for `pointermove` — not implemented.** The handler sends a message on every single `pointermove` event with no debounce/throttle/rAF-batching of any kind. High-polling-rate mice or trackpads can emit far more than 125 events/sec, meaning this can flood the WebSocket well beyond the specced rate.
- **Plan requires "batch at 16ms" for `wheel` — not implemented.** Same issue: every individual wheel event is sent immediately and separately.
- No `ack_id` included on `pointerdown` (depends on the unimplemented T7 ACK feature).
- Coordinate mapping itself is correct and handles a real edge case (letterboxed canvas) the plan didn't explicitly call out — a genuine improvement over the spec's minimum bar.

## Verdict
CONCERNS — coordinate mapping and button/context-menu handling are solid and exceed spec; the missing rate-limiting on high-frequency events (move, wheel) is a real gap that could degrade WebSocket/input performance under normal mouse usage, not just an edge case.
