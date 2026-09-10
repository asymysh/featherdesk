# Windows Input Add-On: Interception Driver

## Purpose

The `interception` add-on is an **opt-in, power-user override** of the default
Windows keyboard+mouse injector for the core Input module (see
[`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)). It
implements the `input::KeyMouseInjector` trait.

> **The default kb/mouse injector is NOT this add-on.** Core ships an in-process
> injector built on the `enigo` crate, whose Windows backend is `SendInput`. It
> is the default because `SendInput` is **anti-cheat-safe** (the same approach
> Sunshine uses), and a stock Windows build is therefore fully controllable
> (not view-only) with no input add-on at all. Install `interception` only when
> you specifically need what `SendInput` cannot do.

It uses the **Interception** filter driver (oblitum/Interception) instead of the
default `SendInput` path, because a kernel-level filter driver:

- **Injects below UIPI.** `SendInput` silently fails (returns 0, no useful
  `GetLastError`) when the target foreground app runs at a higher integrity
  level than the server process — the #1 "input randomly stopped working" bug in
  Windows remote-desktop tools. The Interception driver injects at the
  keyboard/mouse class-driver level, so elevated apps and full-screen games
  receive the events.
- **Looks like real hardware.** DirectInput/RawInput games that ignore
  injected `SendInput` events read the Interception-injected stream as genuine
  device input (scan codes, not virtual keys).
- **Reaches the secure desktop.** Combined with `SendSAS` (below), it can drive
  Ctrl+Alt+Del / the Secure Attention Sequence — something `SendInput` cannot.
  This is the one add-on in the tree that sets `AddonCaps::SECURE_ATTENTION`.

> ⚠️ **Anti-cheat risk — read before enabling.** Interception is a **kernel
> input filter driver**. Anti-cheat systems (Riot Vanguard, EAC, BattlEye) can
> detect, flag, or **ban** machines running kernel input drivers. This is
> precisely why it is **not** the default — the in-core `enigo`/`SendInput` path
> is anti-cheat-safe and covers the common case. Enable `interception` only on
> machines where you accept that risk (e.g. a dedicated remote workstation, not
> a competitive-gaming account).

Ctrl+Alt+Del (the Secure Attention Sequence) is handled **out-of-band** via
`SendSAS` — see below — because no filter driver can synthesize SAS.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Interception driver + DLL | **LGPL-2.1** | oblitum/Interception |
| Our Rust FFI binding | MIT | |

> **LGPL compliance:** the Interception library is consumed as a **dynamically
> linked DLL** (`interception.dll`) + signed driver. The proprietary FeatherDesk
> binary links to it dynamically, satisfying LGPL-2.1 §6 (user can replace the
> DLL). Do NOT statically link Interception into the main binary. The driver is
> installed separately (see Installation).

---

## How It Works

The Interception driver sits in the device stack above `kbdclass`/`mouclass`.
User-mode code talks to it through `interception.dll`:

```rust
// Raw FFI bindings to interception.dll (an `interception-sys`-style module).
let ctx: InterceptionContext = unsafe { interception_create_context() };

// Interception exposes 10 keyboard slots (ids 1..10) + 10 mouse slots (11..20).
// INTERCEPTION_KEYBOARD(0) resolves to slot 1; the slot need not be bound to
// any real device — injection works either way. The add-on always uses slot 1.
let kbd:   InterceptionDevice = interception_keyboard(0);
let mouse: InterceptionDevice = interception_mouse(0);

// Key injection (scan code, set 1):
let mut ks = InterceptionKeyStroke::default();
ks.code  = scan_code;                       // from HID-usage → scancode map
ks.state = if down { INTERCEPTION_KEY_DOWN } else { INTERCEPTION_KEY_UP };
if extended { ks.state |= INTERCEPTION_KEY_E0; }   // arrows, R-Ctrl/Alt, Ins/Del, etc.
unsafe { interception_send(ctx, kbd, &ks as *const _ as *const InterceptionStroke, 1); }

// Relative mouse move:
let mut ms = InterceptionMouseStroke::default();
ms.flags = INTERCEPTION_MOUSE_MOVE_RELATIVE;   // deltas
ms.x = dx; ms.y = dy;
unsafe { interception_send(ctx, mouse, &ms as *const _ as *const InterceptionStroke, 1); }

// Absolute mouse move (pointer-lock OFF): use MOVE_ABSOLUTE with 0..65535 range.
// Guard against degenerate dims to avoid divide-by-zero during a Resize race.
if width  > 1 { ms.x = (x as i64 * 65535 / (width  as i64 - 1)) as i32; }
if height > 1 { ms.y = (y as i64 * 65535 / (height as i64 - 1)) as i32; }
ms.flags = INTERCEPTION_MOUSE_MOVE_ABSOLUTE;

// Buttons (W3C index → Interception state bitmask):
//   0 (left)    → INTERCEPTION_MOUSE_LEFT_BUTTON_DOWN  (0x01) / _UP (0x02)
//   1 (middle)  → INTERCEPTION_MOUSE_MIDDLE_BUTTON_DOWN (0x10) / _UP (0x20)
//   2 (right)   → INTERCEPTION_MOUSE_RIGHT_BUTTON_DOWN  (0x04) / _UP (0x08)
//   3 (back)    → INTERCEPTION_MOUSE_BUTTON_4_DOWN      (0x40) / _UP (0x80)
//   4 (forward) → INTERCEPTION_MOUSE_BUTTON_5_DOWN     (0x100) / _UP (0x200)
ms.flags = 0;
ms.state = state_bit_for(button, down);

// Wheel (multiples of 120 = one detent):
//   Scroll sign: NEGATE dx/dy from the wire — the wire is W3C (positive =
//   down/right), Interception's rolling is hardware-style (positive = up/left).
//   See MODULE_INPUT "Wire sign convention" note.
ms.state   = INTERCEPTION_MOUSE_WHEEL;   // or _HWHEEL for horizontal
ms.rolling = -wire_dy;                   // for vertical; -wire_dx for horizontal
```

### Single-thread requirement

`InterceptionContext` is a driver handle obtained from
`interception_create_context()`, and this add-on makes no `unsafe impl Send`
claim about it. Instead it takes the second of the two options MODULE_ABI
"Thread requirements" allows: the context is **pinned to a dedicated OS thread**
(a `std::thread` the add-on owns for its lifetime), created and destroyed on that
thread, and every `interception_send` is issued from it. The `KeyMouseInjector`
object the host holds is a `Send` proxy that funnels records to that thread over
a bounded `std::sync::mpsc::sync_channel(64)`; on a full channel the record is
dropped and a `warn` is logged at most once per second, exactly as
[`WIN_TOUCH_WINDOWS_SPEC.md`](./WIN_TOUCH_WINDOWS_SPEC.md) "Single-thread
requirement" specifies for touch. `send_sas` crosses the same funnel, so SAS
cannot race an in-flight key.

### HID-usage → scan code

The add-on maps the core's neutral HID usage IDs to **Set 1 scan codes** (with
the E0 extended prefix where needed). The map is generated from the USB HID
Usage Tables §10 and the standard PS/2 scan-code set 1. Extended keys (arrows,
right modifiers, Insert/Delete/Home/End/PageUp/PageDown, Numpad Enter, the Win
keys) set `INTERCEPTION_KEY_E0`.

---

## Ctrl+Alt+Del (Secure Attention Sequence)

The Interception driver **cannot** generate SAS — Windows intercepts the real
hardware Ctrl+Alt+Del in `winlogon`/`csrss` before any filter driver, and refuses
software-synthesized SAS for security. The add-on therefore sets
`AddonCaps::SECURE_ATTENTION` in its `ProbeReport` and serves `send_sas` on the
`KeyMouseInjector` it returns — there is no separate `SecureAttention` trait,
because a `#[sabi_trait]` object cannot be downcast and an optional capability
reached by a downcast has no implementation
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Optional-method
capability flags"). The core dispatcher detects the CAD chord (see
[`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)
"Ctrl+Alt+Del Chord Detection"), checks the bit, and calls `send_sas()`, which
calls `SendSAS()`:

```rust
// sas.dll — requires SoftwareSASGeneration policy enabled AND the caller
// running as a SYSTEM service (session 0) or with SeTcbPrivilege.
// SendSAS is resolved dynamically from sas.dll via the `windows` crate loader.
use windows::core::s;
use windows::Win32::Foundation::BOOL;
use windows::Win32::System::LibraryLoader::{GetProcAddress, LoadLibraryA};

type SendSasFn = unsafe extern "system" fn(as_user: BOOL);
let module = unsafe { LoadLibraryA(s!("sas.dll"))? };
let send_sas: SendSasFn = unsafe { std::mem::transmute(GetProcAddress(module, s!("SendSAS"))) };
unsafe { send_sas(BOOL(0)); } // AsUser=FALSE → from a service
```

The method on `KeyMouseInjector` that the bit gates:

```rust
/// Serves AddonCaps::SECURE_ATTENTION on the interception injector. The receiver
/// is `&mut self`, matching every other injector method.
/// Returns `input::InputError::SasUnavailable` if the policy or privilege blocks
/// it at call time — the bit may be claimed at probe and still be refused later.
fn send_sas(&mut self) -> Result<(), input::InputError>;
```

**Requirements:**
- The FeatherDesk input component must run as a **Windows Service at SYSTEM**
  level (session 0) for `SendSAS(FALSE)` to work, OR be granted `SeTcbPrivilege`.
- The Group Policy `SoftwareSASGeneration` (or registry
  `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System\SoftwareSASGeneration`)
  must be **1 (Services)** or **3 (Both Services and Ease of Access)**. Value
  **0** disables software SAS entirely; value **2** (Ease of Access only) does
  NOT enable services and is treated as disabled by this add-on. The installer
  sets the value to **3** if it is currently 0 or 2.
- If `SendSAS` cannot run (policy / privilege), the function returns
  `input::InputError::SasUnavailable`; the dispatcher logs a one-time warning and drops
  the chord. The constituent Ctrl/Alt keys are never injected as a fallback.
- `probe()` performs the same policy and privilege check, and sets
  `AddonCaps::SECURE_ATTENTION` **only** when `[addon_module_interception]
  enable_sas = true` **and** that check passes. With the bit clear the dispatcher
  never calls `send_sas` at all, and the chord is dropped with one log line
  instead of a failed call per press.

The chord detection algorithm is platform-neutral and lives in the core
dispatcher (any `(L|R)Ctrl + (L|R)Alt + Delete` combination triggers it); only
`send_sas` is Windows-specific. An injector that does not set
`AddonCaps::SECURE_ATTENTION` — the in-core `enigo` default, and every non-Windows
injector — drops the chord there.

---

## Held-input release (no stuck keys)

The authoritative owner of "release everything still held" is the core
`Dispatcher::release_all` (see [`MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md)),
which the server calls on controller disconnect/takeover. This add-on cooperates:

- It injects exactly the key-down/button-down events it is told to, so the
  Dispatcher's pressed-set is accurate.
- Its `Drop` impl **defensively** sends a `KEY_UP` for any key and a button-up for
  any mouse button it still believes is down (a belt-and-suspenders guard in case
  drop is reached without a prior `release_all`), so a mid-keypress disconnect
  never leaves Shift/Ctrl/a game-movement key latched at the OS class-driver
  level. Relative-mode pointer state needs no release (it carries no held state).

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-interception   # cdylib  featherdesk-addon-interception.dll
```

FFI link config (in `build.rs`, plus the `extern` block):

```rust
// build.rs — dynamic link against interception.dll's import library:
//   println!("cargo:rustc-link-search=native=vendor/interception");
//   println!("cargo:rustc-link-lib=dylib=interception");

#[link(name = "interception")]
extern "C" {
    fn interception_create_context() -> InterceptionContext;
    fn interception_destroy_context(ctx: InterceptionContext);
    fn interception_send(
        ctx: InterceptionContext,
        device: InterceptionDevice,
        stroke: *const InterceptionStroke,
        nstroke: u32,
    ) -> i32;
}
```

### Installation (driver)

The Interception **driver** is a kernel driver and must be installed once
(reboot required), exactly like Sunshine's ViGEmBus dependency:

- Bundle the signed Interception installer with the FeatherDesk Windows package.
- On first launch, if the driver is absent, prompt for the one-time install
  (`install-interception.exe /install`) which triggers a UAC elevation + reboot.
- The driver persists across reboots; subsequent launches detect it and skip.

`interception.dll` ships alongside the binary (dynamic link, LGPL compliance).

---

## Constructor & Probe

```rust
// crate: featherdesk-addon-interception (the add-on's cdylib)

// Layer 1 — what the host actually calls (MODULE_ABI "Root module surface"):
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// `descriptor().kind` is `InputKeyMouse` (0x06), so this is the one of
/// `InputAddon`'s three constructors that is valid here; `new_touch` and
/// `new_gamepad` return `PipelineError::AddonBackend`.
fn new_key_mouse(&self, cfg: input::InjectorConfig)
    -> Result<Box<dyn input::KeyMouseInjector>, PipelineError>;
```

`probe` checks `interception_create_context()` returns non-null (driver present),
then **immediately calls `interception_destroy_context`** to avoid leaking a
driver handle on repeated probes. It also runs the SAS policy/privilege check
described above, and reports:

```rust
ROk(ProbeReport {
    available: true, reason: RString::new(), codecs: RVec::new(),
    caps: if sas_enabled_and_permitted { AddonCaps(AddonCaps::SECURE_ATTENTION) }
          else                          { AddonCaps(0) },
    displays: RVec::new(),
})
```

**Availability is not an error.** A missing `interception.dll` or an uninstalled
driver is `ROk(ProbeReport { available: false, reason: "Interception driver not
installed; run install-interception.exe /install (one-time, reboot required)" })`.
`RErr` is reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` here
means Ctrl+Alt+Del is silently dropped, with no error and no warning.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); the constructed object's
`caps()` is authoritative and may be a strict subset of this one — which is what
happens when the SAS policy is changed between probe and construction.

---

## Error Handling

| Failure | Returned as | Behavior |
|---------|-------------|----------|
| Driver not installed | `ProbeReport { available: false, reason }` | Add-on not selected → the in-core `enigo`/`SendInput` injector stays in use; install instructions are logged |
| `interception.dll` missing | `ProbeReport { available: false, reason }` | Same |
| Driver disappears after probe (uninstalled between probe and construct) | `PipelineError::AddonBackend` from `new_key_mouse` | The pipeline falls through; the in-core default keeps the host controllable |
| `SendSAS` blocked by policy or privilege | `input::InputError::SasUnavailable` | The dispatcher logs a one-time warning and drops the chord. The constituent Ctrl/Alt keys are never injected as a fallback |
| `send_sas` called without the bit | `input::InputError::Unsupported` | A host bug or a capability lie — the host clears the bit for the session and logs once (MODULE_ABI "Misbehaving add-ons"). It never happens on the specified path, because the dispatcher checks the bit first |
| Injection call fails mid-session | `input::InputError::Backend(detail)` | Log + continue (do not crash the stream); surface in metrics |
| Driver handle invalidated mid-session | `input::InputError::DeviceLost` | The pipeline rebuilds the injector; injection reverts to the in-core default if it cannot |
| Not running as SYSTEM service | — | SAS unavailable, so `probe` leaves `SECURE_ATTENTION` clear; normal injection still works → warn at startup |

---

## File Structure

```
addons/interception/
├── src/
│   ├── lib.rs            // KeyMouseInjector impl (cfg(windows))
│   ├── sas.rs            // SendSAS wrapper
│   └── scancode_map.rs   // HID usage → Set 1 scan code (+E0)
├── vendor/interception/  // interception.h + import lib (dynamic)
└── build.rs              // link config for interception.dll
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## Configuration

Reads `[addon_module_interception]` from the TOML config
(see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_interception]
keyboard_device = 0    # 0 = first available keyboard device (1..10 to pin a specific one)
mouse_device    = 0    # 0 = first available mouse device (11..20 to pin)
enable_sas      = true # allow Ctrl+Alt+Del via SendSAS (requires SYSTEM service)
```

If absent, defaults apply. Strictly validated only when this add-on is loaded.

---

## Status

📋 Specced — not yet built. Implementation order: DLL load + probe → key
injection (scan-code map) → mouse abs/rel/button/wheel → SAS wiring →
service-mode packaging.
