# User Stories Index

QA acceptance-criteria user stories used to validate that the Rust
implementation of FeatherDesk actually delivers the behavior described in
`specs/`. Each story cites the module spec(s) it is validated against.

**90 stories across 8 areas. Every story has an owning spec — there are no
open `GAP` citations.** The three that were flagged `GAP` in the first pass
(client-side held-key release, browser audio teardown, per-client connection
metrics) were closed by writing the missing spec, not by deleting the story;
each keeps a note recording what the gap was. One deliberate non-feature is
recorded under US-MC-8: the **active bandwidth test is intentionally not
specced**, because there is no bandwidth estimator in v1 at all — the loop
reacts to congestion rather than measuring capacity, and an active probe would
steal throughput from the stream it measures.

Wire coverage — every message type, frame type, and `AddonCaps` bit mapped to the
story that exercises it — is tracked in [COVERAGE.md](./COVERAGE.md).

| # | File | Stories | Description |
|---|------|:-------:|-------------|
| 01 | [01_connection_and_auth.md](./01_connection_and_auth.md) | 11 | Connecting to the host, authentication modes (token/password/PIN), pairing, and session handshake. |
| 02 | [02_video_streaming.md](./02_video_streaming.md) | 15 | Video capture, encoding, codec selection, cursor delivery, datagram loss recovery, the capture-error ladder, HDR negotiation, and streaming to the browser client. |
| 03 | [03_audio.md](./03_audio.md) | 9 | Host-to-client audio capture, encoding, playback, and teardown. |
| 04 | [04_input_control.md](./04_input_control.md) | 11 | Keyboard, mouse, touch, and gamepad input injection from client to host, plus gamepad rumble delivery. |
| 05 | [05_multi_client.md](./05_multi_client.md) | 10 | Multiple simultaneous viewers, role assignment, controller takeover, parameter-change gating, held-input release on slot change, per-session isolation, and connection-quality visibility. |
| 06 | [06_clipboard_and_filetransfer.md](./06_clipboard_and_filetransfer.md) | 10 | Bidirectional clipboard sync and drag-and-drop file transfer between host and client. |
| 07 | [07_resilience_and_reconnection.md](./07_resilience_and_reconnection.md) | 11 | Network loss and session resume, congestion-reactive bitrate control (including its cold start), encoder crash recovery, hardware fallback, and clipboard delivery under failure. |
| 08 | [08_cross_platform.md](./08_cross_platform.md) | 13 | Cross-platform behavior derived from the compatibility matrix, plus add-on load rejection and optional-capability handling — what works identically on Linux/Windows/macOS today versus what's still specced-but-not-built. |

### Stories added by later review passes

These exist because a review found paths that every module *consumed* but none
*produced*, or behaviour with no witness at all — the specs read as complete
because each module correctly described its own half:

| Story | What was missing |
|-------|------------------|
| US-VID-12 | The cursor overlay had a wire type, a `send_cursor` method, and a client renderer — but no producer, and its bitmap could not fit the datagram it was specified on. Closed by `CursorCapturer` + CENTRAL_SPEC Contract 8 + the cursor stream (tag `0x11`). |
| US-RR-9 | No story exercised repeated encoder crashes, so TD-39's restart-loop had no acceptance criteria. Closed by MODULE_PIPELINE "Add-On Crash Recovery". |
| US-RR-10 | US-CF-1 asserted bidirectional clipboard, but host→client had **no server method and no drainer**. Closed by `send_clipboard` + CENTRAL_SPEC Contract 9. |
| US-VID-13 | Fragment loss, the reassembly deadline, and the keyframe request that follows — the most common failure of an unreliable video path had no witness. |
| US-VID-14 | The capture-error escalation ladder (3 → restart, 10 → fatal, `Unrecoverable` → fall-through). Only the encoder ladder had been written. |
| US-VID-15 | `set_hdr` and `{"type":"hdr_unavailable"}` — HDR was asserted as a state, never as a request that must be answered. |
| US-MC-9 | `set_bitrate` / `set_fps` role gating and clamping — both messages appeared in zero criteria. |
| US-MC-10 | The host half of TD-35: releasing held input when the controller slot is released or seized. `release_all()` had no caller and no story. |
| US-RR-11 | The adaptive loop under the shipped default config (`bitrate_bps = 0` with `adaptive = true`) — the one configuration no story exercised. |
| US-XP-12 | ABI version / layout-hash / os-arch load rejection and `abi_strict`. |
| US-XP-13 | `AddonCaps` bits driving host behaviour, including an add-on that claims a bit it does not implement. |
| US-INP-11 | `GAMEPAD_RUMBLE` (type 15) delivery, slot ownership, and the disabled case. |
