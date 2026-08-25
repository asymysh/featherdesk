# Review: T3 - Write audio capture tests

## Not Implemented

Searched the full history of `internal/audio/` across all branches (`git log --all -- internal/audio/`) and the final tree on `feature-libav-vp8s8` and `master`. No `*_test.go` file exists anywhere under `internal/audio/` at any point in the project's history.

None of the three specified test cases exist:
- PipeWire (pw-cat) init/destroy lifecycle test — not found
- Channel drop-behavior test (doesn't block when full) — not found
- Buffer-size consistency test (48000 * 2 * 2 bytes/sec for S16, or *4 for the specced Float32) — not found

## Files Created
None.

## Test Results
No test files exist. `go test ./internal/audio/...` would report "no test files."

## Concerns / Trade-offs
This is the one task in Phase 1 with zero corresponding work. The channel drop-oldest logic (T2) and reconnect logic (Phase 4) are exactly the kind of concurrent/edge-case behavior unit tests are meant to catch, and neither has coverage.

## Verdict
FAIL — task not implemented, no commit found.
