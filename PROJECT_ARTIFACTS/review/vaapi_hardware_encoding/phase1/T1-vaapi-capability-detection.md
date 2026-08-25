# Review: T1 - Implement VA-API capability detection

## Changes Made
- `encode.ProbeVAAPI()`: checks `/dev/dri/renderD128` exists via `os.Stat`, then
  shells out to `ffmpeg -hide_banner -vaapi_device <render> -encoders` and
  string-matches `"h264_vaapi"` in the combined output.

## Files Created
- `internal/encode/vaapi.go`

## Test Results
- `TestProbeVAAPI` (in `ffmpeg_test.go`) only logs the boolean result — it does
  not assert availability either way, so it never fails; it's a smoke test, not
  a real capability-detection test.

## Concerns / Trade-offs
- **Deviates materially from the plan.** Phase 1 specified cgo `libva` bindings
  (open VA display, query `VAProfileH264High` + `VAEntrypointEncSliceLP`
  directly) and logging the VA-API vendor string. The shipped code does neither
  — it infers availability from ffmpeg's compiled-in encoder list, which
  confirms ffmpeg *was built with* VA-API support but says nothing about
  whether the driver/hardware can actually use `EncSliceLP` at runtime (a
  degraded/software-fallback VA-API driver would still list `h264_vaapi` and
  pass this probe).
- No vendor-string logging, so "Log VA-API vendor string and encode support"
  was not done.
- Functionally sufficient for auto-detection to work in practice (confirmed by
  `TestFFmpegEncoderVAAPI` actually encoding via the hardware path when probed
  available), but it's a weaker guarantee than the spec asked for.

## Verdict
CONCERNS
