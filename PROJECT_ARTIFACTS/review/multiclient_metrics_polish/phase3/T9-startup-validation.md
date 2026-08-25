# Review: T9 - Implement startup validation

## Changes Made
- `checkUInput()`, `checkPipeWire()`, `checkFFmpeg()` helpers plus an inline
  `capKMS := os.Geteuid() == 0` check, all run at the top of `main()`
- All five capabilities (KMS, VA-API, uinput, PipeWire, ffmpeg) logged in a
  single summary line: `"capabilities: KMS=%v VA-API=%v uinput=%v PipeWire=%v ffmpeg=%v"`

## Files Touched
- `cmd/server/main.go`

## Test Results
No tests found for this task's logic.

## Concerns / Trade-offs
- Spec: "Check /dev/dri/card* access (**fail early with clear error** if no
  CAP_SYS_ADMIN)." Actual: `capKMS` is only a `Geteuid() == 0` proxy check
  (not an actual `/dev/dri/card*` stat), and its result only feeds the
  summary log line — startup does **not** fail early or exit if `capKMS` is
  false. This is the one capability the spec explicitly wants to be a hard
  failure, and it behaves the same as the soft-warn capabilities instead.
- PipeWire, ffmpeg, and uinput are correctly warn-and-continue (matches spec
  intent), so 4 of 5 capabilities are handled as specced; only the KMS
  fail-early requirement is missing.
- The capability check function names (`checkPipeWire`, using `pw-cat`
  specifically) are reasonable but narrower than "PipeWire connectivity" —
  it only confirms the `pw-cat` binary is on `PATH`, not that a working
  PipeWire session is reachable.

## Verdict
CONCERNS
