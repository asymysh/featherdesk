# Windows Input Add-On: Touch Injection

## Purpose

The `win_touch` add-on adds multitouch injection on Windows. It implements the
`input::TouchInjector` trait (see [`specs/interaction/MODULE_INPUT.md`](../../../interaction/MODULE_INPUT.md))
and is **independent** of the in-core keyboard/mouse injector (`enigo`/`SendInput`,
which covers no touch) — they compose: a touch deployment just drops in
`win_touch` on top of the in-core kb/mouse (and optionally the `interception`
override).

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
| Our binding | MIT | Pure FFI to `user32.dll` via the `windows` crate — no C dependency |

---

## How It Works

Two-step Windows API (`user32.dll`):

```rust
// Win32 touch injection via the `windows` crate (windows-rs).
// Once at startup: max 10 simultaneous contacts, no system visual feedback.
unsafe { InitializeTouchInjection(10, TOUCH_FEEDBACK_NONE)?; }

// Per frame: an array of POINTER_TOUCH_INFO, one per active contact.
let mut c = POINTER_TOUCH_INFO::default();
c.pointerInfo.pointerType        = PT_TOUCH;
c.pointerInfo.pointerId          = contact_id;           // stable per finger
c.pointerInfo.ptPixelLocation.x  = screen_x;             // physical screen px
c.pointerInfo.ptPixelLocation.y  = screen_y;
c.pointerInfo.pointerFlags       = POINTER_FLAG_DOWN     // first contact
                                 | POINTER_FLAG_INRANGE | POINTER_FLAG_INCONTACT;
c.touchFlags = TOUCH_FLAG_NONE;
c.touchMask  = TOUCH_MASK_CONTACTAREA | TOUCH_MASK_PRESSURE;
c.rcContact  = RECT { left: screen_x - 2, top: screen_y - 2, right: screen_x + 2, bottom: screen_y + 2 };
c.pressure   = pressure_0_to_1024;
unsafe { InjectTouchInput(&contacts)?; }
```

### Contact state machine

Windows enforces a strict per-contact lifecycle; violating it returns
`ERROR_INVALID_PARAMETER`:

```
DOWN (POINTER_FLAG_DOWN|INRANGE|INCONTACT)
  → UPDATE (POINTER_FLAG_UPDATE|INRANGE|INCONTACT)   // repeated for moves
  → UP   (POINTER_FLAG_UP)
```

The add-on maps the core `TouchContact.phase` (0=down,1=move,2=up,3=cancel) to
these flag sets and maintains per-`PointerID` state so it always emits a valid
transition. Cancel is treated as UP.

### Single-thread requirement

The touch injection context is **per-thread**. All `InjectTouchInput` calls MUST
come from the same thread that called `InitializeTouchInjection`. The add-on
spawns a dedicated OS thread (a `std::thread` it owns for the add-on's lifetime)
and funnels all touch frames to it over an `std::sync::mpsc` channel.

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
   be too late and could race the capture add-on). Instead, at `new()` it
   **verifies** the context via `GetThreadDpiAwarenessContext` /
   `AreDpiAwarenessContextsEqual` and returns an actionable error if the process
   is not PER_MONITOR_AWARE_V2 — because otherwise `GetSystemMetrics(SM_CXSCREEN)`
   returns DPI-scaled (logical) pixels and touch lands at the wrong physical
   position on every HiDPI display.
2. **Virtual-screen metrics, not SM_CXSCREEN.** On any multi-monitor setup
   `SM_CXSCREEN` only covers the primary display. Use:
   ```rust
   let vx = unsafe { GetSystemMetrics(SM_XVIRTUALSCREEN) };
   let vy = unsafe { GetSystemMetrics(SM_YVIRTUALSCREEN) };
   let vw = unsafe { GetSystemMetrics(SM_CXVIRTUALSCREEN) };
   let vh = unsafe { GetSystemMetrics(SM_CYVIRTUALSCREEN) };
   ```
   The captured monitor's offset within the virtual screen
   (`MONITORINFO.rcMonitor`) is added to the scaled stream coordinate.

Single-monitor target with the primary display at virtual origin:
`screenX = streamX * monitorPxW / streamWidth`, with `monitorPxW` taken from
`GetMonitorInfo` on the captured `HMONITOR` (already per-monitor-V2 aware).
A `resize` updates the stored stream dims. Multi-monitor is out of scope.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-win_touch   # cdylib  featherdesk-addon-win_touch.dll
```

Pure FFI to `user32.dll` (`InitializeTouchInjection`, `InjectTouchInput`) via the
`windows` crate — no external dependency, no driver install (the cdylib exports
the abi_stable `FeatherDeskAddonOpen` entry point). Available on Windows 8+.

---

## Constructor & Probe

```rust
// crate: featherdesk-addon-win_touch (the add-on's cdylib)

/// `probe` returns true if InitializeTouchInjection is available (Windows 8+).
/// Implemented via GetProcAddress on user32.dll — does NOT call
/// InitializeTouchInjection itself (that has the side effect of registering a
/// per-thread injection context, which would conflict with the pinned-thread
/// pattern used at construction).
fn probe(&self) -> Result<ProbeResult, PipelineError>;

/// Initialize touch injection (max contacts) and start the pinned thread.
fn new(&self, cfg: input::InjectorConfig) -> Result<Box<dyn input::TouchInjector>, input::InputError>;
```

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| `InitializeTouchInjection` fails | `new` error → touch unavailable; in-core kb/mouse still work |
| `InjectTouchInput` `ERROR_INVALID_PARAMETER` | log; reset the offending contact's state machine |
| `InjectTouchInput` `ERROR_TIMEOUT` | retry once next frame |
| > max contacts received | inject the first N (10), drop extras, log once |

---

## File Structure

```
addons/win_touch/
├── src/
│   ├── lib.rs           // TouchInjector impl
│   ├── pointer_info.rs  // POINTER_TOUCH_INFO struct + flag mapping
│   └── thread.rs        // pinned-thread funnel (dedicated std::thread)
└── tests.rs
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate; an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## Configuration

Reads `[addon_module_win_touch]` (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_win_touch]
max_contacts = 10        # 1..256; max simultaneous touch points
feedback     = "none"    # "none" | "default" | "indirect" — system touch visual
```

If absent, defaults apply. Strictly validated only when this add-on is loaded.

---

## Status

📋 Specced — not yet built. Implementation order: init + pinned thread → contact
state machine (down/update/up) → coordinate scaling + pressure → cancel handling.
