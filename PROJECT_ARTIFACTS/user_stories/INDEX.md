# User Stories Index

QA acceptance-criteria user stories used to validate that the Rust
implementation of FeatherDesk actually delivers the behavior described in
`specs/`. Each story cites the module spec(s) it is validated against.

**81 stories across 8 areas. Every story has an owning spec — there are no
open `GAP` citations.** The three that were flagged `GAP` in the first pass
(client-side held-key release, browser audio teardown, per-client connection
metrics) were closed by writing the missing spec, not by deleting the story;
each keeps a note recording what the gap was. One deliberate non-feature is
recorded under US-MC-8: the **active bandwidth test is intentionally not
specced**, because the adaptive loop measures bandwidth passively and an
active probe would steal throughput from the stream it measures.

| # | File | Stories | Description |
|---|------|:-------:|-------------|
| 01 | [01_connection_and_auth.md](./01_connection_and_auth.md) | 11 | Connecting to the host, authentication modes (token/password/PIN), pairing, and session handshake. |
| 02 | [02_video_streaming.md](./02_video_streaming.md) | 12 | Video capture, encoding, codec selection, cursor delivery, and streaming to the browser client. |
| 03 | [03_audio.md](./03_audio.md) | 9 | Host-to-client audio capture, encoding, playback, and teardown. |
| 04 | [04_input_control.md](./04_input_control.md) | 10 | Keyboard, mouse, touch, and gamepad input injection from client to host. |
| 05 | [05_multi_client.md](./05_multi_client.md) | 8 | Multiple simultaneous viewers, role assignment, controller takeover, per-session isolation, and connection-quality visibility. |
| 06 | [06_clipboard_and_filetransfer.md](./06_clipboard_and_filetransfer.md) | 10 | Bidirectional clipboard sync and drag-and-drop file transfer between host and client. |
| 07 | [07_resilience_and_reconnection.md](./07_resilience_and_reconnection.md) | 10 | Network loss and session resume, bandwidth adaptation, encoder crash recovery, hardware fallback, and clipboard delivery under failure. |
| 08 | [08_cross_platform.md](./08_cross_platform.md) | 11 | Cross-platform behavior derived from the compatibility matrix — what works identically on Linux/Windows/macOS today versus what's still specced-but-not-built. |

### Stories added by the final review pass

These three exist because the review found paths that every module *consumed*
but none *produced* — the specs read as complete because each module correctly
described its own half:

| Story | What was missing |
|-------|------------------|
| US-VID-12 | The cursor overlay had a wire type, a `send_cursor` method, and a client renderer — but **no producer**. Closed by `CursorCapturer` + CENTRAL_SPEC Contract 8. |
| US-RR-9 | No story exercised repeated encoder crashes, so TD-39's restart-loop had no acceptance criteria. Closed by MODULE_PIPELINE "Add-On Crash Recovery". |
| US-RR-10 | US-CF-1 asserted bidirectional clipboard, but host→client had **no server method and no drainer**. Closed by `send_clipboard` + CENTRAL_SPEC Contract 9. |
