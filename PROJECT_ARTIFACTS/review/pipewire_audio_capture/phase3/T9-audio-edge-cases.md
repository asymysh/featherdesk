# Review: T9 - Handle edge cases

## Not Implemented (mostly)

Checked each of the four specced sub-items against `cmd/server/client/compositor.js` on `feature-libav-vp8s8`:

| Sub-item | Spec'd behavior | Found |
|---|---|---|
| Sample rate mismatch | Log warning if `AudioContext.sampleRate != 48000` | **Not found** — no such check anywhere in the file |
| Tab hidden | Pause AudioContext on `visibilitychange` to hidden | **Not found** — no `visibilitychange` listener exists at all |
| Reconnect | Re-create AudioContext on new WebSocket connection | **Not found** — `ws.onclose` only tears down the video `decoder`; `audioCtx`/`audioWorklet`/`audioStarted` are never reset, so a reconnect reuses (or leaves stale) the original audio context |
| Fallback | `ScriptProcessorNode` if `AudioWorklet` unavailable | **Not found** — `addModule(...).catch()` only logs `console.error`; there is no fallback path, audio simply stays silent if AudioWorklet is unsupported |

## Files Created
None — this task was not implemented.

## Test Results
N/A — no code to test.

## Concerns / Trade-offs
Of the four sub-items, none are implemented. In practice this mostly matters for edge browsers/platforms (AudioWorklet has been broadly supported since ~2021, so the fallback gap is low real-world risk) and for the reconnect case, which is plausibly fine in practice (the existing `AudioContext` likely keeps working across a WebSocket reconnect since it's independent of the socket) but was never verified against the spec's explicit "re-create" requirement.

## Verdict
FAIL — task not implemented; all four specced sub-behaviors are absent from the shipped client code.
