# Windows Input Add-On: Interception Driver

## Purpose

The `interception` add-on is the Windows keyboard+mouse injection backend for the
core Input module (see [`specs/MODULE_INPUT.md`](../../../specs/MODULE_INPUT.md)).
It implements `input.KeyMouseInjector`.

It uses the **Interception** filter driver (oblitum/Interception) rather than
`SendInput`, because a kernel-level filter driver:

- **Injects below UIPI.** `SendInput` silently fails (returns 0, no useful
  `GetLastError`) when the target foreground app runs at a higher integrity
  level than the server process — the #1 "input randomly stopped working" bug in
  Windows remote-desktop tools. The Interception driver injects at the
  keyboard/mouse class-driver level, so elevated apps and full-screen games
  receive the events.
- **Looks like real hardware.** DirectInput/RawInput games that ignore
  injected `SendInput` events read the Interception-injected stream as genuine
  device input (scan codes, not virtual keys).

Ctrl+Alt+Del (the Secure Attention Sequence) is handled **out-of-band** via
`SendSAS` — see below — because no filter driver can synthesize SAS.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Interception driver + DLL | **LGPL-2.1** | oblitum/Interception |
| Our CGo binding | MIT | |

> **LGPL compliance:** the Interception library is consumed as a **dynamically
> linked DLL** (`interception.dll`) + signed driver. The proprietary FeatherDesk
> binary links to it dynamically, satisfying LGPL-2.1 §6 (user can replace the
> DLL). Do NOT statically link Interception into the main binary. The driver is
> installed separately (see Installation).

---

## How It Works

The Interception driver sits in the device stack above `kbdclass`/`mouclass`.
User-mode code talks to it through `interception.dll`:

```c
#include "interception.h"

InterceptionContext ctx = interception_create_context();

// Interception exposes 10 keyboard slots (ids 1..10) + 10 mouse slots (11..20).
// INTERCEPTION_KEYBOARD(0) resolves to slot 1; the slot need not be bound to
// any real device — injection works either way. The add-on always uses slot 1.
InterceptionDevice kbd   = INTERCEPTION_KEYBOARD(0);
InterceptionDevice mouse = INTERCEPTION_MOUSE(0);

// Key injection (scan code, set 1):
InterceptionKeyStroke ks;
ks.code  = scanCode;                       // from HID-usage → scancode map
ks.state = down ? INTERCEPTION_KEY_DOWN : INTERCEPTION_KEY_UP;
if (extended) ks.state |= INTERCEPTION_KEY_E0;   // arrows, R-Ctrl/Alt, Ins/Del, etc.
interception_send(ctx, kbd, (InterceptionStroke*)&ks, 1);

// Relative mouse move:
InterceptionMouseStroke ms = {0};
ms.flags = INTERCEPTION_MOUSE_MOVE_RELATIVE;   // deltas
ms.x = dx; ms.y = dy;
interception_send(ctx, mouse, (InterceptionStroke*)&ms, 1);

// Absolute mouse move (pointer-lock OFF): use MOVE_ABSOLUTE with 0..65535 range.
// Guard against degenerate dims to avoid divide-by-zero during a Resize race.
if (width  > 1) ms.x = (int)((int64_t)x * 65535 / (width  - 1));
if (height > 1) ms.y = (int)((int64_t)y * 65535 / (height - 1));
ms.flags = INTERCEPTION_MOUSE_MOVE_ABSOLUTE;

// Buttons (W3C index → Interception state bitmask):
//   0 (left)    → INTERCEPTION_MOUSE_LEFT_BUTTON_DOWN  (0x01) / _UP (0x02)
//   1 (middle)  → INTERCEPTION_MOUSE_MIDDLE_BUTTON_DOWN (0x10) / _UP (0x20)
//   2 (right)   → INTERCEPTION_MOUSE_RIGHT_BUTTON_DOWN  (0x04) / _UP (0x08)
//   3 (back)    → INTERCEPTION_MOUSE_BUTTON_4_DOWN      (0x40) / _UP (0x80)
//   4 (forward) → INTERCEPTION_MOUSE_BUTTON_5_DOWN     (0x100) / _UP (0x200)
ms.flags = 0;
ms.state = stateBitFor(button, down);

// Wheel (multiples of 120 = one detent):
//   IMPORTANT: NEGATE Dx/Dy from the wire — the wire is W3C (positive = down/right),
//   Interception's rolling is hardware-style (positive = up/left). See MODULE_INPUT
//   "Wire sign convention" note.
ms.state   = INTERCEPTION_MOUSE_WHEEL;   // or _HWHEEL for horizontal
ms.rolling = -wireDy;                    // for vertical; -wireDx for horizontal
```

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
software-synthesized SAS for security. The add-on implements the optional
`input.SecureAttention` capability; the core dispatcher detects the CAD chord
(see [`../../../../specs/MODULE_INPUT.md`](../../../specs/MODULE_INPUT.md)
"Ctrl+Alt+Del Chord Detection") and calls `SendSAS()`:

```c
// sas.dll — requires SoftwareSASGeneration policy enabled AND the caller
// running as a SYSTEM service (session 0) or with SeTcbPrivilege.
typedef VOID (WINAPI *SendSAS_t)(BOOL AsUser);
SendSAS_t pSendSAS = (SendSAS_t)GetProcAddress(LoadLibrary(L"sas.dll"), "SendSAS");
pSendSAS(FALSE);  // AsUser=FALSE → from a service
```

Implementation in Go satisfies the capability:

```go
// SendSAS implements input.SecureAttention on the interception injector.
// Returns input.ErrSASUnavailable if the policy or privilege blocks it.
func (i *injector) SendSAS() error
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
  `input.ErrSASUnavailable`; the dispatcher logs a one-time warning and drops
  the chord. The constituent Ctrl/Alt keys are never injected as a fallback.

The chord detection algorithm is platform-neutral and lives in the core
dispatcher (any `(L|R)Ctrl + (L|R)Alt + Delete` combination triggers it); only
the `SecureAttention` implementation is Windows-specific.

---

## Held-input release (no stuck keys)

The authoritative owner of "release everything still held" is the core
`Dispatcher.ReleaseAll` (see [`MODULE_INPUT.md`](../../../specs/MODULE_INPUT.md)),
which the server calls on controller disconnect/takeover. This add-on cooperates:

- It injects exactly the key-down/button-down events it is told to, so the
  Dispatcher's pressed-set is accurate.
- Its `Close()` **defensively** sends a `KEY_UP` for any key and a button-up for
  any mouse button it still believes is down (a belt-and-suspenders guard in case
  Close is reached without a prior `ReleaseAll`), so a mid-keypress disconnect
  never leaves Shift/Ctrl/a game-movement key latched at the OS class-driver
  level. Relative-mode pointer state needs no release (it carries no held state).

---

## Build & Distribution

```bash
go build -tags "interception" -o featherdesk.exe ./cmd/server
```

CGo config:

```go
/*
#cgo CFLAGS:  -I${SRCDIR}/vendor/interception
#cgo LDFLAGS: -L${SRCDIR}/vendor/interception -linterception
#include "interception.h"
*/
import "C"
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

```go
// internal/input/interception/interception_windows.go  (build tag: interception)

// Probe returns true if interception.dll loads AND the driver is present.
// Side-effect-free: if a context is allocated to test connectivity, it is
// destroyed before returning.
func Probe() bool

// New creates the injector. Fails if the driver is not installed.
func New(cfg input.InjectorConfig) (input.KeyMouseInjector, error)
```

`Probe` checks `interception_create_context()` returns non-null (driver present),
then **immediately calls `interception_destroy_context`** to avoid leaking a
driver handle on repeated probes. If the driver is missing, `New` returns an
actionable error telling the operator to run the installer.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| Driver not installed | `New` returns error → pipeline starts view-only, logs install instructions |
| `interception.dll` missing | `Probe` false → add-on not selected |
| `SendSAS` blocked by policy | Log warning once; drop Ctrl+Alt+Del chords |
| Injection call fails mid-session | Log + continue (do not crash the stream); surface in metrics |
| Not running as SYSTEM service | SAS unavailable; normal injection still works → warn at startup |

---

## File Structure

```
internal/input/interception/
├── interception_windows.go   // build tag: interception (KeyMouseInjector impl)
├── sas_windows.go            // SendSAS wrapper
├── scancode_map.go           // HID usage → Set 1 scan code (+E0)
├── stub.go                   // build tag: !interception (no-op, never registers)
├── vendor/interception/      // interception.h + import lib (dynamic)
└── interception_test.go
```

---

## Configuration

Reads `[addon_module_interception]` from the TOML config
(see [`specs/MODULE_CONFIG.md`](../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_interception]
keyboard_device = 0    # 0 = first available keyboard device (1..10 to pin a specific one)
mouse_device    = 0    # 0 = first available mouse device (11..20 to pin)
enable_sas      = true # allow Ctrl+Alt+Del via SendSAS (requires SYSTEM service)
```

If absent, defaults apply. Strictly validated only when this add-on is compiled in.

---

## Status

📋 Specced — not yet built. Implementation order: DLL load + probe → key
injection (scan-code map) → mouse abs/rel/button/wheel → SAS wiring →
service-mode packaging.
