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
- Given the selected display's native resolution, When `[capture] width/height = 0`, Then the capturer emits frames/surfaces at that native resolution and the encoder (not the capturer) performs any scaling to the advertised stream dimensions.
- Given the screen is idle, When `next_frame()`/`next_surface()` is called, Then it returns `Ok(None)` within the per-frame deadline rather than a stale frame, and no re-encode occurs — a newly joined client is still served from the IDR cache at near-zero bandwidth cost.

**Validated by:** specs/media/MODULE_CAPTURE.md — "Public Interface", "Resolution: capture is always native; the encoder scales", "Testing Strategy"

---

## US-VID-2: Hardware encoder is preferred when available

**As a** host user
**I want** the server to automatically use my GPU's hardware encoder instead of burning CPU on software encoding
**So that** I get lower latency and lower host CPU usage without manual configuration

**Acceptance Criteria:**
- Given `[encode] mode = "auto"` and one or more HW encoder add-ons are loaded, When the pipeline starts, Then it probes them in vendor-priority order (NVENC/AMF/libva before generic MF HW; VT HW on macOS) before falling through to any SW encoder.
- Given a HW encoder is selected, When it processes a native-resolution GPU surface, Then it scales in-encoder to `initial_params.width × height` so encoder-output dims equal the advertised `config` dims.
- Given the HW encoder advertises its active codec via `codec()`, When the config handshake is sent, Then the server forwards that exact WebCodecs codec string (`avc1.*` by default, `hvc1.*` only for HDR) with no separate browser codec-negotiation handshake.

**Validated by:** specs/media/MODULE_HARDWARE_ENCODE.md — "Selection (Pipeline Owns This)", "Zero-Copy Pipeline"

---

## US-VID-3: Software encoder fallback when no hardware encoder is present

**As a** host user
**I want** streaming to still work on a machine with no usable GPU encoder
**So that** FeatherDesk isn't limited to hardware-accelerated hosts only

**Acceptance Criteria:**
- Given no HW encoder add-on probes successfully, When the pipeline selects an encoder, Then it falls through to a loaded SW encoder add-on in the order `x264 > VT SW (macOS only) > OpenH264`.
- Given the SW path is active, When a capture `Frame` (BGRA on macOS/Windows, RGBA on Linux GL) arrives, Then the `Converter` transforms it to I420 via the correct libyuv function (`ARGBToI420` for BGRA-in-memory, `ABGRToI420` for RGBA-in-memory) before encoding.
- Given no hardware exists at all (e.g. "No GPU" row in the platform compatibility matrix), When the server starts, Then OpenH264 (Rust FFI, BSD) is available as the universal SW fallback on every OS.

**Validated by:** specs/media/MODULE_ENCODE.md — "Encoder Selection (Pipeline Owns This)", "Color Space Conversion"; specs/PLATFORM_COMPAT.md — "Codec Fallback Order"

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
- Given a session with existing viewers already receiving a live video stream, When a new client authenticates, Then the server serves the cached IDR (keyframe access unit, containing SPS+PPS+IDR) over a fresh bootstrap uni stream — it does NOT force an unconditional keyframe request or restart the capturer.
- Given the new client join, When the server handles it, Then existing viewers observe no capture restart, no keyframe storm, and no visible glitch in their stream.
- Given repeated rapid joins, When the server serves cached IDRs, Then any genuinely required fresh-keyframe requests are rate-limited rather than triggering one force-keyframe per join.

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
- Given a resize is applied, When the new dimensions take effect, Then the pipeline forces an IDR on the first frame after the change (the new resolution invalidates the prior GOP).
- Given multiple resize requests arrive concurrently, When the pipeline processes its `param_ch`, Then only the latest is applied — no stale intermediate resolution is ever rendered.

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Dynamic Resolution Change Flow", "Testing Strategy" (resize hysteresis, concurrent resize coalescing)

---

## US-VID-7: HDR requests succeed end-to-end or fail gracefully to SDR

**As a** host user with an HDR-capable display and GPU
**I want** to enable HDR streaming when supported, and get a clear fallback when it isn't
**So that** I either get real HDR or a working SDR stream — never a broken half-HDR state

**Acceptance Criteria:**
- Given `Params.hdr = true` is requested and a HEVC-Main10-capable encoder (NVENC/AMF/MF HW HEVC/VT HW HEVC) is loaded, plus a capture add-on that supports 10-bit, When the pipeline processes the request, Then it configures capture for 10-bit/`bt2020`, switches the encoder to HEVC Main10, and sends the client a `config` with codec `hvc1.*` and HDR10 mastering-display/content-light-level metadata.
- Given no HEVC-Main10-capable encoder is loaded, When an HDR request arrives, Then the pipeline rejects it, sends `{"type":"hdr_unavailable"}` on the control stream, and the session stays SDR (H.264, bt709) — capture is never half-switched to 10-bit.
- Given an encoder has already initialized for HDR, When an SDR revert is requested, Then it requires a full teardown/recreate (HDR is a one-way trip per session without a restart).

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "HDR Pipeline (Full Flow)", "Testing Strategy" (HDR terminal case)

---

## US-VID-8: Chroma subsampling upgrades fall back transparently to 4:2:0

**As a** viewer using the native client (or a capable browser)
**I want** sharper text/UI via 4:2:2 or 4:4:4 chroma when both the encoder and my decoder support it
**So that** remote-desktop text rendering looks crisp, without ever ending up with an undecodable stream

**Acceptance Criteria:**
- Given `Params.chroma_subsampling = "444"` is requested, When the active encoder cannot produce it (e.g. OpenH264, 4:2:0-only), Then the encoder returns `StreamError::ChromaUnsupported` and the pipeline downgrades to the encoder's best supported chroma.
- Given the encoder-side chroma is accepted, When the resulting codec string is advertised in `config` and the client's `VideoDecoder.isConfigSupported()` check fails, Then the client sends `{"type":"chroma_unsupported"}` on the control stream.
- Given the client rejects the chroma, When the server processes that message, Then it downgrades to "420", re-sends `config` with the 4:2:0 codec string, and forces a keyframe — this fallback always terminates at 4:2:0, which is guaranteed decodable everywhere.

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Chroma Subsampling", "Testing Strategy" (chroma fallback cascade)

---

## US-VID-9: Frame drop under load stays above a minimum FPS floor

**As a** viewer on a congested network or an overloaded host
**I want** the stream to drop frames intelligently rather than accumulate unbounded latency
**So that** what I see stays close to real-time instead of drifting further and further behind

**Acceptance Criteria:**
- Given the encode/send path falls behind under load, When the pipeline's frame-drop logic engages, Then it skips frames to keep the effective delivered rate above a defined floor rather than queuing every frame indefinitely.
- Given a sustained datagram-out-queue drop spike is observed server-side, When the fast adaptation path triggers, Then bitrate is immediately reduced (0.5×, single measurement) rather than continuing to force full-quality frames into a congested link.

**Validated by:** specs/CENTRAL_SPEC.md — "New Issues Found During Design Review" (TD-22: "Pipeline: 5 FPS floor + skip logic"); specs/core/MODULE_STREAM_PARAMS.md — "Bandwidth Adaptation" (FAST path)

---

## US-VID-10: Bitrate adapts to network conditions without a visible stream restart

**As a** viewer on a variable-quality network connection
**I want** the video bitrate to automatically decrease when my connection degrades and recover when it improves
**So that** I get the best possible quality my network can sustain without manual tuning or a broken decoder

**Acceptance Criteria:**
- Given `packet_loss_pct > 5%` for 2 consecutive 100ms windows (200ms), When the Manager's slow-path adaptation runs, Then `new_bitrate = max(current * 0.7, min_bitrate_bps)` is applied.
- Given `packet_loss_pct < 1%` for 10 consecutive windows (1s) and RTT is stable, When the recovery path runs, Then `new_bitrate = min(current * 1.1, max_bitrate_bps)` is applied.
- Given a bitrate change is applied, When it is sent to the frame loop via `param_ch`, Then no `config` message is sent to the client — a bitrate change is transparent to the client's decoder (no reconfigure/glitch).

**Validated by:** specs/core/MODULE_STREAM_PARAMS.md — "Bandwidth Adaptation (Server-Measured, Manager-Driven)"

---

## US-VID-11: Idle screens cost near-zero bandwidth

**As a** host user stepping away from the keyboard
**I want** an unchanging screen to stop consuming meaningful encode/network resources
**So that** an idle session doesn't waste bandwidth or CPU while nothing is happening

**Acceptance Criteria:**
- Given the screen produces no new content, When `next_frame()`/`next_surface()` is called within the per-frame deadline, Then it returns `Ok(None)` and the caller skips that tick — it does not re-encode a duplicate frame.
- Given a new client joins during this idle period, When it connects, Then it is still served a decodable stream immediately from the IDR cache via the bootstrap stream, at near-zero incremental bandwidth cost.

**Validated by:** specs/media/MODULE_CAPTURE.md — "Public Interface" (`next_frame` semantics), "Testing Strategy" (idle-screen unit test)

---

## US-VID-12: The mouse cursor is visible and keeps moving even when video stalls

**As a** viewer controlling the host
**I want** to always see where the pointer is, and to see it move the instant I move my mouse
**So that** I can aim and click accurately even on a static screen or a congested link

**Acceptance Criteria:**
- Given `cursorMode` is `"separate"`, When the pointer moves but the screen content does not change, Then the client still receives `CURSOR_UPDATE` (type 11) datagrams and the overlay moves — the pointer is not frozen waiting for a video frame that will never come.
- Given the pipeline is dropping frames under load (`skip_budget > 0`), When the pointer moves, Then cursor updates continue at the full frame-loop tick rate, because the cursor is polled *before* the frame-skip check.
- Given the cursor changes shape (arrow → I-beam), When the update is sent, Then `image_changed = true` and the RGBA bitmap is included; given only the position changed, Then `image_changed = false`, `rgba` is empty, and the datagram stays 10 bytes.
- Given the selected capture add-on does not report `AddonCaps::CURSOR`, When the session starts, Then the server resolves `cursorMode` to `"embedded"` and logs a warning — the viewer sees a cursor composited into the video, and **never** a stream with no visible pointer at all.
- Given `CursorUpdate` datagrams are unreliable and one is lost, When the next update arrives, Then it simply supersedes the lost one (latest-wins) — the client never requests a retransmit.
- Given the cursor query fails mid-session, When the error is returned, Then cursor polling is disabled, a fresh `config` with `cursorMode: "embedded"` is pushed, video is unaffected, and the capture-error escalation ladder is **not** advanced.

**Validated by:** specs/CENTRAL_SPEC.md — **Contract 8: Cursor -> Server**; specs/media/MODULE_CAPTURE.md — `CursorCapturer` + "Cursor delivery and the `separate`/`embedded` decision"; specs/core/MODULE_PIPELINE.md — frame loop step (0b), startup step 6a; specs/client/MODULE_WEB_CLIENT.md — "Cursor Overlay"

> Added in the final review pass. Every module *consumed* `CursorUpdate` — the wire type, `server.send_cursor`, and the client's `cursor.js` overlay all existed — but nothing **produced** it, and no story exercised the path, which is why the hole survived earlier passes.
