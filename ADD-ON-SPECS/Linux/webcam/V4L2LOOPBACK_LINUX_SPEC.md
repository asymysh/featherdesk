# Linux Webcam Add-On: v4l2loopback

## Purpose

The `v4l2loopback` add-on is the Linux virtual-camera sink for the core Webcam
module (see [`../../../../specs/MODULE_WEBCAM.md`](../../../../specs/MODULE_WEBCAM.md)).
It implements `webcam.Sink`: the core decode path produces NV12 frames and calls
`WriteFrame` at the negotiated fps; this add-on presents those frames as a real
local camera so host apps (Zoom, Teams, Meet, Chrome/WebRTC, OBS) can open it.

v4l2loopback is a Linux kernel module that creates virtual V4L2 video devices. A
producer writes raw frames to `/dev/videoN`; any consumer that enumerates
cameras sees `/dev/videoN` as an ordinary capture device and reads frames from
it. FeatherDesk is the producer; the conferencing app is the consumer.

> The kernel module is **not** loaded by this add-on by default (it needs root).
> The operator loads it once; the add-on only opens the resulting device. See
> Build & Distribution.

---

## License

| Component | License | Notes |
|-----------|---------|-------|
| v4l2loopback kernel module | GPL (kernel) - used via `ioctl`/`write`, no linkage | `/dev/videoN` is a normal V4L2 node |
| Our implementation | MIT | Pure Go via `syscall`/`golang.org/x/sys/unix` - **no CGo** |

The module is consumed through the stable V4L2 `ioctl` ABI (no symbol linkage),
so our MIT code does not derive from the GPL module. Reference Go bindings to
adapt the output/write path: `vladimirvivien/go4vl` or `blackjack/webcam`.

---

## How It Works

The sink opens the loopback node `O_WRONLY`, sets the output format once, then
writes one NV12 buffer per frame:

```
open("/dev/videoN", O_WRONLY)
  -> ioctl(VIDIOC_S_FMT, v4l2_format{
         type         = V4L2_BUF_TYPE_VIDEO_OUTPUT,
         width        = cfg.Width,
         height       = cfg.Height,
         pixelformat  = V4L2_PIX_FMT_NV12,        // apps prefer NV12; no convert
         field        = V4L2_FIELD_NONE,
         bytesperline = cfg.Width,
         sizeimage    = cfg.Width*cfg.Height*3/2,
     })
  -> per frame: write(fd, nv12, len)               // len = W*H*3/2
```

`V4L2_BUF_TYPE_VIDEO_OUTPUT` is correct here because FeatherDesk is the *output*
side feeding the loopback; the consuming app sees the matching `VIDEO_CAPTURE`
side. NV12 is written straight through with no color convert.

### Why exclusive_caps=1 is critical

The module must be loaded with `exclusive_caps=1`. Without it the node reports
both CAPTURE and OUTPUT capabilities at once, and Chrome / WebRTC (and some
Electron apps) **filter the device out** - they refuse a camera that also
advertises OUTPUT. With `exclusive_caps=1` the node presents as OUTPUT until the
first consumer opens it, then flips to a pure CAPTURE device, which every app
accepts.

### Device discovery

The add-on finds the right node by scanning
`/sys/devices/virtual/video4linux/*/name` for the configured `card_label`
("FeatherDesk Camera"). The first match yields `/dev/videoN`. If `device_path`
is set in config, discovery is skipped and that node is used directly.

---

## Build & Distribution

```bash
go build -tags v4l2loopback -o featherdesk-linux ./cmd/server
```

No CGo and no `-l` libraries: format negotiation and frame writes are plain
`ioctl`/`write` syscalls. The kernel module must be present at runtime:

- Install: `apt install v4l2loopback-dkms` (Debian/Ubuntu) or build from source
  (needs matching kernel headers).
- Load (operator, once, as root):
  ```bash
  modprobe v4l2loopback devices=1 card_label="FeatherDesk Camera" exclusive_caps=1
  ```
- The add-on does **not** `modprobe` itself by default. If the daemon happens to
  run as root it MAY attempt the load above (`auto_modprobe`); the documented
  path is an operator-managed module plus a udev rule so the daemon need not be
  root to open `/dev/videoN`.

---

## Constructor & Probe

```go
// internal/webcam/v4l2loopback/v4l2loopback_linux.go  (build tag: v4l2loopback)

// Probe returns true if a v4l2loopback node with the configured card_label
// exists (module loaded). False -> add-on not selected.
func Probe() bool

// New constructs the sink from its addon config. The device is opened in Start.
func New(cfg Config) (webcam.Sink, error)
```

`Probe` scans `/sys/devices/virtual/video4linux/*/name` for `card_label` (or
stats `device_path` when set). No match means the module is not loaded -> the
add-on is not selected and a hint is logged (`modprobe v4l2loopback ...`).

`Start(cfg webcam.SinkConfig)` discovers/opens the node and runs `VIDIOC_S_FMT`
at `cfg.Width x cfg.Height` in NV12. `Stop` closes the fd. `Close` releases any
cached state. `WriteFrame` issues one `write` of the NV12 buffer.

---

## Error Handling

| Failure | Behavior |
|---------|----------|
| module not loaded (no node) | `Probe` false -> not selected; log `modprobe v4l2loopback` hint |
| label set but no matching node | `Start` error naming the expected card_label |
| EACCES opening `/dev/videoN` | `Start` error; log video-group / udev hint |
| `VIDIOC_S_FMT` rejects geometry | `Start` error (no half-open device) |
| `write` short / EAGAIN | log once (rate-limited), drop the frame, keep device open |
| `write` EBADF / ENODEV (module unloaded mid-run) | `Stop` then surface error + metric |

Frame drops are rate-limited in the log; lifecycle failures are surfaced to the
receiver (no silent teardown).

---

## File Structure

```
internal/webcam/v4l2loopback/
  v4l2loopback_linux.go   // build tag: v4l2loopback (Sink impl)
  ioctl_linux.go          // VIDIOC_* numbers + v4l2_format / v4l2_pix_format structs
  discover_linux.go       // scan /sys .../video4linux/*/name by card_label
  stub.go                 // build tag: !v4l2loopback (no-op, never registers)
  v4l2loopback_test.go    // mock-fd unit tests + /dev/videoN integration tests
```

---

## Configuration

Reads `[addon_module_v4l2loopback]` (see
[`../../../../specs/MODULE_CONFIG.md`](../../../../specs/MODULE_CONFIG.md)).

```toml
[addon_module_v4l2loopback]
device_path   = ""                  # "" = auto-discover by card_label; else e.g. "/dev/video10"
card_label    = "FeatherDesk Camera"  # must match the modprobe card_label
auto_modprobe = false               # if true AND running as root, load the module on Start
```

If absent, defaults apply. Strictly validated only when this add-on is compiled
in; unknown keys fail startup.

---

## Status

Specced - not yet built. Implementation order: probe/discover by card_label ->
open + `VIDIOC_S_FMT` NV12 (Start/Stop) -> `WriteFrame` write path + short-write
handling -> optional `auto_modprobe` (root) -> ffplay/Chrome integration test.
