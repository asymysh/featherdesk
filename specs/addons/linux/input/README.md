# Linux Input Add-Ons

## Default binary input: kb/mouse via the in-core `enigo` default

Unlike capture and encode, keyboard + mouse injection is **built into core** via
the default `enigo` `KeyMouseInjector` (crate `featherdesk-input`; its Linux
backend is XTEST/libei). So a default Linux binary is **not** view-only for
kb/mouse — it injects via `enigo` whenever `[input] enabled = true` (set
`enabled = false` for a view-only deployment).

The **`uinput` add-on is an opt-in override** of that `enigo` default — a
kernel-level injector for power users who need Wayland/console reach or
gaming-grade input (keyboard, mouse, scroll, **gamepad force-feedback**). It is
not required; without it, kb/mouse still work through the in-core `enigo`
default. Loading it **replaces** the `enigo` `KeyMouseInjector` for the session.

This keeps the core dependency-free for the common case and makes the
device-permission surface of kernel-level injection explicit and opt-in.

---

## Available input add-ons

| Add-on | Add-on ID | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **uinput** | `uinput` | [`./UINPUT_LINUX_SPEC.md`](./UINPUT_LINUX_SPEC.md) | Opt-in override of the `enigo` default — kernel-level injection for Wayland/console + gaming-grade input | 📋 Specced |

`uinput` writes synthetic events into the kernel via `/dev/uinput` — below the
display server, so behavior is identical on X11 and Wayland with no compositor
cooperation. When loaded it overrides the in-core `enigo` `KeyMouseInjector` for
keyboard + mouse, adds **gamepad** (one extra virtual device + an `FF_RUMBLE`
read loop) when [`MODULE_GAMEPAD`](../../../interaction/MODULE_GAMEPAD.md) is
enabled, and is the designated host for future touch injection.

### Recommended combinations

| Deployment | Input backend |
|------------|--------------|
| Standard remote control (X11 or Wayland) | *(none — in-core `enigo` default)* |
| Wayland/console reach or gaming-grade input | `uinput` (overrides `enigo`) |
| View-only monitoring (no injection) | *(none + `[input] enabled = false`)* |

The in-core `enigo` default handles kb/mouse out of the box. To override it with
the kernel-level path, build the `uinput` add-on as a cdylib and drop it into the
add-ons directory —
`cargo build --release -p featherdesk-addon-uinput` (cdylib → `featherdesk-addon-uinput.so`).
Leave it out to stay on the `enigo` default.

> **XTEST is the `enigo` default's X11 backend, not a standalone add-on.** The
> `uinput` override is preferred when you need gamepad support or Wayland/console
> reach, which the user-space `enigo` path cannot always provide.

---

## Device permissions

`/dev/uinput` must be writable by the agent. Grant access by either:

- adding the agent's user to the **`input` group** + a **udev rule** that sets
  `/dev/uinput` group ownership and `0660` mode (recommended), or
- running the agent as **root**.

Without write access the add-on fails closed at startup with a clear error.

---

## Runtime input registration

The `uinput` override is selected only when present and usable; otherwise the
in-core `enigo` default stays in charge:

```
1. uinput loaded AND /dev/uinput writable?       → override enigo: register keyboard + mouse
                                                  (+ gamepad when [gamepad] enabled)
2. Otherwise                                       → in-core enigo default (kb/mouse via XTEST/libei)
```

---

## When ready to add a new input backend

1. Write the spec at `specs/addons/linux/input/{NAME}_LINUX_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Build as a cdylib from `addons/input/{name}/`
5. Wire the capability registration in `MODULE_PIPELINE.md`
