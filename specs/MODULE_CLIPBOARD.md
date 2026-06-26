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
    Set(c Content) error

    Close() error
}

// Content is one clipboard payload. Exactly one of Text/HTML is the primary;
// HTML may carry a PlainText fallback for non-HTML paste targets.
type Content struct {
    Format    Format // FormatText or FormatHTML
    Text      string // UTF-8 plain text (always set; HTML carries its text fallback here)
    HTML      string // sanitized HTML (set only when Format == FormatHTML)
}

type Format uint8
const (
    FormatText Format = iota // text/plain
    FormatHTML               // text/html (+ Text fallback)
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
| **Windows** | `AddClipboardFormatListener(hwnd)` → `WM_CLIPBOARDUPDATE` (event-driven, no polling) | `OpenClipboard` + `GetClipboardData(CF_UNICODETEXT / CF_HTML)` | `OpenClipboard` + `EmptyClipboard` + `SetClipboardData` |
| **Linux (X11)** | `XFixesSelectSelectionInput` + `XFixesSelectionNotify` (event-driven) | `XConvertSelection` targets `UTF8_STRING` / `text/html` | own the `CLIPBOARD` selection, serve on request |
| **Linux (Wayland)** | `wlr-data-control` / `wl-paste --watch` subprocess (compositor-dependent) | data-control offer | data-control source |
| **macOS** | **No notification API** — poll `NSPasteboard.changeCount` every 300 ms | `NSPasteboard.string(forType:)` / `NSPasteboardTypeHTML` | `clearContents` + `setString(forType:)` |

> The macOS polling interval (300 ms) matches what every macOS remote-desktop
> tool does (Sunshine, Parsec). Polling is cheap (`changeCount` is an integer
> compare); the actual read only happens when the count changes.

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

The core Windows clipboard code MUST parse/serialize this header (compute the
byte offsets) when reading/writing HTML. A small dedicated serializer handles it.

---

## Wire Protocol

Clipboard is **low-rate and human-triggered**, so it uses JSON (not the binary
input path). Directions are asymmetric, matching the existing channel design:

**Client → host: JSON text frame**
```json
{"type": "clipboard", "format": "text/plain", "text": "hello"}
{"type": "clipboard", "format": "text/html", "html": "<b>hi</b>", "text": "hi"}
```

**Host → client: binary frame, `FrameTypeClipboard = 12`, JSON payload**
(server→client is always binary-framed; the payload is the same JSON shape).
See [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md).

```json
{"format": "text/html", "html": "<b>hi</b>", "text": "hi"}
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
    → server sends FrameTypeClipboard (12) to controller client(s)

Client copy (clipboardchange / copy event)
    → navigator.clipboard.read() / event.clipboardData
    → WS text {"type":"clipboard",...}
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
