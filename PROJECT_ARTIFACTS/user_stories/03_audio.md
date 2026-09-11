# User Stories: Audio

QA acceptance-criteria stories covering audio capture startup, the Opus/PCM
codec choice, audio-master A/V sync, and browser playback lifecycle. Audio is
🔒 design-locked / ⏸️ implementation-deferred — these stories describe the
behavior the design commits to, to validate against once implementation lands.
Source specs: `specs/media/MODULE_AUDIO.md`, `specs/PLATFORM_COMPAT.md`,
plus one regression-guard story informed by
`PROJECT_ARTIFACTS/summaries/pipewire_audio_capture/phase3.md`.

---

## US-AUD-1: Audio capture starts alongside video when enabled

**As a** host user
**I want** my system's audio output to stream to viewers alongside video when I enable audio
**So that** viewers hear what I hear on my machine, in sync with the screen

**Acceptance Criteria:**
- Given `[audio] enabled = true` and a per-OS audio capture add-on (`wasapi` / `sck_audio` / `pipewire`) is loaded, When the session starts, Then the add-on normalizes the OS device format to the canonical 48 kHz / S16LE format and delivers `PcmChunk`s stamped at capture time (`CLOCK_MONOTONIC` ns).
- Given the host's output is stereo, When capture starts, Then the client receives 2-channel audio; given the host is configured for 5.1/7.1, When `[audio] channels = "auto"` (default), Then the capture add-on delivers 6/8 channels end-to-end with no upmixing.
- Given `[audio] enabled = true` but no audio capture add-on is loaded, When the server starts, Then it prints a startup warning and audio is silently disabled (matching the "needs an add-on" pattern used by gamepad/input).

**Validated by:** specs/media/MODULE_AUDIO.md — "Overview", "Pluggable Capture Add-Ons (one per OS)", "Configuration"

---

## US-AUD-2: Opus is the default codec when the `opus` add-on is loaded

**As a** viewer
**I want** audio delivered as compressed, loss-resilient Opus by default
**So that** I get good audio quality at low bandwidth even over an imperfect network

**Acceptance Criteria:**
- Given the `opus` add-on is loaded, When the server advertises the config handshake, Then `audioCodec` is `"opus"` and packets use `frame_type::AUDIO_OPUS` (0x08), encoded with `OPUS_APPLICATION_AUDIO`, 20ms frames, VBR, and in-band FEC enabled.
- Given a stereo 20ms Opus packet is produced, When it is sent on the wire, Then it fits in a single datagram (no fragmentation) at ~100–300 bytes.
- Given a 5.1 or 7.1 host layout, When Opus encodes it, Then it uses Opus multistream encoding (channel mapping family 1) rather than treating channels independently.

**Validated by:** specs/media/MODULE_AUDIO.md — "Codec (pluggable, advertised in `config`)"; specs/PLATFORM_COMPAT.md — "Audio" section

---

## US-AUD-3: Raw PCM fallback when the `opus` add-on is not loaded

**As a** host user running a minimal/zero-dependency build
**I want** audio to still work over raw PCM if I haven't loaded the Opus add-on
**So that** audio isn't an all-or-nothing feature gated on an extra library

**Acceptance Criteria:**
- Given no `opus` add-on is loaded, When the server advertises the config handshake, Then `audioCodec` reports the PCM fallback and packets use `frame_type::AUDIO_PCM` (0x04) carrying raw interleaved S16LE.
- Given PCM has no loss concealment, When a datagram carrying a PCM chunk is lost, Then the result is a ~20ms audio gap with no concealment (unlike Opus's FEC/PLC).
- Given a 20ms PCM stereo frame is ~3840 bytes, When it is sent, Then it is fragmented into multiple datagrams (like video), unlike the single-datagram Opus case.

**Validated by:** specs/media/MODULE_AUDIO.md — "Codec (pluggable, advertised in `config`)", "Wire Format"

---

## US-AUD-4: Audio is the sync master — video slaves to the audio clock

**As a** viewer
**I want** video to adjust itself to stay in sync with audio, not the other way around
**So that** what I hear never glitches, even if that means an occasional held or dropped video frame

**Acceptance Criteria:**
- Given both audio and video are stamped with the same `CLOCK_MONOTONIC` epoch at capture, When the client renders, Then the audio playout position (the `AudioContext`/worklet's gapless playback) defines "now" for the session.
- Given a decoded video frame's capture timestamp is ahead of the current audio playout time, When the renderer selects a frame, Then it holds the current frame a beat rather than advancing early.
- Given a decoded video frame is behind the audio playout time by more than ~1 frame interval, When the renderer selects a frame, Then it drops the stale frame(s) to catch up.
- Given audio is disabled or no audio add-on is loaded, When video renders, Then it presents on its own capture clock (no master to slave to) — this is the accepted pre-audio behavior.
- Given the desync bound, When audio and video drift, Then the drift is self-correcting and bounded by the ~40ms audio jitter buffer.

**Validated by:** specs/media/MODULE_AUDIO.md — "A/V Sync — render video to the audio clock (audio is master)", "Performance Targets" (A/V desync bound ≤ ~40ms)

---

## US-AUD-5: Browser playback starts cleanly on the first user gesture

**As a** viewer
**I want** audio playback to begin as soon as I interact with the page, respecting browser autoplay rules
**So that** I hear audio without the browser silently blocking it

**Acceptance Criteria:**
- Given the browser's autoplay policy requires a user gesture, When the viewer first interacts with the page (e.g. a click), Then an `AudioContext` at 48 kHz is created at that moment, not before.
- Given the `config` message reports `audioCodec`, When the client initializes, Then it reads that field to select the decode path (WebCodecs `AudioDecoder` for Opus, direct S16LE→Float32 conversion for PCM) rather than hardcoding a codec assumption.
- Given decoded Float32 audio is available, When it is delivered to playback, Then it is pushed into an `AudioWorklet` ring buffer (~40ms) and played gaplessly.
- Given the decoded channel count exceeds the output device's channel count, When the client prepares playback, Then it downmixes to the device layout (e.g. 5.1 → stereo) using standard ITU-R coefficients before buffering.

**Validated by:** specs/media/MODULE_AUDIO.md — "Browser Playback"

---

## US-AUD-6: Genuine audio loss is concealed, not a stall

**As a** viewer
**I want** a brief real network loss to be smoothed over rather than causing playback to stall
**So that** my listening experience stays continuous even under imperfect network conditions

**Acceptance Criteria:**
- Given a datagram carrying an Opus packet is lost, When the decoder cannot recover it via FEC, Then Opus PLC (packet loss concealment) fills the gap.
- Given a genuine gap with no FEC recovery, When the worklet reaches that gap in playout, Then it outputs PLC/silence for that ~20ms window rather than stalling or throwing a decode error.

**Validated by:** specs/media/MODULE_AUDIO.md — "Codec (pluggable, advertised in `config`)" (Opus FEC/PLC), "Browser Playback" (step 5)

---

## US-AUD-7: Clean teardown of browser audio playback

**As a** viewer
**I want** audio playback to stop cleanly when I leave or the session ends
**So that** I don't get a stuck `AudioContext`, a leaked worklet, or leftover sound after disconnecting

**Acceptance Criteria:**
- Given the viewer closes the tab, navigates away, or the session ends, When the client tears down, Then the `AudioContext` and its worklet are released with no dangling audio resources.
- Given the viewer's tab loses visibility (backgrounded) mid-session, When playback continues or pauses, Then the behavior is well-defined (either continues gaplessly or pauses/resumes deterministically) rather than left unspecified.

**Validated by:** specs/client/MODULE_WEB_CLIENT.md — **R-CLI-14** "Deterministic Teardown and Backgrounded-Tab Behavior" (idempotent `teardown()` off `pagehide`, worklet node disconnected before `audioContext.close()`, per-resource backgrounding table), cross-referenced from specs/media/MODULE_AUDIO.md — "Browser Playback" step 6.

> Was flagged `GAP` in the first pass. Closed by R-CLI-14, which also answers the story's second criterion explicitly rather than leaving it "well-defined either way": a backgrounded tab **keeps audio playing gaplessly** and stops video decode, because audio is the master clock and pausing it would desync on return.

---

## US-AUD-8: Back-pressure never causes unbounded audio latency

**As a** host user
**I want** the audio pipeline to prioritize freshness over completeness when it falls behind
**So that** audio doesn't accumulate delay that would break the audio-is-master sync guarantee

**Acceptance Criteria:**
- Given the add-on's own capture ring is small (4 frames, ~80 ms at 20 ms chunks), When it overflows, Then the oldest chunk is dropped so the host's audio thread always pulls the freshest audio, rather than growing the buffer — the ring lives inside the add-on, on its own side of the ABI, and no channel crosses the boundary.
- Given timestamps are stamped at capture (not at consumption), When chunks flow through the pipeline, Then they remain on the same monotonic timeline as video regardless of pipeline scheduling jitter.

**Validated by:** specs/media/MODULE_AUDIO.md — "Realtime Pipeline" ("Small buffers everywhere"), "Testing Strategy" (Unit: back-pressure drop-oldest behavior)

---

## US-AUD-9: Audio playback must never silently drop the majority of samples

**As a** viewer
**I want** every sample of audio the host sends to actually reach my speakers (barring genuine network loss)
**So that** what I hear is complete and correct, not silently truncated by a client-side processing bug

**Acceptance Criteria:**
- Given a continuous stream of PCM chunks is delivered to the browser's audio worklet, When the worklet's processing callback runs repeatedly, Then it consumes and plays back all buffered samples via a continuous ring buffer — it must not discard the majority of samples per callback.
- Given a fixed-size processing callback shorter than the available buffered audio, When the callback returns, Then the unplayed remainder is retained in the ring buffer for the next callback, not dropped.
- Given a synthetic test feeds a known sample sequence through the playback path, When the output is captured, Then the played sample count matches the input sample count (net of genuine, documented network loss) rather than losing ~83% of samples per callback.

**Validated by:** specs/media/MODULE_AUDIO.md — "Browser Playback" (step 4, AudioWorklet ring buffer)
**Regression guard:** PROJECT_ARTIFACTS/summaries/pipewire_audio_capture/phase3.md — T7 documents a shipped bug where the `PCMProcessor` discarded ~83% of samples per callback; it was fixed by switching to a continuous ring buffer, but no regression test currently guards against a recurrence.

---

## US-AUD-10: The client microphone reaches host applications as a real input device

**As a** controller
**I want** my microphone to appear on the remote host as a normal input device
**So that** I can talk in a call, dictate, or use voice input in an application running on the host

**Acceptance Criteria:**
- Given `[audio] mic_enabled = true` and a virtual-mic add-on is loaded, When I grant the browser microphone permission and speak, Then my audio is encoded in the session's codec, sent as an `AUDIO_MIC` datagram (frame type 9, the only client→server datagram in v1), decoded on the host, and is readable at the host's virtual input device at the same frequency with no channel swap.
- Given a session whose effective role is `view` or `player`, When it sends an `AUDIO_MIC` datagram, Then the server drops it and increments `featherdesk_role_rejects_total{op="mic"}`, and the session is not closed — a viewer must never be able to speak into the host.
- Given `mic_enabled = true` on a host with no `AudioSink` add-on loaded (including every macOS host in v1), When the server starts, Then it logs a warning, the mic stays off, and host→client audio and the rest of the session are unaffected.
- Given the mic is streaming, When `decode()` fails on one packet or `write_chunk()` returns `Unrecoverable`, Then only that packet is dropped (or the sink is poisoned) and the video and audio session keeps running.
- Given no session holds the controller slot, When the host's virtual device is read, Then it yields silence rather than a repeat of the last controller's buffer.
- Given FeatherDesk created the virtual device, When the server shuts down cleanly or is killed, Then no `FeatherDesk Mic` device is left behind on the host.

**Validated by:** specs/media/MODULE_AUDIO.md — "Microphone (client→host)"
