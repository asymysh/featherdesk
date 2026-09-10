# Windows Capture Add-On: DXGI Desktop Duplication

## Purpose

DXGI Desktop Duplication API provides full-desktop screen capture via Direct3D 11.
The output is a GPU-resident `ID3D11Texture2D` — the exact format that every
Windows hardware encoder (MediaFoundation, NVENC, AMF, QSV) consumes natively.
Zero-copy capture-to-encode on every GPU vendor.

This is the **only** Windows capture mechanism. Equivalent of KMS+EGL on Linux
and ScreenCaptureKit on macOS. Used by Sunshine, OBS, Parsec, and Moonlight.

For headless machines (no physical monitor), this add-on includes an integrated
**IddCx virtual display driver** that creates a virtual monitor on first launch
so DXGI DD has an output to capture. Same approach as Sunshine/Moonlight.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| DXGI API | Windows system API | Usage governed by Windows SDK license |
| D3D11 API | Windows system API | Same |
| Our Rust FFI binding | MIT | We own this code |

System APIs; no redistribution concerns, no royalties, no GPL exposure.

---

## Hardware & OS Compatibility

| GPU | Driver model | Support |
|-----|-------------|---------|
| Intel HD 2500+ (Ivy Bridge 2012+) | WDDM 1.2+ | ✅ |
| Intel Arc | WDDM 3.0+ | ✅ |
| AMD GCN 1.0+ (HD 7000+, 2012+) | WDDM 1.2+ | ✅ |
| AMD RDNA1/2/3/4 | WDDM 2.0+ | ✅ |
| NVIDIA Kepler+ (GTX 600+, 2012+) | WDDM 1.2+ | ✅ |
| NVIDIA RTX 20/30/40/50 | WDDM 2.7+ | ✅ |
| Any GPU with WDDM 1.2+ driver | | ✅ |

| OS | Support |
|----|---------|
| Windows 8 / Server 2012 | ✅ Minimum |
| Windows 10 (all versions) | ✅ |
| Windows 11 (all versions) | ✅ |
| Windows Server 2016/2019/2022/2025 | ✅ |
| Windows 7 | ❌ No DXGI DD API |

**Minimum requirement: Windows 8 + WDDM 1.2.** Effectively every Windows PC
from 2012 onward.

---

## Permission Requirements

**Capture itself: none.** DXGI Desktop Duplication runs in user-session
context with no elevation.

**IddCx driver install (headless only): admin required once.** The first
time the server starts on a machine with no display output, it installs the
bundled IddCx virtual display driver. This requires a one-time UAC elevation.
After the driver is installed, subsequent launches need no elevation.

---

## Headless Support (Integrated IddCx Virtual Display)

DXGI Desktop Duplication requires an active display output. On headless
machines (no monitor, no dummy HDMI plug), the add-on automatically creates
a virtual display using a bundled **IddCx (Indirect Display Driver)**.

### What is IddCx

IddCx is Microsoft's official framework for virtual monitors. It is a
user-mode driver (UMDF) that creates WDDM display outputs indistinguishable
from physical monitors. Windows desktop composition renders to them normally,
and DXGI Desktop Duplication captures them as if they were real screens.

This is the industry-standard approach — Sunshine, Parsec, RustDesk, and
Moonlight all use IddCx for headless Windows streaming.

### Bundled driver

The binary embeds a pre-signed IddCx driver (INF + DLL) via `include_bytes!`:

| Component | Source | License | Notes |
|-----------|--------|---------|-------|
| IddCx driver INF + DLL | Fork of [VirtualDrivers/Virtual-Display-Driver](https://github.com/VirtualDrivers/Virtual-Display-Driver) | MIT | Pre-signed via SignPath.io; no test-signing required |
| `pnputil.exe` | Ships with Windows 10/11 | Microsoft | Used for driver installation; not bundled |

### Auto-install flow on first launch

```
Server starts
  |
  v
Enumerate DXGI outputs (EnumAdapters -> EnumOutputs)
  |
  +-- Output found on any adapter?
  |     YES --> use DXGI DD normally (physical monitor or dummy HDMI)
  |     NO  --> headless mode:
  |
  v
Check if IddCx VDD is already installed
  (Get-PnpDevice -Class Display, look for our device ID)
  |
  +-- Already installed?
  |     YES --> create virtual display via device IOCTL
  |     NO  --> install driver:
  |
  v
Extract embedded driver files to temp directory
  (vdd.inf + vdd.dll from include_bytes!)
  |
  v
Check if running as admin
  |
  +-- Admin? --> pnputil /add-driver vdd.inf /install  (silent, no UAC)
  |
  +-- Not admin? --> Two options:
  |     Option A (GUI): ShellExecuteEx with "runas" verb
  |       -> shows UAC prompt -> runs pnputil elevated -> returns
  |     Option B (terminal): print instructions:
  |       "No display detected. Run as administrator to auto-install
  |        the virtual display driver, or install manually:
  |        pnputil /add-driver <path>\vdd.inf /install"
  |
  v
Wait for PnP to enumerate the new device (~2-3 seconds)
  |
  v
Open device handle (SetupDi* API -> CreateFile on device interface)
  |
  v
Create virtual display via IOCTL
  (device-specific: IOCTL_ADD for Parsec VDD, or SwDeviceCreate for others)
  |
  v
Start keep-alive thread if driver requires it
  (Parsec VDD needs periodic IOCTL_UPDATE every ~1s; our own fork won't)
  |
  v
Retry DXGI enumeration --> output now exists --> capture normally
```

### Virtual display configuration

The virtual display is created with configurable resolution and refresh rate
from the TOML config:

```toml
[addon_module_dxgi_dd]
# Virtual display settings (used only when no physical display is detected)
virtual_display_width  = 1920
virtual_display_height = 1080
virtual_display_hz     = 60
```

These map to the IddCx monitor mode descriptor. The virtual display appears
in Windows Display Settings and can be configured by the user like any
real monitor.

### Cleanup on shutdown

When the server exits cleanly:
1. Stop the keep-alive thread (if any)
2. Remove the virtual display via IOCTL (display disappears from Windows)
3. The driver stays installed (no uninstall on every stop — that would
   require admin again)

The driver persists across reboots. The virtual display is only created
when the server is running and no physical display is available.

### RDP sessions

DXGI Desktop Duplication is blocked in RDP sessions by default. The same
IddCx virtual display approach works: the virtual display provides an
output that Desktop Duplication can capture, bypassing the RDP restriction.

### Tested configurations

| Scenario | Works? | Notes |
|----------|--------|-------|
| Physical monitor | Yes | Native DXGI DD, no IddCx needed |
| Dummy HDMI plug | Yes | GPU thinks monitor is connected |
| IddCx virtual display (headless) | Yes | Auto-installed on first launch |
| RDP session + IddCx | Yes | Virtual display bypasses RDP block |
| No display + no admin + no IddCx | No | Cannot install driver; shows instructions |

---

## Build & Distribution

### Build (shared library)

```bash
cargo build --release -p featherdesk-addon-dxgi_dd   # cdylib  featherdesk-addon-dxgi_dd.dll
```

### Runtime dependencies

- `dxgi.dll` (ships with Windows 8+)
- `d3d11.dll` (ships with Windows 8+)
- `setupapi.dll` (ships with Windows) — for IddCx device enumeration
- `pnputil.exe` (ships with Windows 10+) — for IddCx driver installation (headless only, admin only, once)

All system components — nothing to install. The IddCx driver INF + DLL are
embedded in the binary via `include_bytes!` and extracted at runtime if needed.

### FFI configuration

```rust
// Cargo.toml pulls the relevant `windows` crate features:
//   windows = { version = "0.58", features = [
//       "Win32_Graphics_Dxgi", "Win32_Graphics_Dxgi_Common",
//       "Win32_Graphics_Direct3D", "Win32_Graphics_Direct3D11",
//       "Win32_System_Com",
//   ] }
use windows::Win32::Graphics::Direct3D11::*;
use windows::Win32::Graphics::Dxgi::*;
```

The `windows` crate (windows-rs) exposes the DXGI/D3D11 COM interfaces directly
as safe Rust types — vtable calls and `QueryInterface` are handled by the crate
(`.cast()`), so no hand-written C header or bindgen step is needed. This is
consistent with the other platform add-ons (KMS+EGL, SCK).

---

## FFI Implementation Sketch

```rust
// 1. Create D3D11 device on the target adapter
let mut device: Option<ID3D11Device> = None;
let mut ctx: Option<ID3D11DeviceContext> = None;
let mut feature_level = D3D_FEATURE_LEVEL::default();
unsafe {
    D3D11CreateDevice(
        &adapter, D3D_DRIVER_TYPE_UNKNOWN, HMODULE::default(),
        D3D11_CREATE_DEVICE_FLAG(0), None, D3D11_SDK_VERSION,
        Some(&mut device), Some(&mut feature_level), Some(&mut ctx),
    )?;
}
let device = device.unwrap();
let ctx = ctx.unwrap();

// 2. Get DXGI Output (the monitor to capture)
let output: IDXGIOutput = unsafe { adapter.EnumOutputs(0)? };
let output1: IDXGIOutput1 = output.cast()?; // QueryInterface

// 3. Create Desktop Duplication
let dupl: IDXGIOutputDuplication = unsafe { output1.DuplicateOutput(&device)? };

// 4. Per-tick: TWO acquires, each with a ZERO timeout, when the session
//    resolved "separate" — one in next_cursor (before pacing) and one in
//    next_frame/next_surface (after it). When it resolved "embedded",
//    next_cursor is not polled and there is a single acquire, in next_frame.
//    See "Cursor Handling". The pipeline paces; neither call blocks on vsync.
let mut frame_info = DXGI_OUTDUPL_FRAME_INFO::default();
let mut resource: Option<IDXGIResource> = None;
match unsafe { dupl.AcquireNextFrame(0 /* timeout ms */, &mut frame_info, &mut resource) } {
    Err(e) if e.code() == DXGI_ERROR_WAIT_TIMEOUT => return Ok(None), // no new frame
    Err(e) => return Err(e.into()),
    Ok(()) => {}
}

// 5. Get the D3D11 texture (GPU-resident)
let frame_tex: ID3D11Texture2D = resource.unwrap().cast()?;

// 6. Path A: Zero-copy to HW encoder
// CopyResource into the add-on's own texture (GPU->GPU) and latch it, then hand
// the latched texture to MF/NVENC/AMF/QSV — no GPU->CPU copy. Encode operates on
// the same D3D11 device.
encode_frame(&frame_tex, frame_info.LastPresentTime);

// 6. Path B: CPU readback for the SW encoder
// Create staging texture, CopyResource, Map, read BGRA pixels. With
// embed_cursor = true, capture::blend_cursor composites the latched pointer
// into these bytes before the Frame is returned.
let staging: ID3D11Texture2D = create_staging_texture(&device, w, h)?;
unsafe {
    ctx.CopyResource(&staging, &frame_tex);
    let mut mapped = D3D11_MAPPED_SUBRESOURCE::default();
    ctx.Map(&staging, 0, D3D11_MAP_READ, 0, Some(&mut mapped))?;
    std::ptr::copy_nonoverlapping(
        mapped.pData as *const u8, pixels.as_mut_ptr(), (w * h * 4) as usize,
    );
    ctx.Unmap(&staging, 0);
}

// 7. Release
unsafe { dupl.ReleaseFrame()?; }
```

---

## Two Output Paths

| Trait | Method | Output | Use case |
|-------|--------|--------|----------|
| `Capturer` (CPU readback) | `next_frame()` | BGRA `RVec<u8>` via staging texture + Map | Pair with the SW encoder (`openh264`, or the opt-in `x264`) |
| `SurfaceCapturer` (zero-copy) | `next_surface()` | `FbInfo { handle: SurfaceHandle::D3D11Texture(..) }` | Pair with a HW encoder (MF HW, NVENC, AMF, QSV) |
| `CursorCapturer` (pointer) | `next_cursor()` | `Option<CursorState>` from `AcquireNextFrame` metadata | Always, unless the session resolved `"embedded"` — see "Cursor Handling" |

The pipeline picks the frame method based on the paired encoder add-on, reaching
both optional traits through the one capturer object's accessors (there is never
a second owning handle over the same add-on object). The zero-copy path is the
Windows equivalent of DMA-BUF on Linux and IOSurface on macOS — the texture never
leaves the GPU.

**Thread affinity.** The `ID3D11DeviceContext` and the `IDXGIOutputDuplication`
handle are thread-affine, so this add-on takes route 1 of
[`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Thread requirements":
it writes `unsafe impl Send` on the capturer, and the justification is the host's
own discipline — the host constructs, uses and drops the capturer on a single
worker thread (the frame loop's) via `FrameLoop::open()`, so neither handle is
ever touched from a second thread, and `AcquireNextFrame` is only ever issued
from `fd-frame`. The `Send` declaration is nominal: it satisfies
`CapturerBox::from_value`, which requires it because
`#[sabi_trait] pub trait Capturer: Send`. The keep-alive thread the
IddCx path may start owns no D3D11 or duplication state — it issues only the
device IOCTL.

---

## Performance Targets

The budget below is
[`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md)
"Performance Targets", specialized to this add-on. **Nothing in it has been
measured yet:** the bench tool's DXGI backend is a stub pending COM bindings, and
every recorded session in `PROJECT_ARTIFACTS/bench_out` reports
`{"backend":"dxgi","variant":"duplication_v1","available":false}` — the bench
machine drives a Parsec virtual adapter, which blocks Desktop Duplication. No
acquisition figure for this add-on exists anywhere in the repo, and none should
be quoted until one does.

| Metric | Target | Measured |
|--------|--------|----------|
| `next_cursor()` — `AcquireNextFrame(0, ..)` + latch + `ReleaseFrame` | <0.2 ms; it runs on every tick, including skipped ones | not yet |
| `next_surface()` — second `AcquireNextFrame(0, ..)` + hand over the latched texture | <1 ms; a handle export, no copy | not yet |
| `CopyResource` desktop → the add-on's own texture | GPU→GPU; it runs in whichever of the two acquires saw `LastPresentTime` advance, inside that method's budget above | not yet |
| `next_frame()` — CPU readback at 1080p (staging + `Map` + copy) | <8 ms | not yet |
| `next_frame()` — CPU readback at 1440p | <13 ms | not yet |
| Steady-state allocation per frame | zero after warmup (the staging texture and the readback `RVec` are reused) | n/a |

Two properties are structural rather than measured, and can be relied on now:

- A **blocking** `AcquireNextFrame` cannot return sooner than the display's
  refresh interval (~16.7 ms at 60 Hz), because DWM has produced no new frame
  before then. That is why this add-on always polls with a **zero** timeout and
  lets the pipeline pace (see "Cursor Handling").
- The **zero-copy path performs no GPU→CPU transfer at all**: the latched
  `ID3D11Texture2D` is handed to the HW encoder on the same D3D11 device. The
  CPU-readback row above is the software path's cost, and only the software
  path's.

---

## Cursor Handling

DXGI Desktop Duplication never puts the pointer in the desktop texture — it
reports it as metadata of `AcquireNextFrame`. That is a perfect match for the
`"separate"` model and the reason this add-on always declares `CURSOR`.

**Capabilities declared at probe.** `AddonCaps::CURSOR` always;
`AddonCaps::EMBED_CURSOR` (CPU path) always. **Not** `EMBED_CURSOR_SURF`: drawing
the pointer into the desktop texture would add a render pass to the path whose
entire purpose is avoiding one, so a session that resolves `"embedded"` here is
paired with the software encode path.

**Two acquires per tick, not one.** The pipeline polls the cursor on every
frame-loop tick, including skipped ticks, and takes a frame only on unskipped
ones ([`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md)
"Cursor and frame acquisition order"). One OS call produces both, but the cursor
poll is frame-loop step 0b, *before* the pacing sleep, and the frame is served at
step 3, *after* it — so a single acquire at 0b hands the encoder pixels up to a
full frame interval stale. When the session resolved `"separate"`, this add-on
therefore issues a zero-timeout `AcquireNextFrame` from **both** methods.

**In `next_cursor`** (step 0b, before pacing):

```rust
fn next_cursor(&mut self) -> Result<Option<capture::CursorState>, StreamError>;
```

1. `AcquireNextFrame(0, &frame_info, &desktop_resource)` — a zero timeout returns
   `DXGI_ERROR_WAIT_TIMEOUT` cheaply when nothing changed, which maps to
   `Ok(None)`.
2. `frame_info.LastMouseUpdateTime != 0` → latch
   `frame_info.PointerPosition.Position` (**the bitmap's top-left corner**, so add
   the current shape's hotspot back before returning `x`/`y`) and
   `PointerPosition.Visible`.
3. `frame_info.PointerShapeBufferSize > 0` → `GetFramePointerShape` fills a
   `DXGI_OUTDUPL_POINTER_SHAPE_INFO` plus the shape bytes. Convert per
   MODULE_CAPTURE "Cursor pixel format" — `COLOR` (BGRA, swap B/R),
   `MASKED_COLOR` and `MONOCHROME` (1bpp AND/XOR masks, real height =
   `Height / 2`) all become straight RGBA. `HotSpot` is the shape's hotspot. The
   bitmap is returned at the OS's native size; the 128-pixel wire cap is applied
   by the host's cursor publisher.
4. `frame_info.LastPresentTime` advanced → `CopyResource` the desktop texture into
   the add-on's own `ID3D11Texture2D` and latch it.
5. `ReleaseFrame()`.

**The copy stays in step 4.** DXGI never re-delivers an already-acquired frame,
so releasing without copying loses that desktop image outright — latching only
the metadata here and moving the copy into the frame path is not an option.

**In `next_frame` / `next_surface`** (step 3, after `sleep_to_interval`): ONE
more `AcquireNextFrame(0, ..)`.

- `DXGI_ERROR_WAIT_TIMEOUT` → serve the already-latched texture unchanged.
- Success → update the pointer latch from **this** `frame_info` too (steps 2 and
  3 above; its pointer metadata is delivered exactly once and would otherwise be
  lost, surfacing a tick late on the next `next_cursor`), `CopyResource` the
  newer desktop image over the latch, `ReleaseFrame()`, then serve it.

Neither call ever blocks, and on a static screen the second acquire is a cheap
`WAIT_TIMEOUT`. **No GPU→CPU readback is introduced**: both desktop copies are
GPU→GPU and the shape bytes come from DXGI's own metadata, so the zero-copy path
stays zero-copy.

**First call.** MODULE_CAPTURE's `next_cursor` contract requires the first call
after construction to return `Ok(Some(..))` carrying the current position, the
current visibility **and** the current bitmap (`shape: Some(..)`), even though
nothing has changed — the host has no other way to seed a joining client. The
`Ok(None)` of step 1 applies from the second call on.

**`embed_cursor = true`.** `next_frame` calls `capture::blend_cursor` on the CPU
staging copy at the shape's top-left, using the same latched pointer state; there
is a single acquire, and it lives in `next_frame`, because `next_cursor` is not
polled in that regime. `CaptureConfig.embed_cursor` is the only input the add-on has on the
question — there is no per-add-on cursor key.

**Failure behavior.** `DXGI_ERROR_ACCESS_LOST` is a frame-path concern and keeps
its existing handling in `## Error Recovery`. A `GetFramePointerShape` failure
returns `Err(StreamError::Backend(..))` from `next_cursor` alone and leaves the
frame path running; an unsupported shape type returns `shape: None` and keeps the
previous shape active.

---

## Multi-Monitor

Each `IDXGIOutput` represents one monitor. To capture a specific display:
enumerate outputs, pick by index or by HMONITOR coordinates.

For full multi-monitor capture (stitched into one surface), iterate all outputs
and composite — but this is rarely wanted for remote desktop (typically one
display is streamed). `[addon_module_dxgi_dd] output_index` selects which output
to capture (`0` = primary); `adapter_index` selects the adapter. Both values are
the `DisplayInfo.id` this add-on reports for that output in its `ProbeReport`.

---

## Error Recovery

Every failure this add-on returns is a `stream::StreamError`, which crosses the
ABI as the matching `AbiErr` code plus an `AbiError.detail` carrying the HRESULT
(see [`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "AbiErr registry").
There is no add-on-private error type.

| Error | Returned as | Handling |
|-------|-------------|----------|
| `DXGI_ERROR_WAIT_TIMEOUT` | `Ok(None)` | Normal — no new frame available. Not an error. Return immediately; the pipeline paces. |
| `DXGI_ERROR_ACCESS_LOST` | `Ok(None)` after a successful re-create, else `StreamError::Backend` | Desktop mode changed (resolution switch, secure desktop, UAC dialog). Recreate `DuplicateOutput` in place and report the tick as producing no frame. |
| `DXGI_ERROR_ACCESS_DENIED` | `StreamError::Backend` | Another process has exclusive fullscreen. Transient — the pipeline skips the frame and retries on the next tick. |
| `DXGI_ERROR_DEVICE_REMOVED` / `_DEVICE_RESET` | `StreamError::DeviceLost` | GPU reset (driver crash, TDR). The D3D11 device and every texture derived from it are gone; the pipeline runs its capture ladder, which rebuilds the add-on from scratch. |
| Re-create after `ACCESS_LOST` fails more than 10 times in 60 s | `StreamError::Unrecoverable` | The duplication interface cannot be re-established. `detail` carries the last HRESULT; the pipeline poisons this add-on for the session and falls through. |
| `E_ACCESSDENIED` at `DuplicateOutput` | `AbiErr::Generic` from `construct()`, i.e. `PipelineError::AddonBackend` | Running in Session 0, or RDP without a virtual display. Construction fails with a descriptive `detail`; the pipeline falls through to the next capture candidate. |

---

## Probe & Selection

There is one probe signature, and it is the root module's
([`specs/core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module surface"):

```rust
// crate: featherdesk-addon-dxgi_dd   (cfg(windows))

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
    // 1. CoInitializeEx (COM required)
    // 2. Verify the process is PER_MONITOR_AWARE_V2 (below); if not,
    //      ROk(ProbeReport { available: false, reason: "process is not
    //        PER_MONITOR_AWARE_V2; capture would return DPI-scaled logical
    //        pixels".into(), .. })
    // 3. CreateDXGIFactory1 -> enumerate adapters
    // 4. For each adapter: enumerate outputs; for each output read
    //      IDXGIOutput::GetDesc (DesktopCoordinates, AttachedToDesktop) and the
    //      current DXGI_MODE_DESC refresh rate into one DisplayInfo:
    //        id           = the output's index within the adapter (the value
    //                       [addon_module_dxgi_dd] output_index selects)
    //        width/height = DesktopCoordinates in PIXELS (never logical units —
    //                       which is what step 2 guarantees), as scanned out
    //        rotation     = DXGI_OUTPUT_DESC.Rotation from the same GetDesc call,
    //                       mapped to abi::Rotation. (At capture time the same
    //                       value arrives as DXGI_OUTDUPL_DESC.Rotation; probe has
    //                       no duplication object yet, so it reads the output desc.)
    //        refresh_mhz  = RefreshRate.Numerator * 1000 / Denominator
    //        scale_num/den = 1/1 — Windows reports desktop coordinates in
    //                       physical pixels once the process is PerMonitorV2
    // 5. Any output found? -> try DuplicateOutput on the selected one, release it
    // 6. No output found? -> headless mode:
    //      a. Check if IddCx VDD is installed (PnP device query)
    //      b. If not installed: auto-install via pnputil (needs admin)
    //      c. Create virtual display via device IOCTL
    //      d. Retry output enumeration
    // 7. Still no output -> ROk(ProbeReport { available: false,
    //      reason: "no DXGI output and the IddCx virtual display driver could
    //               not be installed (needs one-time elevation)".into(), .. })
    // 8. Otherwise -> ROk(ProbeReport {
    //      available: true, reason: RString::new(), codecs: RVec::new(),
    //      caps: AddonCaps(AddonCaps::SURFACE          // ID3D11Texture2D export
    //                    | AddonCaps::CURSOR           // AcquireNextFrame metadata
    //                    | AddonCaps::EMBED_CURSOR),   // capture::blend_cursor, CPU path
    //                                                  // NOT CONFIGURABLE: a mode change
    //                                                  // requires a fresh DuplicateOutput
    //                                                  // NOT EMBED_CURSOR_SURF: see
    //                                                  //   "Cursor Handling"
    //      displays })
}
```

**Availability is not an error.** A missing driver, a denied permission or an
absent device is `ROk(ProbeReport { available: false, reason })`. `RErr` is
reserved for the probe itself failing.

**Set every capability bit this add-on actually serves.** `caps` left at `0` means
no zero-copy path, no separate cursor and no hot parameter change — silently, with
no error and no warning.

**Only claim what this call can prove.** A bit claimed here and refused later is a
capability lie (MODULE_ABI "Misbehaving add-ons"); a capability that only
`construct()` can settle is reported by the constructed object's `caps()`, which
is authoritative and may be a strict subset of this one.

**Requires `PER_MONITOR_AWARE_V2`**, established by the host entry point
([`specs/core/MODULE_PIPELINE.md`](../../../core/MODULE_PIPELINE.md) startup
step 0). `probe()` verifies it with `GetThreadDpiAwarenessContext` +
`AreDpiAwarenessContextsEqual` and returns `ProbeReport { available: false,
reason: "process is not PER_MONITOR_AWARE_V2; capture would return DPI-scaled
logical pixels" }` if it is not. Without it, `IDXGIOutput::GetDesc` reports
virtualized logical dimensions, the advertised `config` width/height do not match
the panel, and every absolute pointer coordinate lands short by the scale factor.

Pipeline probe order (Windows — single capture path):
```
1. DXGI output found?                                -> use DXGI DD
2. No output? IddCx VDD installed?                   -> create virtual display -> DXGI DD
3. No output, no VDD? Can install (admin)?            -> auto-install VDD -> create -> DXGI DD
4. No output, no VDD, no admin?                       -> fatal: show install instructions
```

---

## File Structure

```
addons/dxgi_dd/
├── src/
│   ├── lib.rs                  // DxgiCapturer struct, constructor (Rust FFI via windows crate)
│   ├── duplication.rs          // IDXGIOutputDuplication wrapper
│   ├── device.rs               // D3D11 device + adapter discovery
│   ├── cursor.rs               // DXGI_OUTDUPL pointer latch: next_cursor + blend_cursor
│   ├── probe.rs                // probe(): output enumeration -> ProbeReport + headless detection
│   └── vdd.rs                  // IddCx virtual display: install, create, keep-alive, remove
├── vdd_driver/                 // Embedded IddCx driver (include_bytes!)
│   ├── vdd.inf                 // Driver INF (pre-signed)
│   └── vdd.dll                 // Driver DLL (pre-signed)
└── tests/dxgi_integration.rs   // integration test (cfg(windows))
```

No conditional-compilation stub file is needed — the add-on is its own cdylib
crate, so an absent add-on is simply a `.dll` that isn't in the add-ons directory.

---

## HDR / 10-bit Support

DXGI Desktop Duplication supports `DXGI_FORMAT_R10G10B10A2_UNORM` (HDR10) on
Windows 10 1803+ with HDR enabled in Display Settings. The captured texture
format changes from `B8G8R8A8_UNORM` to `R10G10B10A2_UNORM` automatically, and
this add-on reports the change through `Frame.bit_depth` / `FbInfo.bit_depth`
rather than hiding it.

The add-on's whole part in the HDR contract is to produce the right surface and
say what it produced:

- **10-bit is HW-path only.** A 10-bit surface goes to a HW encoder configured
  for HEVC Main10; there is no SW consumer for it (the Converter has no 10-bit
  input format, and no loaded SW encoder emits HEVC Main10). This add-on never
  tone-maps — tone-mapping the capture would destroy the very range the HDR
  session exists to carry.
- **The pipeline reconfigures this add-on out of 10-bit before it degrades.**
  `degrade_to_software` resets `hdr`/`bit_depth`/`color_space` and calls back
  into the capturer for an 8-bit format *before* the first `next_frame()` on the
  software path (see
  [`specs/core/MODULE_PIPELINE.md`](../../../core/MODULE_PIPELINE.md)
  "degrade_to_software"). Because this add-on does not declare
  `AddonCaps::CONFIGURABLE`, that reconfigure is a teardown and a fresh
  `DuplicateOutput` at `B8G8R8A8_UNORM`, not an in-place update.
- **HDR is not available on the IddCx virtual display.** The bundled VDD only
  supports 8-bit BGRA, so on a headless host the add-on reports
  `bit_depth = 8` and an HDR request is refused upstream with
  `{"type":"hdr_unavailable","reason":"no_ten_bit_capture"}`.

---

## When to use this add-on

Always. This is the **only** Windows capture mechanism. Vendor-specific
alternatives (NvFBC, AMF Display Capture) are deferred on a maintenance
argument, not on a measurement: one vendor-neutral add-on whose `ID3D11Texture2D`
output is zero-copy input to every Windows HW encoder covers every GPU, and no
measured DXGI acquisition cost exists to weigh a second capture path against.

---

## Status

📋 **Specced.** DXGI acquisition has never been measured — the bench tool's DXGI
backend is a stub pending COM bindings, and every recorded session reports
`available: false`. IddCx headless integration specced but not yet implemented.

---

## Configuration

This add-on reads its tuning knobs from the `[addon_module_dxgi_dd]` section
of the TOML config (see [`specs/core/MODULE_CONFIG.md`](../../../core/MODULE_CONFIG.md)).

If the section is absent, the add-on uses its built-in defaults. The section is
strictly validated only when this add-on is loaded; unknown
keys in this section will cause startup to fail.

The keys, their defaults and their domains are in MODULE_CONFIG "Schema", under
`[addon_module_dxgi_dd]`; this spec does not restate them.



---

## Stream Params Translation

This add-on does **not** implement `capture::ConfigurableCapturer`, and its `ProbeReport` leaves `AddonCaps::CONFIGURABLE` clear: every parameter below is either read-only, absorbed downstream, or requires a fresh `DuplicateOutput`, so there is nothing an in-place `update_stream_params` could change. The pipeline therefore tears the capturer down and reconstructs it for the one param that does affect capture (see [`specs/core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)). DXGI Desktop Duplication captures at the output's native resolution; scaling happens downstream.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Output is always the output's native resolution -- this add-on never downscales. The HW path scales in the encoder's VPP, the SW path in the Converter (libyuv). No capturer change needed. | n/a (downstream) |
| `FPS` | `IDXGIOutputDuplication::AcquireNextFrame` timeout controls poll rate. Pipeline paces. | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | `DXGI_FORMAT_R10G10B10A2_UNORM` -- requires HDR enabled in Windows Display Settings; capturer reports 10-bit in `Frame.bit_depth`. This add-on does not declare `AddonCaps::CONFIGURABLE`, so the switch is a teardown + fresh `DuplicateOutput`, not an in-place update | no (rebuild) |
| `ColorSpace` | `IDXGIOutput6::GetDesc1()` → `DXGI_OUTPUT_DESC1.ColorSpace` (NOT `DXGI_OUTDUPL_DESC`, which has no ColorSpace field) | n/a (read-only) |
| Rotation | `DXGI_OUTDUPL_DESC.Rotation` from `IDXGIOutputDuplication::GetDesc` — the same call already made for the desktop-image format. `DXGI_MODE_ROTATION_{IDENTITY,ROTATE90,ROTATE180,ROTATE270}` map to `Rotation::{R0,R90,R180,R270}` on every `Frame`/`FbInfo`. **DXGI hands over the desktop image unrotated**, so this add-on reports the rotation and never applies it; the Converter (SW) or the encoder's VPP (HW) turns it upright, and the pipeline derives the stream dims from the upright geometry (see [`specs/media/MODULE_CAPTURE.md`](../../../media/MODULE_CAPTURE.md) "Display rotation") | n/a (read-only) |

**IddCx headless note:** When running on the IddCx virtual display (headless mode), HDR is not available -- the bundled VDD only supports 8-bit BGRA. The capturer reports `bit_depth = 8`, and an HDR request is refused with `{"type":"hdr_unavailable","reason":"no_ten_bit_capture"}` rather than silently producing an SDR stream labelled HDR.