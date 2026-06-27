# Windows Input Add-On: Touch Injection

## Purpose

The `win_touch` add-on adds multitouch injection on Windows. It implements
`input.TouchInjector` (see [`specs/MODULE_INPUT.md`](../../../specs/MODULE_INPUT.md))
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

### Coordinate mapping (DPI-aware, virtual screen)

`InjectTouchInput` expects coordinates in the **virtual-screen physical-pixel
space** — the union of all monitors' physical pixel grids. Two corrections vs
the naive read are mandatory:

1. **DPI awareness (owned by the process entry point, NOT this add-on).**
   `PER_MONITOR_AWARE_V2` is **process-global and may be set only once**, before
   any DPI-dependent call — and the `dxgi_dd` capture add-on depends on it too
   (for correct physical surface dimensions). So it MUST be established **once at
   process startup**, owned by the FeatherDesk entry point via the **application
   manifest** (`<dpiAwareness>PerMonitorV2</dpiAwareness>`, the most robust path)
   or a single early `SetProcessDpiAwarenessContext(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2)`
   call. This add-on does **not** set it (a lazy set at injector-create time would
   be too late and could race the capture add-on). Instead, at `New()` it
   **verifies** the context via `GetThreadDpiAwarenessContext` /
   `AreDpiAwarenessContextsEqual` and returns an actionable error if the process
   is not PER_MONITOR_AWARE_V2 — because otherwise `GetSystemMetrics(SM_CXSCREEN)`
   returns DPI-scaled (logical) pixels and touch lands at the wrong physical
   position on every HiDPI display.
2. **Virtual-screen metrics, not SM_CXSCREEN.** On any multi-monitor setup
   `SM_CXSCREEN` only covers the primary display. Use:
   ```c
   int vx = GetSystemMetrics(SM_XVIRTUALSCREEN);
   int vy = GetSystemMetrics(SM_YVIRTUALSCREEN);
   int vw = GetSystemMetrics(SM_CXVIRTUALSCREEN);
   int vh = GetSystemMetrics(SM_CYVIRTUALSCREEN);
   ```
   The captured monitor's offset within the virtual screen
   (`MONITORINFO.rcMonitor`) is added to the scaled stream coordinate.

Single-monitor target with the primary display at virtual origin:
`screenX = streamX * monitorPxW / streamWidth`, with `monitorPxW` taken from
`GetMonitorInfo` on the captured `HMONITOR` (already per-monitor-V2 aware).
`Resize` updates the stored stream dims. Multi-monitor is out of scope.

---

## Build & Distribution

```bash
go build -tags "interception,win_touch" -o featherdesk.exe ./cmd/server
```

Pure `syscall` to `user32.dll` (`InitializeTouchInjection`, `InjectTouchInput`) —
no CGo, no external dependency, no driver install. Available on Windows 8+.

---

## Constructor & Probe

```go
// internal/input/wintouch/wintouch_windows.go  (build tag: win_touch)

// Probe returns true if InitializeTouchInjection is available (Windows 8+).
// Implemented via GetProcAddress on user32.dll — does NOT call
// InitializeTouchInjection itself (that has the side effect of registering a
// per-thread injection context, which would conflict with the pinned-thread
// pattern used at construction).
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

Reads `[addon_module_win_touch]` (see [`specs/MODULE_CONFIG.md`](../../../specs/MODULE_CONFIG.md)).

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
