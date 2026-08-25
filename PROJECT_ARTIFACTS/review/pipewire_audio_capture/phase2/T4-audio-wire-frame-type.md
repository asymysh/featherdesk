# Review: T4 - Add audio frame type to wire protocol

## Changes Made
- `FrameTypeAudioPCM uint8 = 4` added to `internal/protocol/protocol.go` (commit 830e3b4).
- Spec (FR-2) asked for a dedicated audio sub-header carrying `sample_rate(u32)`, `channels(u16)`, `bits_per_sample(u16)`, `frame_count(u32)`. This was **not built**. Instead, `BroadcastAudio` (server.go) reuses the existing video `FrameHeader` and overloads its `Width` field to carry the sample rate (`48000`) and `Height` to carry channel count (`2`). `bits_per_sample` and `frame_count` are not transmitted at all — the client hardcodes S16 assumptions instead of reading them off the wire.
- No dedicated marshal function for audio frames, and no unit tests for audio frame encoding.

## Files Created
- Modified: `internal/protocol/protocol.go` (+1 constant), `internal/server/server.go` (`BroadcastAudio`)

## Test Results
No unit tests for audio frame encoding were found.

## Concerns / Trade-offs
- This is the exact "overloaded Width/Height" pattern the `featherdesk-refactor` spec later identifies and fixes — see `specs/media/MODULE_AUDIO.md` Refactoring Directive **R-AUD-10: "overloaded Width/Height" → "Done — audio params moved to the `config` message"**. This review independently confirms that refactor decision was addressing a real, verified defect in the current code, not a hypothetical one.
- Hardcoding format assumptions client-side works only as long as server and client agree out-of-band; any future format change (e.g. switching to Float32 per the original spec) would silently desync without a protocol version bump.

## Verdict
CONCERNS — a working frame type exists and audio does flow end-to-end, but the sub-header FR-2 asked for was never built; the server instead reused/overloaded unrelated header fields, which the refactor spec has already flagged as technical debt.
