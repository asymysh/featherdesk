# Windows Webcam Add-On: DirectShow Virtual Camera

## Purpose

The `dshow_vcam` add-on is the Windows virtual-camera sink for the core Webcam
module (see [`../../../../specs/MODULE_WEBCAM.md`](../../../../specs/MODULE_WEBCAM.md)).
It implements `webcam.Sink`: the core decode path produces NV12 frames and calls
`WriteFrame` at fps; this add-on publishes those frames through a DirectShow
capture source filter so host apps enumerate a "FeatherDesk Camera".

A DirectShow video capture source filter is a COM DLL registered under
`CLSID_VideoInputDeviceCategory`. Any app that enumerates cameras through
DirectShow sees it. Modern apps (newer Teams/Zoom, UWP) capture via the
MediaFoundation Frame Server, but Windows ships a MediaFoundation->DirectShow
compatibility shim for capture sources, so a DirectShow source still reaches the
large majority of apps. This is exactly how OBS Virtual Camera works.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| DirectShow / COM (system) | Windows system | Loaded at the app side, not redistributed |
| Our Go shared-mem writer + glue | MIT | Pure `syscall` to `kernel32` - no CGo on the Go side |
| Virtual-camera COM filter (C++) | MIT (small vendored component) | Shipped prebuilt, `regsvr32`'d at install; ref obs-virtualcam / softcam |

The COM filter is a small prebuilt C++ component (DLL). The FeatherDesk Go side
never loads into host apps; it only writes shared memory.

---

## How It Works

A Go process cannot be an in-proc COM server inside arbitrary host apps, so the
design follows OBS: **decouple the producer from the filter via shared memory**.

```
FeatherDesk (Go)                        Host app (Zoom / Chrome / ...)
  decode -> NV12                           enumerates cameras (DirectShow)
     |                                          |
     v                                          v
  CreateFileMapping(name) -------------- maps the same named region
  MapViewOfFile (write)                  the registered COM filter reads it
     |                                          |
  copy NV12 + header{w,h,seq,fps,pts}     wraps the bytes as IMediaSample
     |                                          |
  SetEvent(frame-ready) -------------->   serves frames on the capture pin
```

The Go side owns a named file mapping plus a named frame-ready event. Each
`WriteFrame` copies the NV12 buffer and a small header (width, height, sequence,
fps, pts) into the region and signals the event. The registered C++ filter,
loaded into each consuming app, maps the same region and serves the latest frame
as an `IMediaSample` on its output pin when the app pulls.

### Shared-memory layout

```
[ header ]  magic, version, width, height, fps, seq, pts_nanos, byte_len
[ frame  ]  NV12 plane (width*height*3/2 bytes), double-buffered to avoid tearing
```

Two frame slots are used round-robin; the header `seq` tells the reader which
slot is current, so the filter never reads a half-written buffer.

### Registration

The filter DLL is registered once at install (admin):

```
regsvr32 featherdesk-vcam.dll
```

This writes the filter CLSID under `CLSID_VideoInputDeviceCategory`. The Go
add-on does not register anything at runtime; it only checks registration.

---

## Build & Distribution

```bash
go build -tags dshow_vcam -o featherdesk-windows.exe ./cmd/server
```

The Go side is pure `syscall` (`CreateFileMapping`, `MapViewOfFile`,
`CreateEvent`, `SetEvent`) - no CGo required. The installer ships the prebuilt
`featherdesk-vcam.dll` and runs `regsvr32` once (admin); uninstall runs
`regsvr32 /u`. The C++ filter is the only non-Go piece and is vendored.

---

## Constructor & Probe

```go
// internal/webcam/dshow_vcam/dshow_vcam_windows.go  (build tag: dshow_vcam)

// Probe returns true if the virtual-camera filter CLSID is registered under
// CLSID_VideoInputDeviceCategory and shared memory is creatable.
func Probe() bool

// New constructs the sink from its addon config. The region is created in Start.
func New(cfg Config) (webcam.Sink, error)
```

`Probe` queries the registry for the filter CLSID under
`CLSID_VideoInputDeviceCategory` (gated by `register_check`) and confirms a
mapping of the configured `shared_mem_name` can be created. Not registered ->
add-on not selected, with a logged install hint.

`Start(cfg webcam.SinkConfig)` creates the named mapping sized for
`cfg.Width x cfg.Height` NV12 (double-buffered) plus the frame-ready event, and
publishes the geometry header. `Stop` unmaps/closes the region and event.
`Close` releases handles. `WriteFrame` copies NV12 into the current slot and
signals the event.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| filter CLSID not registered | `Probe` false -> not selected; log `regsvr32 featherdesk-vcam.dll` hint |
| `CreateFileMapping` / `MapViewOfFile` fails | `Start` error (no usable region) |
| `CreateEvent` fails | `Start` error |
| region exists but wrong size (stale) | recreate at the new geometry; log once |
| `WriteFrame` after `Stop` | error: sink not started |

No host app attached is **not** an error: the producer keeps refreshing the
latest frame; the filter simply has no reader until an app opens the camera.

---

## File Structure

```
internal/webcam/dshow_vcam/
  dshow_vcam_windows.go   // build tag: dshow_vcam (Sink impl)
  sharedmem_windows.go    // CreateFileMapping / MapViewOfFile + header layout
  registry_windows.go     // CLSID_VideoInputDeviceCategory registration check
  stub.go                 // build tag: !dshow_vcam (no-op, never registers)
  dshow_vcam_test.go      // shared-mem round-trip unit tests
  // NOTE: the C++ COM filter DLL is a separate vendored component:
  //   third_party/featherdesk-vcam/  (prebuilt DLL + regsvr32 sources)
```

---

## Configuration

Reads `[addon_module_dshow_vcam]` (see
[`../../../../specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_dshow_vcam]
shared_mem_name = "FeatherDeskVCam"   # named file mapping shared with the filter DLL
register_check  = true                # if true, Probe fails when the CLSID is not registered
```

If absent, defaults apply. Strictly validated only when this add-on is compiled
in; unknown keys fail startup.

---

## Status

Specced - not yet built. Implementation order: registry probe + shared-mem
create (Start/Stop) -> header + double-buffered `WriteFrame` + frame-ready event
-> match the vendored C++ filter's layout -> regsvr32 install/uninstall ->
DirectShow test-app + Zoom/Chrome integration test.
