# Module Spec: Clipboard

## Overview

The Clipboard module provides **bidirectional clipboard synchronization** between
the browser client and the remote host. It is **core** (not an add-on) — clipboard
sync is a baseline remote-desktop expectation and the OS clipboard APIs are small
enough to live in core with per-OS files behind build constraints.

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

```go
package clipboard

// Monitor watches the host OS clipboard and reports changes. Per-OS
// implementations live behind build constraints (no add-on / build tags —
// these are core, compiled unconditionally per GOOS).
type Monitor interface {
    // Start begins watching. Changes are delivered on Changes().
    // Blocks until ctx is cancelled.
    Start(ctx context.Context) error

    // Changes yields host clipboard updates (already size-checked + sanitized).
    Changes() <-chan Content

    // Set writes content to the host OS clipboard (client → host direction).
    // All-or-nothing: on partial failure (e.g. CF_UNICODETEXT succeeds but
    // CF_HTML fails on Windows), the implementation MUST call EmptyClipboard /
    // equivalent so the OS clipboard does not end up in a half-set state, then
    // return the original error.
    Set(c Content) error

    Close() error
}

// NewMonitor constructs a per-OS Monitor. Returns ErrUnsupportedCompositor on
// Linux running a Wayland compositor without wlr-data-control (GNOME Mutter,
// KDE KWin), so the server can warn at startup and disable clipboard cleanly
// instead of failing silently at first copy.
func NewMonitor(cfg Config) (Monitor, error)

// Probe reports whether the host can support clipboard sync today (correct
// Wayland compositor, X display reachable, etc.). Server uses this at startup
// to surface the unsupported state.
func Probe() error

var ErrUnsupportedCompositor = errors.New("clipboard: Wayland compositor lacks wlr-data-control")

// Content is one clipboard payload. Exactly one of Text/HTML is the primary;
// HTML may carry a PlainText fallback for non-HTML paste targets.
type Content struct {
    Format    Format // FormatText or FormatHTML
    Text      string // UTF-8 plain text (always set; HTML carries its text fallback here)
    HTML      string // sanitized HTML (set only when Format == FormatHTML)
}

type Format uint8
const (
    FormatUnknown Format = iota // zero value; treated as invalid (sanity guard)
    FormatText                  // text/plain
    FormatHTML                  // text/html (+ Text fallback)
)

// Config controls clipboard behavior. Sourced from [clipboard] TOML section.
type Config struct {
    Enabled   bool
    Direction Direction
    MaxBytes  int          // hard cap per payload (default 1 MiB)
    Logger    *slog.Logger
}

type Direction uint8
const (
    DirDisabled    Direction = iota // no sync
    DirBidirectional                // host <-> client
    DirClientToHost                  // client copies, host pastes only
    DirHostToClient                  // host copies, client pastes only
)
```

---

## Host-Side Clipboard Access (per OS)

All three are **core code** under build constraints, not add-ons.

| OS | Change detection | Read | Write |
|----|------------------|------|-------|
| **Windows** | `AddClipboardFormatListener(hwnd)` → `WM_CLIPBOARDUPDATE` (event-driven) | `OpenClipboard` + `GetClipboardData(CF_UNICODETEXT / CF_HTML)` | `OpenClipboard` + `EmptyClipboard` + `SetClipboardData` |
| **Linux (X11)** | `XFixesSelectSelectionInput` + `XFixesSelectionNotify` (event-driven) | `XConvertSelection` targets `UTF8_STRING` / `text/html` | own the `CLIPBOARD` selection, serve on request |
| **Linux (Wayland)** | `wlr-data-control-unstable-v1` protocol — **wlroots-family only** | data-control offer | data-control source |
| **macOS** | **No notification API** — poll `NSPasteboard.changeCount` every 300 ms | `NSPasteboard.string(forType:)` / `NSPasteboardTypeHTML` | `clearContents` + `setString(forType:)` |

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

On unsupported compositors `NewMonitor` returns `ErrUnsupportedCompositor`;
the server logs a clear warning at startup and clipboard sync is silently
disabled (no spurious errors at first copy). A future portal-based path can
be added when one ships.

### Implementation notes (the hidden costs)

- **Windows.** `AddClipboardFormatListener` requires an `HWND`. The
  implementation creates a **message-only window** (`HWND_MESSAGE` parent) and
  runs a `GetMessage`/`TranslateMessage`/`DispatchMessage` pump on the **same
  OS thread that created the window** — i.e. in a goroutine pinned with
  `runtime.LockOSThread`. Shutdown posts `WM_QUIT`/`PostQuitMessage` to that
  thread.
- **X11.** The Monitor owns a dedicated `XOpenDisplay` connection in its own
  goroutine. The same connection MUST stay alive to serve `XConvertSelection`
  requests when this process owns the `CLIPBOARD` selection — if the goroutine
  dies, every paste in every other X11 app for that selection fails.
- **X11 INCR protocol (mandatory at this size).** A selection larger than the
  server's maximum request size (`XMaxRequestSize`, often ~256 KiB) CANNOT be
  transferred in one `XConvertSelection`. Since `[clipboard] max_bytes` is 1 MiB,
  the X11 path MUST implement the **INCR** protocol in BOTH roles: when reading a
  large selection (requestor receives the `INCR` target, then loops on
  `PropertyNotify` reading chunks until a zero-length property) and when serving
  one (owner advertises `INCR`, then writes chunks on each `PropertyNotify`
  delete). Skipping INCR silently truncates large pastes — a common bug.
- **Wayland (wlr-data-control).** Use `golang.org/x/exp/...`-compatible Wayland
  protocol bindings (or generate from XML); `wl-paste --watch` subprocess is
  intentionally NOT used as a fallback because it relies on the same
  `wlr-data-control` and offers no additional compositor coverage.

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
fragment is serialized. Implementations using `%d` instead of `%010d` will
produce a header whose own length changes when offsets grow, corrupting the
offsets it just wrote. The core Windows clipboard code MUST use a small
dedicated serializer for this format.

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
hostile HTML on the clipboard). Use a vetted Go HTML sanitizer
(e.g. `bluemonday` with a UGC-style policy).

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
Host clipboard change (WM_CLIPBOARDUPDATE / XFixes / changeCount poll)
    → Monitor reads + size-checks + sanitizes
    → Changes() channel → server
    → direction check ([clipboard] direction)
    → server writes [u32 Len][JSON] on the clipboard stream to controller client(s)

Client copy (clipboardchange / copy event)
    → navigator.clipboard.read() / event.clipboardData
    → [u32 Len][JSON {"type":"clipboard",...}] on the clipboard stream
    → server direction check
    → Monitor.Set() → OS clipboard
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

📋 **Specced — not yet implemented.** Core module. Text first, rich HTML second.
Image + file clipboard explicitly out of scope for v1.
