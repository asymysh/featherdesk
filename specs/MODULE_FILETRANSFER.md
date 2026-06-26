# Module Spec: File Transfer

## Overview

The File Transfer module moves files between the browser client and the remote
host. It is **core** (not an add-on). Two design decisions define it:

1. **Separate WebSocket connection.** File transfer runs on its own
   `/files` WebSocket, NOT the main media/input socket. Large chunks must never
   stall the real-time video/input stream (TCP has no in-connection message
   prioritization; a 64 KiB chunk queued ahead of a 16-byte input event would
   add latency). A dedicated connection gives file transfer independent TCP
   congestion control and back-pressure.

2. **Fixed destination folder.** Uploads always land in a single, configured
   folder (default: the OS Downloads directory). There is NO arbitrary remote
   filesystem write access and NO remote directory browser for writing. This is
   the security boundary — a compromised client can only write into one known
   sandbox folder, never traverse the host filesystem.

**Primary UX:** drag-and-drop a file from the local machine onto the remote
desktop view → it uploads to the host's fixed folder.

---

## Directories

Two fixed folders, resolved per-OS, both configurable:

| Direction | Default folder | Purpose |
|-----------|----------------|---------|
| **Upload** (client → host) | `<Downloads>/FeatherDesk/Incoming/` | Dropped files land here |
| **Download** (host → client) | `<Downloads>/FeatherDesk/Outgoing/` | Files the host offers for download |

`<Downloads>` resolves to:
- Windows: `SHGetKnownFolderPath(FOLDERID_Downloads)` → `%USERPROFILE%\Downloads`
- Linux: `$XDG_DOWNLOAD_DIR`, else `~/Downloads`
- macOS: `~/Downloads`

The host creates the `FeatherDesk/Incoming` and `FeatherDesk/Outgoing`
subfolders on first use (mode 0700). Both paths are overridable in
`[filetransfer]` config.

> **Hard boundary.** The server confines every write to the configured Incoming
> folder. Sandbox enforcement (see "Path-traversal guard" below) is mandatory.

---

## Public Interface

```go
package filetransfer

// Service manages transfers over the dedicated /files WebSocket.
type Service interface {
    // Serve handles one /files WebSocket connection (one authenticated client).
    // Blocks until the connection closes.
    Serve(ctx context.Context, conn Conn) error

    // ListOutgoing returns the files currently offered for download
    // (contents of the Outgoing folder).
    ListOutgoing() ([]FileInfo, error)

    Close() error
}

type FileInfo struct {
    Name     string
    Size     int64
    ModTime  time.Time
    SHA256   string // computed lazily / cached
}

// Config sources the [filetransfer] TOML section.
type Config struct {
    Enabled       bool
    IncomingDir   string        // default <Downloads>/FeatherDesk/Incoming
    OutgoingDir   string        // default <Downloads>/FeatherDesk/Outgoing
    MaxFileBytes  int64         // per-file cap (default 0 = unlimited)
    MaxConcurrent int           // concurrent transfers (default 4)
    RateLimitBps  int64         // 0 = unlimited; else throttle to protect video
    Logger        *slog.Logger
}
```

---

## Wire Protocol (dedicated `/files` connection)

### Authentication on the `/files` upgrade

Browsers cannot set arbitrary headers on the `WebSocket()` constructor, so
`Authorization: Bearer` does not work. The client carries the session token in
the **`Sec-WebSocket-Protocol`** subprotocol header — a standard pattern (used
by Kubernetes for `kubectl exec`) — as `bearer.<session_token>`:

```js
const ws = new WebSocket("wss://host:port/files", ["bearer." + sessionToken]);
```

Server verifies the token at upgrade, echoes the protocol back, and requires
the connecting principal to be the current **controller**. Anything else is
rejected with HTTP 401 before upgrade. The same mechanism is used on `/ws` (see
[`MODULE_SERVER.md`](./MODULE_SERVER.md) and
[`MODULE_AUTH.md`](./MODULE_AUTH.md)) — URL `?token=` / `?resume=` query params
are NOT used (they leak to proxy access logs and Referer headers).

### Wire framing

Binary framing on its own connection, so its type space is independent of the
media/input protocol. Every message:

```
Offset  Size  Field        Notes
0       1     Version      uint8 (currently 1)
1       1     MsgType      uint8
2       4     TransferID   uint32 LE (0 for connection-level messages)
6       4     Seq          uint32 LE (chunk sequence within a transfer)
10      4     PayloadLen   uint32 LE
14      4     CRC32C       uint32 LE (Castagnoli, poly 0x1EDC6F41; 0 if PayloadLen==0)
18      …     Payload      PayloadLen bytes
```

> **CRC32C, not CRC32.** Use the Castagnoli polynomial (`0x1EDC6F41`,
> hardware-accelerated on x86 via SSE4.2 and on ARMv8). In Go,
> `hash/crc32.MakeTable(crc32.Castagnoli)` — NOT the default `IEEEPoly` /
> `crc32.IEEETable`. Mismatch corrupts the protocol.

The 1-byte `Version` lets a future binary wire change (wider `Seq`, different
chunk size, new compression layer) negotiate via a `HELLO` message at the start
of the connection without forcing every reader to guess.

### Message types

| Msg | ID | Dir | Payload |
|-----|----|-----|---------|
| `LIST_REQUEST` | 0x01 | C→H | (empty) — request Outgoing folder listing |
| `LIST_RESPONSE` | 0x02 | H→C | JSON array of `FileInfo` |
| `INIT` | 0x10 | both | JSON `{name, size, sha256}` — begin a transfer |
| `ACCEPT` | 0x11 | both | JSON `{transfer_id, resume_from_seq}` — receiver ready |
| `CHUNK` | 0x12 | both | raw bytes (≤ 64 KiB) |
| `ACK` | 0x13 | both | JSON `{ack_seq}` — cumulative ack for windowed flow control |
| `COMPLETE` | 0x14 | both | JSON `{final_sha256}` |
| `CANCEL` | 0x15 | both | JSON `{reason}` |
| `RESUME` | 0x16 | both | JSON `{transfer_id}` — after reconnect |
| `PROGRESS` | 0x17 | both | JSON `{bytes_done, total}` — for UI |
| `ERROR` | 0x18 | both | JSON `{transfer_id, code, message}` |

### Transfer flow (upload, client → host)

```
C: INIT {name:"a.zip", size:10485760, sha256:"…"}
H: validate name (sandbox guard), check MaxFileBytes, MaxConcurrent
H: ACCEPT {transfer_id:7, resume_from_seq:0}
C: CHUNK seq=0 … CHUNK seq=N  (window-limited; ≤ 32 unacked chunks)
H: ACK {ack_seq:k}  (cumulative; lets sender advance the window)
C: COMPLETE {final_sha256:"…"}
H: verify SHA-256 of received file == final_sha256, then os.Rename(.part → final)
H: COMPLETE (echo) on success, or ERROR on mismatch (.part discarded)
```

**Transfer ID ownership.** The **receiver** assigns `transfer_id` in `ACCEPT`,
regardless of direction. On uploads the host receives → host assigns; on
downloads the client receives → client assigns. IDs are unique per `/files`
connection. This prevents collisions when both directions are active.

**Atomic write.** The receiver writes chunks into `<IncomingDir>/<name>.part`,
fsyncs, then `os.Rename` to the final name **only after** SHA-256 verifies. On
SHA-256 mismatch / `CANCEL` / disconnect-without-resume the `.part` is
`os.Remove`d so partial corrupt files never appear in the user's Downloads.

**Name collisions.** If `<name>` already exists when renaming, the receiver
appends ` (N)` before the extension (`report (1).pdf`, `report (2).pdf`, …)
until a free name is found. This mirrors browser download behavior.

Download (host → client) is the same flow with directions reversed; the client
writes to disk via `showSaveFilePicker` (Chrome/Edge streaming) or Blob + `<a>`
(Firefox/Safari, ≤ ~100 MB).

### Chunk size & flow control

- **64 KiB** chunks (matches `File.stream()` reader output; stays under proxy
  fragmentation thresholds; ~5 ms transmit at 100 Mbps).
- **Windowed flow control:** sender keeps ≤ 32 unacked chunks in flight; receiver
  sends cumulative `ACK` as chunks are written. This bounds memory and lets the
  receiver apply back-pressure without TCP-level stalls bleeding into the
  (separate) video connection.
- **Resume (durability rule):** the receiver tracks two distinct watermarks —
  `lastWrittenSeq` (what's hit the kernel) and `lastFsyncSeq` (what's actually
  on disk). At every window flush it `fsync`s and updates `lastFsyncSeq`.
  `ACK` advertises `lastFsyncSeq` (NOT `lastWrittenSeq`), and on `RESUME` the
  receiver replies `ACCEPT {resume_from_seq: lastFsyncSeq+1}`. Without this rule
  a crash between write and fsync would let resume skip an unwritten chunk; the
  SHA-256 check catches it but at the cost of the whole transfer.

---

## Browser-Side Behavior

### Upload (drag-and-drop, primary)

```
dragover  on the video canvas → preventDefault + show drop overlay
drop      → DataTransfer.files → for each File:
            open /files WebSocket (if not already open)
            INIT, then stream file.stream().getReader() as 64 KiB CHUNKs
```

Drag-over feedback (dim the remote view, show a drop zone with the target folder
name) is required UX — without it users won't discover the feature.

Also offer a fallback `<input type="file" multiple>` button for browsers/contexts
where drag-and-drop is awkward (touch clients).

### Download (file list panel)

The client shows a small **Files** panel listing the Outgoing folder
(`LIST_REQUEST` → `LIST_RESPONSE`). Clicking a file starts a host→client
transfer. There is no "drag a file out of the browser" path — browsers cannot
emit real files from a `dragstart` on a canvas, so an explicit panel is the
correct model (same as Chrome Remote Desktop / AnyDesk).

| Browser | Download sink |
|---------|---------------|
| Chrome / Edge | `showSaveFilePicker()` → streaming `createWritable()` (true streaming, any size) |
| Firefox / Safari | accumulate chunks → `Blob` → `URL.createObjectURL` + `<a download>` (practical ≤ ~100 MB) |

---

## Performance Considerations

- **Why a separate connection:** on the single media socket, a 64 KiB file chunk
  queued ahead of input/video bytes adds 5-50 ms of video latency under load
  (TCP can't reorder within a connection). A dedicated `/files` socket isolates
  this completely.
- **Throughput:** WebSocket over WSS reaches ~80-95% of raw TCP. On 1 Gbps,
  expect ~100-120 MB/s; the bottleneck is JS processing + disk I/O, not framing.
- **Rate limiting:** `[filetransfer] rate_limit_bps` is a **single token bucket
  shared across all in-flight transfers** (not per-transfer). This bounds total
  bandwidth so video latency stays predictable even with `max_concurrent`
  simultaneous transfers.
- **Overflow on `max_concurrent`:** the server **queues** the new transfer (FIFO,
  bounded queue depth of 64) and emits `PROGRESS {state:"queued"}` to the
  sender. When a slot frees, the queued transfer is `ACCEPT`ed. The queue is
  rejected (`ERROR {code:"queue_full"}`) only at the depth cap, which is a
  hard misuse case.
- **Compression:** off by default. Most transferred files (zip, jpg, mp4, gz) are
  already compressed. An optional per-transfer `zstd` negotiation can be added
  later for text/log/source payloads; not in v1.

---

## Configuration

```toml
[filetransfer]
enabled        = false                # opt-in
incoming_dir   = ""                   # "" = <Downloads>/FeatherDesk/Incoming
outgoing_dir   = ""                   # "" = <Downloads>/FeatherDesk/Outgoing
max_file_bytes = 0                    # 0 = unlimited; else per-file cap
max_concurrent = 4                    # simultaneous transfers
rate_limit_bps = 0                    # 0 = unlimited; else throttle to protect video
```

---

## Security Considerations

- **Opt-in default.** `enabled = false`.
- **Fixed-folder sandbox.** Writes are confined to `incoming_dir`. The path-
  traversal guard is precisely specified (substring matches on `..` are NOT
  sufficient and produce both false positives and false negatives). The
  receiver performs **all** of these steps before opening any file:
  1. Reject the name if it contains a NUL byte, a control character
     (`\x00-\x1F`), or any of `<>:"/\|?*` (Windows-invalid). Reject trailing
     `.` or trailing space.
  2. Reject the name if it matches a Windows reserved name **case-insensitively
     with any extension**: `CON`, `PRN`, `AUX`, `NUL`, `COM1..COM9`, `LPT1..LPT9`,
     plus `COM¹/COM²/COM³`, `LPT¹/LPT²/LPT³`. (`CON.txt` is also reserved.)
  3. `joined = filepath.Clean(filepath.Join(IncomingDir, name))`.
  4. `rel, err := filepath.Rel(IncomingDir, joined)`; reject if `err != nil`,
     `rel == ".."`, or `strings.HasPrefix(rel, ".." + string(os.PathSeparator))`,
     or `filepath.IsAbs(rel)`.
  5. `os.Lstat(joined)` and every ancestor between `IncomingDir` (exclusive) and
     `joined` (inclusive). If any is a symlink, reject — a previously planted
     symlink would otherwise rewrite the prefix on TOCTOU.
  6. Open with `O_CREATE|O_EXCL` (so a concurrent transfer can't race the
     existence check) and a restrictive mode (`0600`).
- **Controller-only.** Only the authenticated controller may transfer files;
  viewers cannot. The `/files` connection re-uses the main session's auth
  (Bearer token / session token), validated at upgrade.
- **Integrity.** Every transfer is verified by an end-to-end SHA-256; a mismatch
  discards the received file.
- **Per-chunk CRC32C** catches corruption early (before the full-file hash).
- **Resource caps.** `max_file_bytes`, `max_concurrent`, and the 32-chunk window
  bound memory and disk usage; reject transfers that would exceed them.
- **No execution.** Uploaded files are written, never executed or opened by the
  host. The fixed folder should not be an auto-run / startup location.
- **TLS.** The `/files` socket is WSS (same mandatory-TLS rule as the main socket).

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | Filename sanitization + path-traversal rejection (`..`, absolute, symlink) | No |
| Unit | Chunk framing encode/decode, CRC32C verify | No |
| Unit | Windowed flow control (advance on cumulative ACK) | No |
| Unit | SHA-256 mismatch → file discarded | No |
| Integration | Drop file → lands in Incoming, hash matches | Yes |
| Integration | Download from Outgoing → client receives, hash matches | Yes |
| Integration | Resume after mid-transfer disconnect | Yes |
| Integration | Concurrent transfers respect max_concurrent | Yes |
| Load | Large transfer does NOT raise video frame latency (separate socket) | Yes |

---

## Status

📋 **Specced — not yet implemented.** Core module. Drag-and-drop upload + file
list download, fixed folders, dedicated `/files` connection. Compression and a
full remote file browser are explicitly out of scope for v1.
