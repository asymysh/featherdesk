# macOS Input Add-Ons

## Default binary input: NONE — view-only

Mirroring the capture and encoder architecture: the default macOS binary ships
with **zero input backends**. With no input add-on compiled in, the core binary
is **view-only** — it streams the desktop but injects nothing. Input injection
is an opt-in build-tagged add-on. Compose the binary you need by choosing
capture + encoder + input add-on(s).

In practice every interactive macOS deployment compiles in `cgevent` — the only
supported injection path. The pluggable structure exists for cross-platform symmetry.

---

## Available input add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **CGEventPost** | `cgevent` | [`./CGEVENT_MACOS_SPEC.md`](./CGEVENT_MACOS_SPEC.md) | Always — keyboard + mouse injection via the Core Graphics event API | 📋 Specced |
| **GCVirtualController** | `gcvirtual` | [`./GCVIRTUAL_MACOS_SPEC.md`](./GCVIRTUAL_MACOS_SPEC.md) | Browser gamepad redirection (macOS 14+) — virtual controller via the Game Controller framework | 📋 Specced |

### Recommended combinations

| Deployment | Input add-on(s) | Build command |
|------------|-----------------|---------------|
| Any interactive Mac | `cgevent` | `go build -tags "sck,vt_hw,cgevent"` |
| Casual gaming with gamepad (macOS 14+) | `cgevent,gcvirtual` | `go build -tags "sck,vt_hw,cgevent,gcvirtual"` |
| View-only monitoring (no injection) | *(none)* | `go build -tags "sck,vt_hw"` |

`gcvirtual` only reaches apps using Apple's GameController framework. SDL2-based
games that read IOKit HID directly do **not** see the virtual controller — this
is a real limitation, document it in user-facing release notes.

---

## Permission requirements

`cgevent` calls `CGEventPost`, which macOS gates behind **Accessibility**
permission. The app must be:

- **signed + notarized**, and
- granted **Accessibility** in **System Settings → Privacy & Security →
  Accessibility** (one-time, per machine).

Until Accessibility is granted, injected events are silently swallowed by the
OS — the spec covers detecting and surfacing this state to the operator.

---

## No touch on macOS

There is **no public touch-injection API** on macOS — `UITouch` is iOS-only with
no AppKit equivalent. macOS input is keyboard + mouse only; touch clients fall
back to mouse emulation. There's also no vendor fragmentation to add backends for.

---

## Runtime input registration

With a single input add-on the selection is trivial:

```
1. cgevent compiled in AND Accessibility granted?    → register keyboard + mouse
2. gcvirtual compiled in AND macOS 14+?              → register gamepad injection
3. Neither?                                            → view-only: no input capabilities
```

Each add-on is independent — `gcvirtual` doesn't require `cgevent`, though a typical
gaming build compiles both.

---

## When ready to add a new input backend

1. Write the spec at `specs/addons/macos/input/{NAME}_MACOS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/input/{name}/` with a Go build tag
5. Wire the capability registration in `MODULE_PIPELINE.md`
