# Windows Input Add-Ons

## Default binary input: in-core `enigo`/`SendInput` (keyboard + mouse)

Unlike capture and encoders, **keyboard + mouse injection is built into core**,
not an add-on. The default Windows binary ships an in-process injector backed by
the `enigo` crate, whose Windows backend is `SendInput`. `SendInput` is chosen
because it is **anti-cheat-safe** (the same approach Sunshine uses). So a stock
Windows build is **fully controllable** (keyboard + mouse) with no input add-on
at all — it is *not* view-only.

The input add-ons below are **opt-in overrides/extensions** of that in-core
default — install them only for capabilities `SendInput` can't provide
(elevated-window / Secure Attention Sequence injection, touch, gamepad).
View-only is a config choice (disable input), not the consequence of omitting an
add-on.

---

## Available input add-ons

| Add-on | Add-on ID | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **Interception** | `interception` | [`./INTERCEPTION_WINDOWS_SPEC.md`](./INTERCEPTION_WINDOWS_SPEC.md) | Opt-in **override** of the in-core kb/mouse default: kernel filter driver that injects below UIPI (elevated windows / full-screen games) + Ctrl+Alt+Del via SendSAS. ⚠️ anti-cheat risk (see spec) | 📋 Specced |
| **Windows Touch Injection** | `win_touch` | [`./WIN_TOUCH_WINDOWS_SPEC.md`](./WIN_TOUCH_WINDOWS_SPEC.md) | Multi-touch / tablet clients — adds the Touch Injection API on top of the in-core keyboard/mouse (`enigo` covers no touch) | 📋 Specced |
| **ViGEmBus (Gamepad)** | `vigem` | [`./VIGEM_WINDOWS_SPEC.md`](./VIGEM_WINDOWS_SPEC.md) | Browser-driven gamepad redirection — Xbox 360 virtual controller via the ViGEmBus driver (`enigo` covers no gamepad) | 📋 Specced |

- `interception` requires a **one-time signed driver install (reboot required)**,
  and carries an **anti-cheat-ban risk** (Vanguard/EAC/BattlEye can flag kernel
  input drivers) — it is an opt-in override, not the default.
- `win_touch` needs **no driver** — it uses the user32 Touch Injection API
  (`InjectTouchInput`, Windows 8+) and adds no install step.
- `vigem` requires a **separate one-time signed driver install (reboot required)**.
  Same UX as `interception` — bundle the installer, prompt on first launch.

### Recommended combinations

| Deployment | Input add-on(s) | Add-on libraries to drop in |
|------------|-----------------|------------------------------|
| Standard remote control (keyboard + mouse) | *(none — in-core `enigo`/`SendInput`)* | `featherdesk-addon-{dxgi_dd,mf_hw,openh264}.dll` |
| Elevated windows / full-screen games / Ctrl+Alt+Del | `interception` (override) | `featherdesk-addon-{dxgi_dd,mf_hw,openh264,interception}.dll` |
| Tablet / touchscreen clients | `win_touch` | `featherdesk-addon-{dxgi_dd,mf_hw,openh264,win_touch}.dll` |
| Casual gaming with gamepad | `vigem` | `featherdesk-addon-{dxgi_dd,mf_hw,openh264,vigem}.dll` |
| View-only monitoring (input disabled in config) | *(none)* | `featherdesk-addon-{dxgi_dd,mf_hw,openh264}.dll` |

Build each add-on separately as a cdylib and drop the resulting `.dll`
into the add-ons directory, e.g.:
```bash
cargo build --release -p featherdesk-addon-interception   # cdylib  featherdesk-addon-interception.dll
```

`interception` / `win_touch` / `vigem` are **complementary, not alternatives** — pick the
combination of capabilities you need.

---

## Ctrl+Alt+Del (Secure Attention Sequence)

`interception` cannot synthesize the Secure Attention Sequence from user space —
Windows blocks SAS injection by design. The add-on routes Ctrl+Alt+Del through
**SendSAS**, which requires the agent to run as a **SYSTEM service** (or the
`SoftwareSASGeneration` policy). Without it every other key works but Ctrl+Alt+Del doesn't.

---

## Runtime input registration

Input add-ons are **complementary, not priority-ranked** — each registers the
capabilities it provides:

```
(always, in core)          → enigo/SendInput provides keyboard + mouse
interception loaded?       → OVERRIDE keyboard + mouse with the driver + add SendSAS
win_touch loaded?          → register touch injection
vigem loaded?              → register gamepad injection (Xbox 360 emulation) + rumble forwarding
none loaded?               → keyboard + mouse still work via in-core enigo/SendInput
```

Records for capabilities the binary doesn't have are silently dropped (touch without
`win_touch`, gamepad without `vigem`). Other capabilities continue to flow normally.

---

## When ready to add a new input backend

1. Write the spec at `specs/addons/windows/input/{NAME}_WINDOWS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Follow CENTRAL_SPEC "Where to register a new add-on" steps 4–7 for the code
   side (root module, `AddonCaps`, cdylib path, probe-order wiring).
