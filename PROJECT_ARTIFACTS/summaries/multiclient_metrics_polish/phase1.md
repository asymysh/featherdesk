# Phase 1: Multi-Client Infrastructure - Summary

## T1 - Refactor server for multi-client broadcast
- **Commit:** 1e00c5d
- **Changes:** Client set moved to `sync.Map` (max 25, enforced via `HTTP 503` rejection, not eviction as planned). Per-client `writePump` goroutine with a 16-slot buffered channel, non-blocking drop-on-full send. New client sent cached IDR on join.
- **Files:** `internal/server/server.go`, `internal/server/client.go`
- **Why:** Single-client server can't support multiple simultaneous viewers.
- **Deviation:** plan calls for `[]Client`+`sync.RWMutex` and "26th connection evicts oldest" — actual code uses `sync.Map` and rejects the 26th connection instead of evicting.

## T2 - Implement role-based access control
- **Commit:** 1e00c5d
- **Changes:** `?role=control` parsed at WebSocket upgrade; single controller enforced via `atomic.Pointer[Client]` CompareAndSwap; viewer clients get no input handler so their text frames are inert; controller slot released on disconnect.
- **Files:** `internal/server/server.go`
- **Why:** Only one person should be able to drive the mouse/keyboard while others watch.

## T3 - Write multi-client tests
- **Commit:** none — not implemented
- **Changes:** None of the five specified test scenarios (25-concurrent, 26th-eviction, controller-exclusivity, viewer-input-ignored, non-blocking-broadcast) exist.
- **Files:** N/A
- **Why:** N/A — task not completed.
