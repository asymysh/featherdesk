# Review: T4 - Implement frame write and NAL read

## Changes Made
- `Encode()` concatenates Y/U/V planes and writes raw bytes to ffmpeg's stdin.
- `readNALs`/`extractNALs` scan stdout for `00 00 00 01` start codes and split
  on each occurrence.
- **Same-day follow-up fix (commit `99648f2`, ~11.5h after the original):**
  `Encode()` initially returned only the single NAL that arrived first per
  call — each NAL (e.g. SPS, PPS, IDR) was handed back as its own "frame,"
  which the commit message for `99648f2` confirms caused each ~3.9KB NAL to be
  sent as a separate WebSocket message instead of a full access unit. The fix
  drains every NAL immediately available on the channel before returning, so
  they're batched as one frame.

## Files Created
- `internal/encode/ffmpeg.go` (original: commit `3507455`; NAL-batching fix:
  commit `99648f2`)

## Test Results
- No test uses a known/pre-recorded H.264 bitstream sample to validate
  `extractNALs`' start-code splitting in isolation (plan explicitly asked for
  this). Coverage is indirect, via full encoder round-trip tests only.
- No test specifically validates "frame boundary detection" as its own unit —
  the drain-all-available fix in `99648f2` is a timing heuristic ("whatever
  arrived by the time we check the channel"), not a semantic boundary
  (detecting the next VCL NAL / access-unit delimiter). Under slow or bursty
  ffmpeg I/O this could still split one logical access unit across two
  `Encode()` returns.

## Concerns / Trade-offs
- **Spec input-format deviation:** FR-4 specified `-pix_fmt bgra` raw input
  piped directly from KMS capture. The shipped encoder instead takes an
  already-converted `I420Frame` (Y/U/V planes) — conversion to I420 happens
  upstream (in `encode.Converter`) before reaching `FFmpegEncoder`, not inside
  the ffmpeg pipeline via a `format=nv12` filter as FR-4 implied.
- **The original per-NAL bug this track shipped with is materially the same
  class of bug later catalogued as TD-23** in the refactor's tech-debt list
  (`specs/CENTRAL_SPEC.md`: "broadcasts one WebSocket message per NAL unit,
  not per frame"). It was fixed same-day here for the ffmpeg/VA-API path
  specifically, but TD-23 as catalogued describes the *general* `server.go`
  broadcast behavior — worth confirming during the Rust rewrite that the fix
  in `99648f2` (drain-all-available) is not reintroducing a narrower version of
  the same problem under load.

## Verdict
CONCERNS
