# Review: T1 - Refactor server for multi-client broadcast

## Changes Made
- Client set tracked via `sync.Map` (NOT the specced `[]Client` + `sync.RWMutex` — a
  functionally-equivalent but architecturally different choice; RWMutex is never
  used for the client set, only for the separate `lastIDR` cache)
- Per-client `writePump()` goroutine with a 16-slot buffered channel (`internal/server/client.go`)
- Non-blocking send: `select { case c.send <- msg: default: /* drop */ }`
- Client removed from the set on `ReadLoop` return (disconnect or read error)
- New client immediately sent the cached `lastIDR` under `idrMu.RLock()`

## Files Touched
- `internal/server/server.go`
- `internal/server/client.go`

## Test Results
- No dedicated unit tests for this task's behavior (see T3 — the plan's own
  "write multi-client tests" task was not fulfilled)

## Concerns / Trade-offs
- **Deviation from plan:** the plan explicitly says "26th connection evicts
  oldest." The actual implementation does not evict — `handleWS` rejects any
  connection once `count >= maxClients` with `HTTP 503 "max clients reached"`
  (`server.go` `handleWS`). This satisfies the acceptance criterion ("25 tabs
  connect, all see the stream") but is a different behavior than specced, not
  just an implementation detail: a client that would have displaced an idle
  oldest connection is instead flatly refused.
- `sync.Map` instead of `[]Client`+`RWMutex` is a reasonable, arguably better
  choice for this access pattern (many concurrent reads on broadcast, rare
  add/remove), but it is a deviation from what the plan/spec call for.
- New-client join still invokes an `onNewClient()` callback into the pipeline
  (main.go) that, at the time this track shipped, forced a keyframe **and**
  restarted the capture subprocess — disrupting existing viewers on every
  join. This is tracked separately as TD-26 and was fixed later (commit
  `1c9a97b`, after this track's commits); it is not a defect introduced by
  this task, but it means this task's original behavior did not fully meet
  the "handle slow clients... broadcast loop" spirit of FR-1 until that later fix.

## Verdict
CONCERNS
