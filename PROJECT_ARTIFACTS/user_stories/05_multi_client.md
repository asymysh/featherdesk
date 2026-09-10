# User Stories: Multi-Client Sessions

Covers concurrent viewers/controllers, role assignment, controller takeover,
per-session isolation, and connection-quality visibility, as specified in
`specs/core/MODULE_SERVER.md` and `specs/CENTRAL_SPEC.md`.

---

## US-MC-1: Multiple simultaneous viewers

**As a** host user
**I want** more than one person to be able to view my screen at the same time
**So that** I can demo or support multiple remote parties in one session

**Acceptance Criteria:**
- Given the server is running with `max_clients` unset (default 25), When up to 25 clients connect with role `view` or `control`, Then all are accepted and each receives the live video/audio stream.
- Given 25 clients are already connected, When a 26th client attempts to connect, Then the server rejects it post-auth with `close::SERVER_FULL (4429)` and a "max_clients" reason string — deliberately not `AUTH_FAILED`, which a client cannot tell from a bad credential.
- Given multiple viewers are connected, When the host broadcasts a frame, Then the server assembles ONE access unit and fans it out to every session's frame-granular out-queue (not re-encoded per client).

**Validated by:** specs/core/MODULE_SERVER.md — "Public Interface" (`broadcast`), "DoS Protection" (Session limit), Testing Strategy row "Multi-client broadcast fan-out"

---

## US-MC-2: New viewer joins without disrupting existing viewers

**As a** viewer already connected to a session
**I want** my stream to keep playing smoothly when another viewer joins
**So that** one person joining doesn't glitch or freeze everyone else's view

**Acceptance Criteria:**
- Given one or more clients are already connected and receiving frames, When a new client connects, Then the capturer is NEVER restarted — capture continues uninterrupted for all existing sessions.
- Given the cached keyframe is **fresh** — its sequence equals `last_broadcast_seq`, so nothing has been broadcast since it — When a new client joins, Then the server serves it straight from the bootstrap stream and forces nothing (no keyframe storm for existing viewers).
- Given the cached keyframe is **stale**, or no keyframe has ever been cached, When a new client joins, Then the server raises one rate-limited keyframe request and existing viewers simply receive that one extra IDR in their normal stream (no restart, no gap) — seeding a joiner with a stale IDR would hand it P-frames referencing pictures it never received.
- Given the keyframe-request rate limiter, When multiple clients join within the same 500ms window, Then at most one forced keyframe is emitted, coalesced across all of them — every request path (join, client-requested, queue-drop) funnels through the same limiter, bounding the total at 2 IDR/s.

**Validated by:** specs/core/MODULE_SERVER.md — "WebTransport Session Lifecycle" step 13, "Keyframe Caching Strategy", "**Never** restart the capturer on connect" note
**Regression guard:** PROJECT_ARTIFACTS entry TD-26 in specs/CENTRAL_SPEC.md (Known Technical Debt) — the old Go implementation's new-client handler forced a keyframe AND called `capturer.Restart()`, causing a capture-restart storm that disrupted every existing viewer. The refactor spec fixes this structurally: never restart capture, conditional keyframe only, rate-limited.

---

## US-MC-3: Role assignment — control vs. view

**As a** host user
**I want** the first person who connects with a control role to become the controller, and everyone else to be view-only by default
**So that** only one person drives keyboard/mouse unless I explicitly allow more

**Acceptance Criteria:**
- Given no controller is currently assigned, When a client's auth message specifies `"role":"control"`, Then that client claims the controller slot via CAS and receives an input stream (tag 0x01).
- Given a controller is already assigned, When another client authenticates with `"role":"control"` but no `"takeover":true`, Then that client is downgraded to a viewer (no input stream granted), and it receives `auth_ok` with `role:"view"` and `downgrade_reason:"controller_slot_taken"`, does not open the input stream, and does not reconnect-loop.
- Given a client authenticates with `"role":"view"`, When it attempts to open a stream tagged 0x01 (input) or send input records, Then the server cancels that stream with `close::PROTOCOL_ERROR`.
- Given the controller disconnects, When any client subsequently authenticates with `"role":"control"`, Then it claims the now-open controller slot.

**Validated by:** specs/core/MODULE_SERVER.md — "WebTransport Session Lifecycle" step 11 (role check + controller slot), "Controller Model (+ gamepad co-op)"

---

## US-MC-4: Controller takeover

**As a** host user or a second operator
**I want** to be able to explicitly take over keyboard/mouse control from the current controller when takeover is allowed
**So that** control can be handed off during a session without disconnecting and reconnecting everyone

**Acceptance Criteria:**
- Given `Config.allow_takeover = true`, When a new client authenticates with `"role":"control","takeover":true`, Then it claims the controller slot and the previously-connected controller is closed with `close::CONTROLLER_TAKEOVER (4410)`.
- Given `Config.allow_takeover = false`, When a client sends `"takeover":true`, Then the takeover is ignored and the requesting client is treated as a regular viewer (existing controller keeps control).
- Given a takeover occurs, When the displaced controller's session ends, Then its gamepad slot 0 (if any) is released and available to the new controller.
- Given a controller was displaced by a takeover, When its client auto-reconnects with `resume:true`, Then it is restored as a **viewer**, never alongside the new controller.

**Validated by:** specs/core/MODULE_SERVER.md — "WebTransport Session Lifecycle" step 11 ("Controller slot + takeover (BOTH paths)"), "Public Interface" `Config.allow_takeover`

---

## US-MC-5: Passive viewers cannot inject input

**As a** host user
**I want** passive viewers to never be able to control my keyboard or mouse
**So that** I can safely let many people watch without giving them control

**Acceptance Criteria:**
- Given a client with role `view`, When it attempts to open an input stream (tag 0x01) or a clipboard stream (tag 0x02) it is not entitled to, Then the server calls `cancel_read`+`cancel_write` with `close::PROTOCOL_ERROR`.
- Given a client with role `view`, When any datagram arrives from it, Then it is counted in a metric and dropped (v1 has no legitimate client→server datagrams).
- Given a `view` session for its entire lifetime, When it sends no input at all, Then it is never disconnected for inactivity — liveness is QUIC's job, not an input-activity check.

**Validated by:** specs/core/MODULE_SERVER.md — "WebTransport Session Lifecycle" step 17, "Datagram-in loop", "Keepalive, liveness & timeouts" (No idle-input disconnect)

---

## US-MC-6: Gamepad co-op player slots are isolated from the controller

**As a** host user running a co-op session with `[gamepad] allow_coop`
**I want** each "player" client to only be able to send gamepad input for its own assigned slot
**So that** co-op players can't hijack keyboard/mouse or another player's pad

**Acceptance Criteria:**
- Given `[gamepad] allow_coop` is enabled, When a client authenticates with `"role":"player"`, Then it is assigned one gamepad slot (1..max_controllers-1) tracked in `player_slots`.
- Given a `player` client sends keyboard, mouse, or touch records on its input stream, Then those records are dropped — only gamepad records (0x40-0x4F) are forwarded to `input::Dispatcher`.
- Given a player client disconnects, When its slot is freed, Then the corresponding virtual gamepad is disconnected and the slot becomes available to a new player.
- Given a rumble event is requested for gamepad slot N, When the pipeline calls `send_gamepad_rumble`, Then it is routed only to the client that owns slot N.

**Validated by:** specs/core/MODULE_SERVER.md — "Controller Model (+ gamepad co-op)", "WebTransport Session Lifecycle" step 16

---

## US-MC-7: Per-session isolation of frame delivery under a slow client

**As a** host user with several viewers of varying network quality
**I want** one slow viewer's connection to never affect what other viewers receive
**So that** a single bad connection doesn't degrade everyone's experience

**Acceptance Criteria:**
- Given each session has its own bounded frame-out queue (cap = `datagram_send_queue_frames`, default 8), When one client's queue fills because it can't keep up, Then the server drops the OLDEST queued frame for that client only and increments a per-session drop metric — other sessions' queues are unaffected.
- Given a frame is dropped for a slow client, When frames continue to arrive, Then no client ever receives a half-sent/mid-frame fragment (the queue is frame-granular, not fragment-granular).
- Given per-session state (control stream, input stream, clipboard stream, frame_out), When one session closes, Then only that session's tasks are cancelled and its resources released — no cross-session interference.

**Validated by:** specs/core/MODULE_SERVER.md — "Broadcasting (frame-granular fan-out; pump fragments at send)", "Session State", Testing Strategy row "frame_out drop-oldest under overflow"; specs/core/MODULE_STREAM_PARAMS.md — "Aggregating N clients into one encoder setting" (one congested client's ring drops never move the encoder)

---

## US-MC-8: Connection-quality / per-client metrics visibility

**As a** host user or viewer
**I want** to see each client's connection quality (RTT, bandwidth, drop rate) during a multi-client session
**So that** I can tell whether a problem is my network or someone else's before troubleshooting

**Acceptance Criteria:**
- Given N clients are connected, When `/metrics` is scraped, Then it exposes per-client RTT (`featherdesk_rtt_seconds`, one series per connected client), per-client frames sent, frames dropped, bytes sent, and connection duration — aggregate-only counters do not satisfy this.
- Given a connected client, When the server sends a `PING` datagram (type 2, 4-byte `u32` LE nonce), Then the client replies `{"type":"pong","nonce":…}` on the control stream echoing that nonce, and the server derives RTT from the round trip — a `pong` whose nonce was never sent is discarded without updating any metric.
- Given the F9 HUD is open on the client, When a session is running, Then it displays FPS, `inputLatencyMs`, resolution, codec, carrier and measured throughput, and connection state — all values the client already holds, refreshed at 1 Hz.
- Given a connected client, When 1 second elapses, Then it sends exactly one `{"type":"stats","decodeMs":…,"dropped":…,"fps":…}` on the control stream, `dropped` being the per-window delta rather than a running total, and the server feeds it to the congestion-reactive slow path.
- This capability was planned in the prior Go implementation (spec tasks T4-T7: server-side per-client metrics, client-side RTT/latency display, and a bandwidth test) but was **never actually built** — server metrics stayed aggregate-only (no per-client tracking, no per-second rates), the client UI never got RTT/input-latency display, and the bandwidth-test feature was never started at all. The first two are now owned; the third is deliberately not specced (see the note below).
**Validated by:** specs/core/MODULE_SERVER.md — **R-SRV-04** "Add Client Metrics Per-Connection" (per-client frames sent, frames dropped, bytes sent, connection duration, ping/pong RTT) exposed on the Prometheus endpoint; specs/client/MODULE_WEB_CLIENT.md — **R-CLI-13** "On-Screen Connection/Stats HUD" (F9-toggleable FPS, `inputLatencyMs`, resolution+codec+carrier, measured throughput, connection state).

> Was flagged `GAP` in the first pass. Two of the three never-built capabilities now have owners: server-side per-client metrics (R-SRV-04) and the client-side RTT/latency display (R-CLI-13).
>
> **The active bandwidth test is deliberately NOT specced, and that is a decision rather than an omission.** An active probe steals throughput from the stream it is measuring. What the adaptive loop does instead is *react* to congestion — it does not estimate bandwidth, and no spec should say it does: it reads QUIC `smoothed_rtt` and `cwnd`, the server's own datagram-drop rate, and the client's `{"type":"stats"}` deltas, and ramps the bitrate up or down against them (MODULE_STREAM_PARAMS "Congestion-Reactive Bitrate Control"). The HUD's throughput figure is likewise the client's own measured byte-delta over the last second, not an estimate of link capacity. If an active probe is ever wanted, it needs a new frame type and a new directive — it must not be smuggled in as part of the HUD.
**Regression guard:** PROJECT_ARTIFACTS/summaries/multiclient_metrics_polish/phase2.md — documents T4 ("no per-client tracking... spec's FR-3 is only partially met"), T5 ("RTT ... and input-latency ... displays from the spec were not built"), and T6 ("not implemented ... No `bandwidth_test` frame type, server chunk-sender, or client-side measurement exists anywhere in the repo") as never-completed work, not a regression to fix but a missing feature to explicitly track before claiming parity.

---

## US-MC-9: Only the controller can change stream parameters, and changes are bounded

**As a** host operator running a session with several viewers
**I want** only the controlling client to be able to change bitrate, frame rate, or resolution
**So that** one viewer cannot degrade the stream for everyone else

**Acceptance Criteria:**
- Given a `view`-role client sends `{"type":"set_bitrate","kbps":8000}` or `{"type":"set_fps","fps":30}`, When the server dispatches it, Then the message is dropped silently — no `config` is emitted, no `stream::Params` is built, and the sending client receives no error (it learns nothing about the host's limits).
- Given the controller sends `{"type":"set_bitrate","kbps":N}`, When the Manager applies it, Then the value is clamped to `[stream.adaptive] min_bitrate_bps` (1 Mbps) … `max_bitrate_bps` (25 Mbps) and the applied value is what `featherdesk_effective_bitrate_kbps` reports — an out-of-range request is clamped, never rejected and never applied verbatim.
- Given the controller sends `{"type":"set_fps","fps":N}` with N outside the active add-on's `params_capability` bounds (or, for a non-configurable add-on, `[stream] fps`'s 1–240), When `Manager::apply` runs, Then the value is clamped into range and the clamped value is what `featherdesk_effective_fps` reports — an out-of-range parameter is never rejected and never closes the session.
- Given a valid `set_fps` is applied, When the next frame is captured, Then the frame loop's pacing uses the new interval — the delivered rate measured over the following 5 seconds is within 10% of the requested `fps`, not the previous one, unless sustainable-rate control has lowered the effective rate below the requested ceiling.
- Given a `set_bitrate` change is applied, When it reaches the encoder, Then **no** `config` message is sent to the client — a bitrate change must not cause a `VideoDecoder` reconfigure.

**Validated by:** specs/core/MODULE_AUTH.md — the role/permission table for `set_*`; specs/core/MODULE_PROTOCOL.md — "Control-stream JSON messages" (`resize`, `set_*` are gated by authorization role); specs/core/MODULE_STREAM_PARAMS.md — "Congestion-Reactive Bitrate Control" (clamping); specs/core/MODULE_STREAM_PARAMS.md — "Capability Probing" (the "Validation bounds for incoming client requests" bullet)

---

## US-MC-10: Losing the controller slot releases everything that client was holding

**As a** host user watching someone's session end mid-keypress
**I want** every key and button that client was holding to be released on the host
**So that** a dropped laptop lid does not leave W held down in my game forever

**Acceptance Criteria:**
- Given the controller is holding one or more keys/buttons/touch points, When its QUIC session times out or closes for any reason, Then the host injects a matching up-event for every held input before the session's teardown completes, and the host-side virtual device reports zero keys down within 100 ms of the close.
- Given `[auth] allow_takeover = true` and a second client seizes the controller slot, When the displaced client is closed with `close::CONTROLLER_TAKEOVER (4410)`, Then the displaced client's held inputs are released **before** the new controller's first input record is injected — the two clients' state never overlaps on the host.
- Given a co-op `player` client whose gamepad slot is released, When the slot is freed, Then that slot's held buttons and axes are zeroed on the virtual controller, and other players' slots are untouched.
- Given a client that was holding nothing, When its slot is released, Then no up-events are injected at all — the release path is idempotent and produces no spurious input.
- Given the host has released a client's inputs, When that client reconnects and resumes, Then it starts with no keys held; a resumed session never inherits pre-disconnect held state.

**Validated by:** specs/interaction/MODULE_INPUT.md — `Dispatcher::release_all()`; specs/core/MODULE_SERVER.md — session lifecycle steps 11 (takeover) and 19 (close), "Controller Model (+ gamepad co-op)"; specs/CENTRAL_SPEC.md — TD-35
