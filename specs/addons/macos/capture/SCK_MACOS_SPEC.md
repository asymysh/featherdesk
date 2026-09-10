# macOS Capture Add-On: ScreenCaptureKit (SCK)

## Purpose

ScreenCaptureKit (SCK) is Apple's unified screen-capture framework, introduced in
macOS 12.3 (Monterey) and the **only** supported capture path on macOS 26 (Tahoe)
and later. Captures the framebuffer as an `IOSurface`-backed `CMSampleBuffer` —
the format VideoToolbox consumes directly for a true zero-copy GPU→encoder path.

The default macOS capture add-on. There is no realistic alternative on modern macOS
(see [`./README.md`](./README.md) for the legacy-API removal table).

SCK delivers **physical pixels** — a 2x Retina display arrives at its full backing
resolution, not at its point size. The stream space is pixels end-to-end
([`specs/core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)
"Coordinate space"), and the macOS injector converts to points at its own
boundary (see
[`../input/CGEVENT_MACOS_SPEC.md`](../input/CGEVENT_MACOS_SPEC.md) "Coordinate
space"). Nothing on the capture side scales.

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
cfg.showsCursor = YES; // this add-on embeds; see "Cursor Handling"
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

**Thread affinity.** The `SCStream` and the `CMSampleBuffer`s it hands over are
thread-affine, so this add-on takes route 1 of
[`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Thread requirements":
it writes `unsafe impl Send` on the capturer, and the justification is the host's
own discipline — the host constructs, uses and drops the capturer on a single
worker thread (the frame loop's) via `FrameLoop::open()`, and every
`SCStreamConfiguration` mutation and every `startCapture` / `stopCapture` is
issued from `fd-frame` and nowhere else. The `Send` declaration is nominal: it
satisfies `CapturerBox::from_value`, which requires it because
`#[sabi_trait] pub trait Capturer: Send`.

Two things cross a thread boundary, and naming them is the point — an affinity
claim that quietly excludes the exceptions is worth nothing.

**1. `sck_audio` touches this add-on's `SCStream`.** It deliberately does not open
its own: [`../audio/SCK_AUDIO_MACOS_SPEC.md`](../audio/SCK_AUDIO_MACOS_SPEC.md)
registers an `SCStreamOutput` for `SCStreamOutputType.audio` on the single stream
`sck` owns, so audio rides the same clock. Those calls run on the AUDIO thread,
not on `fd-frame`, and there are **several of them, not one**: at `sck_audio`
construction, again whenever `sck` rebuilds the stream, and again on every
`kAudioHardwarePropertyDefaultOutputDevice` change (that spec's "Stream stops /
reconfigures" and "Default output device changes mid-session" rows).

`startCapture` is **not** gated on any of this. `sck` configures and starts its
stream as soon as it is ready — the sequence in "Implementation sketch" above is
unconditional — because ScreenCaptureKit permits `addStreamOutput:` on an
already-running stream. A start barrier waiting on audio would deadlock the common
case outright: `[audio] enabled` defaults to `false`, and `MODULE_PIPELINE` step 11
sets `self.audio = None` and spawns no audio thread when audio is off or no audio
capture add-on loaded, so on a default macOS deployment there is no `sck_audio`
object in existence to attach or to decline. Video must not depend on it.

What the two add-ons DO need is mutual exclusion, not ordering: `sck` owns a
`Mutex` around the shared stream, and every mutation — its own
`SCStreamConfiguration` writes and `stop`/rebuild from `fd-frame`, and
`sck_audio`'s attach and re-attach from `fd-audio` — takes it.

> **Open item, carried with the deferred audio module.** `MODULE_ABI` defines no
> inter-add-on route (`AddonObject` variants are returned to the HOST; add-ons do
> not address each other), so the mechanism by which `sck_audio` obtains a handle
> to `sck`'s `SCStream` is not specified by the ABI and is not specified here
> either. It is a macOS-specific arrangement between these two add-ons and belongs
> to `SCK_AUDIO_MACOS_SPEC`, which today asserts the attach without saying how the
> handle crosses. This is a real hole, listed rather than papered over; it blocks
> nothing until the audio module leaves deferred status, and the `unsafe impl Send`
> justification below does not depend on it.

**2. The sample buffers.** ScreenCaptureKit delivers each `CMSampleBuffer` on its
own `sampleHandlerQueue`, not on `fd-frame`. A raw `CMSampleBufferRef` is
`*mut opaqueCMSampleBuffer` — a raw pointer, hence `!Send + !Sync`, so
`Mutex<Option<CMSampleBufferRef>>` is neither `Send` nor `Sync` and could not be
shared between the two threads at all. The slot is therefore declared over a
newtype that states the safety argument explicitly:

```rust
#[repr(transparent)]
struct SampleBuf(CMSampleBufferRef);
// SAFETY: CoreFoundation retain/release are atomic and a CMSampleBuffer carries no
// thread affinity of its own once retained — only the SCStream that produced it does.
// SampleBuf owns exactly one retain (taken by the delegate) and releases it on Drop.
unsafe impl Send for SampleBuf {}
```

The delegate retains the buffer, wraps it, and publishes it into a latest-only
`Mutex<Option<SampleBuf>>`, **dropping** any occupant still sitting there — that
drop is the release for the eviction path, and it is what stops a slow frame loop
from pinning IOSurfaces. `next_frame` / `next_surface` lock the same mutex on the
frame thread and `take()` it; **the taker then owns that retain and releases it by
dropping the `SampleBuf` once the `Frame` (CPU path) or the `FbInfo` (surface path)
derived from it has been consumed** — one retain, exactly one release, on both
exits. No ScreenCaptureKit object crosses the ABI: the slot is drained inside the
add-on and only the resulting `Frame` / `FbInfo` goes out.

---

## Performance Targets

### Hackintosh (AMD Ryzen 5 3600 + RX 570 + virtual display, macOS 26.5.1)

| Resolution | FPS | p50 | p95 | p99 |
|-----------|-----|-----|-----|-----|
| 2112×1188 (native 2x) | 89.6 | 11.9ms | 14.1ms | 14.1ms |
| 1920×1080 | 91.4 | 10.5ms | 14.1ms | 14.3ms |

**Raw CSVs:** `/tmp/fd_bench/cap_sck_native.csv`, `cap_sck_1080p.csv` — these
live **outside this repository**. There is no macOS session in
`PROJECT_ARTIFACTS/bench_out`, so the numbers above are not reproducible from the
tree; read them as a recorded observation, not as a checked-in benchmark.

Real Apple Silicon Mac will run significantly faster (~2–4ms p50) due to
unified memory and tighter display-compositor integration. The Hackintosh
numbers represent a worst-case modern macOS capture scenario and are still
solidly within budget.

---

## Cursor Handling

**Capabilities declared at probe.** `AddonCaps::EMBED_CURSOR |
AddonCaps::EMBED_CURSOR_SURF`, and **not** `AddonCaps::CURSOR`. macOS therefore
resolves `cursorMode = "embedded"`, which on this platform is not a degradation:
ScreenCaptureKit composites the pointer before the `CMSampleBuffer` is delivered,
so it costs nothing, keeps working on the zero-copy IOSurface path, and never
produces the double cursor that a separate overlay plus a composited pointer
would.

**Why `CursorCapturer` is not implemented here.** The only public shape source on
macOS is `NSCursor`, which is AppKit and therefore affine to **the main thread
specifically** — not merely to one consistent thread, which is all the frame
loop's dedicated `std::thread` provides
([`specs/CENTRAL_SPEC.md`](../../../CENTRAL_SPEC.md) "Capture loop"). Every poll
would have to be posted to `dispatch_get_main_queue()` and read back through a
latest-only slot, for a pointer ScreenCaptureKit already composites for free.
Core Graphics offers a thread-safe *position* (`CGEventGetLocation`) but no
public shape query, which is not enough to build a `CursorState`. Rather than
poll an AppKit class off the main thread, this add-on embeds. There is no
`NSCursor` query, no `dispatch_get_main_queue()` post and no latest-only cursor
slot on this path — the arrangement was considered and rejected, not shipped, and
this add-on owns no pinned OS thread of its own.

**`embed_cursor`.** `cfg.showsCursor = (embed_cursor ? YES : NO)` on the
`SCStreamConfiguration`. Since this add-on declares no `CURSOR` capability, the
host only ever constructs it with `embed_cursor = true`, so in practice
`showsCursor = YES`. There is no `[addon_module_sck]` cursor key: the host's
`CaptureConfig` is the single input
([`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md) "Cursor
delivery").

**Failure behavior.** None to specify — `next_cursor` is never called on this
add-on, and a compositing failure inside ScreenCaptureKit is an ordinary frame
error handled by [`## Error Recovery`](#error-recovery).

---

## File Structure

```
featherdesk-addon-sck/   (its own cdylib crate)
├── src/lib.rs              // SckCapturer struct + abi_stable root module
├── src/sck_objc.m          // Objective-C SCK wrapper
├── src/sck_objc.h          // C-callable function declarations
├── src/ffi.rs              // extern "C" binding to the Obj-C wrapper (Rust FFI)
├── src/cursor.rs           // showsCursor plumbing from CaptureConfig.embed_cursor
├── src/probe.rs            // probe_sck() — checks bundle + permission
└── tests/integration.rs    // integration test (cfg(feature = "integration"))
```

> No build-tag stub file is needed — the add-on is its own cdylib crate, so an
> absent add-on is simply a library that isn't in the directory.

---

## Error Recovery

Every failure this add-on returns is a `stream::StreamError`, which crosses the
ABI as the matching `AbiErr` code plus an `AbiError.detail` carrying the
`OSStatus` / `NSError` description (see
[`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "AbiErr registry").
There is no add-on-private error type.

| Error | Returned as | Handling |
|-------|-------------|----------|
| No new `CMSampleBuffer` this tick | `Ok(None)` | Normal — ScreenCaptureKit delivers only on change. Not an error; the pipeline paces. |
| `SCStreamDelegate stream:didStopWithError:` | `StreamError::Backend` | Transient stop (a display went to sleep, a space switched). The add-on restarts the stream in place and reports the tick as producing no frame. |
| `startCaptureWithCompletionHandler:` / `updateConfiguration:` fails | `StreamError::Backend` | Log with the `NSError` in `detail`; the previous configuration keeps running and the pipeline retries on the next tick. |
| Captured display disconnected or its mode changed | `StreamError::DeviceLost` | The `SCDisplay` is gone. The pipeline's capture ladder rebuilds the add-on; if the display returns at a new size, the resolution-change flow re-advertises `config`. |
| Screen Recording permission revoked mid-session | `StreamError::Unrecoverable` | TCC cannot be re-granted from inside the process. `detail` names the toggle; the pipeline poisons this add-on for the session and, since it is the only macOS capture add-on, shuts the pipeline down rather than streaming a frozen frame. |
| Stream restart fails more than 10 times in 60 s | `StreamError::Unrecoverable` | The `SCStream` cannot be re-established; `detail` carries the last `NSError`. |
| Permission or bundle missing at `construct()` | `AbiErr::Generic`, i.e. `PipelineError::AddonBackend` | Construction fails with a descriptive `detail`; the pipeline falls through, and with no other macOS capture candidate that is a startup failure. |
| `next_cursor` called | `StreamError::Unsupported` | Cannot happen on the specified path — this add-on never sets `AddonCaps::CURSOR`, so the host does not call it (see "Cursor Handling"). |

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-sck   (cfg(target_os = "macos"))

// Layer 1 — what the host actually calls:
fn probe(&self) -> RResult<ProbeReport, AbiError>;

// Layer 2 — the adapter shape the host wraps it in (MODULE_PIPELINE):
fn probe(&self) -> Result<ProbeResult, PipelineError>;
```

```rust
/// The root module's `probe`. A missing prerequisite is NOT an error — it is
/// `ROk(ProbeReport { available: false, reason, .. })`. `RErr` means the probe
/// itself broke.
fn probe(&self) -> RResult<ProbeReport, AbiError> {
    // 1. Verify the process is running inside an app bundle with a
    //      CFBundleIdentifier; a raw CLI binary can never be granted the TCC
    //      permission, so
    //      ROk(ProbeReport { available: false, reason: "not running inside an
    //        app bundle; the TCC daemon reads CFBundleIdentifier before it will
    //        show the Screen Recording prompt".into(), .. })
    // 2. CGPreflightScreenCaptureAccess(); if not granted,
    //      CGRequestScreenCaptureAccess() to trigger the dialog, then
    //      ROk(ProbeReport { available: false, reason: "Screen Recording
    //        permission not granted (System Settings → Privacy & Security →
    //        Screen Recording)".into(), .. })
    // 3. SCShareableContent.getShareableContentWithCompletionHandler → displays.
    //      For each SCDisplay fill one DisplayInfo:
    //        id            = its CGDirectDisplayID (the value
    //                        [addon_module_sck] display_id selects; the same
    //                        number NSScreen exposes as NSScreenNumber)
    //        width/height  = the display's PIXEL dimensions
    //                        (CGDisplayPixelsWide/High) — never points
    //        rotation      = ALWAYS Rotation::R0. ScreenCaptureKit composites
    //                        rotation itself, so the buffer is already upright
    //                        (MODULE_CAPTURE "Rotation"); reporting anything
    //                        else would make the host transpose twice
    //        refresh_mhz   = the active mode's refresh rate in milliHertz
    //        scale_num/den = the backing scale as a rational (Retina → 2/1)
    //        primary       = id == CGMainDisplayID()
    // 4. No display → ROk(ProbeReport { available: false,
    //      reason: "no shareable display".into(), .. })
    // 5. Otherwise → ROk(ProbeReport {
    //      available: true, reason: RString::new(), codecs: RVec::new(),
    //      caps: AddonCaps(AddonCaps::SURFACE            // IOSurface export
    //                    | AddonCaps::CONFIGURABLE       // updateConfiguration:
    //                    | AddonCaps::EMBED_CURSOR       // showsCursor, CPU path
    //                    | AddonCaps::EMBED_CURSOR_SURF),// …and on the IOSurface
    //                                                    // NOT CURSOR: see
    //                                                    //   "Cursor Handling"
    //      displays })
}
```

**Availability is not an error.** A missing permission, an absent app bundle or a
display-less session is `ROk(ProbeReport { available: false, reason })`. `RErr` is
reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` means
no zero-copy path, no embedded cursor and no hot parameter change — silently, with
no error and no warning. Because this add-on declares no `AddonCaps::CURSOR`, a
`[capture] cursor_mode = "separate"` makes it **ineligible** and startup fails
naming the rejection, rather than streaming with no visible pointer (see
[`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md) "Cursor
delivery").

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); a capability that only
`construct()` can settle is reported by the constructed object's `caps()`, which
is authoritative and may be a strict subset of this one.

`scale_num`/`scale_den` are **descriptive**. Nothing downstream converts with
them: the stream space is pixels, and the macOS injector queries CoreGraphics for
its own points conversion (see
[`../input/CGEVENT_MACOS_SPEC.md`](../input/CGEVENT_MACOS_SPEC.md) "Coordinate
space").

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

📋 **Specced; Hackintosh-benchmarked.** An Objective-C SCK wrapper exists and was
measured on a Hackintosh running macOS 26.5.1, with the raw data outside this
repository (see "Performance Targets"); real Apple Silicon hardware has not been
measured, and macOS is not a built/shipped platform. The refactor moves that
wrapper into the `featherdesk-addon-sck` crate, built into the `sck` add-on
cdylib, without changing the underlying capture logic.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_sck]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

```toml
[addon_module_sck]
display_id       = 0              # 0 = main display, or a CGDirectDisplayID
```

`display_id` is the one key this section declares. `0` is a **sentinel, not an
id**: `kCGNullDirectDisplay` is `0`, so no real display can carry it, which is
what makes it usable as "whatever `CGMainDisplayID()` returns right now". Any
other value is a `CGDirectDisplayID` and must match one of the
`ProbeReport.displays[].id` values this add-on enumerated — the same number
`NSScreen` exposes as `NSScreenNumber`. A `display_id` naming no enumerated
display fails construction with `AbiErr::BadConfig`; it is not silently replaced
by the main display, because a stream of the wrong monitor looks like a working
stream. Display selection is static: a change takes effect on restart (see
[`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md) "Display
selection").

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail. There is deliberately **no**
cursor key here — whether the pointer is composited is `CaptureConfig.embed_cursor`
and nothing else (see "Cursor Handling").



---

## Stream Params Translation

This add-on implements the `ConfigurableCapturer` trait (see [`specs/core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)). SCK supports hot reconfiguration via `updateConfiguration:`.

| Param change | SCK API | Hot? |
|--------------|---------|------|
| `width`, `height` | `SCStreamConfiguration.width/height` + `[stream updateConfiguration:completionHandler:]` | yes |
| `fps` | `SCStreamConfiguration.minimumFrameInterval` + `updateConfiguration:` | yes |
| `bit_depth=10` / `hdr=true` | `SCStreamConfiguration.pixelFormat = kCVPixelFormatType_64RGBALeAccurate` + `updateConfiguration:` (requires macOS 14+) | yes |
| `color_space` | Set automatically based on display; `CGColorSpaceCreateWithName` from `CMSampleBuffer` attachment | n/a (read-only) |
| Rotation | **ScreenCaptureKit composites display rotation itself** — the `CMSampleBuffer` always arrives upright — so this add-on reports `Rotation::R0` on every `Frame` and `FbInfo` and never applies a rotation of its own. A rotated Mac panel is a plain resolution change to the pipeline (see [`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md) "Display rotation") | n/a (composited) |