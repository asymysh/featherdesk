# Review: T5 - Write injection unit tests

## Changes Made
None found for the injection primitives specifically.

## Files Created
None new. `input_test.go` tests `ParseMessage`/`BrowserCodeToLinux`/`MouseButtonToLinux` (protocol and mapping logic), not injection.

## Test Results
- "Test input_event struct serialization (16 bytes, correct layout)" — **no such test exists.** Also note: the plan's own expectation of "16 bytes" does not match the actual (and correct, for 64-bit Linux) 24-byte `input_event` layout used in `device.go` (`int64` Sec/Usec + uint16 + uint16 + int32 = 24). This looks like an error in the plan's original estimate, not in the shipped code — but it means this specific test, if written literally as specced, would have been asserting the wrong size.
- "Test SYN_REPORT is appended after each event" — no such test exists
- "Test coordinate scaling (browser coords -> ABS range)" — no such test exists in Go; coordinate scaling is actually implemented client-side in JavaScript (see T10), not server-side, so a Go-level test for it was never applicable as specced — the plan's task placement (Phase 2, Go injection tests) doesn't match where the scaling logic actually ended up (Phase 4, browser-side)

## Concerns / Trade-offs
This task was not delivered. None of its three listed sub-goals have corresponding tests, and one of the three (coordinate scaling) was specced for the wrong layer of the stack relative to where the feature was actually implemented.

## Verdict
FAIL
