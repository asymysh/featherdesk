# Review: T6 - Implement --no-audio flag

## Changes Made
- `--no-audio` bool flag added in `cmd/server/main.go` (commit 830e3b4): `flag.BoolVar(&cfg.noAudio, "no-audio", false, "Disable audio capture")`.
- When set, `audio.NewCapturer` is never called; `log.Info("main", "audio: disabled")` is logged.
- `/status` endpoint reports audio state via `Server.SetAudioEnabled(v bool)` / `s.audioEnabled`, exposed as `"audio": s.audioEnabled` in the status JSON.

## Files Created
- Modified: `cmd/server/main.go`, `internal/server/server.go`

## Test Results
No dedicated test, but the logic is a straightforward conditional; low risk.

## Concerns / Trade-offs
None of note — this task matches the spec (FR-4) exactly, including the `/status` reporting requirement.

## Verdict
PASS
