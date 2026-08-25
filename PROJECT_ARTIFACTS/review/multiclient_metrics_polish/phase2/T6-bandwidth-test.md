# Review: T6 - Implement bandwidth test

## Changes Made
None found.

## Files Touched
None.

## Test Results
N/A — feature does not exist.

## Concerns / Trade-offs
A repo-wide search for `bandwidth_test` / `BandwidthTest` returns matches only
in the track's own `plan.md`/`spec.md` — zero occurrences in any `.go` or
`.js` source file. None of FR-5's sub-requirements exist:
- No `bandwidth_test` frame type defined in `internal/protocol`
- No server-side handler that sends N MB in 4MB chunks
- No client-side download/upload speed measurement or request/ack flow
- No UI element for "DL: X MB/s | UL: Y MB/s"

This task was not started.

## Verdict
FAIL
