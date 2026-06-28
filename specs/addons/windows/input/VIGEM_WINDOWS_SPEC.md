# Windows Gamepad Add-On: ViGEmBus (Xbox 360 emulation)

## Purpose

The `vigem` add-on is the Windows virtual-gamepad backend for the core Gamepad
module (see [`specs/interaction/MODULE_GAMEPAD.md`](../../../interaction/MODULE_GAMEPAD.md)).
It implements the `input::GamepadInjector` trait.

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
| Our Rust FFI binding | MIT | |

`ViGEmClient.dll` is consumed as a dynamic DLL shipped alongside the binary; the
kernel driver is installed separately. BSD-3-Clause requires attribution in the
distributable - the bundled installer and the FeatherDesk About dialog credit
the ViGEm Project and the community maintainers.

---

## How It Works

The driver exposes a single device handle; `ViGEmClient.dll` is the user-mode
wrapper. State pushes are full XUSB report writes - the driver compares against
its last report internally, so a no-change `Update` is cheap.

```rust
// Raw FFI bindings to ViGEmClient.dll (a `vigem-client-sys`-style module).
let client: PVIGEM_CLIENT = unsafe { vigem_alloc() };
let err: VIGEM_ERROR = unsafe { vigem_connect(client) }; // opens driver handle
if err != VIGEM_ERROR_NONE { /* driver missing or busy */ }

let pad: PVIGEM_TARGET = unsafe { vigem_target_x360_alloc() };
let err = unsafe { vigem_target_add(client, pad) };      // Windows enumerates a new
                                                         // Xbox 360 device on success

let mut report = XUSB_REPORT::default();
report.wButtons      = XUSB_GAMEPAD_A | XUSB_GAMEPAD_DPAD_UP;
report.bLeftTrigger  = 0;                                // 0..255
report.bRightTrigger = 0;
report.sThumbLX      = 0;                                // -32768..32767
report.sThumbLY      = 0;
report.sThumbRX      = 0;
report.sThumbRY      = 0;
unsafe { vigem_target_x360_update(client, pad, report); }

// Rumble notifications: callback fires when a host game calls XInputSetState
// to vibrate the controller. We forward (large_motor, small_motor) to the client.
unsafe { vigem_target_x360_register_notification(client, pad, on_vibration, userdata); }

// Teardown (driven by the injector's Drop impl):
unsafe {
    vigem_target_remove(client, pad);
    vigem_target_free(pad);
    vigem_disconnect(client);
    vigem_free(client);
}
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
3. The dispatcher forwards to `server::send_gamepad_rumble`.

`largeMotor` / `smallMotor` are 8-bit; we left-shift to 16-bit for the wire
format. Duration is fixed at 250 ms because XInput rumble is "set magnitude
until next call"; the client refreshes on each event.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-vigem   # cdylib  featherdesk-addon-vigem.dll
```

FFI link config (in `build.rs`, plus the `extern` block):

```rust
// build.rs — dynamic link against ViGEmClient.dll's import library:
//   println!("cargo:rustc-link-search=native=vendor/vigem");
//   println!("cargo:rustc-link-lib=dylib=ViGEmClient");

#[link(name = "ViGEmClient")]
extern "C" {
    fn vigem_alloc() -> PVIGEM_CLIENT;
    fn vigem_connect(client: PVIGEM_CLIENT) -> VIGEM_ERROR;
    fn vigem_target_x360_alloc() -> PVIGEM_TARGET;
    fn vigem_target_add(client: PVIGEM_CLIENT, target: PVIGEM_TARGET) -> VIGEM_ERROR;
    fn vigem_target_x360_update(
        client: PVIGEM_CLIENT,
        target: PVIGEM_TARGET,
        report: XUSB_REPORT,
    ) -> VIGEM_ERROR;
    // … target_remove / free / disconnect / free / register_notification
}
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

```rust
// crate: featherdesk-addon-vigem (the add-on's cdylib)

/// `probe` returns true if ViGEmClient.dll loads AND vigem_connect succeeds
/// (driver present and not in a broken state).
pub fn probe() -> bool;

/// Allocate the client, connect to the driver, and return the injector.
/// No virtual controllers are plugged in until `connect(index, id)` is called.
pub fn new(cfg: input::InjectorConfig) -> Result<Box<dyn input::GamepadInjector>, input::Error>;
```

`probe` is non-destructive: it calls `vigem_alloc` + `vigem_connect`, then
`vigem_disconnect` + `vigem_free`. If `vigem_connect` returns
`VIGEM_ERROR_BUS_NOT_FOUND`, `probe` returns false and the pipeline starts
without gamepad capability; the operator sees an actionable install hint in
the log.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| `ViGEmClient.dll` missing | `probe` false -> add-on not selected |
| Driver not installed (`VIGEM_ERROR_BUS_NOT_FOUND`) | `probe` false; log install instructions + installer path |
| `vigem_target_add` fails | `connect` returns error; that index stays unbound; other indices keep working |
| Out of slots (4 x360 + 7 DS4 cap) | `connect` returns `Error::TooManyControllers`; dispatcher refuses extra indices |
| `vigem_target_x360_update` fails mid-session | log + continue; mark the target for re-add on next `connect` |
| Notification callback delivers after `disconnect` | drop silently (race window between unplug and worker thread drain) |

---

## File Structure

```
addons/vigem/
├── src/
│   ├── lib.rs        // GamepadInjector impl (Rust FFI)
│   ├── buttons.rs    // W3C bit -> XUSB_GAMEPAD_* mask table
│   └── rumble.rs     // notification callback + emitter wiring
├── vendor/vigem/     // ViGEm/Client.h + import lib (dynamic)
└── build.rs          // link config for ViGEmClient.dll
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## Configuration

Reads `[addon_module_vigem]` from the TOML config
(see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_vigem]
driver_check    = true   # at startup verify the driver is loaded; false skips and lets Connect fail
controller_type = "x360" # v1 supports "x360" only; "ds4" is reserved for a future release
```

If absent, defaults apply. Strictly validated only when this add-on is loaded.

---

## Status

Specced - not yet built. Implementation order: DLL load + probe -> `vigem_connect`
lifecycle -> `connect`/`disconnect` per-index target tracking -> button mapping
table + `update` with diff suppression -> rumble notification callback wired to
`set_rumble_emitter` -> driver-install first-run flow.
