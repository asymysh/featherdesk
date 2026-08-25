# Review: T8 - Implement audio UI and lifecycle

## Changes Made
- `initAudio()` creates the `AudioContext` and wires up the worklet on first `pointerdown` (a user gesture, satisfying autoplay-policy requirements) — commit 830e3b4, with async chaining fixed in 3e28ed3 (`resume().then(addModule)`, catching worklet load errors).
- `playAudio(s16Data)` converts incoming S16 bytes to Float32 (`view.getInt16(i*2, true) / 32768.0`) and posts to the worklet via a transferable buffer, matching the spec's "Transfer to worklet via postMessage with Transferable buffers" requirement.
- Server audio frames are parsed in the main `ws.onmessage` handler by checking `type === FRAME_TYPE_AUDIO_PCM`.

## Files Created
- Modified: `cmd/server/client/compositor.js`

## Test Results
No automated tests for client-side audio.

## Concerns / Trade-offs
- Spec (Phase 3, task 2) asked for parsing `sample_rate`, `channels`, `bits`, `frame_count` out of the audio frame. None of these are actually parsed from the wire — the client hardcodes S16 + 48000Hz + stereo assumptions (consistent with the T4 finding that the server never sends this metadata). This works only because client and server are the same codebase deployed together; it is not a self-describing protocol.
- `AudioContext({sampleRate: 48000})` requests but does not guarantee 48kHz — some browsers/platforms may not honor the requested rate. There's no explicit check/warning that the granted rate matches expectations.

## Verdict
CONCERNS — playback works via a hardcoded format contract between client and server rather than the self-describing header the spec called for.
