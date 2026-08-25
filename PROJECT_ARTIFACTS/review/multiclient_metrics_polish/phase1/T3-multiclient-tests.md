# Review: T3 - Write multi-client tests

## Changes Made
None found. `internal/server/server_test.go` contains `TestStatusEndpoint`,
`TestWebSocketUpgrade`, `TestWebSocketReceivesBinaryFrame`,
`TestClientDisconnectNoPanic`, and `TestNewClientGetsIDR` — all pre-dating or
orthogonal to this track's multi-client requirements.

## Files Touched
None.

## Test Results
Searched `internal/server/server_test.go` and `cmd/server/main_test.go` for
each of the five scenarios the plan requires:
- "Test 25 concurrent connections accepted" — **not found**
- "Test 26th connection evicts oldest" — **not found** (and per T1, the code
  doesn't evict — it rejects, so this test could not pass as specced even if written)
- "Test controller role exclusivity" — **not found**
- "Test viewer text frames are ignored" — **not found**
- "Test broadcast doesn't block on slow client" — **not found**

## Concerns / Trade-offs
This task was not completed. The multi-client and role-based-access logic in
T1/T2 is real and functional (verified by direct code reading), but it ships
with zero automated coverage for the specific behaviors this track added —
only pre-existing single-client tests remain.

## Verdict
FAIL
