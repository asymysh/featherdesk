# Review: T5 - Implement encode.Encoder interface for ffmpeg

## Changes Made
- `FFmpegEncoder` implements `Encode`, `ForceKeyframe`, `Close` matching the
  same interface shape as `OpenH264Encoder` (`Encode(*Frame) ([][]byte, error)`).
- IDR-on-demand: `ForceKeyframe()` sets an atomic flag; the next `Encode()`
  call restarts the ffmpeg subprocess (forces a fresh GOP/keyframe sequence)
  rather than sending an in-band force-IDR command.
- Automatic restart: if `running` is false (subprocess exited) or a stdin write
  fails, `Encode()`/`restart()` kills and respawns ffmpeg.

## Files Created
- `internal/encode/ffmpeg.go`

## Test Results
- No test kills the ffmpeg subprocess mid-stream to verify the restart path
  actually recovers and resumes producing NALs — the plan explicitly lists
  "Test crash recovery (kill subprocess, verify restart)" as a required test
  and it was not written.

## Concerns / Trade-offs
- **"Backoff on repeated failures (don't spin)" from the plan was not
  implemented.** `restart()` is a flat `kill()` + 50ms sleep + `start()`, with
  no attempt counter, no exponential backoff, and no circuit breaker. A
  persistently crashing ffmpeg (e.g. driver fault) would restart roughly every
  ~250ms indefinitely (200ms `Encode()` read-timeout + 50ms sleep), consuming
  CPU and spamming the log rather than degrading gracefully.
- IDR-via-full-process-restart is a heavyweight way to force a keyframe (tears
  down and respawns the whole subprocess) versus an in-band signal; acceptable
  given the ffmpeg-pipe architecture, but worth noting as a cost the later
  cgo-based direct encoder (see track summary) was specifically built to avoid.

## Verdict
CONCERNS
