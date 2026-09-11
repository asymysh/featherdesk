# QA 07 — Audio (host → client)

Matching user stories: `PROJECT_ARTIFACTS/user_stories/03_audio.md`.

> **Status.** `MODULE_AUDIO.md` is design-locked. Its implementation trigger is
> being changed under **OQ-02** from "video working on all three OSes" to "video
> working on **Linux**", so audio is expected in v1 on Linux first. Until it
> lands, every row here is `DEFERRED` — which is **not** a pass, and a release
> note must say so.
>
> Client→host **microphone** (OQ-02b) is a separate, later item and is not
> covered here.

## Playback and correctness

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-AUD-1 | Play music on the host with `[audio] enabled = true` | Audible in the browser, continuous, no clicks or gaps over 60 s | `MODULE_AUDIO` | ☐ |
| QA-AUD-2 | **Sample conservation.** Play a known tone for 60 s and compare samples in vs. out | `samples_out == samples_in`. Any loss must be an explicitly counted back-pressure event, **never** a buffer-size mismatch. This is the exact shape of the bug that shipped once | TD-40 | ☐ |
| QA-AUD-3 | Client-side ring: push `frame_ms`-sized chunks, pull 128-sample render quanta where the chunk size is **not** a multiple of 128 | Nothing lost. The original bug discarded ~83 % of samples per callback | TD-40 | ☐ |
| QA-AUD-4 | Stereo, then 5.1, then 7.1 host output | Channel count follows the host layout up to 7.1; channels are in the right places (front-left really is front-left) | `MODULE_AUDIO` / `PLATFORM_COMPAT` | ☐ |
| QA-AUD-5 | `[audio] channels = "stereo"` on a 7.1 host | Forced to 2 and correctly downmixed | `MODULE_AUDIO` | ☐ |
| QA-AUD-6 | With the `opus` add-on loaded, and then without it | Opus when present; raw S16LE PCM fallback when absent. The wire codec is advertised in `config` and the client honours it | `OPUS_AUDIO_CODEC_SPEC` | ☐ |
| QA-AUD-7 | Change the host output device mid-session | Handled without killing the stream, or fails with a clear message. Record which | `MODULE_AUDIO` | ☐ |

## A/V sync

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-AUD-8 | Play a video with clear lip movement, or a clapper-board test clip | Lips match sound. **Audio is the master clock** — video holds or drops to track audio; audio is never held or dropped for sync | CENTRAL_SPEC "Canonical Media Clock" | ☐ |
| QA-AUD-9 | Sustained 10-minute A/V playback | No progressive drift. Drift is the signature of a wall-clock timestamp sneaking back in | TD-25 | ☐ |
| QA-AUD-10 | Compare audio and video timestamps at the wire level | Both are `CLOCK_MONOTONIC` **nanoseconds** from one process-wide epoch, sampled **at capture**. Not `UnixMilli`, not sampled at consumption | TD-25; R-PRO-05 | ☐ |
| QA-AUD-11 | Motion-to-photon with audio **disabled**, then **enabled** | Roughly ~20 ms vs ~65 ms, as the budget states. This is a documented trade, not a regression | CENTRAL_SPEC "Motion-to-photon budget" | ☐ |
| QA-AUD-12 | `[audio] frame_ms = 10` | Total drops toward ~45 ms as documented | CENTRAL_SPEC budget note | ☐ |

## Loss and teardown

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-AUD-13 | Induce 1–2 % loss with audio playing (WebTransport) | Concealment behaves per path: full Opus FEC/PLC on the wasm-libopus path, bounded clock-preserving worklet concealment on the WebCodecs path. No pitch drift, no runaway buffer | `MODULE_AUDIO` "Loss concealment, by path" | ☐ |
| QA-AUD-14 | Audio out-queue overflow (stall the client's read) | Drop-oldest at `audio_send_queue_chunks`; a gap the client conceals; **video is not delayed** — separate ring | Queue 2 | ☐ |
| QA-AUD-15 | Background the browser tab, then return | Audio behaves per spec (no unbounded buffering while hidden); video presentation resumes cleanly | R-CLI-14 | ☐ |
| QA-AUD-16 | Close the tab mid-playback | `AudioContext` and worklet released promptly — no leaked audio device handle on the client. Check the OS audio device list | R-CLI-14 | ☐ |
| QA-AUD-17 | `[audio] enabled = false` (the default) | No audio capture occurs at all on the host — confirm no capture add-on is opened and no device is held | `MODULE_CONFIG` | ☐ |
| QA-AUD-18 | Audio add-on missing while `[audio] enabled = true` | Clean degradation to a video-only session with a clear log line — not a startup failure | `MODULE_PIPELINE` step 11 | ☐ |
