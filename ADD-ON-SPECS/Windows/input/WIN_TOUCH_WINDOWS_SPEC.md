# Windows Input Add-On: Touch Injection

## Purpose

The `win_touch` add-on adds multitouch injection on Windows. It implements
`input.TouchInjector` (see [`specs/MODULE_INPUT.md`](../../../../specs/MODULE_INPUT.md))
and is **separate** from the `interception` keyboard/mouse add-on — they compose:
a Windows build that wants both compiles `interception,win_touch`.

Touch is Windows-only in v1. Linux multitouch is a future extension of the
`uinput` add-on; macOS has no public touch-injection API.

Pen/stylus from the client is **downgraded to touch** here: the Windows Touch
Injection API carries a `pressure` field (0-1024), so pen pressure is preserved;
tilt/twist are dropped.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| Windows Touch Injection API (`user32`) | Windows system | Linked, not redistributed |
| Our binding | MIT | Pure `syscall` to `user32.dll` — no CGo needed |

---

## How It Works

Two-step Windows API (`user32.dll`):

```c
// Once at startup: max 10 simultaneous contacts, no system visual feedback.
InitializeTouchInjection(10, TOUCH_FEEDBACK_NONE);

// Per frame: an array of POINTER_TOUCH_INFO, one per active contact.
POINTER_TOUCH_INFO c = {0};
c.pointerInfo.pointerType   = PT_TOUCH;
c.pointerInfo.pointerId     = contactId;                 // stable per finger
c.pointerInfo.ptPixelLocation.x = screenX;               // physical screen px
c.pointerInfo.ptPixelLocation.y = screenY;
c.pointerInfo.pointerFlags  = POINTER_FLAG_DOWN          // first contact
                            | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT;
c.touchFlags = TOUCH_FLAG_NONE;
c.touchMask  = TOUCH_MASK_CONTACTAREA | TOUCH_MASK_PRESSURE;
c.rcContact  = (RECT){screenX-2, screenY-2, screenX+2, screenY+2};
c.pressure   = pressure0to1024;
InjectTouchInput(count, contacts);
```

### Contact state machine

Windows enforces a strict per-contact lifecycle; violating it returns
`ERROR_INVALID_PARAMETER`:

```
DOWN (POINTER_FLAG_DOWN|INRANGE|INCONTACT)
  → UPDATE (POINTER_FLAG_UPDATE|INRANGE|INCONTACT)   // repeated for moves
  → UP   (POINTER_FLAG_UP)
```

The add-on maps the core `TouchContact.Phase` (0=down,1=move,2=up,3=cancel) to
these flag sets and maintains per-`PointerID` state so it always emits a valid
transition. Cancel is treated as UP.

### Single-thread requirement

The touch injection context is **per-thread**. All `InjectTouchInput` calls MUST
come from the same thread that called `InitializeTouchInjection`. The add-on
pins a dedicated OS thread (`runtime.LockOSThread`) and funnels all touch frames
to it via a channel.

### Coordinate mapping

Core delivers stream-pixel coordinates; the add-on converts to **physical screen
pixels**. With display scaling (125/150%), `GetSystemMetrics(SM_CXSCREEN/CYSCREEN)`
returns physical pixels and touch injection expects physical pixels, so
`screenX = streamX * SM_CXSCREEN / streamWidth`. `Resize` updates the stored
stream dims.

---

## Build & Distribution

```bash
go build -tags "interception,win_touch" -o viewport-rds-windows.exe ./cmd/server
```

Pure `syscall` to `user32.dll` (`InitializeTouchInjection`, `InjectTouchInput`) —
no CGo, no external dependency, no driver install. Available on Windows 8+.

---

## Constructor & Probe

```go
// internal/input/wintouch/wintouch_windows.go  (build tag: win_touch)

// Probe returns true if InitializeTouchInjection is available (Windows 8+).
func Probe() bool

// New initializes touch injection (max contacts) and starts the pinned thread.
func New(cfg input.InjectorConfig) (input.TouchInjector, error)
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| `InitializeTouchInjection` fails | `New` error → touch unavailable; key/mouse still work |
| `InjectTouchInput` `ERROR_INVALID_PARAMETER` | log; reset the offending contact's state machine |
| `InjectTouchInput` `ERROR_TIMEOUT` | retry once next frame |
| > max contacts received | inject the first N (10), drop extras, log once |

---

## File Structure

```
internal/input/wintouch/
├── wintouch_windows.go   // build tag: win_touch (TouchInjector impl)
├── pointer_info.go       // POINTER_TOUCH_INFO struct + flag mapping
├── thread.go             // pinned-thread funnel (LockOSThread)
├── stub.go               // build tag: !win_touch (no-op, never registers)
└── wintouch_test.go
```

---

## Configuration

Reads `[addon_module_win_touch]` (see [`specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_win_touch]
max_contacts = 10        # 1..256; max simultaneous touch points
feedback     = "none"    # "none" | "default" | "indirect" — system touch visual
```

If absent, defaults apply. Strictly validated only when this add-on is compiled in.

---

## Status

📋 Specced — not yet built. Implementation order: init + pinned thread → contact
state machine (down/update/up) → coordinate scaling + pressure → cancel handling.
