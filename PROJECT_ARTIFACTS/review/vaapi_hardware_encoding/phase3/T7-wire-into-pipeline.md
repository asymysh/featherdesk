# Review: T7 - Wire hardware encoder into pipeline

## Changes Made
- `main.go`'s capture loop now selects `FFmpegEncoder` (hw) or the existing
  `OpenH264Encoder` (sw) via the `useHW` logic from T2, assigns to the shared
  `encode.Encoder` interface variable, and logs which one was chosen.
- Client-facing wire format is unchanged (same `FrameHeader` + H.264 NAL
  framing regardless of encoder), so acceptance criterion #5 ("Browser client
  cannot tell which encoder is active") is satisfied by construction — no
  protocol branching was introduced.

## Files Touched
- `cmd/server/main.go`

## Test Results
- No automated CPU-usage profiling exists to confirm AC's "<5% CPU during
  streaming" target; this is a manual-verification item (see the track's
  "Conductor - User Manual Verification" task, not independently reviewed
  here).

## Concerns / Trade-offs
- **Acceptance criterion #6 ("`/status` endpoint reports active encoder
  type") was NOT implemented in this track's commit (`3507455`).** No
  `internal/server/server.go` changes appear in that commit at all. The
  `"encoder"` field on `/status` was only added later, in commit `cdf3021`
  ("enhanced /status metrics, startup validation, version banner"), which
  belongs to the `multiclient_metrics_polish` track, not this one. This
  acceptance criterion was fulfilled cross-track, not by this track's own
  delivery — worth flagging since the spec lists it as this track's
  responsibility.

## Verdict
CONCERNS
