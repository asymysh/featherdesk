# QA 10 — Regression checks for TD-01 … TD-40

**The highest-value file in this folder.** Every row is a defect that *actually
shipped* in the Go implementation on `feature-libav-vp8s8`, or was found by
review of that code. A rewrite in a different language is the single most likely
moment to reintroduce them, and the specs close many of them "structurally" or
"by design" — which is a claim about the design, not evidence about the build.

**Release rule:** every row here must be `PASS`. There is no acceptable
exception, because each of these has been wrong once already.

Rows marked *(by construction)* should be **cheap** to verify — if verifying one
is hard, that itself is a finding, because it means the structural guarantee is
not observable.

## The originally catalogued defects

| ID | The original bug | How to prove it is gone | Status |
|----|------------------|-------------------------|--------|
| QA-TD-01 | DMA-BUF fd lifetime was a two-site manual discipline | Stream for 10 minutes on the zero-copy path and watch the host's fd count (`ls /proc/<pid>/fd \| wc -l`). Flat. Then force encode errors and `FallbackToSoftware` and confirm it is still flat — the error paths are where the old design could leak *(by construction: `FbInfo` is moved by value and released in `Drop`)* | ☐ |
| QA-TD-02 | `NextFrame` retried a dead capture by recursing with no depth limit | Make capture fail persistently. Observe the bounded ladder (3 → restart, 10 → fatal) and **no stack growth**. The process must not die of stack exhaustion | ☐ |
| QA-TD-03 | File-scope C statics allowed only one EGL readback context per process | Construct a capturer, drop it, construct another in the same process; then (where supported) drive two concurrently. No crash, no cross-talk — state is per instance | ☐ |
| QA-TD-04 | `ForceKeyframe()` set a flag nothing ever read — the IDR was never forced | For **every** encoder add-on: request a keyframe from the client and confirm an IDR actually appears on the wire. A write-only flag is not a conforming implementation | ☐ |
| QA-TD-05 / TD-27 | Input device hardcoded to 2560×1440, so the cursor was offset | Run at several stream resolutions **and** change resolution mid-session. Pointer stays exact at all four corners (see QA-INP-12/15) | ☐ |
| QA-TD-06 | Duplicate `init()` in `compositor.js` — the first body was dead | Client bundle has one initialisation path; confirm the module structure of R-CLI-10 and that no function is defined twice | ☐ |
| QA-TD-07 | Codec type constant carried the wrong payload (H.264 constant, VP8 data) | Client configures its decoder **from the `config` handshake string**, never a constant. Confirm no hardcoded codec exists anywhere in the client | ☐ |
| QA-TD-08 | Race on audio cmd/stdout fields | Run audio under a race detector / sanitizer build for 10 minutes | ☐ |
| QA-TD-10 | No version or sequence in the wire protocol | Header carries version 1 and a server-owned sequence; a receiver rejects any other version rather than misparsing | ☐ |
| QA-TD-11 | All `Inject` errors silently discarded | Force an injection failure; confirm it is returned and logged at `warn`, not swallowed | ☐ |
| QA-TD-12 | Custom `itoa()` reimplementing `strconv` | Code review — no hand-rolled integer formatting | ☐ |
| QA-TD-13 | `fps` parameter accepted but unused | Changing fps actually changes the delivered frame rate | ☐ |
| QA-TD-14 | Unbounded stats slice grew forever | Run 24 h; stats memory is a bounded rolling window (see QA-RR-27) | ☐ |

## Design-review findings

| ID | The original bug | How to prove it is gone | Status |
|----|------------------|-------------------------|--------|
| QA-TD-15 | GPU→CPU→GPU round-trip on the hardware path | Confirm the zero-copy path is actually taken — no ~24 MB/frame readback with a HW encoder and a `SurfaceCapturer` (see QA-VID-14) | ☐ |
| QA-TD-16 | No A/V sync mechanism | See QA-AUD-8/9/10 | ☐ |
| QA-TD-17 / TD-24 | IDR cache stored only the IDR NAL — SPS/PPS were lost, so joiners got an undecodable keyframe | **Join late, repeatedly.** Every join decodes on the first keyframe. Capture the bootstrap payload and confirm it contains SPS+PPS+IDR (or VPS+SPS+PPS+IDR for HEVC) | ☐ |
| QA-TD-18 / TD-23 | Broadcast sent one message **per NAL**, producing partial access units that break WebCodecs | Capture the wire for a multi-NAL frame: **one** access unit per message, contiguous Annex B, start codes retained. Then stress it with bursty I/O — the old same-day "fix" was a timing heuristic that could still resplit | ☐ |
| QA-TD-19 | Client always requested `?role=control` — no viewer mode | Viewer mode reachable and enforced (see QA-MC-2, QA-MC-15) | ☐ |
| QA-TD-20 | No orchestrator spec; wiring lived in `main.go` | Startup/shutdown follow the documented sequence; shutdown order is observable in logs | ☐ |
| QA-TD-21 | No handshake — the client guessed the codec | `config` arrives on the control stream **before** any media, carrying the full computed WebCodecs string | ☐ |
| QA-TD-22 | No frame-drop strategy — latency accumulated unboundedly | See QA-VID-15/16 | ☐ |
| QA-TD-25 | **Video and audio stamped with wall-clock milliseconds**, audio at consumption | See QA-AUD-10. Inspect timestamps on the wire directly; this one is invisible until sync is already broken | ☐ |
| QA-TD-26 | New-client handler forced a keyframe **and restarted the capturer** — a storm for every existing viewer | See QA-VID-17/19/21. Watch an existing viewer's picture while 10 clients join: it must not flinch | ☐ |
| QA-TD-28 | Length-prefix NAL framing added AVCC complexity for no benefit | Wire is Annex B per frame | ☐ |
| QA-TD-29 | Frame loop discarded W/H/timestamp; `continue` did not skip capture | Dimensions and timestamps survive capture→encode→broadcast unmodified; a skip really skips the capture | ☐ |
| QA-TD-30 | Non-existent VA-API entry point in the round-1 spec | The libva add-on builds and runs against a real driver | ☐ |
| QA-TD-31 | Config described as a binary frame; codec string too short for WebCodecs | `config` is JSON on the control stream with a full codec string (e.g. `avc1.64002A`, not `avc1.42E01E` from a fixed constant) | ☐ |
| QA-TD-32 | `InputAck` had nothing to echo — no input seq | Input records carry `seq`; the server echoes it; the client shows a real latency number (QA-INP-28) | ☐ |
| QA-TD-33 | Keyframe/resize as binary types vs. the JSON client channel | Both are control-stream JSON | ☐ |
| QA-TD-34 | Typo'd field, unused `minInterval`, undefined Stats methods | Builds clean with no dead parameters; metrics catalog matches what is exported | ☐ |

## Implementation-history findings (round 3)

| ID | The original bug | How to prove it is gone | Status |
|----|------------------|-------------------------|--------|
| QA-TD-35 | **Keys stuck down on the host** when the tab lost focus — no `blur`/`visibilitychange` handling and no pressed-key set | Both halves must work, and a `release_all` with no caller does not close this: client-side (QA-INP-8) **and** host-side on controller change, session close and session timeout (QA-INP-9/10, QA-MC-3) | ☐ |
| QA-TD-36 | No throttling — every raw `pointermove` and `wheel` event was sent | See QA-INP-20. Confirm the server-side rate limit and `mousemove` coalescing, with a 1000 Hz mouse | ☐ |
| QA-TD-37 | Only F1–F12 mapped; **F13–F24 silently did nothing** | See QA-INP-2. Test all 24 explicitly — silent failure is the whole point of this one | ☐ |
| QA-TD-38 | Scroll magnitude discarded — everything collapsed to ±1 | See QA-INP-18. A trackpad fling and a single notch must be distinguishable on the host | ☐ |
| QA-TD-39 | Encoder subprocess respawned **once per frame** forever with no backoff or circuit breaker | See QA-RR-12/13/14. Watch CPU and log volume during a persistent crash — the failure mode was resource burn, not a crash | ☐ |
| QA-TD-40 | Client audio worklet **discarded ~83 % of samples** per callback | See QA-AUD-2/3. This shipped in working code and no test caught it; the conservation test is the check that would have | ☐ |

## Notes for whoever runs this

- **TD-25, TD-35, TD-37, TD-38 and TD-40 are the ones a user notices and a
  developer does not.** Wrong timestamps, stuck keys, dead function keys,
  flattened scrolling and eaten audio samples all look like "working software"
  from the outside. Give them extra attention.
- Several rows are closed in the specs "by construction" (TD-01, TD-04, TD-23).
  Verify them anyway. A structural guarantee that nobody ever observed is a
  belief, not a result.
- If a row is genuinely impossible to observe from outside, record that as a
  finding — it means the system lacks the instrumentation to prove its own
  correctness.
