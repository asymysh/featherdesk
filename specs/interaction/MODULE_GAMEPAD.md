# Module Spec: Gamepad

## Overview

Browser-driven gamepad redirection. The browser polls connected gamepads via the
W3C Gamepad API and sends state snapshots as compact binary records; the host
injects them into a virtual controller via a per-OS add-on so games on the host
see a real gamepad. Rumble feedback flows the other way (server → client).

**Scope: casual-gaming acceptable, not competitive-grade.** The browser
Gamepad API caps practical latency at ~16-26 ms (it samples on
`requestAnimationFrame`). This is fine for couch-co-op, indie games, emulators,
and most singleplayer; it is **not** suitable for ranked competitive shooters.
A future native client will close this gap and is explicitly out of scope here.

**Zero-by-default.** No virtual gamepad capability unless a gamepad add-on is
loaded. Gamepad records from the client are silently dropped when no
`GamepadInjector` is available — exactly the same pattern as `TouchInjector`.

| Platform | Add-on | Add-on ID | Mechanism | Spec |
|----------|--------|-----------|-----------|------|
| Windows | ViGEmBus | `vigem` | Xbox 360 (XInput) virtual controller via Nefarius ViGEmBus driver | [`../addons/windows/input/VIGEM_WINDOWS_SPEC.md`](../addons/windows/input/VIGEM_WINDOWS_SPEC.md) |
| Linux | uinput | `uinput` | Gamepad evdev device on the existing uinput add-on | [`../addons/linux/input/UINPUT_LINUX_SPEC.md`](../addons/linux/input/UINPUT_LINUX_SPEC.md) |
| macOS | GCVirtual | `gcvirtual` | `GCVirtualController` (Game Controller framework, macOS 14+) | [`../addons/macos/input/GCVIRTUAL_MACOS_SPEC.md`](../addons/macos/input/GCVIRTUAL_MACOS_SPEC.md) |

On Linux the gamepad capability **extends** the existing `uinput` add-on rather
than introducing a new add-on — uinput is the universal evdev injector and
adding a second virtual device for gamepad costs nothing.

---

## Browser Approach

The W3C Gamepad API is poll-based: there is no event for button presses or stick
movement. The client samples on `requestAnimationFrame`, builds a state
snapshot, and only sends when something differs from the last sent state.

```js
let lastSent = new Map(); // index -> serialized state

function pollGamepads(ts) {
    const gps = navigator.getGamepads();
    for (let i = 0; i < gps.length; i++) {
        const gp = gps[i];
        if (!gp) { continue; }
        const cur = serializeState(gp); // 17-byte payload, see Wire Format
        if (lastSent.get(i) !== cur) {
            sendGamepadState(i, cur);
            lastSent.set(i, cur);
        }
    }
    requestAnimationFrame(pollGamepads);
}
requestAnimationFrame(pollGamepads);

window.addEventListener('gamepadconnected',    e => sendConnect(e.gamepad));
window.addEventListener('gamepaddisconnected', e => sendDisconnect(e.gamepad.index));
```

Polling on rAF gives 60-144 Hz sampling matched to the monitor refresh rate.
The "only send on change" rule cuts steady-state bandwidth to near-zero when
the user is idle on a menu screen.

### Standard Gamepad mapping

The W3C **Standard Gamepad** layout (17 buttons + 4 axes) is the wire contract.
The browser remaps Xbox / DualShock / DualSense to this layout when
`gamepad.mapping === "standard"`. Non-standard controllers (`mapping === ""`)
are sent as-is and the host can choose to drop them — the host add-on advertises
only Standard layout in v1.

| W3C button | Index | Notes |
|------------|------:|-------|
| A / Cross  | 0 | bottom face |
| B / Circle | 1 | right face |
| X / Square | 2 | left face |
| Y / Triangle | 3 | top face |
| LB / L1    | 4 |
| RB / R1    | 5 |
| LT / L2    | 6 | also analog axis below |
| RT / R2    | 7 | also analog axis below |
| Back / Share / Select | 8 |
| Start / Options       | 9 |
| LS (left stick click) | 10 |
| RS (right stick click) | 11 |
| D-Pad Up    | 12 |
| D-Pad Down  | 13 |
| D-Pad Left  | 14 |
| D-Pad Right | 15 |
| Home / Guide / PS button | 16 |

Axes: 0=LX, 1=LY, 2=RX, 3=RY (float -1.0 to 1.0).

### Browser limitations (honest)

- **Latency floor ~16-26 ms.** rAF-aligned polling adds up to one frame of
  jitter; the OS→browser path adds another. No way around this without leaving
  the browser.
- **No gyroscope / accelerometer.** Not in the W3C Gamepad spec. Chrome has
  experimental `GamepadPose` but it is not stable.
- **No touchpad** (DualShock 4 / DualSense touchpad is not exposed).
- **No microphone / speaker / battery level.**
- **Rumble compatibility:**
  - Chrome / Edge: `gamepad.vibrationActuator.playEffect("dual-rumble", {...})` — works well.
  - Firefox: legacy `gamepad.hapticActuators[0].pulse(...)` — single motor, less precise.
  - Safari: no haptic API — rumble is silently dropped.
- **High-frequency polling** via `setInterval(fn, 4)` (250 Hz target) is allowed
  but browsers throttle background tabs and may not honor the 4 ms interval.
  Not relied on.

---

## Wire Format

All records use the same 6-byte header from
[`MODULE_INPUT.md`](./MODULE_INPUT.md):
`[Version u8=1][Type u8][Seq u32 LE]`. Gamepad occupies the `0x40-0x4F` range
that MODULE_INPUT reserves.

### Client → Server (binary input records)

**GamepadState — Type `0x40` (23 bytes)** — full state snapshot of one controller

```
6    1   u8     Index    controller index (0-3)
7    4   u32    Buttons  LE bitfield; bit N = standard-gamepad button N (0=A, 1=B, ...)
                          bits 17-31 reserved=0
11   2   i16    LX       LE; -32768..32767, mapped from W3C float -1.0..1.0
13   2   i16    LY       LE
15   2   i16    RX       LE
17   2   i16    RY       LE
19   2   u16    LT       LE; 0..65535, mapped from analog trigger 0.0..1.0
21   2   u16    RT       LE
```

Sent only when the snapshot differs from the previously-sent one for the same
index. Server uses Seq to compute input latency via `InputAck` like every other
input record.

**GamepadConnect — Type `0x41` (9 + IdLen bytes)**

```
6    1   u8     Index
7    1   u8     Mapping  0=standard, 1=non-standard (server should drop)
8    1   u8     IdLen    length of IdString, 0..255
9    *   bytes  IdString UTF-8 identifier from gamepad.id (logging only)
```

**GamepadDisconnect — Type `0x42` (7 bytes)**

```
6    1   u8     Index
```

### Server → Client (rumble feedback, unreliable datagram)

Rumble is a single best-effort **datagram** (Type 15) — like cursor updates and
ping, it rides the unreliable datagram channel, NOT a reliable stream: a rumble
for a button press that is already in the past is useless, so dropping it under
loss is preferable to delaying fresher data. It fits in one datagram (fragment
`0 | LAST`, 8-byte `DatagramHeader`); see [`MODULE_PROTOCOL.md`](../core/MODULE_PROTOCOL.md)
"Channel Model". On the **WebSocket fallback carrier** there is no datagram lane:
the same 8-byte `DatagramHeader` + payload travels as a `0x20`-tagged reliable
message, and the send queue **coalesces per gamepad index** so at most one unsent
rumble per index is ever queued — the loss profile changes, the bytes do not
(see [`MODULE_TRANSPORT.md`](../core/MODULE_TRANSPORT.md) "Send-side queue policy
on the fallback carrier").

**Type `15` FrameTypeGamepadRumble (9-byte payload)**

```
0    1   u8     Index
1    2   u16    WeakMagnitude    LE; 0..65535 → 0.0..1.0 in playEffect
3    2   u16    StrongMagnitude  LE
5    4   u32    DurationMs       LE; clamped to [0, 5000]
```

**`Index` is the recipient's own local index, not the host's global slot.** The
host thinks in global slots 0…`max_controllers-1`; the browser thinks in
`navigator.getGamepads()` indices, and a co-op player driving global slot 3 has
exactly one local pad at index 0. So the server applies the **inverse** of the
co-op remap before it builds the datagram: it looks up which session owns global
slot N, then rewrites `Index` to that session's local index for N — the same
mapping its `inputReader` used in the client→server direction, read backwards.
Sending the global slot instead would ask a one-pad client to rumble index 3,
which its `applyRumble` drops.

Servers send Rumble frames only when the source game requests vibration via the
OS API (ViGEmBus notification / FF_RUMBLE evdev event / GameController haptic),
and only through the three gates below. Each is a no-op, not a datagram the
client ignores:

| Condition | Behavior |
|-----------|----------|
| `[gamepad] allow_rumble = false` | no datagram is emitted at all |
| The loaded gamepad add-on does not set `AddonCaps::RUMBLE` | no sink was installed, so nothing ever fires (logged once at startup, see "Dispatcher Integration") |
| No session currently owns global slot N | no datagram is emitted; the request is dropped at `send_gamepad_rumble` |

A frame that does reach a client for a local index without an active connection
is dropped by the client.

---

## Public Interface

```rust
// crate: featherdesk-input

/// GamepadInjector is the optional contract a gamepad add-on implements
/// (`vigem` on Windows, `gcvirtual` on macOS, `uinput` on Linux — the built-in
/// `enigo` default does NOT cover gamepad). The dispatcher checks for it at
/// runtime; gamepad records are dropped when no GamepadInjector is loaded.
/// Cleanup is RAII (`Drop`) — no Close().
///
/// `: Send` because the Dispatcher that owns it is shared across N session tasks
/// as an `Arc<Mutex<Box<dyn Dispatcher>>>` (MODULE_INPUT). An add-on holding a
/// thread-affine handle (`PVIGEM_CLIENT`) states in its own spec which of
/// MODULE_ABI "Thread requirements"' two options it takes.
pub trait GamepadInjector: Send {
    /// connect creates a virtual controller for index. id is logging-only.
    /// `id`'s boundary form is `RStr<'_>` — borrowed for the call only (MODULE_ABI).
    fn connect(&mut self, index: u8, id: &str) -> Result<(), InputError>;

    /// disconnect tears down the virtual controller for index. Idempotent
    /// (disconnect on a never-connected index is a no-op).
    fn disconnect(&mut self, index: u8) -> Result<(), InputError>;

    /// update applies a state snapshot. The injector handles diffing internally
    /// and only touches the OS device when fields actually changed.
    /// `state`'s boundary form is `RGamepadState`, which mirrors `GamepadState`
    /// field for field (MODULE_ABI).
    fn update(&mut self, state: GamepadState) -> Result<(), InputError>;

    /// caps reports which optional methods this injector serves. For an add-on
    /// this is `ProbeReport.caps` masked to the constructed object's own
    /// `caps()`; there is no in-core gamepad default, so every implementation is
    /// an add-on.
    fn caps(&self) -> abi::AddonCaps;

    /// set_rumble_sink hands the injector the host object it calls when the host
    /// game requests vibration through the OS API (ViGEmBus notification,
    /// evdev FF_RUMBLE, GameController haptics). Returns
    /// `Err(InputError::Unsupported)` unless `caps().has(AddonCaps::RUMBLE)`; the
    /// host checks the bit and does not call it otherwise.
    ///
    /// This replaces a `Box<dyn Fn(..)>` emitter, which cannot cross the add-on
    /// ABI: a host closure's vtable is host-layout and an add-on calling through
    /// it is calling into a foreign layout (MODULE_ABI "Two layers").
    fn set_rumble_sink(&mut self, sink: std::sync::Arc<dyn RumbleSink>) -> Result<(), InputError>;
}

/// RumbleSink is the host side of the rumble path. The Layer-2 adapter wraps this
/// into the ABI's `RumbleSinkBox` before it crosses `dlopen`.
pub trait RumbleSink: Send + Sync {
    /// Called from whatever thread the add-on's OS notification arrives on.
    /// MUST be non-blocking: the implementation is a `try_send` onto a bounded
    /// `tokio::sync::mpsc` channel (capacity **64**). On a full channel the event
    /// is DROPPED — rumble is best-effort, exactly like its datagram — and
    /// `featherdesk_gamepad_rumble_dropped_total` is incremented.
    ///
    /// `index` is the **global slot** the host passed to `connect`; the server
    /// rewrites it to the owning client's local index when it builds the datagram
    /// (see "Server → Client").
    fn emit(&self, index: u8, weak: u16, strong: u16, duration_ms: u32);
}

/// GamepadState is one decoded state snapshot.
pub struct GamepadState {
    pub index: u8,
    pub buttons: u32, // bit N = W3C Standard Gamepad button N (see table above)
    pub lx: i16,      // left stick, -32768..32767
    pub ly: i16,
    pub rx: i16,      // right stick
    pub ry: i16,
    pub lt: u16,      // analog triggers, 0..65535
    pub rt: u16,
}
```

---

## Dispatcher Integration

The input `Dispatcher` (MODULE_INPUT) holds one optional `GamepadInjector`
alongside its `KeyMouseInjector` and optional `TouchInjector`. The
gamepad routing slots into the existing `byte[1]` switch:

```
0x10..0x1F  keyboard   → key_mouse.inject_key
0x20..0x2F  mouse      → key_mouse.inject_{pointer_abs,pointer_rel,button,scroll}
0x30..0x3F  touch      → if let Some(t) = &mut touch { t.inject_touch(..) }
0x40..0x4F  gamepad    → if let Some(g) = &mut gamepad { g.connect|disconnect|update }
                         else: drop (with metric)
```

The pipeline creates one `RumbleSink` implementation holding the sender half of a
64-slot `tokio::sync::mpsc` channel, hands it to the gamepad injector at startup
step 12 (only when `caps().has(AddonCaps::RUMBLE)` **and** `[gamepad] allow_rumble`),
and drains the receiver in a task that calls
`server.send_gamepad_rumble(index, weak, strong, duration_ms)`. `duration_ms` is
clamped to `[0, 5000]` at the drain, before the datagram is built. When the
loaded gamepad add-on does not set `AddonCaps::RUMBLE`, no sink is installed and
the host logs once at `info`: `gamepad add-on '<id>' reports no rumble
capability; [gamepad] allow_rumble has no effect`. That log line is the whole
point of the bit — `gcvirtual` has no haptics path, and without it the operator
sees `allow_rumble = true` produce nothing, with no diagnostic.

The server routes the rumble datagram to the client that **owns gamepad slot
`index`** — the controller for slot 0, or the player client for slots 1…N in
co-op mode (see below) — rewriting `Index` to that client's local index on the
way out. Single-controller mode is just the co-op case with one owner, where the
global slot and the local index are both 0.

---

## Co-op: multiple players, one pad each (v1)

Local couch co-op over the network: up to `max_controllers` people, each on their
own client, each driving one virtual pad on the host. The virtual-pad add-ons
(`vigem`/`uinput`/`gcvirtual`) already create up to 4 pads — the only addition is
letting **multiple clients** own slots, via a **player-slot model** in the server.

- **Roles** (auth message, see [`MODULE_AUTH.md`](../core/MODULE_AUTH.md)): `control`
  (keyboard/mouse **+** gamepad slot 0), `player` (gamepad **only**, slots 1…N),
  `view` (nothing). The `player` role is honored only when `[gamepad] allow_coop`.
- **Slot ownership** is server-side. The controller reserves slot 0; each `player`
  client claims the next free slot on connect (≤ `max_controllers` total). The
  server holds one **bidirectional** map per session, `local index ↔ global slot`,
  and uses it in both directions: C→H it remaps a client's local gamepad index to
  its assigned global slot before routing `GamepadState`/`Connect`/`Disconnect` to
  `gamepad.update(slot)` etc.; H→C it inverts the same map so a rumble for global
  slot N leaves as that owner's local index. A single map read two ways is what
  makes the two directions incapable of disagreeing. A player's keyboard/mouse/
  touch records are **ignored** (gamepad-only).
- **Rumble** for slot N goes back to whichever client owns slot N, and to no one
  else; if the slot is unowned the request is dropped at the server.
- **Disconnect** frees the slot and `disconnect`s the virtual pad — which is what
  clears that slot's held buttons and axes, so a player who vanishes mid-input
  does not leave a trigger down. The server forwards a `GamepadDisconnect`
  (`0x42`) for that player's global slot; the dispatcher drops the slot from its
  pressed-set. **Other slots are untouched** — this is the per-slot path, not
  `Dispatcher::release_all`, which releases everything and is reserved for
  controller-slot transitions and `Drop` ([`MODULE_INPUT.md`](./MODULE_INPUT.md)).
- **One pad per player client** in v1 (the client's primary gamepad). A *single*
  client driving several local pads (slots 0…N from one machine) remains the
  non-co-op path. Multi-keyboard/mouse co-op is **out of scope** (single OS cursor).

This is a server + auth change only — the wire format (gamepad records 0x40–0x4F,
the input stream, rumble datagrams) is unchanged.

---

## Configuration

```toml
[gamepad]
enabled        = false        # opt-in. Even with an add-on loaded, off by default.
max_controllers = 4           # 1..4 — XInput cap on Windows; also the co-op player cap
allow_rumble   = true         # forward host vibration requests to the client
allow_coop     = false        # opt-in: let `player`-role clients each claim a pad slot
```

Per-add-on tuning lives in `[addon_module_<id>]` (see each add-on spec).

---

## Security Considerations

- **Input-role-gated.** Gamepad records are accepted only from the `control`
  client (slot 0) and, when `[gamepad] allow_coop`, from `player` clients (slots
  1…N) — an allowlist on the effective role, never a test against `view`
  ([`MODULE_SERVER.md`](../core/MODULE_SERVER.md) "Role gate table" rows 3-4).
  A `player` client's non-gamepad records (keyboard/mouse/touch) are dropped by
  row 2 — players drive only their assigned pad slot. `view` clients may not open
  the input stream at all (row 1).
- **Bounded state.** `Update` validates Index < max_controllers, refuses
  buttons-bitfield bits ≥ 17, clamps axes/triggers. A malformed snapshot is
  rejected before it touches the OS device.
- **Lifecycle.** Virtual controllers exist only between `Connect` and
  `Disconnect`. `Dispatcher::release_all` — invoked on every controller-slot
  transition and on `Drop` — synthesizes a `Disconnect` for every index that had
  been Connected, so no virtual pad outlives the session that created it.
- **No keyboard/mouse via gamepad.** A gamepad add-on cannot inject other event
  types — the OS-level virtual device is a gamepad and only gamepad codes flow.
- **Rumble forwarding** is opt-in (`allow_rumble`) so paranoid operators can
  prevent the host game from communicating timing back to the client.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | Wire decode of every gamepad record, length validation, range clamp | No |
| Unit | Standard Gamepad button-index bit ordering matches W3C | No |
| Unit | Diff suppression: identical Update is a no-op on the injector | No |
| Integration | Linux: virtual gamepad enumerated by `evtest` and SDL2 | Yes (uinput) |
| Integration | Windows: virtual controller visible to a Steam Input test app | Yes (ViGEmBus) |
| Integration | macOS: virtual controller visible to a GCController test app | Yes (macOS 14+) |
| Unit | Rumble slot inversion: a request for global slot 3 owned by a one-pad player leaves as `Index = 0`; a request for an unowned slot, and any request with `allow_rumble = false`, emits no datagram at all | No |
| Unit | An injector that does not set `AddonCaps::RUMBLE` gets no sink, `set_rumble_sink` returns `Unsupported`, and the inert-`allow_rumble` line is logged exactly once | No |
| Integration | Rumble round-trip: ViGEmBus/uinput FF event → server → client `playEffect` | Yes (Chrome) |
| Mock | Fake gamepad injector for dispatcher tests | No |

---

## What's Out of Scope

These all need a native client and are explicitly NOT in v1:

- **Sub-frame latency.** Requires bypassing the browser Gamepad API.
- **Gyroscope / accelerometer.** Browser doesn't expose; even with a native
  client these need motion fusion that is its own project.
- **Touchpad** (DualShock 4 / DualSense touchpad).
- **DS4 / DualSense HID emulation on Windows.** v1 emulates Xbox 360 only.
  DS4-needing games (rare) get the Xbox 360 mapping instead.
- **Force-feedback effects beyond dual-rumble.** Constant force, springs,
  damping — Standard Gamepad doesn't expose them.

---

## Status

📋 **Specced — not yet built.** Implementation order: protocol records →
extend `uinput` add-on with gamepad device + FF_RUMBLE read loop → `vigem`
add-on → `gcvirtual` add-on → browser polling + rumble client.
