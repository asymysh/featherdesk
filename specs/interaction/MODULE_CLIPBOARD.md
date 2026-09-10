# Module Spec: Clipboard

## Overview

The Clipboard module provides **bidirectional clipboard synchronization** between
the browser client and the remote host. It is **core** (not an add-on) — clipboard
sync is a baseline remote-desktop expectation and the OS clipboard APIs are small
enough to live in core.

**Backend (locked): the [`arboard`](https://crates.io/crates/arboard) crate
(MIT/Apache).** `arboard` is a single cross-platform clipboard library covering
Windows / macOS / Linux (X11 **and** Wayland), and it is the core's clipboard
read/write engine for **text + image**. It was chosen explicitly to **AVOID
RustDesk's `libs/clipboard`, which is AGPL** and would force the whole host to
AGPL. `arboard` does not expose every format we need, so a **thin per-OS layer on
top of `arboard`** (behind `cfg(target_os)`) adds the two things it lacks:
**change-notification** and the **rich-HTML / file-list** formats. This replaces
the hand-rolled per-OS backend (`AddClipboardFormatListener` / XFixes /
`NSPasteboard`) as the *primary* engine — those APIs now survive only inside the
thin layer, for the narrow jobs `arboard` doesn't do.

**Scope (v1):**
- **Plain text** (`text/plain`, UTF-8).
- **Rich text** (`text/html`), sanitized in both directions.

**Out of scope (not supported):**
- Image clipboard (`image/png`) — deferred to a later iteration, not in v1.
- File clipboard (copy a file in Explorer/Finder, paste in the remote). Browser
  clipboard APIs cannot read OS file paths; this is fundamentally blocked. Use
  drag-and-drop file transfer instead (see [`MODULE_FILETRANSFER.md`](./MODULE_FILETRANSFER.md)).

**Security posture:** clipboard sync is **opt-in** and **direction-controlled**.
The clipboard frequently carries secrets (passwords, tokens, keys), so the
default is conservative.

---

## Public Interface

```rust
// crate: featherdesk-clipboard

/// A running clipboard monitor is split in two, because its two jobs have
/// incompatible lifetimes: one long-lived driver that owns the OS connection for
/// the whole session, and a short mutating write that N session tasks must be
/// able to make WHILE the driver is running. One `&mut self` cannot serve both —
/// the only arrangement that compiles around a single trait holds a lock across
/// the driver's await and wedges client→host clipboard for the session.
///
/// `spawn` starts the driver and returns the only handle anything else holds.
/// The actual text/image read+write goes through the **`arboard`** crate; a thin
/// per-OS layer (behind `cfg(target_os)`) adds change-notification and the
/// rich-HTML / file-list formats `arboard` does not expose. No add-on shared
/// library — clipboard is core, compiled unconditionally per target.
/// Cleanup is RAII (`Drop`) — no Close(). Returns
/// Err(ClipboardError::UnsupportedCompositor) on Linux running a Wayland
/// compositor without wlr-data-control (GNOME Mutter, KDE KWin), so the server can
/// warn at startup and disable clipboard cleanly instead of failing silently at
/// first copy.
pub fn spawn(
    cfg: Config,
    cancel: CancellationToken,
) -> Result<(ClipboardHandle, Box<dyn Monitor>), ClipboardError>;

/// Monitor is the DRIVER half: the per-OS watch loop. It is moved onto the
/// pipeline's clipboard task and never shared, so it needs no `Sync` and no
/// interior mutability. It owns the receiving ends of the channels `spawn`
/// created, and performs every OS clipboard write itself, on the thread that owns
/// the OS connection (the Windows message-only window's thread / the X11
/// selection-owner thread).
#[async_trait::async_trait]
pub trait Monitor: Send {
    /// Runs until `cancel` fires: watches for host clipboard changes and
    /// publishes them, and serves queued `set` requests. Consumes the driver —
    /// it is the sole owner of the OS clipboard connection for the session, which
    /// is exactly what lets `ClipboardHandle::set` take `&self`.
    ///
    /// **Echo suppression (mandatory).** The change detector cannot distinguish
    /// the driver's own write from a human copy — `WM_CLIPBOARDUPDATE`, an XFixes
    /// `SelectionNotify` and a bumped `NSPasteboard.changeCount` all fire for it —
    /// so without suppression a client copy round-trips back to the client that
    /// sent it, and on the `clipboardchange` path (Chrome/Edge) that loop never
    /// terminates. Before every OS write the driver records
    /// `last_written = blake3(format_byte ‖ text ‖ html)` (the bytes it is about
    /// to place, after the format filter, so a downgraded HTML→text write is
    /// matched by its downgraded form); on every observed change it computes the
    /// same hash and, if it equals `last_written`, **drops the change and clears
    /// the slot** instead of publishing it. The slot holds exactly one hash and is
    /// cleared on the first match, so a human re-copying the same content
    /// immediately afterwards still propagates. Hashing, not event identity, is
    /// what makes this work on macOS, whose only detector is a `changeCount` poll.
    async fn run(self: Box<Self>, cancel: CancellationToken) -> Result<(), ClipboardError>;
}

/// ClipboardHandle is the SHARED half: cheap to clone, `Send + Sync`, and safe to
/// call from any number of session tasks concurrently with the running driver. It
/// owns no OS state — every method is a message to the driver.
#[derive(Clone)]
pub struct ClipboardHandle { /* … */ }

impl ClipboardHandle {
    /// Queues a client→host clipboard write. `&self`, non-blocking, and it never
    /// performs an OS call on the caller's thread: the driver does the write on
    /// the thread that owns the OS clipboard connection.
    ///
    /// All-or-nothing at the driver: on partial failure (e.g. `arboard` text set
    /// succeeds but the thin CF_HTML layer fails on Windows) the driver MUST
    /// clear the OS clipboard so it does not end up half-set, then report the
    /// original error.
    ///
    /// Returns `ClipboardError::Busy` when the pending-write queue (16, see
    /// CENTRAL_SPEC "Queues and buffers") is full — a session that pastes faster
    /// than the OS can accept is dropped, never blocked.
    pub fn set(&self, c: Content) -> Result<(), ClipboardError>;

    /// Host clipboard changes, size-checked against `[clipboard] max_bytes` and
    /// echo-suppressed by the driver, but **NOT sanitized** — sanitization is the
    /// server's, at the single call site in `send_clipboard` (see
    /// "Sanitization"). One owner per direction, and it is the same owner.
    ///
    /// A `watch` receiver, latest-wins: cloneable, so each consumer takes its own
    /// ONCE and never re-fetches inside a loop, and a superseded copy is not a
    /// lost message — the OS clipboard holds exactly one payload.
    pub fn changes(&self) -> tokio::sync::watch::Receiver<Option<Content>>;

    /// Applies a `[clipboard]` hot reload (direction, formats, max_bytes). Called
    /// by the pipeline's `fd-config` applier task (`pipeline::config_applier`),
    /// which builds the `clipboard::Config` from those three reloaded keys and
    /// passes it here — this takes a `clipboard::Config`, never a config section.
    /// `enabled` is restart-required and is not part of the reload.
    pub fn set_config(&self, cfg: Config);
}

/// probe reports whether the host can support clipboard sync today (correct
/// Wayland compositor, X display reachable, etc.). The server uses it at startup
/// to surface the unsupported state.
pub fn probe() -> Result<(), ClipboardError>;

/// The single HTML sanitizer for both clipboard directions. Pure, allocation-only,
/// no I/O. Exported by featherdesk-clipboard so there is exactly one implementation;
/// called by featherdesk-host::server in `send_clipboard` (H→C) and in
/// `clipboardReader` (C→H). The driver does NOT sanitize — neither the changes it
/// publishes nor the writes it performs — so there is no direction in which two
/// owners can each assume the other did it. See "Sanitization" for the allowlist.
pub fn sanitize_html(input: &str) -> String;

/// ClipboardError — stable error enum for the clipboard crate (replaces Go sentinels).
#[derive(thiserror::Error, Debug)]
pub enum ClipboardError {
    #[error("clipboard: Wayland compositor lacks wlr-data-control")]
    UnsupportedCompositor, // was ErrUnsupportedCompositor
    #[error("clipboard: payload exceeds max_bytes cap")]
    TooLarge,
    #[error("clipboard: pending-write queue full")]
    Busy,
    #[error(transparent)]
    Backend(#[from] arboard::Error),
}

/// Content is one clipboard payload. Exactly one of text/html is the primary;
/// HTML may carry a plain-text fallback for non-HTML paste targets.
pub struct Content {
    pub format: Format, // Text or Html
    pub text: String,   // UTF-8 plain text (always set; HTML carries its text fallback here)
    pub html: String,   // raw HTML as read from the OS or the wire; sanitized by the
                        // server before it crosses in either direction
}

#[repr(u8)]
pub enum Format {
    Unknown = 0, // zero value; treated as invalid (sanity guard)
    Text,        // text/plain
    Html,        // text/html (+ text fallback)
}

/// Config controls clipboard behavior. Sourced from [clipboard] TOML section.
/// (Logging is via the global `tracing` subscriber — no per-monitor logger handle.)
pub struct Config {
    pub enabled: bool,
    pub direction: Direction,
    pub max_bytes: usize,     // content cap per payload (default 1 MiB, range 1 KiB … 4 MiB)
    pub formats: Vec<Format>, // subset of {Text, Html} to sync; see "Format filter behavior"
}

#[repr(u8)]
pub enum Direction {
    Disabled = 0,   // no sync
    Bidirectional,  // host <-> client
    ClientToHost,   // client copies, host pastes only
    HostToClient,   // host copies, client pastes only
}
```

---

## Host-Side Clipboard Access (per OS)

All three are **core code**, not add-ons. **`arboard` does the text + image read
and write on every OS**; the per-OS columns below are the **thin layer on top of
`arboard`** that supplies what `arboard` lacks — change-notification, and the
rich-HTML / file-list formats. The base text/image path is identical everywhere
(one `arboard::Clipboard` handle), so only these extras are per-OS.

| OS | Change detection (thin layer) | Read — text+image | Read — rich HTML / file list (thin layer) | Write |
|----|------------------|------|------|-------|
| **Windows** | `AddClipboardFormatListener(hwnd)` → `WM_CLIPBOARDUPDATE` (event-driven) | `arboard` (`get_text` / `get_image`) | `GetClipboardData(CF_HTML)` for rich HTML; `CF_HDROP` for file list | `arboard` (`set_text` / `set_image`); CF_HTML via the thin serializer |
| **Linux (X11)** | `XFixesSelectSelectionInput` + `XFixesSelectionNotify` (event-driven) | `arboard` (X11 backend) | `XConvertSelection` target `text/html`; `text/uri-list` for files | `arboard`; own the `CLIPBOARD` selection for the HTML/URI targets |
| **Linux (Wayland)** | `wlr-data-control-unstable-v1` protocol — **wlroots-family only** | `arboard` (Wayland backend) | data-control offer for `text/html` / `text/uri-list` | `arboard`; data-control source for the extra MIME types |
| **macOS** | **No notification API** — poll `NSPasteboard.changeCount` every 300 ms | `arboard` (`get_text` / `get_image`) | `NSPasteboardTypeHTML`; `NSFilenamesPboardType` for file list | `arboard`; `NSPasteboardTypeHTML` via the thin layer |

> `arboard` covers **text + image** on all three OSes with one API. **Rich-HTML
> and file-list paste** are NOT in `arboard`, so the thin per-OS layer above
> handles exactly those (and change-notification, which `arboard` also does not
> provide). Image clipboard is still out of scope for v1 (see above) even though
> `arboard` supports it — the wire format and sanitization are unchanged.

> The macOS polling interval (300 ms) matches what every macOS remote-desktop
> tool does (Sunshine, Parsec). Polling is cheap (`changeCount` is an integer
> compare); the actual read only happens when the count changes.

### Wayland support matrix (honest)

The clipboard module is **not universally supported on Wayland**. There is no
stable `xdg-desktop-portal` clipboard interface today; the only widely-deployed
clipboard protocol is `wlr-data-control-unstable-v1`, which is wlroots-only.

| Compositor | Supported? | Reason |
|------------|------------|--------|
| Sway, Hyprland, river, Wayfire, Cage (wlroots) | ✅ | implements `zwlr_data_control_manager_v1` |
| GNOME Mutter | ❌ | does not implement `wlr-data-control` |
| KDE KWin | ❌ | does not implement `wlr-data-control` |
| X11 (any DE) | ✅ | XFixes path is universal |

On unsupported compositors `spawn` returns
`Err(ClipboardError::UnsupportedCompositor)`; the server logs a clear warning at
startup and clipboard sync is silently disabled (no spurious errors at first
copy). A future portal-based path can be added when one ships.

> **`arboard` already handles the base X11/Wayland text+image path**, including
> the X11 selection-owner plumbing for plain text/image. The notes below apply to
> the **thin layer's extras only** (change-notification + the rich-HTML / file-list
> MIME types `arboard` does not surface).

### Implementation notes (the hidden costs)

- **Windows.** `AddClipboardFormatListener` requires an `HWND`. The thin layer
  creates a **message-only window** (`HWND_MESSAGE` parent) and runs a
  `GetMessage`/`TranslateMessage`/`DispatchMessage` pump on the **same OS thread
  that created the window** — i.e. on a dedicated thread (`std::thread::spawn`,
  not a Tokio worker, since the pump blocks). That thread is also where the driver
  executes every queued `set`, so reads and writes share one OS connection and one
  thread affinity. Shutdown posts `WM_QUIT`/`PostQuitMessage` to that thread. (The
  text/image read+write itself is just `arboard`.)
- **X11.** For the rich-HTML / file-list targets that `arboard` does not serve,
  the thin layer owns a dedicated `XOpenDisplay` connection on its own thread,
  which is likewise where queued `set` requests are executed. The
  same connection MUST stay alive to serve `XConvertSelection` requests for those
  targets when this process owns the `CLIPBOARD` selection — if the thread dies,
  every paste of those targets in every other X11 app fails. (`arboard` owns the
  plain text/image targets independently.)
- **X11 INCR protocol (mandatory at this size).** A selection larger than the
  server's maximum request size (`XMaxRequestSize`, often ~256 KiB) CANNOT be
  transferred in one `XConvertSelection`. Since `[clipboard] max_bytes` is 1 MiB by
default and may be raised to 4 MiB,
  the X11 HTML/file-list path MUST implement the **INCR** protocol in BOTH roles:
  when reading a large selection (requestor receives the `INCR` target, then loops
  on `PropertyNotify` reading chunks until a zero-length property) and when serving
  one (owner advertises `INCR`, then writes chunks on each `PropertyNotify`
  delete). Skipping INCR silently truncates large pastes — a common bug.
- **Wayland (wlr-data-control).** For the extra MIME types, use Rust Wayland
  protocol bindings (e.g. `wayland-client` + generated `wlr-data-control`
  glue); `wl-paste --watch` subprocess is intentionally NOT used as a fallback
  because it relies on the same `wlr-data-control` and offers no additional
  compositor coverage.

### Windows `CF_HTML` format quirk

Windows clipboard HTML is NOT raw HTML. `CF_HTML` is a UTF-8 payload with an
ASCII offset header:

```
Version:0.9
StartHTML:0000000105
EndHTML:0000000245
StartFragment:0000000141
EndFragment:0000000209
<html><body><!--StartFragment-->…<!--EndFragment--></body></html>
```

**Each offset field is exactly 10 ASCII decimal digits, zero-padded** so the
header byte-length is fixed and the offsets can be patched in place after the
fragment is serialized. Implementations using `{}` instead of `{:010}` (or
`format!`/`write!` without zero-pad width) will produce a header whose own length
changes when offsets grow, corrupting the offsets it just wrote. The thin Windows
layer MUST use a small dedicated serializer for this format (this is one of the
rich-HTML jobs `arboard` does not do).

---

## Wire Protocol

Clipboard is **low-rate and human-triggered**, so it uses JSON — but it rides its
own dedicated **clipboard stream** (StreamType tag `0x02`), which the
**controller** opens immediately after `auth_ok` whenever the `config` message
advertises `clipboard != "disabled"`. It is opened eagerly, not on first use: a
host→client push must have somewhere to land before the client has copied
anything. It is NOT on the control stream because a clipboard payload can reach
1 MiB, far past the 4 KiB control-line cap.

**Framing is symmetric:** `[u32 Len LE][JSON]` in BOTH directions on the
clipboard stream. (The retired binary `FrameTypeClipboard = 12` frame is gone —
see [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) "Frame Types".)

> **`Len` cap (mandatory, checked before allocation).** `Len` is a `u32` and can
> name 4 GiB, so the reader MUST reject an oversized frame **before** allocating
> the payload buffer — otherwise a peer requests a 4 GiB allocation per message.
> The bound, and what a violation does to the stream, are in "Size Limits" below.
> This is the same rule the file-transfer reader applies to `PayloadLen`
> ([`MODULE_FILETRANSFER.md`](./MODULE_FILETRANSFER.md) "PayloadLen cap").

**Client → host**
```json
{"type": "clipboard", "format": "text/plain", "text": "hello"}
{"type": "clipboard", "format": "text/html", "html": "<b>hi</b>", "text": "hi"}
```

**Host → client** (same JSON shape, same stream)
```json
{"type": "clipboard", "format": "text/html", "html": "<b>hi</b>", "text": "hi"}
```

Both directions are gated by `[clipboard] direction`; the exact ordered gate
list, and what a rejection does, is [`CENTRAL_SPEC.md`](../CENTRAL_SPEC.md)
"Contract 9". Every gate lives in the server — `send_clipboard` for H→C, the
`clipboardReader` for C→H — never in the caller and never in this module.

---

## Browser-Side Behavior (gesture constraints)

The browser Clipboard API has strict user-activation rules that differ per
engine. The client handles each:

| Action | Chrome / Edge | Firefox / Safari |
|--------|---------------|------------------|
| Read clipboard (client → host) | `clipboard-read` permission, granted once → silent reads on `clipboardchange` event | requires user gesture → read inside the `paste` (Ctrl+V) event handler only |
| Write clipboard (host → client) | `clipboard-write` permission → silent `writeText`/`write` | requires user gesture → buffer host content; write it during the next Ctrl+V/user action |

**Client strategy:**
- **Chrome/Edge:** request `clipboard-read` + `clipboard-write` on connect. Use
  the `clipboardchange` event (Chrome 124+) to push client copies; write host
  copies silently.
- **Firefox/Safari:** intercept the `copy` and `paste` events in the remote
  session. On `copy`, read `event.clipboardData` and forward to host. On `paste`,
  inject the buffered host clipboard content. No background polling (it triggers
  repeated permission prompts).

---

## Sanitization

HTML from either side is untrusted: a malicious host process can put hostile
markup on the host clipboard, and a client is by definition outside the trust
boundary. Both directions are sanitized **on the host**, by one function, at
exactly two call sites — `Server::send_clipboard` (H→C) and the server's
`clipboardReader` (C→H). `clipboard::sanitize_html` is declared in the Public
Interface above.

**Algorithm.** `ammonia::Builder` configured exactly as below, applied to the
HTML fragment (on Windows, after stripping the `CF_HTML` wrapper and before
re-serializing it). `ammonia` is an allowlist sanitizer built on `html5ever`: it
parses to a DOM with the same algorithm a browser uses, then emits only nodes and
attributes present in the allowlist. Everything else — including every `on*`
handler, `style`, `srcdoc`, `<script>`, `<iframe>`, `<object>`, `<embed>`,
`<form>`, `<base>`, `<meta>`, `<link>`, comments, processing instructions, and
any tag or attribute invented after this spec — is removed because it was never
allowed, not because it was listed.

| Setting | Value |
|---------|-------|
| `tags` | `a abbr b blockquote br code dd del div dl dt em h1 h2 h3 h4 h5 h6 hr i ins kbd li ol p pre q s samp small span strike strong sub sup table tbody td tfoot th thead tr u ul` |
| `generic_attributes` | **empty** (no `class`, no `id`, no `style`, no `title` globally) |
| `tag_attributes` | `a` → `href`, `title`; `td` → `colspan`, `rowspan`; `th` → `colspan`, `rowspan`, `scope`; `ol` → `start`; nothing else |
| `url_schemes` | `http`, `https`, `mailto` (so `javascript:`, `data:`, `vbscript:`, `file:` are dropped with the attribute) |
| `url_relative` | `Deny` — a relative URL has no meaning on the far side |
| `link_rel` | `Some("noopener noreferrer nofollow")` |
| `strip_comments` | `true` |
| `id_prefix` | not used — `id` is not allowed at all |
| `clean_content_tags` | `script`, `style`, `title`, `iframe`, `object`, `embed`, `form`, `base`, `meta`, `link` — their **text content** is discarded too, not re-emitted as visible text |

**Non-goals.** The sanitizer does not attempt to preserve visual fidelity, does
not re-write URLs, and does not validate that the surviving markup renders
identically. A clipboard payload that loses formatting is correct behaviour; a
payload that carries script is not.

**Text is not sanitized** — `text/plain` is placed on the clipboard verbatim
(after the size cap). It is never interpreted as markup by either side.

---

## Size Limits

- **Content cap.** `[clipboard] max_bytes` (default **1 MiB**, range 1 KiB – 4 MiB)
  bounds `text.len() + html.len()` in UTF-8 bytes, in both directions, measured
  **after** sanitization and after the format filter.
- **Wire cap (mandatory, checked before allocation).** The clipboard stream's
  `[u32 Len]` prefix is a `u32` and can name 4 GiB. The reader MUST reject an
  oversized frame **before** allocating the payload buffer:
  `Len > 6 × max_bytes + 1024` → `cancel_read` +
  `cancel_write(close::PROTOCOL_ERROR)` on **that stream** and no allocation.
  (`6 ×` is the genuine worst case, not headroom: JSON escapes a control byte
  as `\u00XX`, six bytes out for one byte in, so a payload at the content cap
  made entirely of control bytes frames to exactly six times its size. `2 ×`
  would reject a legal payload before its own content cap did. `+ 1024` covers
  the envelope. With the 4 MiB ceiling on `max_bytes` the worst-case buffer a
  peer can force is 24 MiB + 1 KiB — larger, but still bounded and still
  checked before a single byte is allocated.) This mirrors the
  file-transfer module's "`PayloadLen` cap (mandatory, checked before
  allocation)".
- **Over-cap content** is **truncated** for text (on a UTF-8 character boundary,
  with a logged warning giving the byte length only) or **dropped** for HTML
  (HTML truncation produces invalid markup). Dropping is silent on the wire and
  counted as `featherdesk_clipboard_drops_total{reason="too_large"}`.
- The content cap is enforced on the **sending** side before framing and again on
  the **receiving** side before writing to the OS clipboard — a sender that
  ignores it does not get to write an oversized payload.

### Format filter behavior

`[clipboard] formats` is a subset of the supported formats, landing in
`Config::formats` as a `Vec<Format>`. If `"html"` is **not** in the configured
list, HTML payloads are **downgraded to their `Text` fallback** before being
placed on the OS clipboard or sent on the wire (both directions) — and the
downgraded form is what the driver hashes into `last_written`, so the echo of a
downgrade is suppressed rather than reflected back over the client's own richer
copy. This lets operators allow plain text only without rejecting copies that
happen to carry HTML.

---

## Configuration

```toml
[clipboard]
enabled    = false              # opt-in. Default OFF (clipboard carries secrets).
direction  = "bidirectional"    # "bidirectional" | "client_to_host" | "host_to_client" | "disabled"
max_bytes  = 1048576            # 1 MiB cap per payload (range 1024 … 4194304)
formats    = ["text", "html"]   # subset of supported formats to sync
```

---

## Internal Architecture

```
Host clipboard change (WM_CLIPBOARDUPDATE / XFixes / changeCount poll — thin layer)
    → driver reads via arboard (+ thin layer for rich HTML) + size-checks
    → echo check: blake3 == last_written? → drop, clear the slot, stop here
    → changes() watch channel (latest-wins) → taken ONCE by the PIPELINE's clipboard task
    → server.send_clipboard(content)   (MODULE_SERVER trait; MODULE_PIPELINE step 12/13)
    → SERVER gates: enabled → direction → role==Control → stream-open → format filter
      → clipboard::sanitize_html → size cap   (CENTRAL_SPEC Contract 9)
    → server writes [u32 Len][JSON] on the clipboard stream to the controller

Client copy (clipboardchange / copy event)
    → navigator.clipboard.read() / event.clipboardData
    → [u32 Len][JSON {"type":"clipboard",...}] on the clipboard stream
    → SERVER gates: length cap before allocation → direction → format filter
      → clipboard::sanitize_html → content cap   (CENTRAL_SPEC Contract 9)
    → set_clipboard_callback → ClipboardHandle::set() → driver records last_written
    → arboard (+ thin layer) → OS clipboard
```

**Viewer isolation (absolute, not configurable).** Only the **controller**
participates in clipboard sync — in either direction, under every value of
`direction`. Viewers and co-op `player` clients never receive a host clipboard
push and may not open the clipboard stream at all
([`MODULE_SERVER.md`](../core/MODULE_SERVER.md) "Role gate table" rows 5-7). This
is not a default and there is no key that relaxes it: the clipboard routinely
carries passwords and tokens, and a passive viewer is exactly the principal that
must not see them.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `CF_HTML` header parse/serialize round-trip | No |
| Unit | HTML sanitizer allowlist: `<script>`/`<iframe>`/`on*`/`style`/`javascript:`/`data:`/relative URLs are removed, `<b>`/`<a href=https>`/`<table>` survive, `<script>` text content is not re-emitted, and the same function is applied on both directions | No |
| Unit | Size cap truncation (text, on a UTF-8 boundary) + rejection (HTML), **and** a `[u32 Len]` prefix above the wire cap resets the stream without allocating | No |
| Unit | Echo suppression: a `set()` followed by a synthetic change event carrying the same bytes emits nothing on `changes()`, the slot is cleared, and a second identical human copy immediately after DOES emit | No |
| Integration | Clipboard C→H while the driver is running: with `Monitor::run` awaiting, a `set` from a session task reaches the OS clipboard (regression guard for the guard-held-across-await deadlock) | Yes |
| Unit | Direction gating (all 4 directions × both paths) | No |
| Integration | Host change → client receives (per OS) | Yes |
| Integration | Client copy → host clipboard (per OS) | Yes |
| Integration | macOS changeCount polling detects change ≤ 300 ms | Yes (macOS) |

---

## Security Considerations

- **Opt-in default.** `enabled = false`. Sync must be explicitly turned on.
- **Direction control.** Operators can restrict to one-way.
- **Secrets in transit.** Already protected by mandatory TLS; no extra encryption.
- **No persistence.** Clipboard content is processed in memory only — never
  written to disk or logs (log lengths, never contents).
- **Bidirectional sanitization, one owner.** Both directions are sanitized on the
  host by `clipboard::sanitize_html`, called by the server — H→C because the host
  process is not trusted to put safe markup on its own clipboard, C→H because the
  client is outside the trust boundary. The browser's own `clipboard.read()`
  sanitization is not relied on: it is on the far side of the boundary and is not
  invoked on the `event.clipboardData` path this module specifies for
  Firefox/Safari.
- **No echo loop.** The driver hashes what it is about to write and drops the
  change event its own write provokes, so a client copy is never reflected back
  to the client that sent it (see `Monitor::run`). Without it the Chrome/Edge
  `clipboardchange` path loops indefinitely at up to `max_bytes` per hop on a
  reliable stream that shares congestion control with the video path.
- **Viewer isolation.** The host clipboard is pushed only to the controller
  session — never to viewers or co-op players, under any `direction` setting.
  Non-controller sessions cannot open the clipboard stream (`0x02`).

---

## Status

📋 **Specced — not yet implemented.** Core module, built on the `arboard` crate
(text+image, all OSes) with a thin per-OS layer for change-notification and
rich-HTML / file-list. `arboard` is MIT/Apache, chosen to avoid RustDesk's AGPL
`libs/clipboard`. Text first, rich HTML second. Image + file clipboard explicitly
out of scope for v1.
