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
  requests back to the user-mode client, which we forward to the browser through
  the host's `RumbleSink`. This add-on therefore sets `AddonCaps::RUMBLE` — it
  serves `set_rumble_sink` **and** actually fires it, which is what the bit
  claims.

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

**Thread affinity.** `PVIGEM_CLIENT` and `PVIGEM_TARGET` are opaque handles onto
an overlapped device handle that ViGEmClient serializes internally, so this add-on
takes the first of the two options MODULE_ABI "Thread requirements" allows: a
`unsafe impl Send` on the injector, justified by ViGEmClient's documented
thread-safety for `vigem_target_x360_update` and `vigem_target_add`/`_remove` on a
connected client. The bare claim is not enough on its own, so it is paired with
one rule: **every call on a given `PVIGEM_TARGET` is serialized by the injector's
own mutex**, including the teardown sequence, so the notification worker thread
and the dispatcher can never touch the same target concurrently. No `unsafe impl
Sync` is claimed — `Sync` comes from the host's `Mutex` around the Dispatcher, not
from this object.

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

The host installs its side of the path at startup step 12 with the Layer-1 call
`set_rumble_sink(sink: RumbleSinkBox)` — a `#[sabi_trait]` object the host
implements and hands down. `Arc<dyn RumbleSink>` is the host-side Layer-2 form
([`specs/interaction/MODULE_GAMEPAD.md`](../../../interaction/MODULE_GAMEPAD.md));
the Layer-2 adapter wraps it into `RumbleSinkBox` before it crosses `dlopen`. A
`Box<dyn Fn(..)>` emitter cannot cross the add-on
ABI: a host closure's vtable is host-layout, and an add-on calling through it is
calling into a foreign layout
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Two layers"). The host
installs the sink only when `caps().has(AddonCaps::RUMBLE)` and
`[gamepad] allow_rumble` is true; `set_rumble_sink` returns
`Err(InputError::Unsupported)` otherwise.

The notification callback runs on a ViGEmClient worker thread. It receives
`(client, target, largeMotor, smallMotor, ledNumber)`. The add-on:

1. Looks up the W3C index for this target (small map kept on Connect).
2. Calls the installed sink:
   `sink.emit(index, weak = smallMotor << 8, strong = largeMotor << 8, duration_ms = 250)`.
3. The host's sink `try_send`s onto its 64-slot channel and returns immediately —
   `emit` never blocks the ViGEmClient worker thread, and a full channel drops the
   event (rumble is best-effort, exactly like its datagram). The pipeline's drain
   task calls `server::send_gamepad_rumble`.

`largeMotor` / `smallMotor` are 8-bit; we left-shift to 16-bit for the wire
format. Duration is fixed at 250 ms because XInput rumble is "set magnitude
until next call"; the client refreshes on each event. The host clamps it to
`[0, 5000]` at the drain, so the value this add-on sends is never the last word.

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

// Layer 1 — what the host actually calls (MODULE_ABI "Root module surface"):
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// `descriptor().kind` is `InputGamepad` (0x08), so this is the one of
/// `InputAddon`'s three constructors that is valid here; `new_key_mouse` and
/// `new_touch` return `PipelineError::AddonBackend`.
/// No virtual controllers are plugged in until `connect(index, id)` is called.
fn new_gamepad(&self, cfg: input::InjectorConfig)
    -> Result<Box<dyn input::GamepadInjector>, PipelineError>;
```

`probe` is non-destructive: it calls `vigem_alloc` + `vigem_connect`, then
`vigem_disconnect` + `vigem_free`. On success it reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(), codecs: RVec::new(),
    caps: AddonCaps(AddonCaps::RUMBLE),   // set_rumble_sink is served AND fired
    displays: RVec::new(),
})
```

**Availability is not an error.** A missing `ViGEmClient.dll`, or
`vigem_connect` returning `VIGEM_ERROR_BUS_NOT_FOUND`, is
`ROk(ProbeReport { available: false, reason: "ViGEmBus driver not installed; run
ViGEmBus_Setup.msi (one-time, reboot required)" })` — never an `RErr`. The
pipeline then starts without gamepad capability and the operator sees an
actionable install hint in the log.

**Set every capability bit this add-on actually serves.** `caps` left at `0` here
means `[gamepad] allow_rumble = true` produces nothing, silently — the failure
mode `AddonCaps::RUMBLE` exists to make visible.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); the constructed object's
`caps()` is authoritative and may be a strict subset of this one.

---

## Error Handling

| Failure | Returned as | Behavior |
|---------|-------------|----------|
| `ViGEmClient.dll` missing | `ProbeReport { available: false, reason }` | Add-on not selected; gamepad records are dropped by the dispatcher |
| Driver not installed (`VIGEM_ERROR_BUS_NOT_FOUND`) | `ProbeReport { available: false, reason }` | Same; log install instructions + installer path |
| Driver disappears between probe and construct | `PipelineError::AddonBackend` from `new_gamepad` | The pipeline starts without gamepad capability |
| `vigem_target_add` fails | `input::InputError::Backend(detail)` from `connect` | That index stays unbound; other indices keep working |
| Out of slots (4 x360 targets) | `input::InputError::Backend("no free x360 slot (4 max)")` from `connect` | The dispatcher refuses extra indices and logs once. `InputError`'s variant set is fixed by the AbiErr registry, so the slot ceiling travels in the `Backend` detail rather than in a variant of its own |
| `vigem_target_x360_update` fails mid-session | `input::InputError::Backend(detail)` | Log + continue; mark the target for re-add on next `connect` |
| Driver handle invalidated (bus removed mid-session) | `input::InputError::DeviceLost` | The pipeline rebuilds the injector; gamepad records are dropped if it cannot |
| `set_rumble_sink` called without `AddonCaps::RUMBLE` | `input::InputError::Unsupported` | Cannot happen on the specified path — this add-on always sets the bit; the host checks it before calling |
| Notification callback delivers after `disconnect` | — | Drop silently (race window between unplug and worker thread drain); the sink is never called for an unbound target |

---

## File Structure

```
addons/vigem/
├── src/
│   ├── lib.rs        // GamepadInjector impl (Rust FFI)
│   ├── buttons.rs    // W3C bit -> XUSB_GAMEPAD_* mask table
│   └── rumble.rs     // notification callback + RumbleSink wiring
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
`set_rumble_sink` -> driver-install first-run flow.
