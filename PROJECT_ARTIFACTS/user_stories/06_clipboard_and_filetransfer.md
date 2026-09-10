# User Stories: Clipboard & File Transfer

Covers bidirectional clipboard sync and drag-and-drop file transfer, as
specified in `specs/interaction/MODULE_CLIPBOARD.md` and
`specs/interaction/MODULE_FILETRANSFER.md`.

---

## US-CF-1: Bidirectional clipboard sync between host and controller

**As a** host user or the controlling viewer
**I want** plain text and rich-HTML clipboard content to sync in both directions
**So that** I can copy on one side and paste on the other without extra steps

**Acceptance Criteria:**
- Given `[clipboard] enabled = true` and `direction = "bidirectional"`, When the host copies plain text, Then the connected controller's clipboard is updated with that text over the dedicated clipboard stream (tag 0x02, `[u32 Len][JSON]` framing).
- Given the same configuration, When the controller copies text or sanitized HTML in the browser, Then the host OS clipboard is updated via `ClipboardHandle::set()`, which the driver serves from inside `Monitor::run` on the thread that owns the OS clipboard connection (through `arboard`, plus the thin per-OS layer for rich HTML).
- Given a `text/html` payload, When it is synced in either direction, Then it passes through the single `clipboard::sanitize_html` allowlist — `ammonia`, which drops every tag and attribute not explicitly listed, so `on*` handlers, `style`, `srcdoc`, `javascript:` and `data:` URLs go by construction rather than by enumeration — at one of its exactly two host-side call sites, before it reaches the destination clipboard.
- Given `[clipboard] enabled = false` (the default), When either side copies something, Then nothing is synced — clipboard sync is strictly opt-in.

**Validated by:** specs/interaction/MODULE_CLIPBOARD.md — "Overview", "Wire Protocol", "Sanitization", "Configuration"

---

## US-CF-2: Clipboard direction control

**As a** host user
**I want** to restrict clipboard sync to one direction only (or disable it)
**So that** I can prevent the controller from pulling secrets off my clipboard, or vice versa

**Acceptance Criteria:**
- Given `direction = "host_to_client"`, When the controller copies something in the browser, Then the server drops that client→host clipboard message (host clipboard is never overwritten).
- Given `direction = "client_to_host"`, When the host clipboard changes, Then the server never pushes it to the client.
- Given `direction = "disabled"`, When either side copies, Then no clipboard message is sent in either direction, even if `enabled = true`.
- Given the four direction values, When each is tested against both sync paths, Then behavior matches exactly one of the four rows above (all four directions x both paths).

**Validated by:** specs/interaction/MODULE_CLIPBOARD.md — "Wire Protocol" (direction gating), "Configuration", Testing Strategy row "Direction gating (all 4 directions × both paths)"

---

## US-CF-3: Clipboard sync excludes passive viewers by default

**As a** host user
**I want** only the controlling client to receive my host clipboard content
**So that** passive viewers watching my screen can't silently harvest clipboard secrets

**Acceptance Criteria:**
- Given multiple clients are connected (one controller, several viewers), When the host clipboard changes and direction allows host-to-client sync, Then only the controller's clipboard stream receives the update.
- Given a viewer session, When it attempts to open a clipboard stream (tag 0x02), Then the server cancels that stream with `close::PROTOCOL_ERROR` and leaves the session running — viewer isolation is absolute in both directions and is not configurable, regardless of `direction`.

**Validated by:** specs/interaction/MODULE_CLIPBOARD.md — "Internal Architecture" ("Viewer isolation (absolute, not configurable)"), "Security Considerations" (Viewer isolation)

---

## US-CF-4: Large clipboard payloads are capped, not silently corrupted

**As a** host user or controller
**I want** an oversized clipboard payload to be handled predictably (truncated or rejected) rather than corrupting the destination clipboard
**So that** a huge copy doesn't produce garbled or unsafe paste content

**Acceptance Criteria:**
- Given `[clipboard] max_bytes = 1048576` (default 1 MiB, range 1 KiB – 4 MiB), When a plain-text payload exceeds this size, Then it is truncated on a UTF-8 character boundary and a warning is logged, and the truncated text is still delivered.
- Given an oversized HTML payload, When it exceeds `max_bytes`, Then it is dropped outright (not truncated, since truncated HTML would be invalid markup) and counted as `featherdesk_clipboard_drops_total{reason="too_large"}`.
- Given a clipboard-stream frame whose `[u32 Len]` prefix exceeds `6 × max_bytes + 1024`, When the reader sees the prefix, Then it resets that stream with `close::PROTOCOL_ERROR (4400)` **before allocating the payload buffer** — a `u32` can name 4 GiB and must never be trusted as an allocation size.
- Given `[clipboard] formats = ["text"]` (HTML not in the allowed list), When an HTML payload up to `max_bytes` is copied, Then it is downgraded to its plain-text fallback before being placed on the OS clipboard or sent on the wire, in both directions.

**Validated by:** specs/interaction/MODULE_CLIPBOARD.md — "Size Limits", "Format filter behavior", Testing Strategy row "Size cap truncation (text, on a UTF-8 boundary) + rejection (HTML)"

---

## US-CF-5: Clipboard sync is honestly disabled on unsupported Wayland compositors

**As a** host user running GNOME or KDE on Wayland
**I want** the host to tell me clearly at startup that clipboard sync isn't available, rather than silently failing on my first copy
**So that** I'm not confused when paste doesn't work mid-session

**Acceptance Criteria:**
- Given the host OS is Linux running a Wayland compositor without `wlr-data-control` support (e.g. GNOME Mutter, KDE KWin), When the server starts and calls `clipboard::spawn`/`probe`, Then it receives `ClipboardError::UnsupportedCompositor` and logs a clear warning, disabling clipboard sync cleanly.
- Given a wlroots-family compositor (Sway, Hyprland, river, Wayfire, Cage) or any X11 desktop, When clipboard is enabled, Then change-notification and rich-HTML/file-list extras work via the thin per-OS layer on top of `arboard`.
- Given macOS, When clipboard change-notification is needed, Then it is detected via polling `NSPasteboard.changeCount` every 300ms (no native notification API exists), and this poll interval is treated as an acceptable design choice, not a bug.

**Validated by:** specs/interaction/MODULE_CLIPBOARD.md — "Host-Side Clipboard Access (per OS)", "Wayland support matrix (honest)", Testing Strategy row "macOS changeCount polling detects change ≤ 300 ms"

---

## US-CF-6: Drag-and-drop file upload from client to host

**As a** controller
**I want** to drag a file from my local machine onto the remote desktop view and have it appear on the host
**So that** I can get files to the host without a separate file-sharing tool

**Acceptance Criteria:**
- Given `[filetransfer] enabled = true` and the client is the authenticated controller, When a file is dragged onto the video canvas and dropped, Then the client opens a new bidirectional stream (tag 0x03), sends `INIT {name, size, sha256}`, and streams the file as 64 KiB `CHUNK` messages.
- Given the transfer completes, When the host receives `COMPLETE {final_sha256}`, Then it verifies the SHA-256 of the received file matches, and only then renames the `.part` file to its final name inside `<Downloads>/FeatherDesk/Incoming/`.
- Given the SHA-256 does not match, When `COMPLETE` is processed, Then the host responds with `ERROR` and discards the `.part` file — no partial/corrupt file ever appears in Incoming.
- Given a name collision with an existing file, When the final rename would overwrite it, Then the receiver appends ` (N)` before the extension until a free name is found.
- Given the client is a `view`-role session (not the controller), When it attempts to open a file-transfer stream, Then the server rejects it with `CancelRead`+`CancelWrite(CloseProtocolError)`.

**Validated by:** specs/interaction/MODULE_FILETRANSFER.md — "Transfer flow (upload, client → host)", "Atomic write", "Name collisions", "Authentication & stream identification", Testing Strategy row "Drop file → lands in Incoming, hash matches"

---

## US-CF-7: File download from host to client via the Files panel

**As a** controller
**I want** to browse files the host has made available and download them to my machine
**So that** I can retrieve files from the host session without drag-and-drop (which browsers can't do outward from a canvas)

**Acceptance Criteria:**
- Given files exist in `<Downloads>/FeatherDesk/Outgoing/`, When the client sends `LIST_REQUEST`, Then the host responds with `LIST_RESPONSE` containing a JSON array of `FileInfo` (name, size, mod_time, sha256).
- Given the client clicks a file in the Files panel, When the download starts, Then the client sends `GET_REQUEST {name}`, the host validates the name and confirms it is in the current Outgoing listing, and the transfer proceeds as INIT/ACCEPT/CHUNK/COMPLETE with directions reversed, verified end-to-end by SHA-256.
- Given a `GET_REQUEST` names a file the host is not currently offering, When the host validates it, Then it answers `ERROR {code:"not_found"}` and opens nothing.
- Given a Chrome/Edge client, When downloading, Then it uses `showSaveFilePicker()` with streaming `createWritable()` (any size); given Firefox/Safari, Then it accumulates chunks into a `Blob` and triggers `<a download>` (practical up to ~100MB).

**Validated by:** specs/interaction/MODULE_FILETRANSFER.md — "Download (file list panel)", "Browser-Side Behavior", Testing Strategy row "Download from Outgoing → client receives, hash matches"

---

## US-CF-8: File transfer progress does not stall or degrade video

**As a** host user actively presenting or being controlled during a file transfer
**I want** a large file transfer running at the same time as video/input to not add latency to my video stream
**So that** sharing a file doesn't make my remote session feel laggy

**Acceptance Criteria:**
- Given a large file transfer is in progress on its own QUIC stream, When video frames and input records continue on their own stream/datagram paths, Then the file-transfer stream's back-pressure never blocks or delays the video datagram path (isolated by QUIC per-stream flow control).
- Given `[filetransfer] rate_limit_bps` is configured (nonzero), When N transfers are in flight, Then their **combined** throughput stays at or below `rate_limit_bps` measured over any 1-second window — one shared token bucket across all in-flight transfers, not one per transfer.
- Given `PROGRESS {bytes_done, total}` messages, When a transfer is running, Then the client can render a progress indicator without polling.

**Validated by:** specs/interaction/MODULE_FILETRANSFER.md — "Performance Considerations", "Chunk size, back-pressure & resume", Testing Strategy row "Large transfer does NOT raise video frame latency (separate socket)"

---

## US-CF-9: File transfer resumes after a mid-transfer disconnect

**As a** controller transferring a large file
**I want** an interrupted transfer to resume from where it left off after reconnecting
**So that** I don't have to restart a multi-gigabyte upload from zero after a brief network drop

**Acceptance Criteria:**
- Given a transfer is interrupted mid-stream, When the client reconnects and sends `RESUME {transfer_id}`, Then the host replies `ACCEPT {resume_from_seq: lastFsyncSeq+1}`, using the durable (fsynced) watermark, not merely the written-to-kernel watermark.
- Given chunks were written but not yet fsynced at the moment of the crash/disconnect, When resume occurs, Then those chunks are re-sent (never skipped), and the final SHA-256 check still catches any residual corruption.
- Given the periodic fsync interval (`fsync_interval`, default 64 chunks ~4MiB) and on `COMPLETE`, When durable writes advance, Then an `ACK` advertising `lastFsyncSeq` is emitted to update the resume checkpoint.

**Validated by:** specs/interaction/MODULE_FILETRANSFER.md — "Chunk size, back-pressure & resume" (Resume durability rule), Testing Strategy row "Resume after mid-transfer disconnect"

---

## US-CF-10: Uploaded files cannot escape the sandboxed Incoming folder

**As a** host user
**I want** an uploaded file's name to never be able to write outside the configured Incoming folder
**So that** a malicious or buggy client can't overwrite arbitrary files on my machine

**Acceptance Criteria:**
- Given an uploaded filename containing `..`, an absolute path, control characters, or Windows-invalid characters (`<>:"/\|?*`), When the host validates the name, Then the transfer is rejected as `FileTransferError::BadName` before any file is opened.
- Given a filename matching a Windows reserved name (`CON`, `PRN`, `COM1`..`COM9`, `LPT1`..`LPT9`, case-insensitive, with any extension), When validated, Then it is rejected.
- Given a symlink planted anywhere between the Incoming root and the resolved target path, When the receiver lstat's the path and its ancestors, Then the transfer is rejected rather than following the symlink (defends against TOCTOU).
- Given a validated, clean name, When the file is opened for writing, Then it uses `O_CREAT|O_EXCL` semantics so a concurrent transfer cannot race the existence check.

**Validated by:** specs/interaction/MODULE_FILETRANSFER.md — "Security Considerations" (Fixed-folder sandbox, 6-step guard), Testing Strategy row "`validate_name` rejection matrix"
