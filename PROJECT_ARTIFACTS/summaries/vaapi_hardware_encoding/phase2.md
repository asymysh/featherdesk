# Phase 2: ffmpeg Pipe Encoder - Summary

## T3 - Implement ffmpeg subprocess management
- **Commit:** 3507455
- **Changes:** Subprocess spawn via `exec.CommandContext`, stdin/stdout pipes,
  stderr routed into the app logger (not a dedicated `ffmpeg_err.log` as
  specced), clean shutdown via `kill()`.
- **Files:** `internal/encode/ffmpeg.go`
- **Why:** ffmpeg-as-subprocess was the chosen encode strategy for both the
  hardware (VA-API) and (unused) software backends.

## T4 - Implement frame write and NAL read
- **Commit:** 3507455 (original); **99648f2** (same-day fix, ~11.5h later)
- **Changes:** Writes I420 planes to stdin (not raw BGRA as FR-4 specified);
  splits stdout on `00 00 00 01` start codes. Original version returned only
  one NAL per `Encode()` call, causing multi-NAL access units (SPS/PPS/IDR) to
  be sent as separate WebSocket messages — fixed same-day by draining all
  immediately-available NALs per call.
- **Files:** `internal/encode/ffmpeg.go`
- **Why:** The pre-fix behavior is the same class of bug later catalogued as
  TD-23 in the refactor spec (per-NAL broadcast breaking WebCodecs access
  units).

## T5 - Implement encode.Encoder interface for ffmpeg
- **Commit:** 3507455
- **Changes:** `Encode`/`ForceKeyframe`/`Close` implementing the shared
  encoder interface; IDR-on-demand via full subprocess restart; automatic
  restart on subprocess exit or stdin write failure. No backoff/circuit
  breaker on repeated crashes (plan explicitly required one).
- **Files:** `internal/encode/ffmpeg.go`
- **Why:** Keeps `FFmpegEncoder` a drop-in alternative to `OpenH264Encoder`
  behind the same interface.

## T6 - Write encoder tests
- **Commit:** 3507455
- **Changes:** Two integration-style tests (software + hardware path) that
  feed synthetic frames and check start-code framing; a non-asserting probe
  smoke test. The plan's three other required tests (known-bitstream NAL
  parsing, frame-boundary detection, crash-recovery-via-kill) were not
  written.
- **Files:** `internal/encode/ffmpeg_test.go`
- **Why:** N/A — this is the gap, not a design choice.
