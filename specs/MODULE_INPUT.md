# Module Spec: Input

## Overview

The Input module is the core, transport-neutral layer that decodes client input
events from the wire and dispatches them to a compiled-in **input add-on** which
performs the actual OS-level injection.

Like capture and encode, **input injection is zero-by-default**: the core binary
ships with NO input add-on and is therefore **view-only** until an injection
add-on is compiled in. The core module owns:

- The **binary input wire format** (decode + validation).
- The platform-neutral **event types** (`KeyEvent`, `PointerEvent`, etc.).
- The **HID-usage keycode** contract (physical-key neutrality across clients).
- The **dispatcher** that routes decoded events to the active injector add-on(s).

Each platform's injection mechanism is a separate build-tagged add-on:

| Platform | Add-on | Build tag | Mechanism | Spec |
|----------|--------|-----------|-----------|------|
| Windows | Interception | `interception` | Interception filter driver + `SendSAS` for Ctrl+Alt+Del | [`../ADD-ON-SPECS/Windows/input/INTERCEPTION_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/input/INTERCEPTION_WINDOWS_SPEC.md) |
| Linux | uinput | `uinput` | Kernel `/dev/uinput` (X11 + Wayland; keyboard, mouse, scroll) | [`../ADD-ON-SPECS/Linux/input/UINPUT_LINUX_SPEC.md`](../ADD-ON-SPECS/Linux/input/UINPUT_LINUX_SPEC.md) |
| macOS | CGEvent | `cgevent` | `CGEventPost` to `kCGHIDEventTap` | [`../ADD-ON-SPECS/macOS/input/CGEVENT_MACOS_SPEC.md`](../ADD-ON-SPECS/macOS/input/CGEVENT_MACOS_SPEC.md) |

Touch is a **separate** add-on (Windows only for now):

| Platform | Add-on | Build tag | Mechanism | Spec |
|----------|--------|-----------|-----------|------|
| Windows | Win Touch | `win_touch` | `InitializeTouchInjection` / `InjectTouchInput` | [`../ADD-ON-SPECS/Windows/input/WIN_TOUCH_WINDOWS_SPEC.md`](../ADD-ON-SPECS/Windows/input/WIN_TOUCH_WINDOWS_SPEC.md) |

> **Not supported:** Pen/stylus is NOT a distinct add-on. Pen input from the
> client is downgraded to touch (pressure preserved where the touch add-on
> supports it; tilt/twist dropped). Gamepad is out of scope for the browser
> client and is revisited only with the future native client.

---

## Wire Format: Binary Input Protocol

Input is the highest-frequency client→server message (1000+ events/sec during
gaming / fast mouse movement). It uses a **fixed-layout binary protocol**, not
JSON. This decision is grounded in measured data (see decision record below):
binary decode is ~121× faster, zero-allocation, ~70-79% smaller on the wire, and
has a far smaller attack surface than JSON.

**Channel discrimination by WebSocket opcode:**
- **Binary** WebSocket frames from the client = input events.
- **Text** WebSocket frames from the client = rare human-triggered JSON control
  (`keyframe`, `pong`, `stats`, `resize`, `set_*`, `clipboard`).

This replaces the previous "client→server is JSON-only" rule.

### Shared 6-byte record header

Every input record begins with the same prefix (mirrors the media header's
`[Version][Type]` convention for sniffability):

```
Offset  Size  Type   Field     Notes
0       1     u8     Version   = 1 (sniff byte; reject if != 1)
1       1     u8     Type      event type (ranges below)
2       4     u32    Seq       LE, per-connection monotonic; echoed in InputAck (type 14)
```

### Type ranges (extensibility)

```
0x01-0x0F  meta / control (batch, heartbeat)
0x10-0x1F  keyboard
0x20-0x2F  mouse
0x30-0x3F  touch / pen
0x40-0x4F  gamepad (see MODULE_GAMEPAD.md)
0xF0-0xFF  vendor / experimental
```

> `0x50` is reserved for a future webcam type (see [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md));
> the dispatcher drops any 0x50 frame today.

### Records (all little-endian)

**KeyEvent — Type `0x10` (9 bytes)**
```
6   2   u16   HidUsage   USB HID Keyboard/Keypad usage ID (KeyA=0x04, …)
8   1   u8    Flags      bit0 down(1)/up(0); bit1 autorepeat; bits2-7 reserved=0
```

**MouseMoveAbs — Type `0x20` (10 bytes)**
```
6   2   u16   X   stream-pixel X (0 .. width-1; server clamps)
8   2   u16   Y   stream-pixel Y (0 .. height-1; server clamps)
```

**MouseMoveRel — Type `0x21` (10 bytes)** — pointer-lock / FPS gaming
```
6   2   i16   Dx   relative dx
8   2   i16   Dy   relative dy
```

**MouseButton — Type `0x22` (8 bytes)**
```
6   1   u8   Button   W3C order: 0=left, 1=middle, 2=right, 3=back, 4=forward
7   1   u8   Flags    bit0 down(1)/up(0)
```

**Scroll — Type `0x23` (11 bytes)**
```
6    2   i16   Dx    horizontal (high-res); positive = scroll right (W3C deltaX)
8    2   i16   Dy    vertical   (high-res); positive = scroll down  (W3C deltaY)
10   1   u8    Unit  0=pixel, 1=line, 2=page (W3C deltaMode)
```

> **Wire sign convention is W3C** (positive = down/right). Every host scroll
> API uses the **opposite** convention (positive = up/left): Windows
> `WHEEL_DELTA` / Interception `rolling`, Linux `REL_WHEEL` / `REL_WHEEL_HI_RES`,
> macOS `kCGScrollEventDelta*`. Every add-on MUST negate Dx and Dy before
> injection. The add-on specs each contain a "Scroll sign" line stating this.

> **Unit translation across OSes:** `Unit=0` (pixel) → use the OS's pixel/
> high-res path. `Unit=1` (line) → typical translation is `lines * 120` for
> Windows `WHEEL_DELTA`, `lines` for Linux `REL_WHEEL`, `lines * 10` (px) for
> macOS line mode. `Unit=2` (page) → translate as **3 lines per page** on
> every OS for consistency; pages are an obscure W3C deltaMode rarely emitted
> by browsers.

**TouchContact — Type `0x30` (13 bytes)** — handled only if a `TouchInjector` add-on is present
```
6    2   u16   PointerId
8    1   u8    Phase       0=down, 1=move, 2=up, 3=cancel
9    2   u16   X           stream-pixel X (server clamps)
11   2   u16   Y           stream-pixel Y (server clamps)
```

**InputBatch — Type `0x01` (header + N records)** — coalescing container
```
6    1   u8    Count
7    …         Count × [u16 RecLen][RecLen bytes = one complete input record]
```
Amortizes per-frame overhead when the client coalesces several samples per
animation frame (`PointerEvent.getCoalescedEvents()`). Sub-records are
length-prefixed and self-delimiting; an unknown inner Type is skipped.

### Type → length validation table

The dispatcher rejects any frame whose total length doesn't match the expected
size for its Type **before any field is read**.

| Type | Total bytes | Notes |
|------|-------------|-------|
| `0x01` InputBatch | 8 .. 4096 (cap) | 7-byte minimum (header + Count=0); strict cap to bound work |
| `0x10` KeyEvent | 9 | exact |
| `0x20` MouseMoveAbs | 10 | exact |
| `0x21` MouseMoveRel | 10 | exact |
| `0x22` MouseButton | 8 | exact |
| `0x23` Scroll | 11 | exact |
| `0x30` TouchContact | 13 | exact |
| `0x40` GamepadState | 23 | exact (see MODULE_GAMEPAD) |
| `0x41` GamepadConnect | 9 .. 264 | 9 + IdLen ≤ 255 |
| `0x42` GamepadDisconnect | 7 | exact |

**Caps on InputBatch:** the dispatcher enforces (a) total frame ≤ 4 KiB,
(b) `Count` ≤ 64, (c) sum of sub-record lengths matches the outer frame.
Any violation drops the whole frame with a metric increment — never partial
inject.

### Forward-compatibility rules

1. **Unknown top-level `Type`** → drop the whole frame (one frame = one record).
2. **Additive evolution** → append new trailing fields; decoders validate
   `len >= knownPrefix`, read known fields, ignore the tail.
3. **Breaking change** (reorder/resize/remove fields) → bump `Version`.

---

## Public Interface

```go
package input

// Event is the decoded, platform-neutral input event (tagged union).
// The protocol layer produces these from binary records; the dispatcher
// routes them to the active injector add-on(s).
type Event struct {
    Kind EventKind
    Seq  uint32

    // Keyboard
    HidUsage   uint16 // USB HID usage ID
    Down       bool
    Autorepeat bool   // Flags bit1 — informational; injectors typically ignore

    // Mouse
    X, Y     int    // absolute, stream-pixel space
    Dx, Dy   int    // relative (move) or scroll delta
    Button   uint8  // W3C button index
    ScrollUnit uint8 // 0=pixel, 1=line, 2=page

    // Touch
    Contacts []TouchContact
}

type EventKind uint8
const (
    KindKeyDownUp EventKind = iota
    KindPointerAbs
    KindPointerRel
    KindButton
    KindScroll
    KindTouch
)

type TouchContact struct {
    PointerID uint16
    Phase     uint8 // 0=down, 1=move, 2=up, 3=cancel
    X, Y      int   // absolute, stream-pixel space
}

// KeyMouseInjector is the base contract every keyboard/mouse input add-on
// implements (interception, uinput, cgevent).
type KeyMouseInjector interface {
    // InjectKey injects a key press/release. hidUsage is a USB HID usage ID;
    // the add-on maps it to the platform keycode (Linux KEY_*, Windows scan
    // code, macOS virtual key).
    InjectKey(hidUsage uint16, down bool) error

    // InjectPointerAbs moves the pointer to an absolute stream-pixel position.
    InjectPointerAbs(x, y int) error

    // InjectPointerRel applies a relative pointer delta (pointer-lock mode).
    InjectPointerRel(dx, dy int) error

    // InjectButton presses/releases a mouse button (W3C index).
    InjectButton(button uint8, down bool) error

    // InjectScroll scrolls. unit is 0=pixel, 1=line, 2=page.
    InjectScroll(dx, dy int, unit uint8) error

    // Resize updates the absolute-coordinate range to match new stream dims.
    Resize(width, height int) error

    Close() error
}

// TouchInjector is the optional contract a touch add-on (win_touch)
// implements. The dispatcher type-asserts for it; touch events are dropped
// if no TouchInjector is compiled in.
type TouchInjector interface {
    InjectTouch(contacts []TouchContact) error
    Close() error
}

// SecureAttention is an optional capability implemented by Windows input
// add-ons (interception) for delivering Ctrl+Alt+Del via SendSAS. The
// dispatcher type-asserts the active KeyMouseInjector for this and routes
// the locked CAD chord here instead of injecting three KeyEvents. On
// platforms / add-ons that don't implement it, the chord is dropped with
// a one-time warning.
type SecureAttention interface {
    // SendSAS triggers the Secure Attention Sequence (Ctrl+Alt+Del).
    // Returns ErrSASUnavailable when the policy / privileges don't permit it.
    SendSAS() error
}

var ErrSASUnavailable = errors.New("input: SAS unavailable (policy or privilege)")

// InjectorConfig is passed to an add-on's constructor.
type InjectorConfig struct {
    Width  int          // initial stream width (absolute-coordinate range)
    Height int          // initial stream height
    Logger *slog.Logger
}

// Dispatcher decodes binary input records and routes Events to injectors.
// Owned by the server; created with whichever add-ons were compiled in.
type Dispatcher interface {
    // Dispatch decodes one binary WebSocket frame and injects it.
    // Returns the record Seq (for InputAck) and any injection error.
    // Performs validation + clamping before injection.
    Dispatch(frame []byte) (seq uint32, err error)

    // Resize propagates a resolution change to all injectors.
    Resize(width, height int) error

    Close() error
}

// NewDispatcher builds the routing layer from whichever injectors the
// pipeline probed. km is required (view-only mode passes a no-op stub
// or omits the dispatcher entirely); touch + gamepad are optional.
func NewDispatcher(km KeyMouseInjector, touch TouchInjector, gp GamepadInjector,
    cfg InjectorConfig) (Dispatcher, error)
```

**Capability composition.** On Windows, the `interception` add-on provides a
`KeyMouseInjector` and the `win_touch` add-on provides a `TouchInjector` — two
independent add-ons. On Linux, `uinput` provides `KeyMouseInjector` (and may
later add `TouchInjector`). The dispatcher holds one `KeyMouseInjector` and an
optional `TouchInjector`, routing by event kind.

**InputAck / latency.** After injecting, the **server** (not this module) emits a
`FrameTypeInputAck` (type 14) carrying `Seq` + a server-receive timestamp. The
client measures input round-trip latency from it. `Dispatch` returns the parsed
`Seq` so the server can ack.

**Coordinate-space constraint.** `X`/`Y` are already in the stream's pixel space
(the client scaled them using the latest `Config` width/height). The injector's
absolute range MUST equal those dims. The pipeline calls `Resize` on a
resolution change; there is no scaling inside an add-on.

---

## HID-Usage Keycode Contract

Keyboard neutrality across clients is achieved by transmitting **USB HID
Keyboard/Keypad usage IDs** (HID Usage Tables §10), not browser key strings.

- **Client** maps `KeyboardEvent.code` (physical position, e.g. `"KeyA"`) to a
  HID usage (`0x04`) via a static table. This is layout-independent.
- **Add-on** maps HID usage → platform keycode:
  - Linux: HID → `KEY_*` (input-event-codes.h).
  - Windows: HID → scan code (set 1), injected via the Interception driver.
  - macOS: HID → virtual key code, via `CGEventCreateKeyboardEvent`.

The HID usage table is **shared core code** (`internal/input/hid/hid.go`), so
every add-on translates from the same neutral source. (Both the core dispatcher
and all add-ons live under `internal/input/...`; the table is a single-direction
import, no cycle. Public re-export to `pkg/input` is deferred until the native
client lands and needs to share the table.) This is the platform-neutral input
encoding required by
[`FUTURE_NATIVE_CLIENT.md`](./FUTURE_NATIVE_CLIENT.md) — a future native client
sends the same HID usages.

**Coverage (must be complete, generated from the HID usage tables):**
letters, digits, F1-F24, modifiers (L/R Ctrl/Shift/Alt/Meta), navigation,
editing, numpad, lock/system, media keys, international keys.

---

## Ctrl+Alt+Del Chord Detection (Windows)

Ctrl+Alt+Del cannot be injected as ordinary keys — Windows intercepts the
hardware combination in winlogon/csrss before any filter driver. The core
dispatcher detects the chord and routes it to `SecureAttention.SendSAS()`
on the active `KeyMouseInjector` (only `interception` implements this).

**Algorithm (pinned):**

1. Track modifier state: `LCtrl`, `RCtrl`, `LAlt`, `RAlt` independently.
   AltGr is `RAlt` and counts as Alt.
2. When a **Delete key down** record arrives, check:
   `(LCtrl || RCtrl) && (LAlt || RAlt)`.
3. If true, this is the SAS chord:
   a. **Swallow** the Delete down record entirely (do not inject as a key).
   b. Type-assert the injector for `SecureAttention`. If absent: log
      `"CAD chord ignored: no SecureAttention capability"` once and drop.
   c. Call `SendSAS()`. On `ErrSASUnavailable` (policy/privilege), log a clear
      warning once and drop. **Do not** inject the chord as ordinary keys
      as a fallback — that would deliver Ctrl+Alt+Del to the foreground app
      instead of the system, which is misleading.
   d. Also swallow the subsequent Delete **up** record so the OS's key state
      tracking stays consistent.
4. Constituent Ctrl/Alt key events are NOT swallowed — they are injected
   normally before the Delete arrives so the user's modifiers behave correctly
   for any non-Delete keystroke in between.

This algorithm is platform-neutral; non-Windows add-ons receive Delete as a
normal key (their `SecureAttention` is absent, step 3b drops with a log).

---

## Dispatch Flow

```
WebSocket BINARY frame (client → server)
    → byte[1] (Type):
        0x50            → reserved for future webcam — current binaries drop
        0x01-0x4F       → input.Dispatcher.Dispatch(frame):
            decode record (zero-alloc, bounds-checked)
            validate: Version==1, len matches Type, ranges in bounds
            clamp:    X∈[0,W-1], Y∈[0,H-1]
            route by kind:
                KindKeyDownUp  → keyMouse.InjectKey(hid, down)
                KindPointerAbs → keyMouse.InjectPointerAbs(x, y)
                KindPointerRel → keyMouse.InjectPointerRel(dx, dy)
                KindButton     → keyMouse.InjectButton(btn, down)
                KindScroll     → keyMouse.InjectScroll(dx, dy, unit)
                KindTouch      → if touch != nil { touch.InjectTouch(contacts) }
                                 else drop
            return Seq
    → server emits InputAck(Seq, recvTimestamp)
```

Only the **controller** client's binary frames reach the dispatcher; viewer
frames are dropped at the server (see [`MODULE_SERVER.md`](./MODULE_SERVER.md)
and [`MODULE_AUTH.md`](./MODULE_AUTH.md)).

---

## Security Considerations

- **Injection privilege.** An input add-on can inject ANY OS input event. Only
  the designated **controller** client reaches the dispatcher; viewers never do.
- **Binary bounds.** Every record is validated to an exact length and field
  range before injection. A frame that is not exactly the expected size for its
  Type is rejected before any work — fixed-size = inherently bounded (no
  billion-laughs / deep-nesting class of attack that JSON carries).
- **Coordinate clamping.** Absolute X/Y are clamped to the current stream dims
  before injection; out-of-range values cannot reach the kernel input layer.
- **Rate limiting.** The server applies a per-client input rate limit
  (`server.input_rate_limit`, default 1000 ev/s) with `mousemove` coalescing
  before dispatch.
- **HID whitelist (optional).** The HID-usage table is static; an optional
  policy can reject dangerous usages. The Secure Attention Sequence
  (Ctrl+Alt+Del) is never synthesizable from ordinary input on Windows — it is
  handled out-of-band by the `interception` add-on via `SendSAS` (see its spec).
- **Privilege of the injector.** On Windows, injection into elevated apps
  requires the Interception driver (kernel-level), avoiding the UIPI silent-fail
  that `SendInput` suffers. On Linux, `/dev/uinput` needs `input`-group/root. On
  macOS, Accessibility permission is mandatory and checked at startup.

---

## Configuration

Input behavior is configured in the `[input]` TOML section; each add-on reads
its own `[addon_module_<tag>]` section. See
[`./MODULE_CONFIG.md`](./MODULE_CONFIG.md).

```toml
[input]
enabled        = true     # master switch; false = view-only even if an add-on is compiled in
relative_mouse = true     # honor pointer-lock relative-mode frames
```

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | Binary decode: every record type, truncated/oversized frames, unknown Type | No |
| Unit | HID-usage → platform keycode mapping (all keys, unknown usage) | No |
| Unit | Coordinate clamping, scroll unit conversion | No |
| Unit | InputBatch unpacking (nested records, unknown inner Type skip) | No |
| Integration | Full inject lifecycle per add-on (create, inject, close) | Yes (per OS) |
| Integration | Linux: events visible in `evtest` | Yes (/dev/uinput) |
| Integration | Windows: events visible to a raw-input test app + Ctrl+Alt+Del via SendSAS | Yes |
| Mock | Fake injector for dispatcher/server tests | No |

---

## Decision Record: Binary vs JSON

Measured on Go 1.26, Ryzen 9 5900X, against the exact prior JSON shapes:

| Operation | JSON (struct) | Binary | Delta |
|-----------|--------------:|-------:|-------|
| Server decode, mousemove | 909.5 ns, 6 allocs, 328 B | 7.49 ns, 0 allocs, 0 B | ~121× faster, zero-alloc |
| Client encode, mousemove | 243 ns, 1 alloc | 0.5 ns, 0 allocs | ~486× faster |
| On-wire mousemove | 53 B | 16 B | ~70% smaller |

Security: binary's fixed-layout decoder is ~10 lines of bounds-checked slicing
with no recursion, no string allocation, no number parsing — an
orders-of-magnitude smaller attack surface than reflection-based JSON, and every
field is range-checkable before injection. Industry precedent is unanimous:
RFB/VNC (6-byte PointerEvent, 8-byte KeyEvent), RDP, Moonlight, Parsec, and Steam
Remote Play all use binary input. FeatherDesk's video path is already binary;
input now matches that discipline.

---

## Status

📋 **Specced — un-deferred.** Replaces the previous Linux-only JSON design. The
core module (wire decode + dispatcher + HID table) plus at least one injection
add-on must be implemented for a binary to accept input. Default binary remains
view-only.
