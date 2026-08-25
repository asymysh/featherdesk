# Review: T8 - Write integration tests

## Changes Made
- `TestParseMessage` in `input_test.go` covers JSON parsing for all 5 real message types (table-driven)
- `TestParseMessageInvalid` covers the invalid-JSON-doesn't-crash requirement

## Files Created
- `internal/input/input_test.go` (shared with T5/T6 coverage — this package has one test file for all pure-logic tests)

## Test Results
- "Test JSON parsing for all message types" — done, passes
- "Test invalid JSON doesn't crash" — done, passes
- "Test input ACK response is well-formed" — **impossible to satisfy**, since the ACK feature (T7) doesn't exist

## Concerns / Trade-offs
These are unit tests of `ParseMessage`, not integration tests in the sense of exercising the WebSocket → `HandleMessage` → device path end-to-end (no test drives a real or fake `*Device` through `HandleMessage`). Two of the three listed sub-goals are genuinely met; the third was never achievable given T7's absence.

## Verdict
CONCERNS — 2/3 sub-goals met with real (if narrowly-scoped) tests; the third is blocked by a missing feature, not a testing gap.
