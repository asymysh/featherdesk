# Review: T2 - Implement role-based access control

## Changes Made
- `?role=control` parsed from the WebSocket upgrade URL (`handleWS`)
- Single controller enforced via `atomic.Pointer[Client]` + `CompareAndSwap(nil, client)`
  — exactly one caller wins the slot, no locking needed
- A second `role=control` request while the slot is taken connects as a
  plain viewer (`client.onText = nil`) rather than being rejected outright
- `client.onText` is wired to `s.onInput` only for the controller; viewer
  clients never get an `onText` handler, so their text (input) frames are
  dropped at the source, not filtered downstream
- On disconnect, `s.controller.CompareAndSwap(client, nil)` releases the
  slot only if the disconnecting client was in fact the controller
- Role assignment and controller connect/disconnect are logged at INFO level

## Files Touched
- `internal/server/server.go`

## Test Results
- No dedicated unit test exercises role assignment or controller exclusivity
  (see T3)

## Concerns / Trade-offs
- None — this task matches its spec (FR-2) closely, including the edge case
  of a rejected control request degrading gracefully to a viewer connection
  rather than erroring.

## Verdict
PASS
