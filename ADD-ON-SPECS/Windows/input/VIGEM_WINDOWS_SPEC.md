# Windows Gamepad Add-On: ViGEmBus (Xbox 360 emulation)

## Purpose

The `vigem` add-on is the Windows virtual-gamepad backend for the core Gamepad
module (see [`specs/MODULE_GAMEPAD.md`](../../../../specs/MODULE_GAMEPAD.md)).
It implements `input.GamepadInjector`.

It uses **ViGEmBus** (Nefarius Virtual Gamepad Emulation Bus) - a kernel-mode
KMDF bus driver that creates virtual USB gamepad device nodes the OS treats as
real Xbox 360 controllers. We choose ViGEmBus because:

- **XInput is universal on Windows.** Every modern PC game and emulator reads
  Xbox 360 controllers through XInput. A virtual XInput device gives the widest
  game compatibility for the least integration surface.
- **Real device node.** DirectInput, RawInput, and XInput games all see the
  virtual pad as genuine hardware - no driver-detection workarounds.
- **Rumble feedback works.** ViGEmBus surfaces `XInputSetState` vibration
  requests back to the user-mode client, which we forward to the browser via
  `SetRumbleEmitter`.

> **Project status note (be honest with operators).** The upstream ViGEm
> project was archived by its original author in November 2023. The driver
> itself is stable and continues to work on Windows 10 and 11; community forks
> on GitHub (under the ViGEm Project organization) maintain it. The first-run
> dialog tells the operator they are installing a community-maintained driver.

v1 emulates **Xbox 360 only**. ViGEmClient also exposes DS4 emulation but it
adds complexity for marginal benefit - the vast majority of Windows games
already speak XInput. DS4 support is deferred.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| ViGEmBus driver | **BSD-3-Clause** | community forks of Nefarius/ViGEmBus |
| ViGEmClient DLL | **BSD-3-Clause** | user-mode wrapper around the driver |
| Our CGo binding | MIT | |

`ViGEmClient.dll` is consumed as a dynamic DLL shipped alongside the binary; the
kernel driver is installed separately. BSD-3-Clause requires attribution in the
distributable - the bundled installer and the FeatherDesk About dialog credit
the ViGEm Project and the community maintainers.

---

## How It Works

The driver exposes a single device handle; `ViGEmClient.dll` is the user-mode
wrapper. State pushes are full XUSB report writes - the driver compares against
its last report internally, so a no-change `Update` is cheap.

```c
#include <ViGEm/Client.h>

PVIGEM_CLIENT client = vigem_alloc();
VIGEM_ERROR err = vigem_connect(client);          // opens driver handle
if (err != VIGEM_ERROR_NONE) { /* driver missing or busy */ }

PVIGEM_TARGET pad = vigem_target_x360_alloc();
err = vigem_target_add(client, pad);              // Windows enumerates a new
                                                  // Xbox 360 device on success

XUSB_REPORT report = {0};
report.wButtons      = XUSB_GAMEPAD_A | XUSB_GAMEPAD_DPAD_UP;
report.bLeftTrigger  = 0;                         // 0..255
report.bRightTrigger = 0;
report.sThumbLX      = 0;                         // -32768..32767
report.sThumbLY      = 0;
report.sThumbRX      = 0;
report.sThumbRY      = 0;
vigem_target_x360_update(client, pad, report);

// Rumble notifications: callback fires when a host game calls XInputSetState
// to vibrate the controller. We forward (largeMotor, smallMotor) to the client.
vigem_target_x360_register_notification(client, pad, &OnVibration, userdata);

// Teardown:
vigem_target_remove(client, pad);
vigem_target_free(pad);
vigem_disconnect(client);
vigem_free(client);
```

### Button mapping (W3C Standard Gamepad bit -> XUSB_GAMEPAD_* mask)

The `XUSB_GAMEPAD_*` constants live in the ViGEm `Common.h` header. The add-on
ships a flat 17-entry table, indexed by the W3C bit position:

| W3C bit | W3C name | XUSB constant | Hex |
|--------:|----------|---------------|-----|
|  0 | A           | XUSB_GAMEPAD_A              | 0x1000 |
|  1 | B           | XUSB_GAMEPAD_B              | 0x2000 |
|  2 | X           | XUSB_GAMEPAD_X              | 0x4000 |
|  3 | Y           | XUSB_GAMEPAD_Y              | 0x8000 |
|  4 | LB          | XUSB_GAMEPAD_LEFT_SHOULDER  | 0x0100 |
|  5 | RB          | XUSB_GAMEPAD_RIGHT_SHOULDER | 0x0200 |
|  6 | LT (digital)| -                           | trigger -> bLeftTrigger  |
|  7 | RT (digital)| -                           | trigger -> bRightTrigger |
|  8 | Back        | XUSB_GAMEPAD_BACK           | 0x0020 |
|  9 | Start       | XUSB_GAMEPAD_START          | 0x0010 |
| 10 | LS click    | XUSB_GAMEPAD_LEFT_THUMB     | 0x0040 |
| 11 | RS click    | XUSB_GAMEPAD_RIGHT_THUMB    | 0x0080 |
| 12 | D-Pad Up    | XUSB_GAMEPAD_DPAD_UP        | 0x0001 |
| 13 | D-Pad Down  | XUSB_GAMEPAD_DPAD_DOWN      | 0x0002 |
| 14 | D-Pad Left  | XUSB_GAMEPAD_DPAD_LEFT      | 0x0004 |
| 15 | D-Pad Right | XUSB_GAMEPAD_DPAD_RIGHT     | 0x0008 |
| 16 | Home/Guide  | XUSB_GAMEPAD_GUIDE          | 0x0400 |

Trigger conversion: W3C u16 (0..65535) -> XUSB u8 by right-shift 8. The digital
trigger bits (W3C 6, 7) are folded into the analog channel - any non-zero LT/RT
in the W3C snapshot raises the corresponding analog byte. Sticks pass through
unchanged (both sides are -32768..32767, same handedness).

### Rumble forwarding

The notification callback runs on a ViGEmClient worker thread. It receives
`(client, target, largeMotor, smallMotor, ledNumber)`. The add-on:

1. Looks up the W3C index for this target (small map kept on Connect).
2. Calls the registered emitter: `emit(index, weak=smallMotor<<8, strong=largeMotor<<8, durationMs=250)`.
3. The dispatcher forwards to `server.SendGamepadRumble`.

`largeMotor` / `smallMotor` are 8-bit; we left-shift to 16-bit for the wire
format. Duration is fixed at 250 ms because XInput rumble is "set magnitude
until next call"; the client refreshes on each event.

---

## Build & Distribution

```bash
go build -tags "vigem" -o featherdesk-windows.exe ./cmd/server
```

CGo config:

```go
/*
#cgo CFLAGS:  -I${SRCDIR}/vendor/vigem
#cgo LDFLAGS: -L${SRCDIR}/vendor/vigem -lViGEmClient
#include <ViGEm/Client.h>
*/
import "C"
```

### Installation (driver)

The ViGEmBus driver is a signed kernel driver; install once, reboot once:

- Bundle the signed `ViGEmBus_Setup.msi` (or matching community-fork installer)
  with the FeatherDesk Windows package.
- On first launch, if `vigem_connect()` returns `VIGEM_ERROR_BUS_NOT_FOUND`,
  prompt for a one-time install (UAC elevation, reboot required).
- The driver persists across reboots; subsequent launches detect it and skip.
- The first-run dialog clearly states this is a third-party community-maintained
  driver and links to the upstream project page.

`ViGEmClient.dll` ships alongside the binary (dynamic link).

---

## Constructor & Probe

```go
// internal/input/vigem/vigem_windows.go  (build tag: vigem)

// Probe returns true if ViGEmClient.dll loads AND vigem_connect succeeds
// (driver present and not in a broken state).
func Probe() bool

// New allocates the client, connects to the driver, and returns the injector.
// No virtual controllers are plugged in until Connect(index, id) is called.
func New(cfg input.InjectorConfig) (input.GamepadInjector, error)
```

`Probe` is non-destructive: it calls `vigem_alloc` + `vigem_connect`, then
`vigem_disconnect` + `vigem_free`. If `vigem_connect` returns
`VIGEM_ERROR_BUS_NOT_FOUND`, `Probe` returns false and the pipeline starts
without gamepad capability; the operator sees an actionable install hint in
the log.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| `ViGEmClient.dll` missing | `Probe` false -> add-on not selected |
| Driver not installed (`VIGEM_ERROR_BUS_NOT_FOUND`) | `Probe` false; log install instructions + installer path |
| `vigem_target_add` fails | `Connect` returns error; that index stays unbound; other indices keep working |
| Out of slots (4 x360 + 7 DS4 cap) | `Connect` returns `ErrTooManyControllers`; dispatcher refuses extra indices |
| `vigem_target_x360_update` fails mid-session | log + continue; mark the target for re-add on next Connect |
| Notification callback delivers after `Disconnect` | drop silently (race window between unplug and worker thread drain) |

---

## File Structure

```
internal/input/vigem/
├── vigem_windows.go      // build tag: vigem (GamepadInjector impl, CGo)
├── buttons.go            // W3C bit -> XUSB_GAMEPAD_* mask table
├── rumble.go             // notification callback + emitter wiring
├── stub.go               // build tag: !vigem (no-op, never registers)
├── vendor/vigem/         // ViGEm/Client.h + import lib (dynamic)
└── vigem_test.go
```

---

## Configuration

Reads `[addon_module_vigem]` from the TOML config
(see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_vigem]
driver_check    = true   # at startup verify the driver is loaded; false skips and lets Connect fail
controller_type = "x360" # v1 supports "x360" only; "ds4" is reserved for a future release
```

If absent, defaults apply. Strictly validated only when this add-on is compiled in.

---

## Status

Specced - not yet built. Implementation order: DLL load + probe -> `vigem_connect`
lifecycle -> `Connect`/`Disconnect` per-index target tracking -> button mapping
table + `Update` with diff suppression -> rumble notification callback wired to
`SetRumbleEmitter` -> driver-install first-run flow.
