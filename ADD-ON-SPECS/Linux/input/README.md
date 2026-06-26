# Linux Input Add-Ons

## Default binary input: NONE — view-only

Mirroring the capture and encoder architecture: the default Linux binary ships
with **zero input backends**. With no input add-on compiled in, the core binary
is **view-only** — it streams the desktop but injects nothing. Input injection
is an opt-in build-tagged add-on. Compose the binary you need by choosing
capture + encoder + input add-on(s).

This keeps the view-only build tiny and dependency-free, and makes the
device-permission surface of input injection explicit per deployment.

---

## Available input add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **uinput** | `uinput` | [`./UINPUT_LINUX_SPEC.md`](./UINPUT_LINUX_SPEC.md) | Always — the single unified injection path for X11 **and** Wayland | 📋 Specced |

One add-on covers everything. `uinput` writes synthetic events into the kernel
via `/dev/uinput` — below the display server, so behavior is identical on X11
and Wayland with no compositor cooperation. It handles keyboard + mouse today
and is the designated host for **future touch and gamepad** injection.

### Recommended combinations

| Deployment | Input add-on | Build command |
|------------|--------------|---------------|
| Any Linux host (X11 or Wayland) | `uinput` | `go build -tags "kms_egl,libva,openh264,uinput"` |
| View-only monitoring (no injection) | *(none)* | `go build -tags "kms_egl,libva,openh264"` |

There is no per-display-server choice to make — `uinput` is the answer on both.

> **XTEST was rejected** — it's X11-only, has no gamepad support, and X11 is
> dying. `uinput` supersedes it on every axis and works on Wayland too.

---

## Device permissions

`/dev/uinput` must be writable by the agent. Grant access by either:

- adding the agent's user to the **`input` group** + a **udev rule** that sets
  `/dev/uinput` group ownership and `0660` mode (recommended), or
- running the agent as **root**.

Without write access the add-on fails closed at startup with a clear error.

---

## Runtime input registration

With a single input add-on the selection is trivial:

```
1. uinput compiled in AND /dev/uinput writable?  → register keyboard + mouse
2. Otherwise                                       → view-only: no input capabilities
```

---

## When ready to add a new input backend

1. Write the spec at `ADD-ON-SPECS/Linux/input/{NAME}_LINUX_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/input/{name}/` with a Go build tag
5. Wire the capability registration in `MODULE_PIPELINE.md`
