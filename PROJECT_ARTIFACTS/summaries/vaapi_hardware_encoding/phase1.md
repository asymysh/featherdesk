# Phase 1: VA-API Probing - Summary

## T1 - Implement VA-API capability detection
- **Commit:** 3507455
- **Changes:** `ProbeVAAPI()` checks `/dev/dri/renderD128` exists, then shells
  out to `ffmpeg -encoders` and string-matches `h264_vaapi`. Deviates from the
  plan's cgo-`libva`-direct-query approach (no entrypoint check, no vendor
  string logged).
- **Files:** `internal/encode/vaapi.go`
- **Why:** Cheapest way to answer "can ffmpeg use VA-API here" without adding a
  cgo `libva` dependency for a probe-only check.

## T2 - Implement encoder selection logic
- **Commit:** 3507455
- **Changes:** `--hardware`/`--software` CLI flags; auto-detect default;
  `--hardware` fails fast if VA-API isn't available; selected encoder logged.
- **Files:** `cmd/server/main.go`
- **Why:** Matches spec FR-2/FR-3 exactly; lets an operator force a specific
  path for testing or troubleshooting.

## Conductor - User Manual Verification 'VA-API Probing'
- Process/checklist item (human sign-off protocol), not reviewed as code —
  no artifact to verify.
