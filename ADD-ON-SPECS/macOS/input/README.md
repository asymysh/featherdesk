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

### Recommended combinations

| Deployment | Input add-on | Build command |
|------------|--------------|---------------|
| Any interactive Mac | `cgevent` | `go build -tags "sck,vt_hw,cgevent"` |
| View-only monitoring (no injection) | *(none)* | `go build -tags "sck,vt_hw"` |

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
1. cgevent compiled in AND Accessibility granted?  → register keyboard + mouse
2. Otherwise                                         → view-only: no input capabilities
```

---

## When ready to add a new input backend

1. Write the spec at `ADD-ON-SPECS/macOS/input/{NAME}_MACOS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the input index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/input/{name}/` with a Go build tag
5. Wire the capability registration in `MODULE_PIPELINE.md`
