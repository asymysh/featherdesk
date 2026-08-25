# Phase 4: Web Client Input Capture - Summary

## T9 - Implement keyboard capture in compositor.js
- **Commit:** d5d6d9b (initial), 6bea81b (fix — see note below)
- **Changes:** `keydown`/`keyup` listeners on `document`, `preventDefault`, sends key events. No pressed-keys `Set` and no `blur`/`visibilitychange` handler — keys can get stuck on the host after the tab loses focus (fails Acceptance Criterion #5).
- **Files:** `cmd/server/client/compositor.js`
- **Why:** Captures keyboard input from the browser for injection.
- **Note:** A separate bug, unrelated to the gaps above, was caught and fixed post-merge: commit `6bea81b` ("input events were never registered") found that a duplicate `init()` function meant `initInput()` was never actually called at all in the shipped code path. Fixed before this became a released regression.

## T10 - Implement mouse capture in compositor.js
- **Commit:** d5d6d9b
- **Changes:** `pointermove` with letterbox-aware coordinate scaling and clamping (exceeds spec), `pointerdown`/`pointerup` with pointer capture, `wheel` with `deltaY`, `contextmenu` prevention. No throttling on `pointermove` (specced: 8ms) or batching on `wheel` (specced: 16ms) — both send unconditionally on every event.
- **Files:** `cmd/server/client/compositor.js`
- **Why:** Captures mouse input from the browser for injection.

## T11 - Implement input latency display
- **Commit:** N/A — not implemented
- **Changes:** None. No pending-ACK tracking, rolling average, or status-bar display.
- **Files:** None
- **Why:** N/A — blocked entirely on the missing T7 ACK feature.
