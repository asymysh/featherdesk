# Review: T1 - Implement PipeWire initialization and stream connection (cgo)

## Changes Made
- Spec required native cgo bindings (`pw_init`, `pw_main_loop_new`, `pw_stream_new`, `PW_KEY_MEDIA_CLASS = "Audio/Sink"`, Float32/48kHz/stereo, low-latency, process callback).
- Actual implementation (`internal/audio/capture.go`, commit 830e3b4) does **not** use cgo or libpipewire at all. It shells out to the `pw-cat` CLI as a subprocess: `pw-cat --record --rate 48000 --channels 2 --format s16 --latency 20ms --target <monitor>`, reading raw PCM from its stdout pipe.
- Format is **S16** (16-bit signed integer), not the Float32 the spec (FR-1) requires.
- Target source discovery went through two later iterations (commits 4bd4da0, 842cf4a) before settling on `pactl get-default-sink` + numeric monitor target — `sink-name.monitor` string targeting didn't resolve correctly in `pw-cat`.
- No PipeWire main loop, no process callback — a `readLoop()` goroutine blocks on `io.ReadFull` from the subprocess's stdout instead.

## Files Created
- `internal/audio/capture.go`

## Test Results
- No tests found (see T3 below).

## Concerns / Trade-offs
- This is a materially different technical approach than specced: subprocess + pipe instead of in-process cgo bindings. It avoids cgo/libpipewire linkage complexity but adds a process-spawn dependency (`pw-cat` binary must be on PATH — the code does check `exec.LookPath` and fails cleanly if absent) and loses the lower-latency direct-callback path the spec wanted.
- S16 instead of Float32 is a real format deviation from FR-1; downstream code (client) compensates by converting S16→Float32 itself, so it works end-to-end, but the wire format doesn't match what was specified.
- Took 2 follow-up bugfix commits (4bd4da0, 842cf4a) to get monitor-source targeting working — the initial version shipped with incorrect source resolution.

## Verdict
CONCERNS — functionally working audio capture exists, but via a fundamentally different mechanism (subprocess vs. cgo) and format (S16 vs. Float32) than what was specced. Should be flagged for the Rust refactor: the `featherdesk-refactor` audio module spec should explicitly decide whether the subprocess approach is acceptable going forward or whether native bindings are still required.
