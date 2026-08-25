# Review: T2 - Implement encoder selection logic

## Changes Made
- `--hardware` and `--software` CLI flags added to `cmd/server/main.go`.
- Selection logic: `useHW := cfg.hardware || (!cfg.software && encode.ProbeVAAPI())`.
- `--hardware` with VA-API unavailable logs an error and exits (fatal), matching
  the spec exactly.
- No flag: auto-detects, hardware preferred when `ProbeVAAPI()` is true.
- Selected encoder is logged: `"encode: WxH H.264 VA-API (hardware)"` or
  `"... OpenH264 (software)"`.

## Files Touched
- `cmd/server/main.go`

## Test Results
- No dedicated unit test for the selection branching itself (no test drives
  `cfg.hardware`/`cfg.software` combinations against a faked `ProbeVAAPI`);
  coverage is only incidental, via the two integration tests in
  `ffmpeg_test.go` that each exercise one branch directly.

## Concerns / Trade-offs
- None functionally — this task matches FR-2/FR-3 of the spec closely (fatal
  on forced-but-unavailable hardware, auto-detect default, correct log lines).
- Minor: the `--software` flag routes to the **pre-existing OpenH264 encoder**
  (`encode.NewH264Encoder`), not through `FFmpegEncoder`'s own `hwAccel=false`
  (libx264) branch — that branch of `FFmpegEncoder` is effectively dead code
  from `main.go`'s perspective (see T4).

## Verdict
PASS
