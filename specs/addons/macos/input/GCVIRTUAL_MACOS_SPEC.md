# macOS Gamepad Add-On: GCVirtualController

## Purpose

The `gcvirtual` add-on is the macOS virtual-gamepad backend for the core
Gamepad module (see [`specs/interaction/MODULE_GAMEPAD.md`](../../../interaction/MODULE_GAMEPAD.md)).
It implements the `GamepadInjector` trait using Apple's `GCVirtualController`
(Game Controller framework).

`GCVirtualController` is Apple's blessed path for synthesizing a gamepad on
macOS. It was added in macOS 14 (Sonoma) and iOS 15, and is the only public
API for creating a virtual controller - there is no IOKit/HID equivalent that
Apple supports.

### Honest limitations

- **macOS 14+ only.** Users on macOS 13 and earlier get no gamepad capability
  from this add-on; the pipeline starts without `GamepadInjector` registered
  and gamepad records from the browser are silently dropped (zero-by-default,
  matching the touch behavior on platforms without a touch add-on).
- **GameController-framework apps only.** Only games and applications that
  read input through the `GCController` API see the virtual controller.
  Many SDL2-based games on macOS read HID directly via IOKit instead of
  going through the `GCController` API; those games will **not** see the
  virtual pad. There is no workaround inside the supported API surface.
  Operators are told this up front.
- **No rumble inbox.** `GCController`'s haptics API (`GCHaptics`) is designed
  for *apps* to play effects on a *connected* controller. There is no public
  inbox by which a virtual controller can observe a host app's vibration
  request. v1 therefore does **not** forward rumble on this add-on -
  `set_rumble_emitter` is registered and stored but never invoked. The dispatcher
  treats this as "rumble unavailable" the same way Safari treats missing
  `vibrationActuator`.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| GameController framework | Apple system framework | Linked, not redistributed |
| Our Rust FFI / Obj-C++ binding | MIT | |

---

## How It Works

`GCVirtualController` is an Objective-C class with an async `connect`
completion handler. The add-on talks to it through a small Objective-C++
shim file linked via Rust FFI - Rust cannot call the Objective-C framework
directly through `objc2` ergonomically here. The shim exposes plain C entry
points the Rust side calls via `extern "C"`.

```objc
// Pseudocode in the shim (gcvirtual_bridge.mm):

#import <GameController/GameController.h>

GCVirtualController *gVC[4];   // one per W3C index 0..3

int gcv_connect(uint8_t index, void (^done)(int err)) {
    if (@available(macOS 14, *)) {
        GCVirtualControllerConfiguration *cfg =
            [[GCVirtualControllerConfiguration alloc] init];
        cfg.elements = [NSSet setWithArray:@[
            GCInputLeftThumbstick,        GCInputRightThumbstick,
            GCInputLeftShoulder,          GCInputRightShoulder,
            GCInputLeftTrigger,           GCInputRightTrigger,
            GCInputButtonA, GCInputButtonB, GCInputButtonX, GCInputButtonY,
            GCInputDirectionPad,
            GCInputButtonMenu,            GCInputButtonOptions,
            GCInputButtonHome,
            GCInputLeftThumbstickButton,  GCInputRightThumbstickButton,
        ]];
        gVC[index] = [[GCVirtualController alloc] initWithConfiguration:cfg];
        [gVC[index] connectWithReplyHandler:^(NSError *err) {
            done(err ? -1 : 0);
        }];
        return 0;
    }
    return -1; // pre-Sonoma
}

void gcv_set_button(uint8_t index, NSString *key, float value) {
    GCController *c = gVC[index].controller;
    GCControllerButtonInput *btn =
        (GCControllerButtonInput *)[c.input elementForKey:key];
    // Newer GCController APIs accept setValue: on virtual elements.
    [btn setValue:value];
}

// ⚠ TECHNICAL SPIKE REQUIRED — BUILD-BLOCKING RISK.
// GCVirtualController is designed to render an ON-SCREEN touch overlay and
// source its state from the user's finger; Apple does NOT clearly document a
// supported way to set element values PROGRAMMATICALLY. `setValue:` on a
// GCControllerButtonInput obtained from a virtual controller may be a no-op,
// may assert, or may work only on undocumented internal classes. Before any
// other gcvirtual work, run a throwaway spike that (1) connects a
// GCVirtualController, (2) drives a button/stick via the chosen setter, and
// (3) confirms a separate GCController observer sees the change. If the spike
// fails, this add-on is not viable on the public API and macOS gamepad support
// is dropped to "unsupported" (zero-by-default), exactly like pre-Sonoma — do
// NOT build the rest of the add-on on an unvalidated injection path.

void gcv_set_thumbstick(uint8_t index, NSString *key, float x, float y) {
    GCController *c = gVC[index].controller;
    GCControllerDirectionPad *dp =
        (GCControllerDirectionPad *)[c.input elementForKey:key];
    [dp setValueForXAxis:x yAxis:y];
}

void gcv_disconnect(uint8_t index) {
    [gVC[index] disconnect];
    gVC[index] = nil;
}
```

The Rust side calls these through FFI, marshalling the W3C `GamepadState` into
per-element updates.

### Button mapping (W3C bit -> GCInput* key)

| W3C bit | W3C name | GCInput key |
|--------:|----------|-------------|
|  0 | A           | `GCInputButtonA` |
|  1 | B           | `GCInputButtonB` |
|  2 | X           | `GCInputButtonX` |
|  3 | Y           | `GCInputButtonY` |
|  4 | LB          | `GCInputLeftShoulder` |
|  5 | RB          | `GCInputRightShoulder` |
|  6 | LT          | `GCInputLeftTrigger`  (analog) |
|  7 | RT          | `GCInputRightTrigger` (analog) |
|  8 | Back        | `GCInputButtonOptions` |
|  9 | Start       | `GCInputButtonMenu` |
| 10 | LS click    | `GCInputLeftThumbstickButton` |
| 11 | RS click    | `GCInputRightThumbstickButton` |
| 12 | D-Pad Up    | `GCInputDirectionPad` (y = +1) |
| 13 | D-Pad Down  | `GCInputDirectionPad` (y = -1) |
| 14 | D-Pad Left  | `GCInputDirectionPad` (x = -1) |
| 15 | D-Pad Right | `GCInputDirectionPad` (x = +1) |
| 16 | Home        | `GCInputButtonHome` |

Axis conversion: W3C i16 (-32768..32767) -> float -1.0..1.0 via divide by
32767. Trigger conversion: W3C u16 (0..65535) -> float 0.0..1.0 via divide
by 65535. The D-pad is a single direction-pad element; the four W3C dpad
bits are folded into `(x, y)` before the call.

---

## Build & Distribution

```bash
cargo build --release -p featherdesk-addon-gcvirtual   # cdylib  featherdesk-addon-gcvirtual.dylib
```

FFI / link config (Rust):

```rust
// build.rs — compile the Objective-C++ shim and link the frameworks:
//   cc::Build::new()
//       .cpp(true)
//       .flag("-x").flag("objective-c++")
//       .flag("-fobjc-arc")
//       .file("src/gcvirtual_bridge.mm")
//       .compile("gcvirtual_bridge");
//   println!("cargo:rustc-link-lib=framework=GameController");
//   println!("cargo:rustc-link-lib=framework=Foundation");
// The Rust side declares the shim entry points in an `extern "C"` block,
// generated from `gcvirtual_bridge.h` (e.g. via `bindgen`).
```

The shim file is `gcvirtual_bridge.mm` (Objective-C++); the header
`gcvirtual_bridge.h` exposes plain C signatures for the Rust `extern "C"` block.

For distribution the app must be **signed + notarized** like every other
macOS add-on. There is no driver to install and no permission prompt -
`GCVirtualController` does not require Accessibility or Input Monitoring.

Apple Silicon and Intel use the identical framework API.

---

## Constructor & Probe

```rust
// crate: featherdesk-addon-gcvirtual (built as a cdylib add-on)

/// probe returns true only on macOS 14 (Sonoma) and later. It does not
/// allocate any virtual controllers; that happens in `connect`.
pub fn probe() -> bool;

/// new stores config and prepares the shim. No virtual controllers are
/// brought online until `connect(index, id)` is called.
pub fn new(cfg: InjectorConfig) -> Result<Box<dyn GamepadInjector>, InputError>;
```

`probe` checks `@available(macOS 14, *)` via the shim. On pre-Sonoma it
returns false and the pipeline starts without gamepad capability; the log
line tells the operator the OS version requirement.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| Pre-macOS 14 | `probe` false -> add-on not selected; log "GCVirtualController requires macOS 14+" |
| `GCVirtualController` init / connect fails | `connect` returns the `NSError` description (mapped to `InputError`); that index stays unbound |
| Host game does not use GameController framework | undetectable from our side; the virtual pad simply has no observer. Document in the user-facing README |
| `setValue:` on an element fails | log once + continue (do not crash the stream) |
| `disconnect` called on never-connected index | no-op (matches trait contract) |
| Rumble request from server | `set_rumble_emitter` is stored; the emitter is never invoked (no inbox); no error returned |

---

## File Structure

```
featherdesk-addon-gcvirtual/   (its own cdylib crate)
├── src/lib.rs              // GamepadInjector impl + abi_stable root module, Rust FFI
├── src/gcvirtual_bridge.h  // plain C signatures for the Rust extern "C" block
├── src/gcvirtual_bridge.mm // Objective-C++ shim -> GameController framework
├── src/buttons.rs          // W3C bit -> GCInput* key table, axis/trigger math
└── build.rs                // compiles the .mm shim + links the frameworks
```

> No build-tag stub file is needed — the add-on is its own cdylib crate.

---

## Configuration

Reads `[addon_module_gcvirtual]` (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_gcvirtual]
layout = "standard"   # v1 supports "standard" only; reserved for future variants
                      # (e.g. extended layouts with paddles when Apple exposes them)
```

If absent, defaults apply. Strictly validated only when this add-on is loaded.

---

## Status

Specced - not yet built. Implementation order: `@available` probe + version
gate -> Objective-C++ shim with `gcv_connect` / `gcv_disconnect` lifecycle ->
button + thumbstick + trigger + D-pad mapping -> `update` diff suppression on
the Rust side (avoid crossing FFI on no-change) -> documentation pass on the
SDL2/IOKit visibility caveat.
