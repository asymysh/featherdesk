# Review: T3 - Implement ffmpeg subprocess management

## Changes Made
- `NewFFmpegEncoder` builds the ffmpeg arg list (`buildArgs`), starts the
  subprocess with `exec.CommandContext`, wires stdin/stdout pipes, and spawns a
  `readNALs` goroutine on stdout.
- `Close()` cancels the context and calls `kill()` (closes stdin, signals
  `os.Kill`, waits, drains the NAL channel) for clean shutdown.
- stderr is wrapped in a `logWriter` that forwards ffmpeg's stderr lines into
  the app's own leveled logger at Debug level.

## Files Created
- `internal/encode/ffmpeg.go`

## Test Results
- Exercised indirectly by `TestFFmpegEncoderSoftware`/`TestFFmpegEncoderVAAPI`
  (both skip if `ffmpeg` isn't on `PATH`). No test independently verifies
  `Close()`'s shutdown sequence (no leaked process, no hang on `cmd.Wait()`).

## Concerns / Trade-offs
- Plan explicitly asked to "Redirect stderr to log file (ffmpeg_err.log)" — a
  dedicated file. The shipped code instead routes stderr through the
  application's own structured logger, not a separate file. This is arguably a
  reasonable (even better) choice for a single consolidated log stream, but it
  is a deviation from what was specced, and no `ffmpeg_err.log` is ever
  produced.

## Verdict
CONCERNS
