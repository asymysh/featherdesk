# Review: T2 - Write device creation tests

## Changes Made
None found.

## Files Created
None. `internal/input/input_test.go` exists but contains only pure-logic tests for `ParseMessage`, `BrowserCodeToLinux`, and `MouseButtonToLinux` — it never touches `Device`, `NewDevice`, `InjectKey`, or any uinput syscall path.

## Test Results
- No test exercises `NewDevice()` open/create succeeding
- No test exercises `Close()`/destroy behavior
- No test verifies the ABS range matches specified capture dimensions
- No `//go:build integration` gated test file exists anywhere under `internal/input/` (contrast with `internal/capture/drm_integration_test.go`, `egl_integration_test.go`, `kms_integration_test.go` from the kms track, which DO have this pattern with real hardware-run results)

## Concerns / Trade-offs
This plan task was not delivered. The device-creation code path (T1) has zero test coverage of any kind — not even a build-tag-gated integration test that could be skipped in CI and run manually, which is exactly the pattern already established elsewhere in this same codebase for hardware-dependent code.

## Verdict
FAIL
