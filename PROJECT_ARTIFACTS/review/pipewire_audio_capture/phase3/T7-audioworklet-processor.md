# Review: T7 - Implement AudioWorklet processor

## Changes Made
- `WORKLET_CODE` in `cmd/server/client/compositor.js` (commit 830e3b4) defines `PCMProcessor` inline as a string, loaded via `Blob` + `URL.createObjectURL` — matches the spec's explicit "no separate file, use Blob URL" requirement.
- Handles stereo (2-channel) output via `outputChannelCount: [2]`.
- **Historical defect (now fixed):** the original per-chunk queue implementation discarded ~83% of incoming samples (128 of 960 per `process()` call) — documented and fixed in commit 523d108, which replaced it with the continuous ring-buffer append/consume shown in the current code (`this.buf` grows on `port.onmessage`, is sliced in `process()`). This was a serious, real, shipped bug that would have made audio choppy/mostly-silent for however long it was live before the fix.
- Current buffer caps at 48000 samples (~0.5s of stereo audio at the growth-then-trim threshold `old.length > 48000` → trims to `old.length - 24000`), which is a drop-oldest-under-overflow strategy — functionally addresses the spec's "queue cap" requirement, though via a sample-count threshold rather than the specced "32 chunks max."
- Underrun handling: fills `0` (silence) when buffer is too short — matches spec exactly.

## Files Created
- Modified: `cmd/server/client/compositor.js`

## Test Results
No automated tests (this is inline browser JS with no test harness in the repo for client code).

## Concerns / Trade-offs
- The 83%-sample-discard bug (fixed in 523d108) is worth flagging explicitly for the refactor: it shipped before being caught, meaning whatever manual verification step Phase 3 was supposed to include either didn't catch audible quality issues or happened before this regression. The Rust refactor's `MODULE_AUDIO.md` testing strategy should include an automated sample-count-conservation test (samples in == samples out over N seconds) specifically to prevent a repeat of this class of bug — it's not currently called out as a distinct test case.

## Verdict
CONCERNS — currently working correctly, but shipped with a serious sample-loss regression for some period before being caught and fixed. The final state is functionally sound.
