# Review: T5 - Implement client-side metrics UI

## Changes Made
- `updateStats()` in `cmd/server/client/compositor.js` computes FPS
  (`frameCount * 1000 / elapsed`) and bandwidth (`byteCount * 8000 / elapsed`),
  recomputed every ~1s via `requestAnimationFrame`
- `updateStatus()` renders `"<fps> fps | <bw>"` into the existing status bar,
  formatting bandwidth as Mbps/kbps

## Files Touched
- `cmd/server/client/compositor.js`

## Test Results
No tests exist for client-side JS in this repo (no JS test harness at all);
consistent with the rest of the client code.

## Concerns / Trade-offs
Two of the four required displays are missing:
- **FPS counter** — done.
- **Bandwidth display** — done.
- **RTT measurement** ("send ping JSON every second, measure pong round-trip")
  — **not found**. The wire protocol already has `FrameTypePing`/`FrameTypePong`
  constants (from the earlier kms track), but nothing in `compositor.js` sends
  a ping or measures a pong round-trip, and nothing server-side drives a
  per-second ping either.
- **Input latency display** ("rolling average of last 20 ACK round-trips")
  — **not found**. There is no `InputAck` concept anywhere in the codebase
  (repo-wide search), so there is nothing for the client to measure a
  round-trip against.

## Verdict
CONCERNS
