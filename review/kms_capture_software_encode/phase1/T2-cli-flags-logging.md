# Review: T2 - Implement CLI flags and logging

## Changes Made
- Implemented leveled logger package (`internal/logger/`) with DEBUG, INFO, WARN, ERROR levels
- Format: `2026-06-04 12:30:45.123 LEVEL [module] message`
- Implemented CLI flag parsing: `--port`, `--fps`, `--verbose`, `--quiet`, `--log-file`
- Wired logger into main entrypoint with flag-driven level selection
- Wrote comprehensive unit tests (level filtering, format validation, method coverage)

## Files Created/Modified
- `internal/logger/logger.go` (new)
- `internal/logger/logger_test.go` (new)
- `cmd/server/main.go` (modified - added flags and logger)

## Test Results
- Coverage: 93.3%
- All tests passing: yes (5 test functions, 16 subtests)

## Concerns / Trade-offs
- Logger uses `fmt.Fprintf` per message; for extreme throughput a buffered approach would be better, but for a streaming server with 30-60 log lines/sec this is fine.
- `--verbose` and `--quiet` are mutually exclusive by convention (quiet wins if both set).

## Verdict
PASS
