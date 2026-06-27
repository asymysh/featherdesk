# Linux Input Add-On: uinput

## Purpose

The `uinput` add-on is the Linux keyboard+mouse injection backend for the core
Input module (see [`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)).
It implements `input.KeyMouseInjector`.

It is the **single, unified Linux input solution**: the kernel `uinput` subsystem
operates below the display server, so **one add-on covers both X11 and Wayland**
with no compositor cooperation. The same mechanism also natively supports gamepad
and multitouch evdev event codes, so when those features arrive they extend this
add-on rather than adding a new one.

> XTEST (X11-only) was rejected: it cannot inject gamepads, breaks on Wayland,
> and X11 is being deprecated by major distros. uinput is strictly more capable.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Linux uinput kernel API | GPL (kernel) — used via syscalls, no linkage | `/dev/uinput` ioctl interface |
| Our implementation | MIT | Pure Go via `syscall`/`golang.org/x/sys/unix` — **no CGo** |

No external library and no CGo: device creation and event writes are plain
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
| `InjectKey(hid, down)` | `EV_KEY` `KEY_*` `value=1/0` + `SYN` |
| `InjectPointerAbs(x, y)` | `EV_ABS ABS_X x` + `EV_ABS ABS_Y y` + `SYN` |
| `InjectPointerRel(dx, dy)` | `EV_REL REL_X dx` + `EV_REL REL_Y dy` + `SYN` |
| `InjectButton(btn, down)` | `EV_KEY BTN_* value=1/0` + `SYN` |
| `InjectScroll(dx, dy, unit)` | `EV_REL REL_WHEEL_HI_RES`/`REL_HWHEEL_HI_RES` + `SYN` |

### HID-usage → KEY_*

The add-on maps the core's neutral HID usage IDs to Linux `KEY_*` codes
(`linux/input-event-codes.h`). The map is generated from the HID Usage Tables to
guarantee full coverage (letters, digits, F1-F24, modifiers, navigation,
editing, numpad, media, international). All registered via `UI_SET_KEYBIT` at
device creation.

### Absolute coordinates & Resize

`ABS_X`/`ABS_Y` ranges MUST equal the stream dimensions (the client already sent
stream-pixel coordinates). On a resolution change the pipeline calls `Resize`,
which:

1. **Releases every held key/button** on the about-to-be-destroyed device
   (`EV_KEY value=0 + SYN_REPORT` for each tracked-down code). Skipping this
   leaves "stuck modifier" / "stuck mouse button" state in the X11/Wayland
   session until the user physically presses+releases the key.
2. Calls `UI_DEV_DESTROY` + close fd.
3. Re-creates the device with new `UI_ABS_SETUP` ranges (the range cannot be
   changed on a live device).

The same "release-all" pass runs at `Close()`.

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
go build -tags "uinput" -o featherdesk ./cmd/server
```

No CGo, no external `-l` libraries. The binary needs **write access to
`/dev/uinput`** at runtime:

- Run as root, OR
- Add the service user to the `input` group and ship a udev rule:
  `KERNEL=="uinput", GROUP="input", MODE="0660"`, then `modprobe uinput`.

The installer/docs cover the udev rule + group membership so the daemon need not
run as full root.

---

## Constructor & Probe

```go
// internal/input/uinput/uinput_linux.go  (build tag: uinput)

// Probe returns true if /dev/uinput is openable for writing.
// Side-effect-free: the FD is closed before return.
func Probe() bool

// New creates and initializes the virtual device at cfg.Width × cfg.Height.
func New(cfg input.InjectorConfig) (input.KeyMouseInjector, error)
```

`Probe` attempts `open("/dev/uinput", O_RDWR|O_CLOEXEC|O_NONBLOCK)` — the **same
mode** the constructor uses, so a probe success implies a setup success. The FD
is closed before returning. Failure (ENOENT / EACCES) means the module isn't
loaded or permissions are wrong → add-on not selected, with a logged hint
(`modprobe uinput` / input-group).

### Device identity

- Name: `FeatherDesk Virtual Input` (configurable)
- Bus: `BUS_VIRTUAL` (0x06)
- Vendor/Product/Version: `0xFEA1 / 0x0001 / 1`

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| `/dev/uinput` absent | `Probe` false → not selected; log `modprobe uinput` hint |
| EACCES on open | `New` error → view-only; log input-group/udev hint |
| ioctl failure during setup | `New` returns descriptive error; no half-created device |
| write() returns short/EBADF | recreate device once; if it fails again, surface error + metric |
| `Resize` recreate fails | keep old device, log error, return error to pipeline |

All injection errors are surfaced to the dispatcher (no silent discard).

---

## File Structure

```
internal/input/uinput/
├── uinput_linux.go        // build tag: uinput (KeyMouseInjector impl)
├── ioctl_linux.go         // UI_* ioctl numbers + input_event/uinput_setup structs
├── keymap.go              // HID usage → KEY_* (generated)
├── stub.go                // build tag: !uinput (no-op, never registers)
└── uinput_test.go         // mock-fd unit tests + /dev/uinput integration tests
```

---

## Configuration

Reads `[addon_module_uinput]` (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_uinput]
device_name = "FeatherDesk Virtual Input"
hi_res_scroll = true    # use REL_WHEEL_HI_RES if the kernel supports it
```

If absent, defaults apply. Strictly validated only when this add-on is compiled in.

---

## Gamepad Capability

This add-on implements `input.GamepadInjector` in addition to
`KeyMouseInjector` — uinput is the universal evdev injector and adding a
gamepad device costs only an extra device-create call. See
[`specs/interaction/MODULE_GAMEPAD.md`](../../../interaction/MODULE_GAMEPAD.md)
for the cross-platform contract.

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
   goroutine harvests that `EV_FF` event, looks up the stashed magnitudes by
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
