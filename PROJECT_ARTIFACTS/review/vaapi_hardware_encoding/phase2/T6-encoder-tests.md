# Review: T6 - Write encoder tests

## Changes Made
- `TestFFmpegEncoderSoftware`: constructs a software-mode `FFmpegEncoder`,
  feeds 30 synthetic I420 frames, asserts every returned NAL starts with the
  `00 00 00 01` start code and that at least one NAL was produced.
- `TestFFmpegEncoderVAAPI`: same shape, hardware mode, skips if VA-API isn't
  probed available.
- `TestProbeVAAPI`: logs the probe result; does not assert.

## Files Created
- `internal/encode/ffmpeg_test.go`

## Test Results
- 3 test functions, all gated by `ffmpegAvailable()`/`ProbeVAAPI()` skips (so
  they no-op in a CI environment without ffmpeg or VA-API hardware).
- Plan required four specific tests; three of the four were not written:
  - ✅ "Integration test: encode synthetic frame, verify output is valid
    H.264" — covered (start-code check on real ffmpeg output).
  - ❌ "Test NAL parsing with known H.264 bitstream samples" — no test feeds a
    fixed/recorded bitstream into `extractNALs` directly; only exercised via
    live ffmpeg output.
  - ❌ "Test frame boundary detection" — not isolated; see T4.
  - ❌ "Test crash recovery (kill subprocess, verify restart)" — not written;
    see T5.

## Concerns / Trade-offs
- The tests that exist are reasonable smoke/integration coverage, but the plan
  asked for unit-level coverage of the two riskiest pieces of this module (NAL
  parsing correctness, crash recovery) specifically because they're hard to
  exercise reliably through a live ffmpeg subprocess — and that's exactly the
  coverage that's missing.

## Verdict
FAIL
