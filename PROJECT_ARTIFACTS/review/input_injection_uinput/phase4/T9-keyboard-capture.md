# Review: T9 - Implement keyboard capture in compositor.js

## Changes Made
- `keydown`/`keyup` listeners on `document` (not scoped to canvas — matches the plan)
- `e.preventDefault()` called on both (only when `connected`)
- Sends `{type:"key",event:"down"|"up",code:e.code}` per event
- `initAudio()` called on first `keydown` (unlocks browser audio autoplay — reasonable piggyback, not specced but harmless)

## Files Created
- `cmd/server/client/compositor.js` (modified)

## Test Results
No automated test coverage for browser-side input capture (none of the plan's tasks in this phase called for one, and none exist).

## Concerns / Trade-offs
- **Plan requires "Track pressed keys in Set, release all on blur/visibilitychange" — not implemented at all.** No `Set` of pressed keys exists anywhere in `compositor.js`, and there is no `blur` or `visibilitychange` listener. Concretely: if a user holds a key (e.g. a modifier) and then alt-tabs away or the tab loses focus, the browser will not deliver a `keyup` for that key, no release message is ever sent, and the virtual uinput device is left with that key permanently "held down" on the host until the process exits. This directly fails **Acceptance Criterion #5**: "All keys released when browser loses focus."
- No `ack_id` included on keydown (depends on the unimplemented T7 ACK feature).

## Verdict
CONCERNS — basic key capture works, but the missing blur/visibilitychange handling is a real, reproducible bug (stuck keys after focus loss), not a cosmetic gap.
