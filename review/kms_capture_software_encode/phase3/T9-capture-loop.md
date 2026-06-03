# Review: T9 - Implement pixel readback and capture loop

## Changes Made
- `KMSCapturer` struct wiring DRM + EGL into a complete pipeline
- `NewKMSCapturer(ctx, fps)`: opens card, creates EGL, finds plane, imports initial DMA-BUF
- `NextFrame()`: polls fb_id for change detection, re-imports on change, reads pixels, frame paces
- Frame pacing: sleeps remaining budget after capture, respects context cancellation
- `Close()`: proper cleanup of EGL then DRM resources
- Implements `Capturer` interface

## Files Created
- `internal/capture/kms.go` (capture loop)
- `internal/capture/kms_integration_test.go` (3 integration tests)

## Test Results
- Unit tests: PASS (4 tests)
- Integration tests (sudo): PASS (3 tests)
  - Single frame: 2560x1440 in 60ms
  - Frame pacing: 5 frames in 170ms at 30fps target
  - Context cancel: immediate error return

## Concerns / Trade-offs
- Frame change detection re-imports DMA-BUF on fb_id change; for static screens this avoids redundant GPU reads but still does a readback. Could add dirty-flag optimization later.
- DRM_FORMAT_XRGB8888 (0x34325241) hardcoded; works for Intel but other GPUs may use different formats.

## Verdict
PASS
