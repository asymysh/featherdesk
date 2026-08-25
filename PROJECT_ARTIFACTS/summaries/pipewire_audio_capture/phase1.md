# Phase 1: PipeWire Stream Setup - Summary

## T1 - Implement PipeWire initialization and stream connection (cgo)
- **Commit:** 830e3b4 (initial), 4bd4da0 + 842cf4a (source-targeting fixes)
- **Changes:** Spec called for native cgo `libpipewire` bindings (Float32, process callback). Actual implementation uses a `pw-cat` subprocess (S16, 20ms chunks) piped into Go via stdout — a different architecture than specced, arrived at without a documented rationale in the commits.
- **Files:** `internal/audio/capture.go`
- **Why:** Captures the default sink monitor for host-audio loopback to the browser.

## T2 - Implement buffer handling and Go channel bridge
- **Commit:** 830e3b4
- **Changes:** 32-slot buffered channel with drop-oldest-on-full semantics, adapted to read from the subprocess pipe instead of a PipeWire callback.
- **Files:** `internal/audio/capture.go`
- **Why:** Prevents a slow consumer from blocking the audio read loop.

## T3 - Write audio capture tests
- **Commit:** None — not implemented.
- **Changes:** No test file exists for `internal/audio` anywhere in project history.
- **Files:** None.
- **Why:** N/A.
