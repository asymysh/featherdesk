# Module Spec: File Transfer

## Overview

The File Transfer module moves files between the browser client and the remote
host. It is **core** (not an add-on). Two design decisions define it:

1. **One QUIC stream per transfer**, multiplexed on the same WebTransport
   session as media and input. Each active transfer is its own bidirectional
   stream. QUIC's independent stream multiplexing means a long file-transfer
   stream **does not block** the video datagram path, the input stream, or any
   other concurrent transfer. This replaces the prior dedicated `/files`
   WebSocket — under WebTransport the isolation we wanted is automatic, with
   one fewer endpoint to operate. On the degraded WebSocket fallback carrier
   there is one connection and therefore no such isolation: a transfer shares the
   single ordered byte stream with video, audio, input and control, and
   `TransferID` rather than stream identity separates concurrent transfers (see
   "Authentication & stream identification").

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

```rust
// crate: featherdesk-filetransfer

/// Service manages file transfers carried over the main session's bidirectional
/// file-transfer lanes. Cleanup is RAII (`Drop`) — no Close().
/// `Send + Sync` because the server calls `serve_stream` from N concurrent
/// stream tasks through an `Arc<dyn Service>`.
#[async_trait::async_trait]
pub trait Service: Send + Sync {
    /// serve_stream handles ONE bidirectional stream that has been identified
    /// (by its first message) as a file-transfer stream. The future resolves when
    /// the stream closes. Called by the server module from its accept-stream loop
    /// after the controller has authenticated. The cancellation token replaces the
    /// Go `context.Context`.
    async fn serve_stream(
        &self,
        cancel: CancellationToken,
        s: Box<dyn transport::Stream>,
    ) -> Result<(), FileTransferError>;

    /// list_outgoing returns the files currently offered for download — the
    /// contents of the Outgoing folder, **regular files only** (symlinks and
    /// non-regular entries are skipped). It is also the authorization list for
    /// `GET_REQUEST`: a name absent from a freshly-recomputed listing cannot be
    /// fetched (see "Read-path guard").
    fn list_outgoing(&self) -> Result<Vec<FileInfo>, FileTransferError>;

    /// set_limits applies a `[filetransfer]` hot reload. Takes `&self` because it
    /// is called on the shared `Arc<dyn Service>` from the pipeline's `fd-config`
    /// applier task (`pipeline::config_applier`) while transfers are in flight;
    /// the new values bind at the next `INIT`/`ACCEPT`
    /// and at the next token-bucket refill, never mid-chunk. `queue_depth` is
    /// deliberately NOT here, and `max_concurrent` deliberately is. `queue_depth`
    /// sizes the accepted-but-waiting FIFO the Service allocates once, at
    /// construction, and it is the term of `config.fileStreamBudget` that tells an
    /// already-connected client how many transfer streams it may leave
    /// OUTSTANDING — changing it mid-session desyncs a number the client is
    /// holding. `max_concurrent` and the two byte limits bound only what the
    /// Service will SERVE at any instant: lowering one makes a waiting transfer
    /// wait longer, which every client already handles, and never invalidates a
    /// budget a client was advertised. `queue_depth` is restart-required.
    fn set_limits(&self, max_concurrent: u32, rate_limit_bps: u64, max_file_bytes: u64);
}

pub struct FileInfo {
    pub name: String,
    pub size: u64,
    pub mod_time: std::time::SystemTime,
    pub sha256: String, // computed lazily / cached
}

/// FileTransferError — stable error enum for the filetransfer crate.
#[derive(thiserror::Error, Debug)]
pub enum FileTransferError {
    #[error("filetransfer: payload exceeds the per-type cap")]
    Oversized,
    #[error("filetransfer: SHA-256 mismatch")]
    HashMismatch,
    #[error("filetransfer: path-traversal / invalid or unsafe filename rejected by validate_name")]
    BadName,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

/// Config sources the [filetransfer] TOML section.
/// (Logging is via the global `tracing` subscriber — no per-service logger handle.)
/// The two directory strings are resolved ONCE at startup into `cap_std::fs::Dir`
/// handles and never re-resolved — see "Fixed-folder sandbox". Crates: `cap-std`
/// (the directory capabilities), `crc32c`, `sha2`, `unicode-normalization`.
pub struct Config {
    pub enabled: bool,
    pub incoming_dir: String,    // default <Downloads>/FeatherDesk/Incoming
    pub outgoing_dir: String,    // default <Downloads>/FeatherDesk/Outgoing
    pub max_file_bytes: u64,     // per-file cap (default 0 = unlimited)
    pub max_concurrent: u32,     // simultaneously TRANSFERRING (default 4)
    pub queue_depth: u32,        // accepted-but-waiting beyond max_concurrent (default 8);
                                 // max_concurrent + queue_depth is advertised to the
                                 // client as config.fileStreamBudget
    pub rate_limit_bps: u64,     // 0 = unlimited; else throttle to protect video
}
```

---

## Wire Protocol (file-transfer lanes on the main session)

### Authentication & stream identification

There is **no separate auth** — the session was already authenticated on its
control stream (see [`MODULE_AUTH.md`](../core/MODULE_AUTH.md)), and only the
**controller** role may open file-transfer streams
([`MODULE_SERVER.md`](../core/MODULE_SERVER.md) "Role gate table" row 8). The
server rejects a `0x03` stream from any other role, and every `0x03` stream when
`[filetransfer] enabled = false`, at the accept point — before this module reads
a byte — with `cancel_read` + `cancel_write(close::PROTOCOL_ERROR)` at stream
scope. The session and its other streams are unaffected.

The server identifies a stream as a file-transfer stream by its **`StreamType`
tag**: every reliable stream's first byte is a `StreamType` tag (see
[`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md)), and tag `0x03` denotes a
file-transfer stream. The transport layer dispatches such streams to this
module's `serve_stream`, which receives the stream positioned **after** the tag
byte. The first application message it then reads is the 18-byte file-transfer
framing header below, with `MsgType = 0x10 INIT` (uploads) or `0x01
LIST_REQUEST` (downloads); on a download stream the client's second message is
`0x03 GET_REQUEST`.

On the **WebSocket fallback carrier** there are no streams to open: `0x03` is a
message tag on the single connection, and concurrent transfers are distinguished
by the header's own `TransferID` rather than by stream identity. The framing,
the message set and the sandbox are byte-identical; only the carrier differs
(see [`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Carrier selection").
The stream-isolation argument in the Overview and under "Performance
Considerations" is a property of the QUIC carrier, and is one of the things the
fallback gives up.

### Wire framing

Binary framing on its own lane, so its type space is independent of the
media/input protocol. Every message:

```
Offset  Size  Field        Notes
0       1     Version      uint8 (currently 1)
1       1     MsgType      uint8
2       4     TransferID   uint32 LE. Assigned by the INITIATOR and present on
                                 EVERY message of the transfer including INIT (see
                                 "Transfer ID ownership"): odd = client-initiated,
                                 even ≥ 2 = host-initiated. 0 ONLY for the
                                 connection-level messages LIST_REQUEST and
                                 LIST_RESPONSE, which belong to no transfer, and on
                                 an ERROR refusing a GET_REQUEST (which names the
                                 request in `request_id` instead). GET_REQUEST
                                 itself carries the client's own odd request id.
6       4     Seq          uint32 LE (chunk sequence within a transfer)
10      4     PayloadLen   uint32 LE
14      4     CRC32C       uint32 LE (Castagnoli, poly 0x1EDC6F41; 0 if PayloadLen==0)
18      …     Payload      PayloadLen bytes
```

> **CRC32C, not CRC32.** Use the Castagnoli polynomial (`0x1EDC6F41`,
> hardware-accelerated on x86 via SSE4.2 and on ARMv8). In Rust, use the
> `crc32c` crate (hardware-accelerated) — NOT a default `IEEE` CRC-32
> (`crc32fast` / the `crc` crate's `CRC_32_ISO_HDLC`). Mismatch corrupts the protocol.

The 1-byte `Version` lets a future binary wire change (wider `Seq`, different
chunk size, new compression layer) negotiate via a `HELLO` message at the start
of the connection without forcing every reader to guess.

> **`PayloadLen` cap (mandatory, checked before allocation).** `PayloadLen` is a
> `uint32` (up to 4 GiB) but the reader MUST reject oversized frames **before**
> allocating the payload buffer, or a malicious peer can request a 4 GiB
> allocation per frame. Caps: a `CHUNK` payload is **≤ 64 KiB**; a JSON control
> message (`INIT`/`LIST_RESPONSE`/`ACK`/…) is **≤ 256 KiB** (a large Outgoing
> listing still fits). Any frame exceeding its type's cap → `CancelRead` +
> `CancelWrite(CloseProtocolError)` and the transfer is dropped.

### Message types

| Msg | ID | Dir | Payload |
|-----|----|-----|---------|
| `LIST_REQUEST` | 0x01 | C→H | (empty) — request Outgoing folder listing |
| `LIST_RESPONSE` | 0x02 | H→C | JSON array of `FileInfo` |
| `GET_REQUEST` | 0x03 | C→H | JSON `{name}` — ask the host to begin a host→client transfer of this Outgoing file. The client puts an **odd** `TransferID` of its own in the header: it is the id the client will use to bind the answer, and the host echoes it in the `request_id` field of whichever reply it sends. The transfer itself is host-initiated and carries the host's own EVEN id |
| `INIT` | 0x10 | both | JSON `{name, size, sha256, request_id?}` — begin a transfer. The header carries the initiator-assigned `TransferID`; it is never 0. `request_id` is present **iff** this `INIT` answers a `GET_REQUEST`, echoing that request's odd id — it is what binds a successful download back to the file the client asked for, exactly as `ERROR.request_id` binds a refusal. Absent on a client-initiated upload `INIT` |
| `ACCEPT` | 0x11 | both | JSON `{transfer_id, resume_from_seq}` — receiver ready. `transfer_id` **echoes** the initiator's id (it does not assign one) and MUST equal the header's `TransferID` |
| `CHUNK` | 0x12 | both | raw bytes (≤ 64 KiB; enforced by the PayloadLen cap above) |
| `ACK` | 0x13 | both | JSON `{ack_seq}` — cumulative **durable-write** checkpoint (for resume), NOT congestion control |
| `COMPLETE` | 0x14 | both | JSON `{final_sha256}` |
| `CANCEL` | 0x15 | both | JSON `{reason}` |
| `RESUME` | 0x16 | both | JSON `{transfer_id}` — after reconnect |
| `PROGRESS` | 0x17 | both | JSON `{bytes_done, total}` — for UI |
| `ERROR` | 0x18 | both | JSON `{transfer_id, code, message, request_id?}` — `transfer_id` echoes the initiator's id, so an `ERROR` answering an `INIT` is bindable even when no `ACCEPT` was ever sent. An `ERROR` that refuses a `GET_REQUEST` has no transfer to name: it sets `transfer_id` to 0 and echoes the request's odd id in `request_id`, which is the only thing binding it to the file the client asked for |

### Transfer flow (upload, client → host)

```
C: INIT {name:"a.zip", size:10485760, sha256:"…"}
H: validate_name(name), check max_file_bytes, max_concurrent (see Security Considerations)
H: ACCEPT {transfer_id:7, resume_from_seq:0}
C: CHUNK seq=0 … CHUNK seq=N  (sender just writes; QUIC stream flow control paces it)
H: ACK {ack_seq:k}  (cumulative durable-write checkpoint; advances the resume point)
C: COMPLETE {final_sha256:"…"}
H: verify SHA-256 of received file == final_sha256, then `incoming.rename(&part, &incoming, &final)` (see "Fixed-folder sandbox")
H: COMPLETE (echo) on success, or ERROR on mismatch (.part discarded)
```

**Flow control (re-justified for QUIC).** There is **no app-level sliding
window**. Under WebTransport each transfer is its own QUIC stream with built-in
per-stream flow control (`[transport] initial_max_stream_data`, default 1 MiB):
when the receiver hasn't consumed data, the sender's `Write` simply blocks. The
old "≤ 32 unacked chunks" window (a TCP/WebSocket-era mechanism) is removed — it
would only duplicate, and conflict with, QUIC's own window. `ACK` survives purely
as a **durable-write checkpoint** for `RESUME` (it tells the sender the highest
`Seq` the receiver has fsynced), not as a congestion or pacing signal. To protect
video latency, an optional `[filetransfer] rate_limit_bps` throttles the sender
above and beyond QUIC's fair sharing.

**Transfer ID ownership.** The **initiator** — the side that sends `INIT` —
assigns `transfer_id`, and puts it in the header of that `INIT`. The id space is
split by parity, exactly as QUIC splits stream ids: **client-initiated transfers
use ODD ids, host-initiated transfers use EVEN ids ≥ 2** (`0` is reserved for the
connection-level messages below). Each side allocates from its own parity class,
so the two directions can never collide and neither side needs to coordinate.

**Allocation lifetime.** Parity prevents collisions *between* the two sides; this
rule prevents them *within* one side. Each side allocates from a strictly
increasing counter in its parity class and never reuses an id.

The counter is scoped to the **resumable session, not the connection** — this is
the whole point, since `RESUME` exists precisely to cross a reconnect. It lives
and dies with the session state `[reconnect] cache_ttl_seconds` holds, so a client
that drops and resumes keeps its id space: the counter does NOT restart at
reconnect, and an id is never reissued while the session survives. An id is
**live** from the `INIT` that introduces it until that transfer reaches
`COMPLETE`, `CANCEL` or `ERROR`; an interrupted transfer's id stays reserved and
resumable for as long as the session does. `RESUME {transfer_id}` therefore always
names exactly one transfer, and an id retired by `COMPLETE` is never handed to a
later transfer that a stale in-flight `CHUNK` could still be addressing. When the
session expires, its transfers are unresumable anyway and the id space goes with
it.

`ACCEPT` and `ERROR` **echo** the initiator's `transfer_id`; they do not assign
one. This is what makes every message of a transfer self-binding from the very
first byte, which the QUIC carrier used to provide implicitly by putting each
transfer on its own stream. On the WebSocket fallback there is no such stream, so
a receiver-assigned id would leave the initiator's `INIT` — and the reply to it —
with nothing to bind them: with two concurrent `INIT`s the initiator could only
guess which `ACCEPT` answered which file, and a wrong guess silently swaps the two
files' contents and fails both SHA-256 checks at `COMPLETE`.

**Atomic write.** The receiver writes chunks into the `.part` name derived in
"Fixed-folder sandbox", fsyncs (`File::sync_all`), reserves the final name, and
renames — see that section for the exact four names and how each is opened.

**Name collisions.** The ` (N)` walk (`report (1).pdf`, `report (2).pdf`, …)
happens at **reservation** time, not at rename time: the receiver reserves the
first free final name with `create_new(true)` before it renames onto it, so two
concurrent transfers of the same name cannot both pick it. This mirrors browser
download behavior.

### Transfer flow (download, host → client)

```
C: LIST_REQUEST                                  (empty)
H: LIST_RESPONSE [{name,size,sha256}, …]         (contents of the Outgoing folder)
C: GET_REQUEST {name:"a.zip"}                     (header TransferID = 1 — the client's own odd request id)
H: validate_name(name) → membership check → open for read (see "Read-path guard")
H: INIT {name:"a.zip", size:10485760, sha256:"…", request_id:1}
                                                   (header TransferID = 2 — the HOST initiates this
                                                    transfer, so it allocates from the EVEN class;
                                                    request_id echoes the GET_REQUEST's odd id, which
                                                    is what tells the client WHICH request this answers)
C: ACCEPT {transfer_id:2, resume_from_seq:0}     (ECHOES the host's id; the receiver assigns nothing)
H: CHUNK seq=0 … CHUNK seq=N
C: ACK {ack_seq:k}
H: COMPLETE {final_sha256:"…"}
```

The client writes to disk via `showSaveFilePicker` (Chrome/Edge streaming) or
Blob + `<a>` (Firefox/Safari, ≤ ~100 MB). The client's own save location is the
browser's business; the host's read is guarded under "Security Considerations".

### Chunk size, back-pressure & resume

- **64 KiB** chunks (matches `File.stream()` reader output; stays under proxy
  fragmentation thresholds; ~5 ms transmit at 100 Mbps).
- **Back-pressure is QUIC's, not an app window.** As stated in "Flow control
  (re-justified for QUIC)" above, there is **no ≤32-unacked-chunk app window** —
  that TCP/WebSocket-era mechanism is removed because it duplicates and conflicts
  with QUIC's per-stream flow control (`[transport] initial_max_stream_data`).
  The sender's `Write` blocks when the receiver hasn't consumed; that *is* the
  back-pressure, and it stays inside this transfer's stream without bleeding into
  the (datagram) video path. `ACK` is **only** a durable-write checkpoint for
  `RESUME` (below), never a congestion/pacing signal.
- **Resume (durability rule):** the receiver tracks two distinct watermarks —
  `lastWrittenSeq` (what's hit the kernel) and `lastFsyncSeq` (what's actually
  on disk). Periodically (every `fsync_interval` chunks, default 64 ≈ 4 MiB, and
  on `COMPLETE`) it `fsync`s and updates `lastFsyncSeq`, then emits an `ACK`
  advertising `lastFsyncSeq` (NOT `lastWrittenSeq`). On `RESUME` the
  receiver replies `ACCEPT {resume_from_seq: lastFsyncSeq+1}`. Without this rule
  a crash between write and fsync would let resume skip an unwritten chunk; the
  SHA-256 check catches it but at the cost of the whole transfer.

---

## Browser-Side Behavior

### Upload (drag-and-drop, primary)

```
dragover  on the video canvas → preventDefault + show drop overlay
drop      → DataTransfer.files → for each File:
            open a new bidirectional stream on the existing WebTransport session
            INIT, then stream file.stream().getReader() as 64 KiB CHUNKs
```

Drag-over feedback (dim the remote view, show a drop zone with the target folder
name) is required UX — without it users won't discover the feature.

Also offer a fallback `<input type="file" multiple>` button for browsers/contexts
where drag-and-drop is awkward (touch clients).

### Download (file list panel)

The client shows a small **Files** panel listing the Outgoing folder
(`LIST_REQUEST` → `LIST_RESPONSE`). Clicking a file sends
`GET_REQUEST {name}` for that entry, which is what starts the host→client
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
  (TCP can't reorder within a connection). Under QUIC, the file-transfer stream isolates
  this completely.
- **Throughput:** a QUIC reliable stream reaches near-line-rate of the underlying UDP path. On 1 Gbps,
  expect ~100-120 MB/s; the bottleneck is JS processing + disk I/O, not framing.
- **Rate limiting:** `[filetransfer] rate_limit_bps` is a **single token bucket
  shared across all in-flight transfers** (not per-transfer). This bounds total
  bandwidth so video latency stays predictable even with `max_concurrent`
  simultaneous transfers.
- **Overflow on `max_concurrent`:** the server **queues** the new transfer (FIFO,
  depth `[filetransfer] queue_depth`, default 8) and emits
  `PROGRESS {state:"queued"}` to the sender. When a slot frees, the queued
  transfer is `ACCEPT`ed. The queue is rejected (`ERROR {code:"queue_full"}`)
  only at the depth cap — which a well-behaved client never reaches, because
  `config.fileStreamBudget` (= `max_concurrent + queue_depth`) tells it exactly
  how many file-transfer streams it may hold open at once. `queue_full` is
  therefore a misuse signal, not a capacity signal.
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
max_concurrent = 4                    # simultaneously transferring
queue_depth    = 8                    # accepted-but-waiting beyond max_concurrent;
                                      # max_concurrent + queue_depth is advertised to
                                      # the client as config.fileStreamBudget
rate_limit_bps = 0                    # 0 = unlimited; else throttle to protect video
```

---

## Security Considerations

- **Opt-in default.** `enabled = false`.
- **Fixed-folder sandbox, enforced by capability, not by string matching.**
  Substring matches on `..` are not sufficient (false positives and false
  negatives), and neither is canonicalize-then-compare-prefix, because the
  comparison is made against a path that can be re-resolved differently a
  microsecond later. Instead the escape is made unrepresentable:

  1. At startup, the service opens each configured folder **once** and keeps the
     handle for the process lifetime:
     `let incoming: cap_std::fs::Dir = Dir::open_ambient_dir(&cfg.incoming_dir, ambient_authority())?;`
     and likewise `outgoing`. `incoming_dir` / `outgoing_dir` are resolved
     (including `<Downloads>` expansion and, on Windows, the `\\?\` long-path
     prefix) exactly here and never again. If the folder is later replaced by a
     symlink, moved, or deleted, the held handle still refers to the original
     directory — the TOCTOU window between "check the path" and "open the file"
     does not exist because the path is never checked twice.
  2. Every subsequent filesystem operation — creating `.part`, writing, fsyncing,
     reserving the final name, renaming, deleting, listing, and opening for read
     on the download path — goes through that `Dir` and takes a
     **single-component name**. `cap_std::fs::Dir` rejects any name containing
     `/`, `\`, a drive prefix, or a `..` component, and opens with `O_NOFOLLOW`,
     so a symlink at the target is an error rather than a redirect. There is no
     code path that constructs a path string.
  3. Names are validated by `validate_name` (below) before they reach step 2.
     Validation is about *legality and OS traps*, not about traversal — traversal
     is already impossible.

- **Every path the receiver opens is validated, not just the one it renames to.**
  A transfer touches four names, and `validate_name` is applied to the base; the
  other three are derived so that validity is preserved by construction:

  | Name | Derivation | How it is opened |
  |------|------------|------------------|
  | `base` | `validate_name(&init.name)?` — NFC-normalised, ≤ 250 bytes | never opened directly |
  | `part` | `format!("{base}.part")` — ≤ 255 bytes because `base` is capped at 250 | `incoming.open_with(&part, OpenOptions::new().write(true).create_new(true))` — `O_CREAT\|O_EXCL\|O_NOFOLLOW`, mode 0600 |
  | `final` | `base`, or `<stem> (N)<ext>` for the first free `N` in 1…999 | reserved atomically with `incoming.open_with(&final, create_new(true))`, then closed |
  | — | rename | `incoming.rename(&part, &incoming, &final)` — replace-existing semantics (POSIX `rename(2)` / Windows `MOVEFILE_REPLACE_EXISTING`), safe because `final` was reserved by this transfer one step earlier |

  On `EEXIST` for `part`, the receiver retries with `<stem> (N)<ext>.part` for `N`
  in 1…999 and then fails the transfer with `ERROR {code:"name_in_use"}`. On any
  failure after the `.part` file exists — SHA-256 mismatch, `CANCEL`, disconnect
  without resume, or shutdown — it is removed with
  `incoming.remove_file(&part)`, so partial corrupt files never appear in the
  user's Downloads.

- **`validate_name` (applied to every client-supplied name, in both directions).**
  Returns `FileTransferError::BadName` on the first failing rule. Order matters:
  normalisation happens first so every later rule sees the form that will actually
  hit the filesystem.

  1. **Normalise** to Unicode **NFC**
     (`unicode-normalization::UnicodeNormalization::nfc`). The NFC form is the
     name used from here on — the client's original spelling is never used to open
     anything. This closes the pair `"a\u{0301}.txt"` / `"á.txt"` resolving to one
     file on macOS (HFS+/APFS normalise) and two on Linux.
  2. **Length.** 1 ≤ `len()` ≤ **250 bytes** UTF-8 **and** ≤ 250 UTF-16 code
     units. 250, not 255, so `<name>.part` still fits every filesystem's 255-unit
     component limit.
  3. **Byte classes.** Reject any `U+0000`–`U+001F`, `U+007F`, or any of
     `< > : " / \ | ? *`.
  4. **Unicode traps.** Reject any of `U+200B`–`U+200F`, `U+202A`–`U+202E`,
     `U+2066`–`U+2069`, `U+FEFF` (zero-width and bidirectional overrides — the
     `report<RLO>gnp.exe` extension-spoofing family), and any unassigned or
     private-use-area code point.
  5. **Whole-name traps.** Reject `"."` and `".."`. Reject a leading or trailing
     ASCII space. Reject a trailing `.` (any number).
  6. **Windows reserved device names**, case-insensitively, comparing the portion
     of the name **before the first `.`** (so `CON.txt` and `con.tar.gz` are both
     rejected): `CON`, `PRN`, `AUX`, `NUL`, `COM0`–`COM9`, `LPT0`–`LPT9`,
     `CONIN$`, `CONOUT$`, plus the superscript forms
     `COM¹ COM² COM³ LPT¹ LPT² LPT³`. Enforced on **every** OS, not only Windows —
     a Linux host must not accept a name that will be unusable when the folder is
     later shared to a Windows machine, and consistency across hosts is worth more
     than the handful of legal names it costs.
  7. **Alternate data streams.** Rule 3 already rejects `:`, which covers
     `file.txt:evil`.

  **Long paths (Windows).** Per-file opens are directory-relative, so the
  260-character `MAX_PATH` limit applies only to `incoming_dir` / `outgoing_dir`
  themselves, resolved once at startup. The host binary ships a manifest with
  `longPathAware` set and prefixes the resolved folder paths with `\\?\`; a
  configured folder that cannot be opened that way fails startup with a clear
  error rather than silently truncating.

- **Read-path guard (download direction).** The host performs all of the following
  before opening any file for reading, and the list is exhaustive:
  1. `let name = validate_name(&req.name)?;` — the same function, the same rules,
     on the same NFC form. A client-supplied name is never used unvalidated in
     either direction.
  2. **Membership.** Recompute `list_outgoing()` and reject with
     `ERROR {code:"not_found"}` unless `name` is present in that fresh result. The
     listing is the authorization decision, so a file that was removed between
     `LIST_RESPONSE` and `GET_REQUEST` cannot be fetched, and a name the host
     never advertised cannot be fetched at all. Do not cache the listing for this
     check.
  3. `let f = outgoing.open_with(&name, OpenOptions::new().read(true))?;` —
     through the `Dir` handle opened at startup, with `O_NOFOLLOW`. A symlink at
     that name is an error, not a redirect out of the folder.
  4. `f.metadata()?.is_file()` must hold — reject directories, FIFOs, devices and
     sockets with `ERROR {code:"not_found"}` (same code as a miss, so the error
     does not distinguish "exists but is not a regular file" from "does not
     exist").
  5. `size` in the `INIT` the host then sends is the size from that already-open
     handle, never a re-stat of the name. `max_file_bytes` is checked against that
     size here — before any `INIT` is sent and before any byte is read — and over
     cap is `ERROR {code:"too_large"}` (see "Size limits").

  `list_outgoing()` itself uses the same `Dir` and skips every entry that is not a
  regular file, so a symlink in the Outgoing folder is never advertised in the
  first place.

- **Size limits.** `[filetransfer] max_file_bytes` (default `0` = unlimited) is
  enforced on both directions. **Upload:** an `INIT` whose declared `size` exceeds
  it is refused immediately with `ERROR {code:"too_large"}`, and the receiver
  additionally aborts the moment bytes actually written exceed it (a sender may
  lie in `INIT`). **Download:** `GET_REQUEST` is `{name}` and declares no size, so
  the cap is checked against the size read from the already-open handle at
  read-path-guard step 5, before any `INIT` is sent and before any byte is read —
  over cap is `ERROR {code:"too_large"}`. `PayloadLen` caps — 64 KiB for `CHUNK`,
  256 KiB for a JSON control message — are enforced before allocation, as already
  specified under "Wire framing".

- **Controller-only.** Only the authenticated controller may open a file-transfer
  stream or send `INIT` / `LIST_REQUEST` / `GET_REQUEST`; viewers and co-op
  players cannot (MODULE_SERVER "Role gate table" rows 8-9). File-transfer streams
  reuse the main session's auth (session token), validated at connect.
- **Integrity.** Every transfer is verified by an end-to-end SHA-256; a mismatch
  discards the received file.
- **Per-chunk CRC32C** catches corruption early (before the full-file hash).
- **Resource caps.** `max_file_bytes`, `max_concurrent` (overflow is FIFO-queued,
  depth `queue_depth`), and QUIC's per-stream flow-control window bound memory and disk
  usage; reject transfers that would exceed them. (There is no app-level
  unacked-chunk window — see "Chunk size, back-pressure & resume".)
- **No execution.** Uploaded files are written, never executed or opened by the
  host. The fixed folder should not be an auto-run / startup location.
- **TLS.** File-transfer streams inherit the main session's TLS 1.3 channel (mandatory under QUIC).

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `validate_name` rejection matrix: traversal, absolute, NUL/control, Windows-invalid characters, reserved names with and without extensions, trailing dot/space, bidi controls, over-length, and the NFC round-trip | No |
| Unit | A symlink planted at `<name>`, at `<name>.part`, and at the reserved final name each cause the transfer to fail with `BadName`/`Io`, never a write outside the folder | No |
| Unit | `GET_REQUEST` for a name absent from `list_outgoing()`, for a symlink inside Outgoing, and for `../../etc/passwd` all return `ERROR {code:"not_found"}` with no file opened | No |
| Unit | Chunk framing encode/decode, CRC32C verify | No |
| Unit | Resume checkpoint: `ACK` advertises `lastFsyncSeq` (not `lastWrittenSeq`) | No |
| Unit | SHA-256 mismatch → file discarded | No |
| Integration | Drop file → lands in Incoming, hash matches | Yes |
| Integration | Download from Outgoing → client receives, hash matches | Yes |
| Integration | Resume after mid-transfer disconnect | Yes |
| Integration | Concurrent transfers respect max_concurrent | Yes |
| Load | Large transfer does NOT raise video frame latency (separate socket) | Yes |

---

## Status

📋 **Specced — not yet implemented.** Core module. Drag-and-drop upload + file
list download, fixed folders, multiplexed on the main WebTransport session. Compression and a
full remote file browser are explicitly out of scope for v1.
