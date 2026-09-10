# Module Spec: Input

## Overview

The Input module is the core, transport-neutral layer that decodes client input
events from the wire and dispatches them to an injector which performs the actual
OS-level injection.

Unlike capture and encode, **keyboard/mouse injection is built into core, not
zero-by-default**: the default injector is **`enigo`** (crate `featherdesk-input`),
a single cross-platform user-space library (SendInput on Windows, XTEST/libei on
Linux, CGEvent on macOS). It was chosen because it is **anti-cheat-safe like
Sunshine**. So a default binary is **NOT view-only for kb/mouse** — it injects via
`enigo` whenever `[input] enabled = true`. Setting `enabled = false` still forces
view-only. The core module owns:

- The **binary input wire format** (decode + validation).
- The platform-neutral **event types** (`KeyEvent`, `PointerEvent`, etc.).
- The **HID-usage keycode** contract (physical-key neutrality across clients).
- The built-in **`enigo` keyboard/mouse injector** (the default `KeyMouseInjector`).
- The **dispatcher** that routes decoded events to the active injector(s) — the
  built-in `enigo` default or an opt-in add-on that overrides it.

The kernel-level injectors are **opt-in add-ons that OVERRIDE the `enigo`
default** when loaded — they beat it on latency or reach but carry tradeoffs:

| Platform | Add-on | Add-on ID | Mechanism | Spec |
|----------|--------|-----------|-----------|------|
| Windows | Interception | `interception` | Interception filter driver + `SendSAS` for Ctrl+Alt+Del — faster than `enigo`, but ⚠️ **anti-cheat-risk** (opt-in) | [`../addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md`](../addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md) |
| Linux | uinput | `uinput` | Kernel `/dev/uinput` (X11 + Wayland + console; keyboard, mouse, scroll, force-feedback) | [`../addons/linux/input/UINPUT_LINUX_SPEC.md`](../addons/linux/input/UINPUT_LINUX_SPEC.md) |

> **macOS `cgevent` add-on is retired/folded.** The former `cgevent` add-on is now
> **subsumed by the `enigo` default** — `enigo`'s macOS backend **IS** CGEvent, so
> there is no separate macOS kb/mouse add-on. On macOS the built-in `enigo`
> injector is the only kb/mouse path (and still needs Accessibility permission).

Touch is a **separate** add-on (Windows only for now):

| Platform | Add-on | Add-on ID | Mechanism | Spec |
|----------|--------|-----------|-----------|------|
| Windows | Win Touch | `win_touch` | `InitializeTouchInjection` / `InjectTouchInput` | [`../addons/windows/input/WIN_TOUCH_WINDOWS_SPEC.md`](../addons/windows/input/WIN_TOUCH_WINDOWS_SPEC.md) |

> **Not supported:** Pen/stylus is NOT a distinct add-on. Pen input from the
> client is downgraded to touch, and the downgrade is lossy by design: the
> `0x30` TouchContact record carries `PointerId`/`Phase`/`X`/`Y` and nothing
> else, so **pressure, tilt and twist are dropped at the client** and no
> injector ever receives them. Gamepad **is** a v1 browser feature via the
> Gamepad API — see [`MODULE_GAMEPAD.md`](MODULE_GAMEPAD.md) (virtual-controller
> injection is per-OS add-ons: `vigem` on Windows, `gcvirtual` on macOS, `uinput`
> on Linux). **`enigo` does NOT cover gamepad** (nor touch), so both remain add-ons.

---

## Wire Format: Binary Input Protocol

Input is the highest-frequency client→server message (1000+ events/sec during
gaming / fast mouse movement). It uses a **fixed-layout binary protocol**, not
JSON. This decision is grounded in measured data (see decision record below):
binary decode is ~121× faster, zero-allocation, ~70-79% smaller on the wire, and
has a far smaller attack surface than JSON.

**Channel: input flows on the input lane — a dedicated reliable bidirectional
lane, StreamType tag `0x01`.** On the WebTransport carrier that lane is a QUIC
bidirectional stream; on the WebSocket fallback carrier it is the same tag, the
same framing and byte-identical records carried as tagged binary messages on the
one TCP connection. Nothing in this module is carrier-aware. See
[`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Carrier selection":
- The lane has NO intrinsic message boundaries — a QUIC stream is a byte stream,
  and on the fallback carrier a WebSocket message boundary is deliberately not
  framing — so every record is wrapped `[u16 RecLen LE][record]`, BOTH
  directions and on BOTH carriers.
- Binary input records (client→server): `[u16 RecLen][6-byte record header + payload]`.
- InputAck (server→client): `[u16 RecLen=13][13-byte InputAck]` — a 13-byte
  message `[Type=14 u8][Seq u32 LE][RecvTimestampNs u64 LE]`, **not** a 22-byte
  FrameHeader (the FrameHeader is media-only now).
- Rare JSON control (keyframe, pong, stats, resize, set_*) flows on the separate
  **control** stream; clipboard rides the **clipboard** stream — neither here.

### Stream framing & `ReadFrame`

Because the input lane carries no message boundaries of its own, the core
provides a single helper that reads exactly one length-prefixed record, used by
both the server's input-reader task and (symmetrically) the client's InputAck
reader. It is the same helper on both carriers:

```rust
// crate: featherdesk-input

/// read_frame reads one [u16 RecLen LE][record] message from `r` and returns the
/// record bytes (without the length prefix). It bounds RecLen at MAX_REC_LEN
/// (4 KiB) and returns Err(InputError::UnexpectedEof) on a short read, so a single
/// dispatch call always receives one complete, self-delimited record.
pub async fn read_frame<R: AsyncRead + Unpin>(r: &mut R) -> Result<Vec<u8>, InputError>;

/// write_frame writes [u16 RecLen][record]. Used by the client to send records
/// and by the server to send InputAck.
pub async fn write_frame<W: AsyncWrite + Unpin>(w: &mut W, record: &[u8]) -> Result<(), InputError>;
```

`Dispatcher::dispatch` operates on the record bytes that `read_frame` returns — it
never sees the length prefix. This is what makes input records unambiguous on the
lane (fixes the "records have no delimiter on a QUIC byte stream" hazard). The
server never writes an ack from the task that runs `read_frame`: the input lane
is split, read half in `inputReader`, write half in `ackWriter`, so a client that
stops draining acks cannot wedge its own input — see "InputAck / latency".

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

> `0x50` is reserved for a future webcam type (see [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md));
> the dispatcher drops any 0x50 frame today.

### Records (all little-endian)

**KeyEvent — Type `0x10` (9 bytes)**
```
6   2   u16   HidUsage   USB HID Keyboard/Keypad usage ID (KeyA=0x04, …)
8   1   u8    Flags      bit0 down(1)/up(0); bit1 autorepeat; bits2-7 reserved=0
```

**MouseMoveAbs — Type `0x20` (10 bytes)**
```
6   2   u16   X   stream-pixel X, 0 .. width-1. The CLIENT clamps into range before encoding (the field is u16 and a negative letterbox coordinate wraps to ~65535); the server clamp is a backstop
8   2   u16   Y   stream-pixel Y, 0 .. height-1; same clamp obligation
```
> "Stream-pixel" throughout this spec means the **stream space** defined
> normatively in [`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md)
> "Coordinate space" — encoded output pixels of the selected display, upright,
> origin top-left. No wire field here carries points, logical pixels, normalized
> coordinates or native capture pixels.

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
> macOS `kCGScrollEventDelta*`. **Every `KeyMouseInjector` implementation MUST
> negate Dx and Dy before injection** — the built-in `enigo` default included,
> not only the add-ons. [`UINPUT_LINUX_SPEC.md`](../addons/linux/input/UINPUT_LINUX_SPEC.md)
> states it as a "Scroll sign" line;
> [`INTERCEPTION_WINDOWS_SPEC.md`](../addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md)
> and [`CGEVENT_MACOS_SPEC.md`](../addons/macos/input/CGEVENT_MACOS_SPEC.md) state
> it in their injection sketches (`ms.rolling = -wire_dy`, `macDy = -wireDy`);
> the `enigo` default's own rule is spelled out below.

> **Unit translation across OSes** (for an injector that has a high-resolution
> path — the `enigo` default does not; its own rule is below): `Unit=0` (pixel) →
> use the OS's pixel/high-res path. `Unit=1` (line) → typical translation is `lines * 120` for
> Windows `WHEEL_DELTA`, `lines` for Linux `REL_WHEEL`, `lines * 10` (px) for
> macOS line mode. `Unit=2` (page) → translate as **3 lines per page** on
> every OS for consistency; pages are an obscure W3C deltaMode rarely emitted
> by browsers.

> **The `enigo` default's sign and unit rule.** `enigo` exposes exactly one
> scroll primitive — `Mouse::scroll(length: i32, axis: Axis)` — and its unit is
> **lines/notches**, not pixels, on all three backends (Windows `WHEEL_DELTA`,
> X11 buttons 4/5 and 6/7, macOS `kCGScrollEventUnitLine`). The core injector
> therefore does in one place what each add-on does in its own:
>
> 1. **Unit.** `notches(d, unit) = match unit { 0 => d / 100, 1 => d, 2 => d * 3 }`,
>    truncating. `Unit=0` divides by **100**, the pixel step browsers emit for one
>    wheel notch at `deltaMode=0`. The truncated remainder is **accumulated in a
>    per-axis `i32` residual** carried across records rather than discarded, so a
>    trackpad's sub-notch deltas still scroll; a call whose accumulated result is
>    zero injects nothing at all.
> 2. **Sign.** What is normative is the observable outcome, not the arithmetic: a
>    positive wire `Dy` MUST scroll the host's content in the same direction a
>    positive W3C `deltaY` scrolls a browser page — down — and a positive `Dx`
>    right. `enigo`'s own `length` sign convention has differed across major
>    versions, so the injector pins it against the version in `Cargo.toml` and the
>    unit-test row below asserts the observed direction, never the sign of the
>    argument.
>
> This is the sign and unit contract for a stock install — the default binary
> loads no input add-on, so nothing else supplies one.

**TouchContact — Type `0x30` (13 bytes)** — handled only if a `TouchInjector` add-on is present
```
6    2   u16   PointerId
8    1   u8    Phase       0=down, 1=move, 2=up, 3=cancel
9    2   u16   X           stream-pixel X, 0 .. width-1; client clamps before encoding, server clamps as backstop
11   2   u16   Y           stream-pixel Y, 0 .. height-1; client clamps before encoding, server clamps as backstop
```
> **Producer contract.** The client emits `0x30` — never `0x20`/`0x21`/`0x22` —
> for every `PointerEvent` whose `pointerType` is `"touch"` or `"pen"`; only
> `"mouse"` produces the mouse records. `PointerId` is the event's `pointerId`
> truncated to its low 16 bits, and is tracked per id (not per `isPrimary`), so
> a two-finger gesture produces two independent contact streams. `Phase` follows
> the event name: `pointerdown`=0, `pointermove`=1, `pointerup`=2,
> `pointercancel`=3. Without this branch a finger tap arrives as a mouse drag
> and the `win_touch` add-on is never reached — see
> [`MODULE_WEB_CLIENT.md`](../client/MODULE_WEB_CLIENT.md) "Input Handling (binary)".

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
| `0x01` InputBatch | 7 .. 4096 (cap) | 7-byte minimum (6-byte header + Count=0); strict cap to bound work |
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

```rust
// crate: featherdesk-input

/// Event is the decoded, platform-neutral input event (tagged union).
/// The protocol layer produces these from binary records; the dispatcher
/// routes them to the active injector (the built-in `enigo` default or an add-on).
pub struct Event {
    pub kind: EventKind,
    pub seq: u32,

    // Keyboard
    pub hid_usage: u16, // USB HID usage ID
    pub down: bool,
    pub autorepeat: bool, // Flags bit1 — informational; injectors typically ignore

    // Mouse
    pub x: i32,
    pub y: i32, // absolute, stream-pixel space
    pub dx: i32,
    pub dy: i32, // relative (move) or scroll delta
    pub button: u8, // W3C button index
    pub scroll_unit: u8, // 0=pixel, 1=line, 2=page

    // Touch
    pub contacts: Vec<TouchContact>,

    // Gamepad (types 0x40-0x4F; see MODULE_GAMEPAD.md for GamepadState layout).
    // The dispatcher decodes the record and routes it to the GamepadInjector.
    pub gamepad_index: u8,             // controller slot 0..3
    pub gamepad: Option<GamepadState>, // Some for KindGamepadState; carries buttons/axes
    pub gamepad_id: String,            // KindGamepadConnect only — browser gamepad id
}

#[repr(u8)]
pub enum EventKind {
    KeyDownUp = 0,
    PointerAbs,
    PointerRel,
    Button,
    Scroll,
    Touch,
    GamepadState,      // 0x40 — full button/axis snapshot
    GamepadConnect,    // 0x41
    GamepadDisconnect, // 0x42
}

/// TouchContact is one contact point. It mirrors the ABI's `RTouchContact`
/// field for field (boundary form: `RTouchContact` — MODULE_ABI "Rich types
/// across the boundary"), so the adapter's conversion is a memcpy-shaped one and
/// no field may be added here without adding it there.
pub struct TouchContact {
    pub pointer_id: u16,
    pub phase: u8, // 0=down, 1=move, 2=up, 3=cancel
    pub x: i32,
    pub y: i32, // absolute, in the stream space (MODULE_STREAM_PARAMS "Coordinate space")
}

/// KeyMouseInjector is the base contract every keyboard/mouse injector implements.
/// The **built-in `enigo` default** implements it (core, all OS); the opt-in
/// kernel add-ons (`interception`, `uinput`) implement it too and override the
/// default when loaded. Cleanup is RAII (`Drop`) — no Close().
///
/// `: Send` because the Dispatcher that owns it is moved to the startup thread's
/// `Arc<Mutex<Box<dyn Dispatcher>>>` and reached from N session tasks
/// (CENTRAL_SPEC "Concurrency Model"). An add-on whose concrete type holds a
/// thread-affine handle (an `InterceptionContext`) must say in its own spec which
/// of MODULE_ABI "Thread requirements"' two options it takes.
pub trait KeyMouseInjector: Send {
    /// inject_key injects a key press/release. hid_usage is a USB HID usage ID;
    /// the injector maps it to the platform keycode (Linux KEY_*, Windows scan
    /// code, macOS virtual key).
    fn inject_key(&mut self, hid_usage: u16, down: bool) -> Result<(), InputError>;

    /// inject_pointer_abs moves the pointer to an absolute position in the
    /// stream space; per-OS unit conversion (macOS points, Windows virtual-screen
    /// physical pixels, the declared uinput ABS_X/ABS_Y range) happens inside the
    /// injector.
    fn inject_pointer_abs(&mut self, x: i32, y: i32) -> Result<(), InputError>;

    /// inject_pointer_rel applies a relative pointer delta (pointer-lock mode).
    fn inject_pointer_rel(&mut self, dx: i32, dy: i32) -> Result<(), InputError>;

    /// inject_button presses/releases a mouse button (W3C index).
    fn inject_button(&mut self, button: u8, down: bool) -> Result<(), InputError>;

    /// inject_scroll scrolls. unit is 0=pixel, 1=line, 2=page.
    fn inject_scroll(&mut self, dx: i32, dy: i32, unit: u8) -> Result<(), InputError>;

    /// resize updates the absolute-coordinate range to match new stream dims.
    fn resize(&mut self, width: u32, height: u32) -> Result<(), InputError>;

    /// caps reports which optional methods this injector serves. The built-in
    /// `enigo` default returns `AddonCaps(0)`; an add-on returns its constructed
    /// object's `caps()` (MODULE_ABI "Optional-method capability flags").
    fn caps(&self) -> abi::AddonCaps;

    /// send_sas triggers the Secure Attention Sequence (Ctrl+Alt+Del). Windows
    /// intercepts the real hardware chord in winlogon/csrss before any filter
    /// driver, so it cannot be injected as ordinary keys — only `SendSAS` reaches
    /// the system, and only the `interception` add-on has it.
    ///
    /// Returns `Err(InputError::Unsupported)` unless
    /// `caps().has(AddonCaps::SECURE_ATTENTION)` — the dispatcher checks the bit
    /// and does not call it otherwise. Returns `Err(InputError::SasUnavailable)`
    /// when policy or privilege blocks it at call time (the injector may claim the
    /// bit at probe and still be refused later).
    ///
    /// This is a folded optional method, not a separate trait: a `#[sabi_trait]`
    /// object cannot be downcast, so an optional capability reached by a downcast
    /// has no implementation (MODULE_ABI "Optional-method capability flags").
    fn send_sas(&mut self) -> Result<(), InputError>;
}

/// TouchInjector is the optional contract a touch add-on (win_touch) implements.
/// The dispatcher checks for it at runtime; touch events are dropped if no
/// TouchInjector is loaded. Cleanup is RAII (`Drop`) — no Close().
/// `: Send` for the same reason as `KeyMouseInjector`.
pub trait TouchInjector: Send {
    /// Boundary form: `RSlice<'_, RTouchContact>` — the contacts are BORROWED for
    /// the call only and an add-on MUST NOT retain them past it (MODULE_ABI).
    fn inject_touch(&mut self, contacts: &[TouchContact]) -> Result<(), InputError>;
}

/// `Injector` is the Layer-1 `#[sabi_trait]` union declared in MODULE_ABI,
/// carrying the methods of all three `Input*` kinds behind one tagged object —
/// one flat vtable, gated by `descriptor().kind` and by `caps`. It is **not** a
/// Layer-2 trait and never a Layer-2 return type. An input add-on implements
/// exactly ONE of `KeyMouseInjector`, `TouchInjector`, or `GamepadInjector` (see
/// MODULE_GAMEPAD); the add-on's capability descriptor declares which, and the
/// `Dispatcher` routes events to it. The Layer-1 `Injector` sabi trait
/// (MODULE_ABI) is the tagged object those three host traits are served from;
/// there is no Layer-2 `Injector`, and the loader therefore does **not** have an
/// `InputAddon::new -> Box<dyn input::Injector>`: it has `new_key_mouse` /
/// `new_touch` / `new_gamepad`, gated by `InputAddon::kind()` (MODULE_PIPELINE).

/// InputError — stable error enum for the input crate (replaces Go sentinels).
/// The first group crosses the add-on ABI and is in bijection with the input
/// domain's AbiErr codes (MODULE_ABI "AbiErr registry"); the second is produced
/// only host-side and is never mapped from a code.
#[derive(thiserror::Error, Debug)]
pub enum InputError {
    // ── crosses the ABI ────────────────────────────────────────────────────
    #[error("input: SAS unavailable (policy or privilege)")]
    SasUnavailable,                 // AbiErr::SasUnavailable (9)   — was ErrSASUnavailable
    #[error("input: injection device lost")]
    DeviceLost,                     // AbiErr::DeviceLost (6)
    #[error("input: add-on unrecoverable, do not retry: {0}")]
    Unrecoverable(String),          // AbiErr::Unrecoverable (7)
    #[error("input: add-on does not serve this optional method")]
    Unsupported,                    // AbiErr::Unsupported (8)
    #[error("input: backend failure: {0}")]
    Backend(String),                // AbiErr::Generic (1)
    // ── host-side only: produced by read_frame / Dispatcher::dispatch ──────
    #[error("input: unexpected EOF on input stream")]
    UnexpectedEof,
    #[error("input: malformed record")]
    Malformed,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

/// InjectorConfig is passed to an injector's constructor. Logging is via the
/// `tracing` crate — but an ADD-ON's events reach the operator only if it
/// installed the host's sink, `featherdesk_abi::install_log_sink(host.log)` in
/// `init()` (MODULE_ABI "Root module surface"); each `cdylib` otherwise has its
/// own uninitialised dispatcher and its output is discarded. The in-core `enigo`
/// injector links the host's dispatcher directly and needs no such call.
pub struct InjectorConfig {
    pub width: u32,  // initial stream width (absolute-coordinate range)
    pub height: u32, // initial stream height
}

/// Dispatcher decodes binary input records and routes Events to injectors.
/// Created at startup step 8 with the built-in `enigo` default (or an add-on
/// that overrode it) plus whichever optional add-ons were loaded, and owned by
/// the pipeline as an `Arc<Mutex<Box<dyn Dispatcher>>>`: `dispatch` takes
/// `&mut self` and is invoked from N session tasks through the server's
/// `Send + Sync` input callback, while the frame thread calls `resize` on a
/// dimension change. Serializing is not a workaround — the pressed-set that
/// `release_all` depends on is shared state two concurrent injections would
/// corrupt. The guard covers exactly one record and is never held across an
/// await (`dispatch` is synchronous).
/// Cleanup is RAII (`Drop`) — Drop releases all held input (see release_all).
pub trait Dispatcher: Send {
    /// dispatch decodes one binary input record (the record bytes from
    /// `read_frame` — no length prefix) and injects it. Returns the record
    /// Seq (for InputAck) and any injection error. Performs validation +
    /// clamping before injection, and records each key/button down in a
    /// pressed-set for release_all. The pressed-set is bounded at **512**
    /// entries (keys + buttons + live touch ids); a down-event beyond the cap is
    /// still injected but is not recorded, and the overflow is logged once
    /// (CENTRAL_SPEC "Queues and buffers").
    fn dispatch(&mut self, frame: &[u8]) -> Result<u32, InputError>;

    /// resize propagates a resolution change to all injectors.
    fn resize(&mut self, width: u32, height: u32) -> Result<(), InputError>;

    /// release_all injects an up-event for every key/button/touch currently held
    /// and synthesizes a `GamepadInjector::disconnect` for every gamepad index
    /// currently connected, then clears the pressed-set. Idempotent: with nothing
    /// held it injects nothing at all, so it is safe to call on every slot
    /// transition. The Dispatcher OWNS held-input state; injectors may ALSO
    /// release defensively in their own `Drop`, but the authoritative owner is the
    /// Dispatcher (it alone knows the cross-injector pressed-set).
    ///
    /// This is the WHOLE-dispatcher release. A single co-op `player` leaving is
    /// not this call: the server forwards a `GamepadDisconnect` (`0x42`) for that
    /// player's global slot, which tears down that one virtual pad and drops that
    /// slot from the pressed-set. Every other player's slot is untouched
    /// ([`MODULE_GAMEPAD.md`](MODULE_GAMEPAD.md) "Co-op").
    ///
    /// **How the server reaches it.** `release_all` is `&mut self` on an object the
    /// server does not hold, so the pipeline wires a second callback over the same
    /// `Arc<Mutex<Box<dyn Dispatcher>>>` at startup step 12, alongside the input
    /// callback:
    /// `Server::set_controller_change_callback(Box<dyn Fn() + Send + Sync>)`.
    /// The server invokes it on **every** controller-slot transition — acquire,
    /// release, takeover (`MODULE_SERVER.md` lifecycle step 11), session close and
    /// session timeout (step 21) — and it runs BEFORE the new controller's first
    /// input record is dispatched, so two clients' held state never overlaps on the
    /// host. Without this callback `release_all` has no caller at all, and a
    /// disconnect mid-keypress leaves the key held down on the host indefinitely —
    /// the host half of the stuck-key defect the client's `releaseAllHeld()` on
    /// `blur`/`visibilitychange` covers only from the browser side.
    fn release_all(&mut self) -> Result<(), InputError>;
}

/// new_dispatcher builds the routing layer. Called by the pipeline at startup
/// step 8, never by the server. `km` is the keyboard/mouse injector — the
/// **built-in `enigo` default** unless a kernel add-on (interception/uinput)
/// overrode it. With `[input] enabled = false` the pipeline does not call this at
/// all: no dispatcher is built, no input callback is installed, and the server
/// rejects the `0x01` stream at accept (MODULE_SERVER "Role gate table" row 1) —
/// that, and only that, is what makes a binary view-only.
/// `touch` + `gp` are optional add-ons.
pub fn new_dispatcher(
    km: Box<dyn KeyMouseInjector>,
    touch: Option<Box<dyn TouchInjector>>,
    gp: Option<Box<dyn GamepadInjector>>,
    cfg: InjectorConfig,
) -> Result<Box<dyn Dispatcher>, InputError>;
```

**Capability composition.** By default the dispatcher's `KeyMouseInjector` is the
built-in **`enigo`** injector (core, all OS). On Windows, loading the
`interception` add-on **overrides** that default with the kernel injector, and the
`win_touch` add-on adds a `TouchInjector` — independent add-ons. On Linux, loading
`uinput` overrides the `enigo` default for `KeyMouseInjector` (and may later add a
`TouchInjector`). On macOS the `enigo` default (CGEvent backend) is the only
kb/mouse path. The dispatcher holds one `KeyMouseInjector` and an optional
`TouchInjector`, routing by event kind.

**InputAck / latency.** After injecting, the **server** (not this module) sends a
13-byte `InputAck` (type 14) — `[Type=14][Seq u32][RecvTimestampNs u64]`,
length-prefixed `[u16 RecLen=13]` on the input stream (NOT a 22-byte FrameHeader).
The client measures input round-trip latency from it. `Dispatch` returns the
parsed `Seq` so the server can ack. **The ack is best-effort:** the server writes
it from a dedicated task fed by a 64-deep drop-oldest queue, so a client that
stops reading its ack side loses measurements rather than stalling its own input
([`MODULE_SERVER.md`](../core/MODULE_SERVER.md) → "Per-session tasks"). The
server acks only records it actually injects — a record dropped by the input rate
limit, coalesced away as a mousemove, or dropped by the role gate is never
acked — so the client's seq→timestamp map must be swept, not keyed on exact-match
deletion alone.

**Coordinate-space constraint.** `X`/`Y` are in the **stream space**
([`MODULE_STREAM_PARAMS.md`](../core/MODULE_STREAM_PARAMS.md) "Coordinate
space") — the client scaled them using the latest `config` width/height. The
injector's absolute range is updated by `resize()` on every resolution change;
**no add-on rescales between stream resolutions**. Converting the stream space
into the OS's own coordinate space (points on macOS, virtual-screen physical
pixels on Windows, the declared `ABS_X`/`ABS_Y` range on uinput) IS the
injector's job and happens inside it — that is a unit conversion at the OS
boundary, not a stream rescale.

---

## HID-Usage Keycode Contract

Keyboard neutrality across clients is achieved by transmitting **USB HID
Keyboard/Keypad usage IDs** (HID Usage Tables §10), not browser key strings.

- **Client** maps `KeyboardEvent.code` (physical position, e.g. `"KeyA"`) to a
  HID usage (`0x04`) via a static table. This is layout-independent.
- **Injector** maps HID usage → platform keycode (the built-in `enigo` default
  and every add-on do this from the same table):
  - Linux: HID → `KEY_*` (input-event-codes.h).
  - Windows: HID → scan code (set 1), injected via SendInput (`enigo`) or the
    Interception driver (add-on).
  - macOS: HID → virtual key code, via `CGEventCreateKeyboardEvent` (`enigo`'s
    CGEvent backend).

The HID usage table is **shared core code** (module `hid` in the
`featherdesk-input` crate), so the built-in `enigo` injector and every add-on
translate from the same neutral source. (The core dispatcher, the `enigo` default,
and the add-on crates all depend on `featherdesk-input`; the table is a
single-direction dependency, no cycle. A public re-export for the v2 native client
is deferred until it lands and needs to share the table.) This is the platform-neutral input
encoding required by
[`MODULE_NATIVE_CLIENT.md`](../client/MODULE_NATIVE_CLIENT.md) — the v2 native client
sends the same HID usages (byte-identical records).

**Coverage (must be complete, generated from the HID usage tables):**
letters, digits, F1-F24, modifiers (L/R Ctrl/Shift/Alt/Meta), navigation,
editing, numpad, lock/system, media keys, international keys.

---

## Ctrl+Alt+Del Chord Detection (Windows)

Ctrl+Alt+Del cannot be injected as ordinary keys — Windows intercepts the
hardware combination in winlogon/csrss before any filter driver. The core
dispatcher detects the chord and routes it to `send_sas()` on the active
`KeyMouseInjector`, gated by `AddonCaps::SECURE_ATTENTION` (bit 16). Only the
`interception` add-on sets that bit — the `enigo` default returns
`AddonCaps(0)`.

**Algorithm (pinned):**

1. Track modifier state: `LCtrl`, `RCtrl`, `LAlt`, `RAlt` independently.
   AltGr is `RAlt` and counts as Alt.
2. When a **Delete key down** record arrives, check:
   `(LCtrl || RCtrl) && (LAlt || RAlt)`.
3. If true, this is the SAS chord:
   a. **Swallow** the Delete down record entirely (do not inject as a key).
   b. If `km.caps().has(AddonCaps::SECURE_ATTENTION)` is false: log
      `"CAD chord ignored: no SecureAttention capability"` once and drop.
   c. Call `send_sas()`. On `Err(InputError::SasUnavailable)` (policy/privilege),
      log a clear warning once and drop. **Do not** inject the chord as ordinary keys
      as a fallback — that would deliver Ctrl+Alt+Del to the foreground app
      instead of the system, which is misleading.
   d. Also swallow the subsequent Delete **up** record so the OS's key state
      tracking stays consistent.
4. Constituent Ctrl/Alt key events are NOT swallowed — they are injected
   normally before the Delete arrives so the user's modifiers behave correctly
   for any non-Delete keystroke in between.

This algorithm is platform-neutral; non-Windows injectors and the `enigo` default
never set `AddonCaps::SECURE_ATTENTION`, so step 3b drops the chord with a log.

---

## Dispatch Flow

```
Input stream binary record (client → server)
    → byte[1] (Type):
        0x50            → reserved for future webcam — current binaries drop
        0x01-0x4F       → Dispatcher::dispatch(frame):
            decode record (zero-alloc, bounds-checked)
            validate: Version==1, len matches Type, ranges in bounds
            clamp:    X∈[0,W-1], Y∈[0,H-1]
            route by kind:
                EventKind::KeyDownUp  → key_mouse.inject_key(hid, down)
                EventKind::PointerAbs → key_mouse.inject_pointer_abs(x, y)
                EventKind::PointerRel → key_mouse.inject_pointer_rel(dx, dy)
                EventKind::Button     → key_mouse.inject_button(btn, down)
                EventKind::Scroll     → key_mouse.inject_scroll(dx, dy, unit)
                EventKind::Touch      → if let Some(t) = &mut touch { t.inject_touch(contacts) }
                                        else drop
                EventKind::GamepadState      → if let Some(g) = &mut gp { g.update(state) } else drop
                EventKind::GamepadConnect    → if let Some(g) = &mut gp { g.connect(index, id) } else drop
                EventKind::GamepadDisconnect → if let Some(g) = &mut gp { g.disconnect(index) } else drop
                  // Co-op: a `player` client's reader remaps the record's local
                  // gamepad index → that client's assigned global slot before
                  // injection (server-side; see MODULE_GAMEPAD/MODULE_SERVER).
            track pressed keys/buttons (for release_all on controller change/Drop)
            return Seq
    → server emits InputAck(Seq, recvTimestamp) as [u16 RecLen=13][13-byte ack]
```

Which client's binary records reach the dispatcher at all is the server's
allowlist, not this module's: keyboard, mouse, wheel and touch records
(`0x00`-`0x3F`) are accepted **only** from the `control` client, and gamepad
records (`0x40`-`0x4F`) from `control` for slot 0 and from `player` clients for
slots 1…N when `[gamepad] allow_coop`. Every other role — and a `player`'s
non-gamepad records — is dropped at the server before the dispatcher sees a byte
(see [`MODULE_SERVER.md`](../core/MODULE_SERVER.md) "Role gate table" rows 1-4
and [`MODULE_AUTH.md`](../core/MODULE_AUTH.md)).

---

## Security Considerations

- **Injection privilege.** An injector (the built-in `enigo` default or a kernel
  add-on) can inject ANY OS input event. Only the designated **controller** client
  reaches the `KeyMouseInjector` and `TouchInjector`; a co-op `player` reaches the
  `GamepadInjector` and nothing else; viewers reach nothing. The gate is an
  allowlist on the effective role, enforced at the server (MODULE_SERVER "Role
  gate table" rows 1-4), never a test against `view`.
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
- **Privilege of the injector.** On Windows, the default `enigo` injector uses
  `SendInput`, which suffers UIPI silent-fail against elevated apps; injecting into
  elevated apps requires the opt-in Interception add-on (kernel-level), which
  avoids it (and carries the anti-cheat-risk warning). On Linux, the `enigo`
  default uses XTEST/libei in user space; the opt-in `uinput` add-on needs
  `input`-group/root but reaches Wayland + console. On macOS, the `enigo` default
  (CGEvent backend) requires Accessibility permission, mandatory and checked at startup.

---

## Configuration

Input behavior is configured in the `[input]` TOML section; each add-on reads
its own `[addon_module_<id>]` section. See
[`../core/MODULE_CONFIG.md`](../core/MODULE_CONFIG.md).

```toml
[input]
enabled        = true     # master switch; true = kb/mouse via the built-in `enigo`
                          # default (or a kernel add-on if one overrode it).
                          # false = view-only, even with the `enigo` default or an add-on.
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
| Unit | Scroll sign + unit: with a fake `enigo` backend, a positive wire `Dy` scrolls the host content down and a positive `Dx` right on all three OSes; pixel/line/page all convert to notches; the truncated pixel remainder accumulates across records instead of being lost | No |
| Unit | Ctrl+Alt+Del: an injector that does not set `SECURE_ATTENTION` drops the chord with exactly one log line and never injects the constituent keys; one that does set it calls `send_sas` exactly once per chord | No |
| Unit | `release_all` is idempotent (nothing held → no injection) and clears the pressed-set, including live touch contacts and every connected gamepad slot | No |
| Integration | Full inject lifecycle (built-in `enigo` + each add-on: create, inject, drop) | Yes (per OS) |
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
core module (wire decode + dispatcher + HID table + the built-in `enigo`
kb/mouse injector) is all a default binary needs to accept keyboard/mouse input —
it is **NOT view-only by default** (only `[input] enabled = false` forces that).
The kernel add-ons (interception/uinput), touch (win_touch), and gamepad
(vigem/gcvirtual/uinput) remain opt-in overrides/extensions.
