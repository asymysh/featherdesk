# Review: T4 - Implement server-side metrics collection

## Changes Made
- `/status` JSON now includes `clients`, `max_clients`, `encoder`, `audio`,
  `has_controller`, `frames_broadcast`, `bytes_broadcast` (commit `cdf3021`)
- `frames_broadcast` / `bytes_broadcast` are `atomic.Uint64` counters
  incremented in `Broadcast()`

## Files Touched
- `internal/server/server.go`
- `cmd/server/main.go`

## Test Results
- `TestStatusEndpoint` checks the endpoint returns 200/JSON and contains a
  `clients` field; it does not assert on the metrics fields added by this task.

## Concerns / Trade-offs
Most of FR-3's required surface is missing:
- Spec asks for **per-second** rates for frames_captured, frames_encoded,
  frames_broadcast. What exists is a **cumulative** `frames_broadcast`
  counter only — no `frames_captured` or `frames_encoded` counters exist
  anywhere in the codebase (confirmed via repo-wide search), and nothing
  computes a per-second rate from the cumulative counter.
- Spec asks for **per-client** tracking: `bytes_sent`, `frames_dropped`,
  `connected_at`. None of these exist per-client — `Client` (client.go) has
  no such fields, and `/status` reports only server-wide totals.
- Spec asks for a **periodic log every 30s** ("25 clients, 60fps, 12.5 MB/s
  total"). No ticker or periodic logging of this kind exists in `main.go` or
  `server.go`.

Only the `/status` endpoint shape and the two cumulative broadcast counters
were actually built; the per-second, per-client, and periodic-log
requirements were not.

## Verdict
FAIL
