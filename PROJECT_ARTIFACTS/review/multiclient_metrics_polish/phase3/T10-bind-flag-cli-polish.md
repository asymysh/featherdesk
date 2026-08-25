# Review: T10 - Implement --bind flag and final CLI polish

## Changes Made
- `--bind` flag added (default `"0.0.0.0"`), threaded into the listen address
- Startup banner logs `"listening on http://<bind>:<port>/"`
- Capabilities summary line doubles as the "detected capabilities" banner
  item from the plan

## Files Touched
- `cmd/server/main.go`

## Test Results
No tests found for CLI flag parsing/banner output.

## Concerns / Trade-offs
- `--version` flag: **not found**. No version flag, no build-version string,
  no early-exit-on-`--version` path anywhere in `main.go`.
- The startup banner itself still reads `"ViewPort RDS v0.1.0"` — the
  project's pre-rebrand name. This is a leftover from the `ea88bbb` rebrand
  commit (which touched 14 lines of this same file but missed this literal
  string), not something this track's commits introduced, but it means the
  "final CLI polish" banner is currently branded incorrectly.

## Verdict
CONCERNS
