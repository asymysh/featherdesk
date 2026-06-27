# Windows Input Add-Ons

## Default binary input: NONE — view-only

Mirroring the capture and encoder architecture: the default Windows binary
ships with **zero input backends**. With no input add-on compiled in, the core
binary is **view-only** — it streams the desktop but injects nothing. Every
input-injection method is an opt-in build-tagged add-on. Compose the binary you
need by choosing capture + encoder + input add-on(s).

This keeps the view-only build tiny and dependency-free (ideal for monitoring
kiosks) and makes input injection's driver/permission surface opt-in, not always-on.

---

## Available input add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **Interception** | `interception` | [`./INTERCEPTION_WINDOWS_SPEC.md`](./INTERCEPTION_WINDOWS_SPEC.md) | Keyboard + mouse injection, plus Ctrl+Alt+Del via SendSAS — the universal default | 📋 Specced |
| **Windows Touch Injection** | `win_touch` | [`./WIN_TOUCH_WINDOWS_SPEC.md`](./WIN_TOUCH_WINDOWS_SPEC.md) | Multi-touch / tablet clients — layers the Touch Injection API on top of keyboard/mouse | 📋 Specced |
| **ViGEmBus (Gamepad)** | `vigem` | [`./VIGEM_WINDOWS_SPEC.md`](./VIGEM_WINDOWS_SPEC.md) | Browser-driven gamepad redirection — Xbox 360 virtual controller via the ViGEmBus driver | 📋 Specced |

- `interception` requires a **one-time signed driver install (reboot required)**.
- `win_touch` needs **no driver** — it uses the user32 Touch Injection API
  (`InjectTouchInput`, Windows 8+) and adds no install step.
- `vigem` requires a **separate one-time signed driver install (reboot required)**.
  Same UX as `interception` — bundle the installer, prompt on first launch.

### Recommended combinations

| Deployment | Input add-on(s) | Build command |
|------------|-----------------|---------------|
| Standard remote control (keyboard + mouse) | `interception` | `go build -tags "dxgi_dd,mf_hw,openh264,interception"` |
| Tablet / touchscreen clients | `interception,win_touch` | `go build -tags "dxgi_dd,mf_hw,openh264,interception,win_touch"` |
| Casual gaming with gamepad | `interception,vigem` | `go build -tags "dxgi_dd,mf_hw,openh264,interception,vigem"` |
| View-only monitoring (no injection) | *(none)* | `go build -tags "dxgi_dd,mf_hw,openh264"` |

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
interception compiled in?  → register keyboard + mouse + SendSAS
win_touch compiled in?     → register touch injection
vigem compiled in?         → register gamepad injection (Xbox 360 emulation) + rumble forwarding
none compiled in?          → view-only: input channel advertises no capabilities
```

Records for capabilities the binary doesn't have are silently dropped (touch without
`win_touch`, gamepad without `vigem`). Other capabilities continue to flow normally.

---

## When ready to add a new input backend

1. Write the spec at `specs/addons/windows/input/{NAME}_WINDOWS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/input/{name}/` with a Go build tag
5. Wire the capability registration in `MODULE_PIPELINE.md`
