# Review: T7 - Implement DRM card discovery and plane enumeration (cgo)

## Changes Made
- cgo bindings: open card, drmSetClientCap(UNIVERSAL_PLANES), drmModeGetPlaneResources
- Iterates /dev/dri/card0..15, finds first card with active primary plane
- Gets CRTC dimensions and refresh rate from primary plane's CRTC
- Exports DMA-BUF fd via drmPrimeHandleToFD
- Proper cleanup: close fds, free DRM resources
- Error types: ErrNoCard, ErrNoPrimaryPlane, ErrCapability
- Capturer interface defined for downstream use

## Files Created
- `internal/capture/types.go` (types, interface, error types)
- `internal/capture/drm.go` (cgo DRM bindings)
- `internal/capture/drm_integration_test.go` (hardware tests, build tag)
- `internal/capture/capture_test.go` (unit tests for non-hardware code)

## Test Results
- Unit tests: PASS (4 tests)
- Integration tests (sudo, real hardware): PASS (4 tests)
  - Card: /dev/dri/card1, 2560x1440 @ 165Hz
  - Primary plane ID: 34
  - DMA-BUF fd: 7

## Concerns / Trade-offs
- Requires CAP_SYS_ADMIN (sudo) for DRM master access
- Only finds first usable card; multi-GPU selection deferred to Track 5

## Verdict
PASS
