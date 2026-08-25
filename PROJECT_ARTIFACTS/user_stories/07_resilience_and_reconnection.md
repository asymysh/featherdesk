# User Stories: Resilience & Reconnection

Covers network loss/reconnect, bandwidth adaptation, and hardware encoder
fallback/device-loss handling, as specified in `specs/core/MODULE_SERVER.md`,
`specs/core/MODULE_STREAM_PARAMS.md`, and `specs/media/MODULE_HARDWARE_ENCODE.md`.

---

## US-RR-1: Session resumes after a short network drop without full re-auth

**As a** viewer or controller who briefly loses network connectivity
**I want** my session to resume where it left off (same resolution/bitrate, no full re-login) if I reconnect quickly
**So that** a short Wi-Fi blip doesn't force me to restart the whole session

**Acceptance Criteria:**
- Given a session disconnects, When it reconnects within `[reconnect] cache_ttl_seconds` (default 300s) and sends `{"type":"auth","token":"<bearer>","resume":true}`, Then `SessionCache.get` hits and the server marks the session "resumed", skipping fresh-session setup.
- Given a resumed session, When the server sends the config handshake, Then it restarts at the same resolution/bitrate/HDR captured in `SessionState.last_params` at disconnect, rather than renegotiating from defaults.
- Given a resumed session, When the decoder needs seeding, Then the server always reseeds via a fresh bootstrap-stream IDR (there is no `LastVideoSeq` optimization — resume never assumes stored sequence continuity).
- Given the reconnect happens after `cache_ttl_seconds` has elapsed, When the client sends `resume:true`, Then the cache misses and the client goes through fresh auth instead.

**Validated by:** specs/core/MODULE_SERVER.md — "WebTransport Session Lifecycle" step 8 (Resume path), "Session Cache (Reconnect)", "Session State" (`last_params`)

---

## US-RR-2: Vanished clients are detected and cleaned up without app-level polling

**As a** host user
**I want** a client that disappears (crashed browser, unplugged cable) to be detected and its resources released automatically
**So that** dead sessions don't linger and consume server resources or a controller slot

**Acceptance Criteria:**
- Given a client vanishes without a clean close, When it stops ACKing the datagrams the server streams to it, Then QUIC's `max_idle_timeout` (default 30s) closes the connection with no application-level liveness logic required.
- Given a vanished controller, When its session is cleaned up, Then its controller slot is released and available to the next client authenticating with `"role":"control"`.
- Given a vanished session, When cleanup runs, Then all per-session tasks are cancelled via the session's cancellation token, and its state is retained in `SessionCache` for `cache_ttl_seconds` in case it reconnects.

**Validated by:** specs/core/MODULE_SERVER.md — "Keepalive, liveness & timeouts", "WebTransport Session Lifecycle" step 19 (Session close), Testing Strategy row "Client disconnect cleanup (no panic, count=0)"

---

## US-RR-3: Bitrate scales down immediately under a sudden congestion signal

**As a** viewer on a degrading network
**I want** the video bitrate to drop quickly when the server detects it can't keep its outgoing queue drained
**So that** I get a lower-quality but smooth stream instead of a stalled/buffering one

**Acceptance Criteria:**
- Given the server's per-session `frame_out` queue is dropping frames on overflow (a sustained drop spike), When the telemetry loop observes this on its next 100ms tick, Then the effective bitrate is immediately cut by 0.5x on that single measurement (the FAST path), without waiting for client-reported stats.
- Given the bitrate is adjusted, When the new value is computed, Then it is sent to the pipeline's `param_ch` and applied on the frame loop — no separate config message is sent to the client (a bitrate change is transparent to the decoder).
- Given `[stream.adaptive] enabled = false`, When congestion occurs, Then no automatic bitrate adjustment happens (the operator opted out).

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Bandwidth Adaptation (Server-Measured, Manager-Driven)" (FAST path)

---

## US-RR-4: Bitrate scales down gradually under sustained packet loss

**As a** viewer on a lossy network
**I want** the stream to back off bitrate proportionally to reported packet loss, and recover once conditions improve
**So that** the video quality tracks my actual network conditions instead of oscillating or staying degraded forever

**Acceptance Criteria:**
- Given `PacketLossPct > loss_threshold_pct` (default 5.0%) for 2 consecutive 100ms windows (200ms), When the SLOW path evaluates, Then `new_bitrate = max(current * adjustment_factor (0.7), min_bitrate)`.
- Given `PacketLossPct < recovery_threshold_pct` (default 1.0%) for 10 consecutive windows (1s) AND RTT is stable, When the SLOW path evaluates, Then `new_bitrate = min(current * recovery_factor (1.1), max_bitrate)`.
- Given `min_bitrate_bps` (default 1 Mbps) and `max_bitrate_bps` (default 25 Mbps), When any adjustment is computed, Then the result never goes below the floor or above the ceiling.
- Given severe degradation (>15% loss for 30s), When the adaptation policy runs, Then only bitrate is adapted — resolution-level adaptation is explicitly deferred to a future version and must not be assumed to exist in v1.

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Bandwidth Adaptation" (SLOW path), "Bounds and policy knobs", "Resolution-level adaptation (deferred)"

---

## US-RR-5: Hardware encoder failure falls back to software without dropping the session

**As a** host user relying on GPU-accelerated encoding
**I want** a hardware encoder failure (surface import failure, GPU reset, driver constraint) mid-session to transparently switch to software encoding
**So that** a GPU hiccup doesn't disconnect my viewers or crash the host

**Acceptance Criteria:**
- Given a hardware encoder add-on is active, When `encode_surface` cannot import the surface (format mismatch, GPU reset, or driver constraint), Then it returns `StreamError::FallbackToSoftware` and still releases the surface exactly once (the FbInfo's RAII Drop fires on this path just as on success/error).
- Given `StreamError::FallbackToSoftware` is returned, When the pipeline catches it, Then it switches to a software encoder add-on for the remainder of that session — existing viewers keep receiving frames, just from the new encoder, with no session teardown.
- Given a fallback has occurred for a session, When the frame loop continues, Then the pipeline does NOT retry the hardware path again mid-session (fallback is one-way per session).
- Given the runtime probe/selection order in `specs/CENTRAL_SPEC.md` (`nvenc -> amf -> libva -> qsv -> mf_hw -> vt_hw` for HW; `x264 -> vt_sw -> openh264` for SW), When falling back, Then the pipeline selects from the SW probe order, not an arbitrary encoder.

**Validated by:** specs/media/MODULE_HARDWARE_ENCODE.md — "Public Interface" (`encode_surface` docs on `FallbackToSoftware`), Testing Strategy rows "the M-1/TD-01 invariant" and "StreamError::FallbackToSoftware degrades the pipeline to a SW encoder add-on for the remainder of the session — never retries the HW path mid-session"; specs/CENTRAL_SPEC.md — "Runtime probe and selection" item 4

---

## US-RR-6: A crashed or reset GPU does not corrupt or double-free surface resources

**As a** developer-operator running on GPU hardware prone to resets under load
**I want** a GPU reset during encoding to be handled as a clean, well-defined error path rather than a resource leak or crash
**So that** the host process stays stable across transient GPU faults

**Acceptance Criteria:**
- Given a GPU reset occurs during `encode_surface`, When the call returns `StreamError::FallbackToSoftware`, Then the surface (`FbInfo`) is dropped and its underlying GPU resource released exactly once — never left dangling, never double-freed.
- Given this is the same code path exercised on success and on ordinary errors, When tested, Then all three outcomes (success, error, `FallbackToSoftware`) are verified to release the resource exactly once, across all vendor implementations.

**Validated by:** specs/media/MODULE_HARDWARE_ENCODE.md — "Public Interface" (SURFACE OWNERSHIP note), Testing Strategy row "encode_surface's FbInfo argument is dropped ... exactly once across all three outcomes"

---

## US-RR-7: Chroma subsampling gracefully falls back when a client can't decode it

**As a** viewer whose browser cannot decode the host's chosen chroma subsampling
**I want** the stream to automatically downgrade to a universally-supported format instead of failing to decode
**So that** I still get a working (if slightly less sharp) picture rather than a broken session

**Acceptance Criteria:**
- Given the host is encoding with 4:2:2 or 4:4:4 chroma, When the client reports `{"type":"chroma_unsupported"}`, Then `stream::Manager` downgrades to 4:2:0 and the server re-sends the config message and forces a keyframe.
- Given an encoder add-on cannot produce the requested chroma format at all, When probed, Then it returns `StreamError::ChromaUnsupported` and the pipeline falls back to 4:2:0 for that session, matching the client-side decode gate.

**Validated by:** specs/core/MODULE_SERVER.md — "WebTransport Session Lifecycle" step 17 (`chroma_unsupported` handling); specs/media/MODULE_HARDWARE_ENCODE.md — Testing Strategy row "Chroma capability advertisement + StreamError::ChromaUnsupported fallback to 4:2:0"

---

## US-RR-8: Graceful shutdown notifies clients instead of silently dropping them

**As a** viewer or controller connected when the host operator stops the server
**I want** to be told the server is shutting down rather than just seeing the connection die
**So that** I understand what happened instead of assuming a network fault

**Acceptance Criteria:**
- Given a graceful shutdown is initiated, When the server closes sessions, Then it closes each with `close::SERVER_SHUTDOWN (4503)`, a distinct code from a network-fault close, so the client can show "server shutting down" rather than a generic connection-lost message.

**Validated by:** specs/core/MODULE_SERVER.md — "Keepalive, liveness & timeouts" (Graceful shutdown), R-SRV-05 (Graceful Client Notification on Shutdown)

---

## US-RR-9: A persistently crashing encoder degrades gracefully instead of restart-looping

**As a** host operator whose GPU driver or `ffmpeg` install is broken
**I want** the server to give up on a failing encoder and try the next one
**So that** I get either a working stream or a clear error — not a machine burning a core in a silent restart loop

**Acceptance Criteria:**
- Given the `x264` add-on's `ffmpeg` child dies, When it is restarted, Then the delay follows a bounded exponential ladder (100/200/400/800/1600 ms, ±20 % jitter) — **not** a flat sleep-and-respawn.
- Given the child dies a 6th time inside the 60 s window, When the next `encode()` is called, Then the add-on returns `StreamError::Unrecoverable` and **stops spawning entirely** — no 7th process, no further sleeps.
- Given a child that dies once and then runs healthy for more than 60 s, When it later dies again, Then the ladder restarts at attempt 1 (100 ms) — the counter decays rather than accumulating, so a once-an-hour blip self-heals forever.
- Given the pipeline receives `Unrecoverable`, When it recovers, Then it **never retries that add-on**: it poisons it for the session (including for `degrade_to_software`), walks the startup probe order to the next candidate, and on a successful swap forces an IDR and pushes a fresh `config`.
- Given no candidate encoder remains after fall-through, When the pipeline gives up, Then it shuts down with a clear terminal error rather than spinning.
- Given a child is dead and awaiting restart, When frames arrive, Then they are **dropped, not buffered**, and the log gets one `warn` **per restart attempt** — never one line per frame. Log flooding was half the original symptom.
- Given `ffmpeg` is missing or not executable, When the add-on is probed at startup, Then it reports `available: false` with a reason — this is caught at probe time, never as a runtime crash loop.

**Validated by:** specs/core/MODULE_PIPELINE.md — "Add-On Crash Recovery (backoff + circuit breaker)" + its four Testing Strategy rows; specs/addons/linux/encoders/SW/X264_SUBPROCESS_LINUX_SPEC.md — "Crash Recovery (subprocess death)"; specs/core/MODULE_STREAM_PARAMS.md — `StreamError::Unrecoverable`; specs/core/MODULE_ABI.md — `AbiErr::Unrecoverable` (code 7)
**Regression guard:** TD-39 — the Go `internal/encode/ffmpeg.go` `restart()` was a flat kill + 50 ms sleep + respawn with no attempt counter, restart-looping roughly every 250 ms indefinitely on a persistent fault.

---

## US-RR-10: Host clipboard changes reach the controller, and the direction policy is enforced

**As a** host user who copies something on the host machine
**I want** it to appear in my remote controller's clipboard, subject to the configured direction policy
**So that** clipboard sync actually works both ways — and does not leak host secrets when I've disabled that direction

**Acceptance Criteria:**
- Given the host clipboard changes, When `Monitor::changes()` emits, Then the pipeline's clipboard task calls `server.send_clipboard`, and the controller receives `[u32 Len][JSON]` on the clipboard stream (tag `0x02`).
- Given `[clipboard] direction` forbids host→client, When the host clipboard changes, Then the server drops the push silently and no client receives it — the gating is in the server, not the caller.
- Given passive viewers are connected, When a host clipboard change is pushed, Then only the **controller** receives it; viewers never do, regardless of `direction`.
- Given a client's clipboard stream is slow or not yet open, When a push is attempted, Then that push is dropped for that session and a metric is bumped — the monitor task is never back-pressured by one slow client.
- Given the session is shutting down, When the clipboard task exits, Then the `changes()` receiver is dropped before the monitor, and no further `send_clipboard` occurs.

**Validated by:** specs/CENTRAL_SPEC.md — **Contract 9: Clipboard <-> Server**; specs/core/MODULE_SERVER.md — `send_clipboard`; specs/core/MODULE_PIPELINE.md — startup step 12/13 + Testing Strategy row "Clipboard H→C drain"; specs/interaction/MODULE_CLIPBOARD.md — "Internal Architecture"

> Added in the final review pass. US-CF-1 asserts bidirectional sync, but the host→client half had **no method on the `Server` trait and no drainer in the pipeline** — both MODULE_CLIPBOARD and MODULE_SERVER assumed the wire existed and neither constructed it.
