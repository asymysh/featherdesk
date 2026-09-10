# User Stories: Video Streaming

QA acceptance-criteria stories covering capture startup, software vs. hardware
encoder selection, new-viewer-join behavior (the TD-26 fix), dynamic
resolution/HDR/chroma changes, and frame-drop/adaptive-bitrate behavior under
load. Source specs: `specs/media/MODULE_CAPTURE.md`, `specs/media/MODULE_ENCODE.md`,
`specs/media/MODULE_HARDWARE_ENCODE.md`, `specs/core/MODULE_STREAM_PARAMS.md`,
`specs/CENTRAL_SPEC.md`.

---

## US-VID-1: Capture starts and streams the host's native resolution

**As a** viewer
**I want** the host to start capturing its screen the moment I connect
**So that** I see live video of the remote desktop without manual setup

**Acceptance Criteria:**
- Given a capture add-on (`kms_egl`, `dxgi_dd`, or `sck`) is loaded, When the pipeline starts, Then it probes the loaded add-on(s), selects one per the configured priority order, and calls its constructor with `CaptureConfig`.
- Given the selected display's native resolution, When `[stream] width/height = 0`, Then the capturer emits frames/surfaces at that native resolution and the encoder (not the capturer) performs any scaling to the advertised stream dimensions.
- Given the screen is idle, When `next_frame()`/`next_surface()` is called, Then it returns `Ok(None)` within one frame interval (16.7 ms at the default `[stream] fps = 60`) rather than a stale frame, and no re-encode occurs — a newly joined client is still served from the IDR cache at near-zero bandwidth cost.

**Validated by:** specs/media/MODULE_CAPTURE.md — "Public Interface", "Resolution: capture is always native; the encoder scales", "Testing Strategy"

---

## US-VID-2: Hardware encoder is preferred when available

**As a** host user
**I want** the server to automatically use my GPU's hardware encoder instead of burning CPU on software encoding
**So that** I get lower latency and lower host CPU usage without manual configuration

**Acceptance Criteria:**
- Given `[encode] mode = "auto"` and one or more HW encoder add-ons are loaded, When the pipeline starts, Then it probes them in vendor-priority order (NVENC/AMF/libva before generic MF HW; VT HW on macOS) before falling through to any SW encoder.
- Given a HW encoder is selected, When it processes a native-resolution GPU surface, Then it scales in-encoder to `initial_params.width × height` so encoder-output dims equal the advertised `config` dims.
- Given the HW encoder advertises its active codec via `codec()`, When the config handshake is sent, Then the server forwards that exact WebCodecs codec string (`avc1.*` by default, `hvc1.*` only for HDR), and the level in that string is computed from the active resolution and frame rate — and the server has already gated that choice on the decode capability every attached client reported at auth.

**Validated by:** specs/media/MODULE_HARDWARE_ENCODE.md — "Selection (Pipeline Owns This)", "Zero-Copy Pipeline"

---

## US-VID-3: Software encoder fallback when no hardware encoder is present

**As a** host user
**I want** streaming to still work on a machine with no usable GPU encoder
**So that** FeatherDesk isn't limited to hardware-accelerated hosts only

**Acceptance Criteria:**
- Given no HW encoder add-on probes successfully, When the pipeline selects an encoder, Then it falls through to a loaded SW encoder add-on in the order `OpenH264 > VT SW (macOS only)`; the `x264` subprocess add-on is **opt-in** and is reached only via `[encode] force_addon = "x264"`, never by `mode = "auto"`.
- Given the SW path is active, When a capture `Frame` (BGRA on macOS/Windows, RGBA on Linux GL) arrives, Then the `Converter` transforms it to I420 via `ARGBToI420Matrix` with the byte-order-correct constants (`kArgbH709Constants` for BGRA-in-memory, `kAbgrH709Constants` for RGBA-in-memory) before encoding — BT.709 limited range. The unsuffixed `ARGBToI420` / `ABGRToI420` are BT.601-limited-only and MUST NOT be used (`MODULE_ENCODE.md`).
- Given no hardware exists at all (e.g. the "No GPU" row in the platform compatibility matrix), When the server starts, Then OpenH264 (Rust FFI, BSD) is available as the universal SW fallback on every OS and encodes at or below 16.7 ms p50 at 1080p on four threads — realtime at 60 fps with no external binary on the host.

**Validated by:** specs/media/MODULE_ENCODE.md — "Encoder Selection (Pipeline Owns This)", "Color Space Conversion"; specs/PLATFORM_COMPAT.md — "Encoder Selection and Codec Policy"

---

## US-VID-4: Hardware path failure degrades permanently to software for the session

**As a** viewer
**I want** streaming to keep working (rather than stall or crash) if the host's GPU encoding path fails mid-session
**So that** a driver hiccup on the host doesn't end my session

**Acceptance Criteria:**
- Given an active HW encoder session, When `encode_surface` returns `StreamError::FallbackToSoftware` (surface import error, driver constraint, GPU reset), Then the moved-in `FbInfo` is still dropped (releasing its resource) even on this failure path.
- Given the fallback is triggered, When the pipeline catches it, Then it switches to a SW encoder add-on for the remainder of the session and never retries the HW path mid-session.

**Validated by:** specs/media/MODULE_HARDWARE_ENCODE.md — "Public Interface" (`encode_surface`), "Testing Strategy" (Integration: FallbackToSoftware degrades permanently)

---

## US-VID-5: A newly joined viewer receives a cached keyframe without disrupting existing viewers

**As a** viewer joining an in-progress session
**I want** to be served video immediately from a cached keyframe
**So that** I don't have to wait for (or trigger) capture/encoder restarts that would glitch everyone else's stream

**Acceptance Criteria:**
- Given a session with existing viewers already receiving a live video stream and a cached IDR that is **fresh** (nothing has been broadcast since it), When a new client authenticates, Then the server serves that cached keyframe access unit (SPS+PPS+IDR) over a fresh bootstrap uni stream and forces nothing — and it never restarts the capturer, on any join.
- Given the new client join, When the server handles it, Then existing viewers observe no capture restart, no keyframe storm, and no visible glitch in their stream.
- Given the cached IDR is **stale** (a frame has been broadcast since it), When a client joins, Then the server raises a keyframe request through the same 500 ms-coalesced path clients use and waits up to `[transport] join_idr_timeout` (default 1 s) for a fresh one, rather than seeding the joiner with an IDR whose successor frames it will never receive.
- Given repeated rapid joins, When the server serves bootstrap IDRs, Then every request — join, client-requested and queue-drop — funnels through the one 500 ms coalescer, so a burst of joins costs at most 2 forced IDR/s in total.

**Validated by:** specs/CENTRAL_SPEC.md — "Round-2 Review Findings" (TD-26: "Serve cached IDR; conditional keyframe; never restart capture; rate-limit"); specs/core/MODULE_TRANSPORT.md — "Connection Lifecycle" (bootstrap uni stream carries the seed IDR)

---

## US-VID-6: Resolution changes mid-session apply cleanly without oscillation

**As a** viewer
**I want** resizing my browser window to smoothly reconfigure the stream resolution
**So that** the video always fills my window at a decodable resolution without flicker or repeated re-negotiation

**Acceptance Criteria:**
- Given the client's window is resized, When the resize event fires, Then the client debounces for 250ms before sending `{"type":"resize","width":W,"height":H}` on the control stream.
- Given the server receives a resize request, When it validates the request, Then it clamps the requested dimensions to the display's native resolution — a client cannot request 4K on a 1080p display.
- Given a requested change is ≤5% per dimension and ≤2% aspect-ratio change, When the server evaluates hysteresis, Then the resize is suppressed and the server instead sends `{"type":"resize_suppressed","width":W,"height":H}` so the client can letterbox/pillarbox instead of stretching.
- Given a resize is applied, When the new dimensions take effect, Then the pipeline forces an IDR on the first frame after the change and that frame reaches the client within 2 frame intervals (33 ms at 60 fps) of the `config` message.
- Given multiple resize requests arrive concurrently, When the pipeline processes its `param_tx`, Then only the latest is applied — no stale intermediate resolution is ever rendered.

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Dynamic Resolution Change Flow", "Testing Strategy" (resize hysteresis, concurrent resize coalescing)

---

## US-VID-7: HDR requests succeed end-to-end or fail gracefully to SDR

**As a** host user with an HDR-capable display and GPU
**I want** to enable HDR streaming when supported, and get a clear fallback when it isn't
**So that** I either get real HDR or a working SDR stream — never a broken half-HDR state

**Acceptance Criteria:**
- Given `Params.hdr = true` is requested and a HEVC-Main10-capable encoder (NVENC/AMF/MF HW HEVC/VT HW HEVC) is loaded, plus a capture add-on that supports 10-bit, When the pipeline processes the request, Then it configures capture for 10-bit/`bt2020`, switches the encoder to HEVC Main10, sends the client a `config` with codec `hvc1.*`, and emits the HDR10 mastering-display/content-light-level SEI inside every keyframe access unit.
- Given no HEVC-Main10-capable encoder is loaded, When an HDR request arrives, Then the pipeline rejects it and sends `{"type":"hdr_unavailable"}` on the control stream via `Server::send_control`, carrying a `reason` (`no_hevc_encoder`), and the session stays SDR (H.264, bt709) — capture is never half-switched to 10-bit.
- Given an encoder has already initialized for HDR, When an SDR revert is requested, Then it requires a full teardown/recreate (HDR is a one-way trip per session without a restart).

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "HDR Pipeline (Full Flow)", "Testing Strategy" (HDR terminal case)

---

## US-VID-8: Chroma subsampling upgrades fall back transparently to 4:2:0

**As a** viewer using the native client (or a capable browser)
**I want** sharper text/UI via 4:2:2 or 4:4:4 chroma when both the encoder and my decoder support it
**So that** remote-desktop text rendering looks crisp, without ever ending up with an undecodable stream

**Acceptance Criteria:**
- Given `Params.chroma_subsampling = "444"` is requested, When the active encoder cannot produce it (e.g. OpenH264, 4:2:0-only), Then the encoder returns `StreamError::ChromaUnsupported` and the pipeline downgrades to the encoder's best supported chroma.
- Given the encoder-side chroma is accepted, When the resulting codec string is advertised in `config` and the client's `VideoDecoder.isConfigSupported()` probe rejects it while `chroma != "420"`, Then the client sends `{"type":"decode_unsupported","codec":"<the string it could not configure>","chroma":"<the advertised chroma>","hdr":<the advertised hdr>}` on the control stream and never calls `configure()` on a config that probed unsupported.
- Given the client rejects the chroma, When the server processes that message, Then it downgrades to "420" session-wide, re-sends `config` with the 4:2:0 codec string, and forces a keyframe — this rung always terminates at 4:2:0, which is guaranteed decodable everywhere.
- Given the probe still fails at `chroma == "420"` and `hdr == false`, When the client reports it, Then the server sends `{"type":"codec_unavailable","codec":"<the string it could not configure>","reason":"client_cannot_decode"}` to that client only and logs at `warn` — there is no lower rung, the stream is unchanged for every other client, and a client that cannot decode `avc1.6400*` is outside the supported browser set.

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Chroma Subsampling", "Testing Strategy" (chroma fallback cascade)

---

## US-VID-9: Frame drop under load stays above a minimum FPS floor

**As a** viewer on a congested network or an overloaded host
**I want** the stream to drop frames intelligently rather than accumulate unbounded latency
**So that** what I see stays close to real-time instead of drifting further and further behind

**Acceptance Criteria:**
- Given the encode/send path falls behind under load, When the pipeline's frame-drop logic engages, Then it skips frames to keep the delivered rate at or above the **5 FPS floor** and never queues more than one frame ahead — a 30-second overload must not increase end-to-end latency by more than one frame interval.
- Given a sustained datagram-out-queue drop spike is observed server-side, When the fast adaptation path triggers, Then bitrate is immediately reduced (0.5×, single measurement) rather than continuing to force full-quality frames into a congested link.

**Validated by:** specs/CENTRAL_SPEC.md — "New Issues Found During Design Review" (TD-22: "Pipeline: skip-accumulation bound + sustainable-rate control"); specs/core/MODULE_STREAM_PARAMS.md — "Congestion-Reactive Bitrate Control" (FAST path)

---

## US-VID-10: Bitrate adapts to network conditions without a visible stream restart

**As a** viewer on a variable-quality network connection
**I want** the video bitrate to automatically decrease when my connection degrades and recover when it improves
**So that** I get the best possible quality my network can sustain without manual tuning or a broken decoder

**Acceptance Criteria:**
- Given `packet_loss_pct > 5%` for 2 consecutive 100ms windows (200ms), When the Manager's slow-path adaptation runs, Then `new_bitrate = max(current * 0.7, min_bitrate_bps)` is applied.
- Given `packet_loss_pct < 1%` for 10 consecutive windows (1s) and RTT is stable, When the recovery path runs, Then `new_bitrate = min(current * 1.1, max_bitrate_bps)` is applied.
- Given a bitrate change is applied, When it is sent to the frame loop via `param_tx`, Then no `config` message is sent to the client — a bitrate change is transparent to the client's decoder (no reconfigure/glitch).

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Congestion-Reactive Bitrate Control (Server-Measured, Manager-Driven)"

---

## US-VID-11: Idle screens cost near-zero bandwidth

**As a** host user stepping away from the keyboard
**I want** an unchanging screen to stop consuming meaningful encode/network resources
**So that** an idle session doesn't waste bandwidth or CPU while nothing is happening

**Acceptance Criteria:**
- Given the screen produces no new content, When `next_frame()`/`next_surface()` is called, Then it returns `Ok(None)` within one frame interval (16.7 ms at `fps = 60`) and the caller skips that tick — it does not re-encode a duplicate frame.
- Given a new client joins during this idle period and the cached IDR is still fresh, When it connects, Then it is served from the IDR cache over the bootstrap stream and the host performs **zero** additional encode calls for that join (`featherdesk_frames_encoded_total` is unchanged across the join).

**Validated by:** specs/media/MODULE_CAPTURE.md — "Public Interface" (`next_frame` semantics), "Testing Strategy" (idle-screen unit test)

---

## US-VID-12: The mouse cursor is visible and keeps moving even when video stalls

**As a** viewer controlling the host
**I want** to always see where the pointer is, and to see it move the instant I move my mouse
**So that** I can aim and click accurately even on a static screen or a congested link

**Acceptance Criteria:**
- Given `cursorMode` is `"separate"`, When the pointer moves but the screen content does not change, Then the client still receives `CURSOR_UPDATE` (type 11) datagrams and the overlay moves — the pointer is not frozen waiting for a video frame that will never come.
- Given the pipeline is dropping frames under load (`skip_budget > 0`), When the pointer moves, Then cursor updates continue at the full frame-loop tick rate, because the cursor is polled *before* the frame-skip check and the capturer latches its pointer source independently of `next_frame`.
- Given the cursor changes shape (arrow → I-beam), When the update is sent, Then the bitmap travels as a reliable Kind 0x01 record on the cursor stream and the datagram carries only its `ShapeID`; given only the position changed, Then nothing goes on the cursor stream and the datagram stays 14 bytes.
- Given a client joins a host whose pointer has not moved since before it connected, When the session is seeded, Then the server writes the cached shape and a Kind 0x02 position record on that session's cursor stream, and the client renders a cursor without any pointer movement — the cache is non-empty because the FIRST `next_cursor` call after the capturer is constructed returns the current position, the current visibility AND the current bitmap even though nothing has changed.
- Given no capture add-on can deliver a cursor in the configured mode, When the pipeline selects a capturer, Then those add-ons are skipped in the dispatch order and — if none remains — startup fails naming what it rejected, rather than streaming with no visible pointer.
- Given a `CURSOR_UPDATE` datagram is lost, When the pointer keeps moving, Then the next update supersedes it (latest-wins, ordered by the cursor sequence); given the pointer then goes idle, Then the cached update is re-sent at 250/500/750 ms so a lost *final* update cannot leave the overlay stale.
- Given the cursor query fails mid-session, When the error is returned, Then a `visible = 0` update hides the overlay, polling stops and the `CURSOR` capability bit is cleared on the handle — the capture object is never dropped — video is unaffected, and the capture-error escalation ladder is **not** advanced; the session flips to `cursorMode: "embedded"` and pushes a fresh `config` **only if** the add-on actually declares `AddonCaps::EMBED_CURSOR` **and** the rebuild with `CaptureConfig.embed_cursor = true` succeeds, otherwise the mode stays `"separate"`, no `config` is pushed, and the session runs on without a client-side pointer.

**Validated by:** specs/CENTRAL_SPEC.md — **Contract 8: Cursor -> Server**; specs/media/MODULE_CAPTURE.md — `CursorCapturer` + "Cursor delivery and the" section (the `"separate"` / `"embedded"` decision) + "Cursor coordinate space"; specs/core/MODULE_PROTOCOL.md — the 14-byte CursorUpdate + cursor stream records; specs/core/MODULE_TRANSPORT.md — `stream_type::CURSOR`; specs/core/MODULE_PIPELINE.md — frame loop step (0b), startup step 6a, "Cursor publishing"; specs/client/MODULE_WEB_CLIENT.md — "Cursor Overlay"

> Added in the final review pass. Every module *consumed* `CursorUpdate` — the wire type, `server.send_cursor`, and the client's `cursor.js` overlay all existed — but nothing **produced** it, and its bitmap could not fit the datagram it was specified on; no story exercised the path, which is why the hole survived earlier passes.

---

## US-VID-13: A lost datagram fragment costs one frame, not the session

**As a** viewer on a lossy Wi-Fi link
**I want** a dropped packet to cost me a single frame and recover on the next keyframe
**So that** a 1% loss rate does not turn into a frozen or permanently corrupt picture

**Acceptance Criteria:**
- Given a fragmented media frame (`VIDEO_H264` type 1, `VIDEO_HEVC` type 7, or `VIDEO_AV1` type 16) loses one of its N fragments, When the client's reassembly deadline for that FrameID expires, Then the partial frame is discarded without being handed to `VideoDecoder.decode()` and its buffers are freed — a partial access unit is never decoded.
- Given the client discards a frame, When it detects the gap via `seq > lastSeq + 1`, Then it sends exactly one `{"type":"keyframe"}` on the control stream for that gap, not one per lost fragment.
- Given repeated losses, When keyframe requests arrive, Then the server coalesces them to at most **one forced IDR per 500 ms** across all clients, and `featherdesk_keyframes_forced_total` increases by at most 2 over any 1-second window regardless of how many clients requested.
- Given the first live datagram frame after a bootstrap-stream IDR, When the client compares sequences, Then it is exactly `bootstrapSeq + 1` on the fresh-IDR path; and when the join hit `[transport] join_idr_timeout`, Then the first live frame is a later forced keyframe and the client's one gap-triggered keyframe request is expected, not a failure.
- Given 5% uniform datagram loss sustained for 30 s at 60 fps, When the session is measured, Then the client recovers a decodable picture within 500 ms of each loss burst and the session is never closed — loss is a quality event, not a connection event.

**Validated by:** specs/core/MODULE_PROTOCOL.md — "Sequence Number Semantics (server → client)", "Fast-Join (bootstrap stream + gap detection)"; specs/core/MODULE_SERVER.md — "Keyframe-Request Rate Limiting"; specs/client/MODULE_WEB_CLIENT.md — fragment reassembly and `decodeVideo`

---

## US-VID-14: A failing capturer is restarted, then replaced, then the host stops — never a silent freeze

**As a** host operator whose display or GPU is misbehaving
**I want** capture failures to escalate on a defined ladder
**So that** I get a recovered stream, a clear terminal error, or a fallback — never a black window and no log

**Acceptance Criteria:**
- Given `next_frame()`/`next_surface()` returns a transient error, When fewer than 3 have occurred consecutively, Then the pipeline logs at `warn`, skips that tick, and continues — one log line per error, not per frame.
- Given the **3rd through 9th** consecutive capture error, When the pipeline reacts, Then it restarts the capturer add-on in place — the same add-on, a fresh instance — zeroing the consecutive counter and advancing the restart counter on each successful restart, and forcing an IDR because a fresh instance has no reference chain.
- Given a capture add-on that has already been restarted 5 times inside one 60 s window — the window measured from `ErrorLadder.window_started_at`, which `decay()` does not clear — When it fails again, Then the pipeline does not restart it a sixth time: the add-on is retired and the next candidate in the dispatch order takes over, and if none remains startup/streaming fails with the documented rejection list. And Given a backend that fails once every 55 s, Then it is never retired, because fewer than 5 restarts fall inside any one 60 s window.
- Given the **10th consecutive** capture error, When the pipeline reacts, Then the add-on is poisoned for the process lifetime and the pipeline walks the capture probe order to the next candidate, forcing an IDR and a fresh `config` on a successful swap; only when no candidate remains does it exit non-zero with `PipelineError::AddonsExhausted`.
- Given a capture add-on returns `StreamError::Unrecoverable`, When the pipeline reacts, Then it does **not** restart it: the add-on is poisoned for the process lifetime and the pipeline walks the capture probe order (`nvfbc → kms_egl` on Linux) to the next candidate, forcing an IDR and a fresh `config` on a successful swap.
- Given a `CursorCapturer` error rather than a capture error, When it is returned, Then the consecutive capture-error counter is **not** advanced, video continues uninterrupted, and the overlay is hidden with a `visible = 0` update.
- Given one capture error followed by 60 s of healthy capture, When another error occurs, Then the counter has decayed to 0 and the ladder restarts at the first rung.

**Validated by:** specs/core/MODULE_PIPELINE.md — "Error Recovery Strategy" (the capture rows), "Add-On Crash Recovery (backoff + circuit breaker)" (Level 2 applied to capturers), Testing Strategy row "A `CursorCapturer` error sends `visible = 0`, stops polling, does **not** touch the capture-error escalation counter"; specs/core/MODULE_STREAM_PARAMS.md — `StreamError::Unrecoverable`

---

## US-VID-15: An HDR request is always answered — with HDR, or with a refusal the client can act on

**As a** viewer on an HDR-capable display
**I want** my HDR toggle to either work or come back with an explicit "no"
**So that** the UI never sits waiting on a request the host silently dropped

**Acceptance Criteria:**
- Given a controller-role client sends `{"type":"set_hdr","hdr":true}` on the control stream, When the host cannot satisfy it (no HEVC-Main10-capable encoder loaded, or the capture add-on cannot produce 10-bit), Then the host sends `{"type":"hdr_unavailable"}` with a `reason` (`no_hevc_encoder` / `no_ten_bit_capture` / `attached_client_cannot_decode` / `degraded_to_software`) on the control stream within 1 second and the session stays SDR — H.264, `bt709`, `bit_depth = 8` — with capture never half-switched to 10-bit.
- Given the same request from a `view`-role client, When the server processes it, Then the message is dropped and **no** `hdr_unavailable` is sent — a viewer cannot learn the host's encoder inventory by probing.
- Given HDR is satisfiable, When the switch completes, Then the client receives a fresh `{"type":"config"}` whose codec string begins `hvc1.`, followed by an IDR carrying the HDR10 mastering-display and content-light-level SEI.
- Given the host has switched to HDR and a client requests SDR, When the request arrives, Then the response is a full encoder teardown and recreate — the same `config` + IDR sequence — not an in-place reconfigure.
- Given `{"type":"hdr_unavailable"}` reaches the client, When the client handles it, Then it reverts its HDR control to off, surfaces the `reason`, and does not re-send `set_hdr` automatically.

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "HDR Pipeline (Full Flow)"; specs/core/MODULE_PROTOCOL.md — "Control-stream JSON messages" (`set_hdr`, `hdr_unavailable`); specs/core/MODULE_AUTH.md — role gating for `set_*`; specs/core/MODULE_SERVER.md — `send_control`
