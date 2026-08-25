# Review: T7 - Write metrics tests

## Changes Made
- `TestStatusEndpoint` (pre-existing, `internal/server/server_test.go`)
  verifies `/status` returns HTTP 200, `Content-Type: application/json`, and
  a `clients` key in the decoded body.

## Files Touched
None added by this task specifically — `TestStatusEndpoint` predates the
metrics fields this track adds and was not extended to cover them.

## Test Results
- "Test counters increment correctly" — **not found** (and `frames_captured`/
  `frames_encoded` counters this would test don't exist — see T4)
- "Test /status JSON schema is valid" — **partially covered** by the
  pre-existing `TestStatusEndpoint`, which checks structural validity but
  only asserts presence of the `clients` field, not the new metrics fields
  (`frames_broadcast`, `bytes_broadcast`, `has_controller`, etc.)
- "Test bandwidth test frame type round-trip" — **not found** (feature
  doesn't exist — see T6)

## Concerns / Trade-offs
No new tests were written for this task. The one relevant existing test
predates the metrics work and doesn't exercise any of the fields T4 added.

## Verdict
FAIL
