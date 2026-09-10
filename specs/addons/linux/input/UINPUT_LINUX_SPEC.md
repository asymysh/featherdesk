# Linux Input Add-On: uinput

## Purpose

The `uinput` add-on is an **opt-in override of the in-core `enigo` default** for
the core Input module (see [`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)).
It implements `input::KeyMouseInjector`; when loaded it **replaces** the built-in
`enigo` `KeyMouseInjector` for the session. It is for power users — it is not
required, and without it kb/mouse still work through the `enigo` default.

As a kernel-level injector, the `uinput` subsystem operates below the display
server, so this one add-on reaches **both X11 and Wayland** (and the console)
with no compositor cooperation — the main reason to override `enigo`. The same
mechanism also natively supports gamepad and multitouch evdev event codes, so
when those features arrive they extend this add-on rather than adding a new one.

> The `enigo` default's X11 backend (XTEST) cannot inject gamepads and does not
> reach Wayland/console; the `uinput` override is strictly more capable for those
> power-user cases.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Linux uinput kernel API | GPL (kernel) — used via syscalls, no linkage | `/dev/uinput` ioctl interface |
| Our implementation | MIT | Pure Rust via the `nix` crate (raw `ioctl`/`write`) — **no C SDK** |

No external library and no C bindings: device creation and event writes are plain
`ioctl`/`write` syscalls.

---

## How It Works

```
open("/dev/uinput", O_RDWR|O_CLOEXEC|O_NONBLOCK)
  → ioctl(UI_SET_EVBIT, EV_KEY)          // keys + buttons
  → ioctl(UI_SET_EVBIT, EV_REL)          // relative mouse + wheel
  → ioctl(UI_SET_EVBIT, EV_ABS)          // absolute mouse
  → ioctl(UI_SET_EVBIT, EV_SYN)
  → ioctl(UI_SET_KEYBIT, KEY_*)          // every key in the HID→KEY map
  → ioctl(UI_SET_KEYBIT, BTN_LEFT/RIGHT/MIDDLE/SIDE/EXTRA)
  → ioctl(UI_SET_RELBIT, REL_X)          // REQUIRED for relative mouse (pointer-lock)
  → ioctl(UI_SET_RELBIT, REL_Y)          // REQUIRED for relative mouse
  → ioctl(UI_SET_RELBIT, REL_WHEEL_HI_RES, REL_HWHEEL_HI_RES)
  → ioctl(UI_SET_ABSBIT, ABS_X, ABS_Y)
  → ioctl(UI_DEV_SETUP, uinput_setup{name, id})       // modern setup ioctl
  → ioctl(UI_ABS_SETUP, {ABS_X, 0..width-1})          // abs ranges = stream dims
  → ioctl(UI_ABS_SETUP, {ABS_Y, 0..height-1})
  → ioctl(UI_DEV_CREATE)                  // device appears under /dev/input/eventN
```

Every injection is one or more `input_event` writes terminated by an
`EV_SYN/SYN_REPORT`:

| Method | Events |
|--------|--------|
| `inject_key(hid, down)` | `EV_KEY` `KEY_*` `value=1/0` + `SYN` |
| `inject_pointer_abs(x, y)` | `EV_ABS ABS_X x` + `EV_ABS ABS_Y y` + `SYN` |
| `inject_pointer_rel(dx, dy)` | `EV_REL REL_X dx` + `EV_REL REL_Y dy` + `SYN` |
| `inject_button(btn, down)` | `EV_KEY BTN_* value=1/0` + `SYN` |
| `inject_scroll(dx, dy, unit)` | `EV_REL REL_WHEEL_HI_RES`/`REL_HWHEEL_HI_RES` + `SYN` |

### HID-usage → KEY_*

The add-on maps the core's neutral HID usage IDs to Linux `KEY_*` codes
(`linux/input-event-codes.h`). The map is generated from the HID Usage Tables to
guarantee full coverage (letters, digits, F1-F24, modifiers, navigation,
editing, numpad, media, international). All registered via `UI_SET_KEYBIT` at
device creation.

### Absolute coordinates & Resize

`ABS_X`/`ABS_Y` ranges MUST equal the stream dimensions (the client already sent
stream-pixel coordinates). On a resolution change the pipeline calls `resize`,
which:

1. **Releases every held key/button** on the about-to-be-destroyed device
   (`EV_KEY value=0 + SYN_REPORT` for each tracked-down code). Skipping this
   leaves "stuck modifier" / "stuck mouse button" state in the X11/Wayland
   session until the user physically presses+releases the key.
2. Calls `UI_DEV_DESTROY` + close fd.
3. Re-creates the device with new `UI_ABS_SETUP` ranges (the range cannot be
   changed on a live device).

The same "release-all" pass runs in `Drop`. It is defensive only: the
authoritative owner of held-input state is `input::Dispatcher::release_all`,
which the server invokes on every controller-slot transition
([`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)) —
this add-on's `Drop` and `resize` passes exist so a device teardown cannot strand
a key even between those calls.

### Scroll sign

The wire format is W3C (positive `Dy` = scroll down). Linux `REL_WHEEL` /
`REL_WHEEL_HI_RES` use the opposite convention (positive = scroll up). The
add-on **negates** wire `Dx`/`Dy` before emitting `REL_HWHEEL` / `REL_WHEEL`.

### High-resolution scroll

Uses `REL_WHEEL_HI_RES` / `REL_HWHEEL_HI_RES` (kernel 5.0+, 120 units = 1
detent) to preserve trackpad/pixel-precise scroll magnitude. Falls back to
`REL_WHEEL`/`REL_HWHEEL` integer detents on older kernels (probe at startup).

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-uinput   # cdylib → featherdesk-addon-uinput.so
```

The uinput payload is pure Rust (no external `-l` libraries); it exports the add-on's `abi_stable` root module as its entry point — no C SDK is linked. The host needs **write access to
`/dev/uinput`** at runtime:

- Run as root, OR
- Add the service user to the `input` group and ship a udev rule:
  `KERNEL=="uinput", GROUP="input", MODE="0660"`, then `modprobe uinput`.

The installer/docs cover the udev rule + group membership so the daemon need not
run as full root.

---

## Constructor & Probe

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-uinput   (cfg(target_os = "linux"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// `descriptor().kind` is `InputKeyMouse` (0x06), so this is the constructor
/// `InputAddon` calls for the keyboard/mouse slot; it creates and initializes
/// the virtual device at cfg.width × cfg.height. `new_touch` returns
/// `PipelineError::AddonBackend` — touch is a future extension here, not a v1
/// capability.
fn new_key_mouse(&self, cfg: input::InjectorConfig)
    -> Result<Box<dyn input::KeyMouseInjector>, PipelineError>;

/// The gamepad extension (see "Gamepad Capability"). It is the same add-on
/// filling startup step 8's `Gamepad` slot, and it creates one independent
/// uinput device per controller index.
fn new_gamepad(&self, cfg: input::InjectorConfig)
    -> Result<Box<dyn input::GamepadInjector>, PipelineError>;
```

`probe` attempts `open("/dev/uinput", O_RDWR|O_CLOEXEC|O_NONBLOCK)` — the **same
mode** the constructor uses, so a probe success implies a setup success. The FD
is closed before returning. It reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(), codecs: RVec::new(),
    caps: AddonCaps(AddonCaps::RUMBLE),   // set_rumble_sink is served AND fired
                                          //   (the FF_RUMBLE read loop below)
    displays: RVec::new(),
})
```

**Availability is not an error.** ENOENT or EACCES on `/dev/uinput` — the module
isn't loaded, or the permissions are wrong — is
`ROk(ProbeReport { available: false, reason: "/dev/uinput not writable; run
modprobe uinput and add the service user to the input group" })`, never an
`RErr`. The in-core `enigo` default then stays in use and the operator sees an
actionable hint in the log.

**Set every capability bit this add-on actually serves.** `caps` left at `0` here
means `[gamepad] allow_rumble = true` produces nothing, silently — the failure
mode `AddonCaps::RUMBLE` exists to make visible.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); the constructed object's
`caps()` is authoritative and may be a strict subset of this one.

### Device identity

- Name: `FeatherDesk Virtual Input` (configurable)
- Bus: `BUS_VIRTUAL` (0x06)
- Vendor/Product/Version: `0xFEA1 / 0x0001 / 1`

---

## Error Handling

| Failure | Returned as | Behavior |
|---------|-------------|----------|
| `/dev/uinput` absent | `ProbeReport { available: false, reason }` | Not selected (stay on `enigo` default); log the `modprobe uinput` hint |
| EACCES on open | `PipelineError::AddonBackend` from `new_key_mouse` | Fall back to the in-core `enigo` default; log the input-group/udev hint |
| ioctl failure during setup | `PipelineError::AddonBackend` from the constructor | Descriptive `detail`; no half-created device |
| write() returns short/EBADF | `input::InputError::Backend(detail)` if the retry fails | Recreate the device once; if it fails again, surface the error + metric |
| `resize` recreate fails | `input::InputError::Backend(detail)` | Keep the old device, log, return the error to the pipeline |

Construction failures are load-domain (`PipelineError`), hot-path failures are
`input::InputError`; both cross the ABI as an `AbiErr` code plus an
`AbiError.detail` the host logs ([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md)
"AbiErr registry"). There is no add-on-private error type, and all injection
errors are surfaced to the dispatcher (no silent discard).

---

## File Structure

```
addons/input/uinput/
├── uinput.rs              // KeyMouseInjector impl (cfg(target_os = "linux"))
├── ioctl.rs               // UI_* ioctl numbers + input_event/uinput_setup structs
├── keymap.rs              // HID usage → KEY_* (generated)
└── tests.rs               // mock-fd unit tests + /dev/uinput integration tests
```

---

## Configuration

Reads `[addon_module_uinput]` (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_uinput]
device_name = "FeatherDesk Virtual Input"
hi_res_scroll = true    # use REL_WHEEL_HI_RES if the kernel supports it
```

If absent, defaults apply. Strictly validated only when this add-on is loaded.

---

## Gamepad Capability

This add-on implements `input::GamepadInjector` in addition to
`KeyMouseInjector` — uinput is the universal evdev injector and adding a
gamepad device costs only an extra device-create call. See
[`specs/interaction/MODULE_GAMEPAD.md`](../../../interaction/MODULE_GAMEPAD.md)
for the cross-platform contract.

It therefore sets **`AddonCaps::RUMBLE`** at probe: `set_rumble_sink` is served
*and* actually fired, by the `FF_RUMBLE` read loop below. A stub that stored the
sink and never emitted would have to leave the bit clear (MODULE_ABI
"Optional-method capability flags").

One independent uinput device is created **per controller index** so SDL/games
enumerate them as separate gamepads. Each gamepad device registers:

```
EV_KEY: BTN_SOUTH (A), BTN_EAST (B), BTN_WEST (X), BTN_NORTH (Y),
        BTN_TL (LB), BTN_TR (RB), BTN_TL2 (LT-as-button), BTN_TR2 (RT-as-button),
        BTN_SELECT, BTN_START, BTN_THUMBL, BTN_THUMBR, BTN_MODE (Home)
EV_ABS: ABS_X, ABS_Y          (left stick, -32768..32767)
        ABS_RX, ABS_RY        (right stick)
        ABS_Z, ABS_RZ         (analog triggers, 0..255 after downscale from u16)
        ABS_HAT0X, ABS_HAT0Y  (D-pad as hat axis, -1/0/+1)
EV_FF:  FF_RUMBLE             (so the kernel forwards game vibration requests)
```

Bus: `BUS_VIRTUAL`. VID/PID/Version: `0xFEA1 / 0x0002 / 1` (distinct from the
keyboard/mouse device). Name: `FeatherDesk Virtual Gamepad N` where N is the
controller index.

### Rumble forwarding (FF_RUMBLE handshake + read loop)

Games drive vibration through the evdev force-feedback protocol, which is a
**two-phase** handshake — uploading an effect is distinct from playing it. The
add-on must implement both phases or the kernel blocks the game:

1. **Enable FF at create time.** Set `UI_SET_EVBIT(EV_FF)` + `UI_SET_FFBIT(FF_RUMBLE)`
   and a non-zero `ff_effects_max` in the `uinput_user_dev`/`uinput_setup` so the
   kernel advertises FF capacity.
2. **Upload handshake (mandatory response).** When the game calls
   `ioctl(EVIOCSFF)`, the kernel emits an `EV_UINPUT / UI_FF_UPLOAD` event on the
   uinput fd. The add-on MUST: `ioctl(UI_BEGIN_FF_UPLOAD)` → read the
   `uinput_ff_upload` (contains the `ff_effect`, kernel-assigned `effect.id`) →
   stash `{id → strong_magnitude, weak_magnitude (u16), replay.length ms}` →
   `ioctl(UI_END_FF_UPLOAD)` with `retval = 0`. **Skipping `UI_END_FF_UPLOAD`
   leaves the game blocked in its ioctl.** `UI_FF_ERASE` (with `UI_BEGIN/END_FF_ERASE`)
   removes a stashed effect.
3. **Play trigger is a SEPARATE event.** Upload does NOT mean "rumble now." The
   game then writes an `EV_FF` input event (`code = effect.id`, `value = 1` to
   start, `0` to stop) which the kernel forwards to the uinput fd. The read-loop
   task harvests that `EV_FF` event, looks up the stashed magnitudes by
   `code`, and invokes the registered rumble emitter (start) — or emits a
   zero-magnitude stop on `value == 0`.

The pipeline forwards the emitter call to the server, which sends a
`FrameTypeGamepadRumble` **datagram** to the controller client (see
[`MODULE_GAMEPAD.md`](../../../interaction/MODULE_GAMEPAD.md)).

### Touch (future)

Touch is also a future extension here (not a new add-on): same uinput device
pattern using the MT-Protocol-B codes (`ABS_MT_SLOT` / `ABS_MT_TRACKING_ID` /
`ABS_MT_POSITION_X/Y`). Not in v1 (touch is Windows-only via `win_touch` for now).

---

## Status

📋 Specced — not yet built. Implementation order: device create + abs setup →
key injection (keymap) → mouse abs/rel/button → hi-res scroll → Resize lifecycle
→ gamepad device + FF_RUMBLE read loop (when MODULE_GAMEPAD lands).
