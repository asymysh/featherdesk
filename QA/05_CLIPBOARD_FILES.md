# QA 05 — Clipboard and file transfer

Matching user stories: `PROJECT_ARTIFACTS/user_stories/06_clipboard_and_filetransfer.md`.

## Clipboard

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CF-1 | Copy plain text on the **host**, paste in a local app | Arrives intact, including Unicode, emoji and multi-line text | CENTRAL_SPEC Contract 9 (H→C) | ☐ |
| QA-CF-2 | Copy plain text **locally**, paste into a host app | Arrives intact | Contract 9 (C→H) | ☐ |
| QA-CF-3 | Copy rich HTML both directions | Formatting preserved; content **sanitised in both directions** — verify a `<script>` or an `onerror=` attribute does not survive either way | `MODULE_CLIPBOARD` | ☐ |
| QA-CF-4 | Copy something near the size cap, then well over it | At cap: works. Over: text truncated, HTML dropped, counted as `reason="too_large"`, and **no error message on the wire** | Contract 9 gate 7 | ☐ |
| QA-CF-5 | `direction = "host_to_client"` then `"client_to_host"` | Only the permitted direction moves; the other is dropped silently and counted | `MODULE_CLIPBOARD` | ☐ |
| QA-CF-6 | `[clipboard] enabled = false` | Nothing syncs either way, and a `0x02` stream from any client is reset at stream scope before a byte is read | `MODULE_SERVER` role gate row 5 | ☐ |
| QA-CF-7 | A **viewer** opens a `0x02` clipboard stream | Reset with `PROTOCOL_ERROR`; the session survives | Role gate row 5 | ☐ |
| QA-CF-8 | Host copies while a viewer and a controller are both attached | Only the controller receives it — host-secret leakage prevention | Contract 9 gate 3 | ☐ |
| QA-CF-9 | Copy an **image** on the host | Documented v1 limitation: image clipboard is out of scope. Verify nothing crashes and text still works | `MODULE_CLIPBOARD` "Out of scope" | ☐ |
| QA-CF-10 | Rapid repeated copies (10 in a second) | Latest-wins; no queue growth; the monitor task never stalls the frame loop | Queue 8 | ☐ |
| QA-CF-11 | Clipboard stream stalls (client stops reading) | That push is dropped for that session and counted `stream_full`; **the frame loop is unaffected** | Contract 9 gate 8 | ☐ |
| QA-CF-12 | Oversized `[u32 Len]` prefix sent by a hostile client | Rejected **before allocating** the buffer; that stream is reset; the session survives | `MODULE_SERVER` "DoS Protection" | ☐ |
| QA-CF-13 | SIGHUP changing `[clipboard] direction` mid-session | Applied live to existing sessions | `MODULE_CONFIG` hot reload | ☐ |
| QA-CF-14 | Clipboard logging | Logs record the **byte length only**. No clipboard content appears in any log at any level | Contract 9 | ☐ |

## File transfer

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CF-15 | Drag-and-drop a file onto the remote view | Lands in the configured Incoming folder; content byte-identical (verify SHA-256) | `MODULE_FILETRANSFER` | ☐ |
| QA-CF-16 | Download a file the host offers | Arrives in the browser, byte-identical | `MODULE_FILETRANSFER` "Transfer flow (download)" | ☐ |
| QA-CF-17 | A large file (≥ 1 GB) | Completes; integrity verified; **video keeps streaming throughout** on WebTransport (independent QUIC streams) | `MODULE_FILETRANSFER` design decision 1 | ☐ |
| QA-CF-18 | Same on the **WebSocket** carrier | Completes correctly, but expect and record media interference — one ordered connection, `TransferID` separates transfers | `MODULE_TRANSPORT` fallback framing | ☐ |
| QA-CF-19 | Several concurrent transfers | Up to `max_concurrent`; extras queue to `queue_depth`; beyond that `ERROR {code:"queue_full"}` — not a hang | Queue 11 | ☐ |
| QA-CF-20 | **Path traversal attempt** — a file named `../../etc/passwd` or an absolute path | Confined to the Incoming folder. The sandbox is mandatory; verify with a symlink target too | `MODULE_FILETRANSFER` "Path-traversal guard" | ☐ |
| QA-CF-21 | Filename collision with an existing file | Handled per spec, never a silent overwrite of unrelated data | `MODULE_FILETRANSFER` | ☐ |
| QA-CF-22 | Corrupt a chunk in flight | Per-chunk CRC32C catches it; whole-file SHA-256 catches an end-to-end mismatch; the transfer fails cleanly rather than writing a corrupt file | `MODULE_FILETRANSFER` | ☐ |
| QA-CF-23 | Abort a transfer mid-flight (close the tab) | No partial file left in place as if complete; the slot is freed for the next transfer | `MODULE_FILETRANSFER` | ☐ |
| QA-CF-24 | A **viewer** opens a `0x03` file-transfer stream | Refused at the role gate | `MODULE_SERVER` role gate | ☐ |
| QA-CF-25 | Client opens more transfer streams than `fileStreamBudget` | Extras flow-controlled; the 10 s open deadline surfaces `too_many_streams` per file; the session and in-flight transfers are unaffected | `MODULE_TRANSPORT` load test | ☐ |
| QA-CF-26 | Incoming/Outgoing folders on first use | Created at mode 0700 at the documented per-OS default location | `MODULE_FILETRANSFER` "Directories" | ☐ |
