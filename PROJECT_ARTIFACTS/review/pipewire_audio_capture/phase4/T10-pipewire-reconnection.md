# Review: T10 - Implement PipeWire reconnection

## Changes Made
- `Capturer.reconnect()` (internal/audio/capture.go): on a read error from the `pw-cat` subprocess, kills the process, waits 2 seconds (matches spec exactly), and calls `start()` again.
- Iterated across three commits: 4bd4da0 and 842cf4a both fixed real bugs in monitor-source target resolution that were surfacing specifically on reconnect/restart paths.

## Files Created
- Modified: `internal/audio/capture.go`

## Test Results
No tests found (consistent with T3's finding — `internal/audio` has zero test coverage).

## Concerns / Trade-offs
- Spec explicitly asked for a retry cap: "Cap retries (after 10 failures, stop trying, log permanent failure)." This is **not implemented** — `readLoop()`'s error path calls `reconnect()` unconditionally and loops forever with no failure counter. A permanently unavailable PipeWire (e.g. the audio server crashed and won't restart) would cause the goroutine to retry every 2 seconds indefinitely for the lifetime of the process, rather than giving up and logging a permanent failure as specced.

## Verdict
CONCERNS — reconnection with backoff works and was hardened by real bugfixes, but the retry cap / permanent-failure behavior from the spec was never added.
