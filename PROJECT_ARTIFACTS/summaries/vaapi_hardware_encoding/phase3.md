# Phase 3: Integration - Summary

## T7 - Wire hardware encoder into pipeline
- **Commit:** 3507455
- **Changes:** `main.go` selects `FFmpegEncoder` or `OpenH264Encoder` behind
  the shared interface; wire format unchanged so the browser client is
  encoder-agnostic (AC #5 satisfied by construction). AC #6 ("`/status`
  reports encoder type") was **not** part of this commit — it landed later via
  commit `cdf3021`, which belongs to the `multiclient_metrics_polish` track.
- **Files:** `cmd/server/main.go`
- **Why:** Central dispatch point so the rest of the pipeline (converter,
  server broadcast) doesn't need to know which encoder is active.

## Conductor - User Manual Verification 'Integration'
- Process/checklist item (human sign-off protocol, includes the "<5% CPU"
  profiling check), not reviewed as code — no automated artifact exists to
  verify the CPU target.

---

## Track-level note: ffmpeg-subprocess vs. direct cgo VA-API

A later commit, **`5d9a6cf`** ("replace ffmpeg subprocess with libavcodec
VA-API cgo encoder"), exists in this repository's history and looks like a
supersession of this track's entire approach — but **it is not**, in the
codebase that matters. `5d9a6cf` lives only on the `feature-vaapi-libav-cgo`
branch, which was never merged back into `feature-libav-vp8s8` (verified via
`git merge-base --is-ancestor`). `feature-libav-vp8s8` — the branch every spec
in this repo calls "the working reference" — still contains this track's
original ffmpeg-subprocess implementation (`internal/encode/ffmpeg.go`,
`vaapi.go`) essentially as shipped in `3507455`, plus the incremental fixes
listed above. There is no `vaapi_cgo.go` on `feature-libav-vp8s8` at all.

So: this track's ffmpeg-pipe VA-API implementation is not a stepping stone
that was later replaced — **it is the current, live hardware-encode path** in
the codebase the refactor spec is being written against. `CENTRAL_SPEC.md`'s
statement that "the legacy ffmpeg-`h264_vaapi` subprocess path ... is REMOVED"
describes the *new Rust design's intent*, not the current Go reality — the
Rust rewrite has not happened yet, so nothing has actually been removed. The
cgo-direct approach was a same-day experiment on a sibling branch that was
abandoned, not adopted.
