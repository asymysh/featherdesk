# macOS Capture Add-On: ScreenCaptureKit (SCK)

## Purpose

ScreenCaptureKit (SCK) is Apple's unified screen-capture framework, introduced in
macOS 12.3 (Monterey) and the **only** supported capture path on macOS 26 (Tahoe)
and later. Captures the framebuffer as an `IOSurface`-backed `CMSampleBuffer` —
the format VideoToolbox consumes directly for a true zero-copy GPU→encoder path.

The default macOS capture add-on. There is no realistic alternative on modern macOS
(see [`./README.md`](./README.md) for the legacy-API removal table).

---

## License

| Component | License |
|-----------|---------|
| ScreenCaptureKit framework | Apple system framework — usage governed by macOS SLA |
| Our Rust FFI / Objective-C bridge | MIT |

System framework; no redistribution, no royalties, no GPL exposure. Same status
as VideoToolbox.

---

## Platform Compatibility

| macOS Version | SCK Support | Notes |
|---------------|-------------|-------|
| 26 Tahoe (2025+) | ✅ Required | Only available capture API — legacy paths removed |
| 15 Sequoia | ✅ | Full support |
| 14 Sonoma | ✅ | Full support |
| 13 Ventura | ✅ | Full support |
| 12.3+ Monterey | ✅ | Minimum version where SCK exists |
| 12.0–12.2 Monterey | ❌ | Pre-SCK — out of scope |
| 11 Big Sur and older | ❌ | End of support |

**Minimum target: macOS 12.3.** Any Mac that cannot upgrade past macOS 12 is
out of scope.

| Hardware | SCK Support |
|----------|-------------|
| Apple Silicon (M1/M2/M3/M4+) | ✅ Native, lowest latency |
| Intel Mac with Apple GPU | ✅ |
| Intel Mac with AMD discrete | ✅ |
| Intel Mac with NVIDIA discrete | ✅ (legacy Mac Pro etc.) |
| Hackintosh + virtual display | ✅ Verified working (see benchmarks) |

---

## Permission Requirements

### Screen Recording TCC permission

SCK requires the **Screen Recording** permission granted through *System Settings
→ Privacy & Security → Screen Recording*.

The binary **must be in a proper app bundle** for the permission dialog to
appear. A raw CLI binary cannot request the permission — the TCC daemon checks
`CFBundleIdentifier` before showing the prompt.

Minimum bundle layout:
```
ViewportRDS.app/
└── Contents/
    ├── Info.plist           # must contain CFBundleIdentifier
    └── MacOS/
        └── featherdesk     # the actual binary
```

### Code-signing requirement (production)

For production deployment the app bundle must be:
1. **Code-signed with an Apple Developer ID certificate**
2. **Notarized** by Apple

Ad-hoc signing (`codesign -s -`) is sufficient for development. macOS 26
enforces **HMAC-signed TCC entries** that can only be created through the
legitimate permission-dialog flow — sqlite injection into `TCC.db` no longer
grants permission as it did pre-26.

---

## Build & Distribution

### Shared library (cdylib)

```bash
cargo build --release -p featherdesk-addon-sck   # cdylib  featherdesk-addon-sck.dylib
```

Without the `sck` library in the add-ons directory the default macOS binary has
no capture backend and will fail at runtime — same pattern as Linux.

### Runtime dependencies

ScreenCaptureKit framework (ships with macOS 12.3+, nothing to install).

### FFI / link configuration (Rust)

```rust
// build.rs — compile the Objective-C wrapper and link the macOS frameworks:
//   cc::Build::new()
//       .flag("-x").flag("objective-c")
//       .flag("-fmodules")
//       .flag("-fobjc-arc")
//       .file("src/sck_objc.m")
//       .compile("sck_objc");
//   for fw in ["ScreenCaptureKit", "CoreMedia", "CoreVideo", "IOSurface", "Foundation"] {
//       println!("cargo:rustc-link-lib=framework={fw}");
//   }
// The Rust side declares the wrapper's C entry points in an `extern "C"` block
// (generated from `sck_objc.h` via `bindgen`); `objc2` covers the rest.
```

The capture itself is written in Objective-C (not Swift) for clean Rust FFI
interop — Swift's runtime isn't ABI-stable across language versions and adds
bridging overhead.

---

## Native Implementation Sketch

```objc
// 1. Discover displays
SCShareableContent *content = nil;
dispatch_semaphore_t sema = dispatch_semaphore_create(0);
[SCShareableContent getShareableContentWithCompletionHandler:
    ^(SCShareableContent *c, NSError *err) {
        content = c;
        dispatch_semaphore_signal(sema);
    }];
dispatch_semaphore_wait(sema, DISPATCH_TIME_FOREVER);

SCDisplay *display = content.displays.firstObject;

// 2. Build filter (full-display capture; per-window also possible)
SCContentFilter *filter = [[SCContentFilter alloc]
    initWithDisplay:display
    excludingWindows:@[]];

// 3. Configure stream
SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
cfg.width = 1920;
cfg.height = 1080;
cfg.minimumFrameInterval = CMTimeMake(1, 60);  // 60 fps cap
cfg.pixelFormat = kCVPixelFormatType_32BGRA;
cfg.showsCursor = NO;  // HW path sends cursor separately as CursorUpdate
cfg.queueDepth = 6;

// 4. Create stream + register output handler
SCStream *stream = [[SCStream alloc] initWithFilter:filter
                                     configuration:cfg
                                          delegate:delegate];
[stream addStreamOutput:outputHandler
                   type:SCStreamOutputTypeScreen
     sampleHandlerQueue:captureQueue
                  error:&err];

[stream startCaptureWithCompletionHandler:^(NSError *err) { ... }];

// 5. Per-frame callback:
- (void)stream:(SCStream *)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
                   ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen) return;
    CVImageBufferRef img = CMSampleBufferGetImageBuffer(sampleBuffer);
    // img is IOSurface-backed — pass directly to VideoToolbox (zero-copy)
    // OR CVPixelBufferLockBaseAddress for CPU access (SW encoder)
}
```

---

## Two Output Paths

| Trait | Method | Output | Use case |
|-------|--------|--------|----------|
| `Capturer` (CPU readback) | `next_frame()` | BGRA `RVec<u8>` via `CVPixelBufferLockBaseAddress` | Pair with OpenH264 add-on |
| `SurfaceCapturer` (zero-copy) | `next_surface()` | `FbInfo { handle: SurfaceHandle::IoSurface(..) }` (CVPixelBuffer/IOSurface-backed) | Pair with VideoToolbox HW/SW add-on |

The pipeline picks the right method based on the paired encoder.

The zero-copy path is the macOS equivalent of Linux's DMA-BUF zero-copy:
`SCStream` → `CMSampleBuffer` → `IOSurface` → `VTCompressionSession`. Nothing
ever touches CPU memory.

---

## Performance Targets

### Hackintosh (AMD Ryzen 5 3600 + RX 570 + virtual display, macOS 26.5.1)

| Resolution | FPS | p50 | p95 | p99 |
|-----------|-----|-----|-----|-----|
| 2112×1188 (native 2x) | 89.6 | 11.9ms | 14.1ms | 14.1ms |
| 1920×1080 | 91.4 | 10.5ms | 14.1ms | 14.3ms |

**Raw CSVs:** `/tmp/fd_bench/cap_sck_native.csv`, `cap_sck_1080p.csv`

Real Apple Silicon Mac will run significantly faster (~2–4ms p50) due to
unified memory and tighter display-compositor integration. The Hackintosh
numbers represent a worst-case modern macOS capture scenario and are still
solidly within budget.

---

## Cursor Handling

`cfg.showsCursor = NO` for the hardware-encoder path. The cursor is captured
separately via `NSCursor` polling and sent as a `FrameTypeCursorUpdate` protocol
frame — client composites the cursor on top of the decoded video. This keeps
the cursor out of the encoded stream (encoders compress cursor motion poorly
anyway) and gives the client smoother cursor latency.

`cfg.showsCursor = YES` is also valid for software paths where compositor cost
is irrelevant.

---

## File Structure

```
featherdesk-addon-sck/   (its own cdylib crate)
├── src/lib.rs              // SckCapturer struct + abi_stable root module
├── src/sck_objc.m          // Objective-C SCK wrapper
├── src/sck_objc.h          // C-callable function declarations
├── src/ffi.rs              // extern "C" binding to the Obj-C wrapper (Rust FFI)
├── src/cursor.rs           // NSCursor polling
├── src/probe.rs            // probe_sck() — checks bundle + permission
└── tests/integration.rs    // integration test (cfg(feature = "integration"))
```

> No build-tag stub file is needed — the add-on is its own cdylib crate, so an
> absent add-on is simply a library that isn't in the directory.

---

## Probe & Selection

```rust
// cfg(target_os = "macos")

pub fn probe_sck() -> Result<SckCapabilities, String> {
    // 1. Verify running inside a code-signed app bundle (check CFBundleIdentifier)
    // 2. Check Screen Recording TCC permission via CGPreflightScreenCaptureAccess()
    //    If not granted: CGRequestScreenCaptureAccess() to trigger dialog
    // 3. Enumerate displays via SCShareableContent
    // 4. Return per-display dimensions + scale factor
}
```

Since SCK is realistically the only macOS capture add-on, the pipeline probe
order reduces to:
```
sck loaded AND Screen Recording granted? → use SCK
Otherwise → fatal: no usable capture
```

---

## When to use this add-on

Always, on macOS 12.3 or later. There is no alternative.

Skip only if:
- Targeting macOS 12.0–12.2 (out of scope)
- Building a CLI-only diagnostic binary that doesn't need capture

---

## Status

✅ **Working** — implemented and benchmarked on Hackintosh + macOS 26.5.1. Real
Apple Silicon hardware not yet measured but expected to be 2–4× faster. The
refactor moves the existing Objective-C SCK wrapper into the
`featherdesk-addon-sck` crate, built into the `sck` add-on cdylib, without
changing the underlying capture logic.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_sck]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.



---

## Stream Params Translation

This add-on implements the `ConfigurableCapturer` trait (see [`specs/core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)). SCK supports hot reconfiguration via `updateConfiguration:`.

| Param change | SCK API | Hot? |
|--------------|---------|------|
| `width`, `height` | `SCStreamConfiguration.width/height` + `[stream updateConfiguration:completionHandler:]` | yes |
| `fps` | `SCStreamConfiguration.minimumFrameInterval` + `updateConfiguration:` | yes |
| `bit_depth=10` / `hdr=true` | `SCStreamConfiguration.pixelFormat = kCVPixelFormatType_64RGBALeAccurate` + `updateConfiguration:` (requires macOS 14+) | yes |
| `color_space` | Set automatically based on display; `CGColorSpaceCreateWithName` from `CMSampleBuffer` attachment | n/a (read-only) |