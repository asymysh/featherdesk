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

/// Monitor watches the host OS clipboard and reports changes. The actual
/// text/image read+write goes through the **`arboard`** crate (one cross-platform
/// library); a thin per-OS layer (behind `cfg(target_os)`) adds change-notification
/// and the rich-HTML / file-list formats `arboard` does not expose. No add-on
/// shared library — clipboard is core, compiled unconditionally per target.
/// Cleanup is RAII (`Drop`) — no Close().
pub trait Monitor {
    /// start begins watching. Changes are delivered on the channel returned by
    /// `changes()`. The future resolves when the cancellation token fires.
    async fn start(&mut self, cancel: CancellationToken) -> Result<(), ClipboardError>;

    /// changes yields host clipboard updates (already size-checked + sanitized).
    fn changes(&self) -> tokio::sync::mpsc::Receiver<Content>;

    /// set writes content to the host OS clipboard (client → host direction).
    /// All-or-nothing: on partial failure (e.g. `arboard` text set succeeds but the
    /// thin CF_HTML layer fails on Windows), the implementation MUST clear the OS
    /// clipboard so it does not end up in a half-set state, then return the
    /// original error.
    fn set(&mut self, c: Content) -> Result<(), ClipboardError>;
}

/// new_monitor constructs a per-OS Monitor over `arboard`. Returns
/// Err(ClipboardError::UnsupportedCompositor) on Linux running a Wayland
/// compositor without wlr-data-control (GNOME Mutter, KDE KWin), so the server can
/// warn at startup and disable clipboard cleanly instead of failing silently at
/// first copy.
pub fn new_monitor(cfg: Config) -> Result<Box<dyn Monitor>, ClipboardError>;

/// probe reports whether the host can support clipboard sync today (correct
/// Wayland compositor, X display reachable, etc.). Server uses this at startup
/// to surface the unsupported state.
pub fn probe() -> Result<(), ClipboardError>;

/// ClipboardError — stable error enum for the clipboard crate (replaces Go sentinels).
#[derive(thiserror::Error, Debug)]
pub enum ClipboardError {
    #[error("clipboard: Wayland compositor lacks wlr-data-control")]
    UnsupportedCompositor, // was ErrUnsupportedCompositor
    #[error("clipboard: payload exceeds max_bytes cap")]
    TooLarge,
    #[error(transparent)]
    Backend(#[from] arboard::Error),
}

/// Content is one clipboard payload. Exactly one of text/html is the primary;
/// HTML may carry a plain-text fallback for non-HTML paste targets.
pub struct Content {
    pub format: Format, // Text or Html
    pub text: String,   // UTF-8 plain text (always set; HTML carries its text fallback here)
    pub html: String,   // sanitized HTML (set only when format == Format::Html)
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
    pub max_bytes: usize, // hard cap per payload (default 1 MiB)
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

On unsupported compositors `new_monitor` returns
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
  not a Tokio worker, since the pump blocks). Shutdown posts
  `WM_QUIT`/`PostQuitMessage` to that thread. (The text/image read+write itself is
  just `arboard`.)
- **X11.** For the rich-HTML / file-list targets that `arboard` does not serve,
  the thin layer owns a dedicated `XOpenDisplay` connection on its own thread. The
  same connection MUST stay alive to serve `XConvertSelection` requests for those
  targets when this process owns the `CLIPBOARD` selection — if the thread dies,
  every paste of those targets in every other X11 app fails. (`arboard` owns the
  plain text/image targets independently.)
- **X11 INCR protocol (mandatory at this size).** A selection larger than the
  server's maximum request size (`XMaxRequestSize`, often ~256 KiB) CANNOT be
  transferred in one `XConvertSelection`. Since `[clipboard] max_bytes` is 1 MiB,
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
own dedicated **clipboard stream** (StreamType tag `0x02`), opened on demand the
first time either side has clipboard data. It is NOT on the control stream
because a clipboard payload can reach 1 MiB, far past the 4 KiB control-line cap.

**Framing is symmetric:** `[u32 Len LE][JSON]` in BOTH directions on the
clipboard stream. (The retired binary `FrameTypeClipboard = 12` frame is gone —
see [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md) "Frame Types".)

**Client → host**
```json
{"type": "clipboard", "format": "text/plain", "text": "hello"}
{"type": "clipboard", "format": "text/html", "html": "<b>hi</b>", "text": "hi"}
```

**Host → client** (same JSON shape, same stream)
```json
{"type": "clipboard", "format": "text/html", "html": "<b>hi</b>", "text": "hi"}
```

Both directions are gated by `[clipboard] direction`. The server drops a
client→host clipboard message if direction is `host_to_client` or `disabled`,
and never sends host→client if `client_to_host` or `disabled`.

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

HTML is sanitized in **both** directions to prevent script injection through
the clipboard:

- Strip `<script>`, `<iframe>`, `<object>`, `<embed>`.
- Strip all `on*` event-handler attributes.
- Strip `javascript:` / `data:` URLs in `href`/`src`.
- Keep structural + formatting tags (`<b>`, `<i>`, `<a href=http(s)>`, `<ul>`,
  `<table>`, etc.).

The browser sanitizes on `clipboard.read()` by default; the **host** side MUST
also sanitize on the host→client path (a malicious host process could place
hostile HTML on the clipboard). Use a vetted Rust HTML sanitizer
(e.g. `ammonia` with a UGC-style allowlist policy).

---

## Size Limits

- Hard cap `[clipboard] max_bytes` (default **1 MiB**) per payload, both directions.
- Oversized content is **truncated** for text (with a logged warning) or
  **rejected** for HTML (HTML truncation produces invalid markup).
- The cap is enforced on the host side before sending and on receipt before
  writing to the OS clipboard.

### Format filter behavior

`[clipboard] formats` is a subset of the supported formats. If `"html"` is
**not** in the configured list, HTML payloads are **downgraded to their
`Text` fallback** before being placed on the OS clipboard or sent on the wire
(both directions). This lets operators allow plain text only without
rejecting copies that happen to carry HTML.

---

## Configuration

```toml
[clipboard]
enabled    = false              # opt-in. Default OFF (clipboard carries secrets).
direction  = "bidirectional"    # "bidirectional" | "client_to_host" | "host_to_client" | "disabled"
max_bytes  = 1048576            # 1 MiB cap per payload
formats    = ["text", "html"]   # subset of supported formats to sync
```

---

## Internal Architecture

```
Host clipboard change (WM_CLIPBOARDUPDATE / XFixes / changeCount poll — thin layer)
    → Monitor reads via arboard (+ thin layer for rich HTML) + size-checks + sanitizes
    → changes() mpsc channel → server
    → direction check ([clipboard] direction)
    → server writes [u32 Len][JSON] on the clipboard stream to controller client(s)

Client copy (clipboardchange / copy event)
    → navigator.clipboard.read() / event.clipboardData
    → [u32 Len][JSON {"type":"clipboard",...}] on the clipboard stream
    → server direction check
    → Monitor::set() → arboard (+ thin layer for rich HTML) → OS clipboard
```

Only the **controller** participates in clipboard sync by default; viewers do
not receive host clipboard pushes (avoids leaking host secrets to passive
viewers). This is configurable but conservative by default.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `CF_HTML` header parse/serialize round-trip | No |
| Unit | HTML sanitizer (script/iframe/on*/js-url stripping) | No |
| Unit | Size cap truncation (text) + rejection (HTML) | No |
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
- **Bidirectional sanitization.** Host→client HTML is sanitized server-side, not
  trusted because it came from the host.
- **Viewer isolation.** Host clipboard is pushed only to the controller by
  default, not to passive viewers.

---

## Status

📋 **Specced — not yet implemented.** Core module, built on the `arboard` crate
(text+image, all OSes) with a thin per-OS layer for change-notification and
rich-HTML / file-list. `arboard` is MIT/Apache, chosen to avoid RustDesk's AGPL
`libs/clipboard`. Text first, rich HTML second. Image + file clipboard explicitly
out of scope for v1.
