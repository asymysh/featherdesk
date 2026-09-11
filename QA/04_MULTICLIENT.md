# QA 04 — Multi-client: roles, controller slot, limits, per-user caps

Matching user stories: `PROJECT_ARTIFACTS/user_stories/05_multi_client.md`.

## Roles and the controller slot

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-MC-1 | First client connects with `role` omitted | Granted `control`; `auth_ok.role` says `control` | `MODULE_AUTH` "Effective role" | ☐ |
| QA-MC-2 | Second client requests `control`, `allow_takeover = false` | Arbitrated to `view`; `auth_ok.role` is `view` while `requested_role` is `control`, and `takeover_allowed` reflects the setting. The client is **told**, not silently downgraded | `MODULE_AUTH` | ☐ |
| QA-MC-3 | Second client requests `control` with `takeover:true`, `allow_takeover = true` | Slot seized; the displaced session is closed with `4410`, its held input is released, and its cached role is rewritten to `view` so its own auto-reconnect returns as a viewer | `MODULE_AUTH` "One controller per session" | ☐ |
| QA-MC-4 | `takeover:true` from an **unauthenticated** client (`require_auth_for_view = false`) | Never honoured, even with `allow_takeover = true` | `MODULE_AUTH` | ☐ |
| QA-MC-5 | Controller disconnects cleanly | Slot released; a waiting viewer can claim `control` on reconnect; held input released | `MODULE_SERVER` step 21 | ☐ |
| QA-MC-6 | `role = "player"` with `[gamepad] allow_coop = false` | Demoted to `view`, and `auth_ok.role` reports it | `MODULE_AUTH` | ☐ |
| QA-MC-7 | Anonymous client (`require_auth_for_view = false`) asking for `control`, `player`, or omitting `role` | Admitted as **`view`** in all three cases, and never promotable. Refused the `0x01`, `0x02` and `0x03` streams | `MODULE_AUTH` "admits, never promotes" | ☐ |

## Concurrency and limits

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-MC-8 | 25 simultaneous viewers (1 controller + 24) | All receive video. Fan-out stays within the <2 ms budget; no viewer is starved; the controller's input latency does not visibly degrade | `MODULE_SERVER` perf targets | ☐ |
| QA-MC-9 | 26th client | Closed with `4429 SERVER_FULL`, reason `max_clients` — **not** `4401`, which would make the client discard its token | `MODULE_SERVER` "DoS Protection" | ☐ |
| QA-MC-10 | Mixed carriers: some viewers on WebTransport, some on WebSocket | `max_clients` is counted across **both**; all work; the metric labels distinguish them | `MODULE_TRANSPORT` "Everything else is unchanged" | ☐ |
| QA-MC-11 | One viewer on a deliberately terrible link among 24 good ones | That viewer's ring drops frames for **that viewer only**. The encoder is not pulled down for everyone — the reference-session + 50 % majority rule holds | `MODULE_STREAM_PARAMS` "Aggregating N clients" | ☐ |
| QA-MC-12 | Half the room goes bad simultaneously | The majority override fires: median RTT/loss is used and the encoder does adapt down | `MODULE_STREAM_PARAMS` | ☐ |
| QA-MC-13 | Kill one viewer abruptly | Others unaffected; count decrements; no panic | `MODULE_SERVER` | ☐ |
| QA-MC-14 | Which session is driving adaptation | `featherdesk_adaptive_reference_client` identifies it, so an operator can see whose link the stream is tuned to | `MODULE_STREAM_PARAMS` | ☐ |

## Parameter-change gating

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-MC-15 | Viewer sends `resize` / `set_bitrate` / `set_fps` / `set_hdr` | Refused per the role gate table; the session is not closed | `MODULE_SERVER` "Role gate table" | ☐ |
| QA-MC-16 | Viewer sends `keyframe` / `pong` / `stats` / `decode_unsupported` | **Allowed** for every authenticated role — loss recovery and capability negotiation are not role-gated, only rate-limited. A viewer that cannot request an IDR can never recover from a lost fragment | `MODULE_AUTH` role table | ☐ |
| QA-MC-17 | Controller sends out-of-range `set_bitrate` / `set_fps` | Clamped, not rejected; the clamped value is what takes effect | US-MC-9 | ☐ |
| QA-MC-18 | A parameter change the encoder **cannot** apply | `Applied { ok: false }` is fanned out as `resize_suppressed` to every session, carrying the dims actually in force | `MODULE_SERVER` step 19 | ☐ |

## Capacity and per-user caps

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-MC-19 | Measure actual host egress with 25 viewers at the configured bitrate | Matches `clients × bitrate` within reason. Record the number — it is the figure an operator needs and the spec previously never stated | OQ-05 | ☐ |
| QA-MC-20 | `[server] max_egress_bps` set below `max_clients × current_bitrate` | Sessions beyond the budget are refused at admission with `4429`, rather than every session silently degrading | OQ-05 | ☐ |
| QA-MC-21 | `[transport] per_session_max_bps` applied to one viewer | That viewer sees a lower effective frame rate at full resolution; **every other session is unaffected** | OQ-05 | ☐ |
| QA-MC-22 | With QA-MC-21 active, watch the adaptive loop | The policy drops are counted under a **separate** metric label and are **excluded** from the congestion reducer — the capped viewer must not trigger a 0.5× cut for the whole room | OQ-05 | ☐ |
| QA-MC-23 | Controller is exempt from the per-session cap by default | The controller's stream is not throttled | OQ-05 | ☐ |

## Isolation

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-MC-24 | Two clients, one copies on the host | Only the **controller** receives the host clipboard push. Viewers and players never do, whatever `direction` says | CENTRAL_SPEC Contract 9 | ☐ |
| QA-MC-25 | One client's file transfer while others stream | On WebTransport, no visible effect on video (independent streams). On WebSocket, expect and record shared-connection interference | `MODULE_FILETRANSFER` | ☐ |
| QA-MC-26 | Per-session state does not leak: session tokens, ack rings, cursor shape caches | No cross-talk; a shape sent to one session is not assumed present in another | `MODULE_SERVER` `send_cursor_shape` gating | ☐ |
