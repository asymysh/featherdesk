# Review: T3 - Implement signal handling and main entrypoint

## Changes Made
- Implemented `setupSignalHandler()` returning context, cancel func, and signal channel
- Context cancels on first SIGINT/SIGTERM; second signal forces os.Exit(1)
- Main blocks on `<-ctx.Done()` with graceful shutdown log message
- Wired signal handler into main entrypoint with deferred cancel

## Files Modified
- `cmd/server/main.go` (added signal handling, context-based shutdown)
- `cmd/server/main_test.go` (new - 4 test functions)

## Test Results
- All 4 tests pass including race detector
- Tests verify: context cancellation on signal, shutdown ordering, idempotent cancel
- Force-exit path verified by code inspection (os.Exit not testable in-process)

## Concerns / Trade-offs
- Second signal calls os.Exit(1) directly - acceptable for a streaming server where hung shutdown should be forcibly terminated.

## Verdict
PASS
