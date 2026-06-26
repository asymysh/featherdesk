# Linux Webcam Add-Ons

## Default binary webcam: NONE

Mirroring the capture and encoder architecture: the default Linux binary ships
with **zero webcam backends**. With no webcam add-on compiled in, the core
binary exposes **no virtual camera** — there is no way to pipe the remote
stream into local apps. Virtual-camera output is an opt-in build-tagged add-on
implementing the `webcam.Sink` interface.

This keeps the default build free of kernel-module assumptions; only
deployments that actually need a virtual webcam pull in the dependency.

---

## Available webcam add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **v4l2loopback** | `v4l2loopback` | [`./V4L2LOOPBACK_LINUX_SPEC.md`](./V4L2LOOPBACK_LINUX_SPEC.md) | Expose the stream as a `/dev/videoN` device that any V4L2 app (browsers, OBS, Zoom) can open | 📋 Specced |

### Recommended combinations

| Deployment | Webcam add-on | Build command |
|------------|---------------|---------------|
| Virtual camera into local apps | `v4l2loopback` | `go build -tags "kms_egl,libva,openh264,v4l2loopback"` |
| No virtual camera needed | *(none)* | `go build -tags "kms_egl,libva,openh264"` |

---

## Runtime requirement: v4l2loopback kernel module

The add-on writes frames into a loopback video device, which requires the
**v4l2loopback kernel module** to be present and loaded:

```
sudo apt install v4l2loopback-dkms      # or the distro equivalent
sudo modprobe v4l2loopback              # creates /dev/videoN
```

At startup the add-on probes for a loopback device and fails closed with a clear
error if the module isn't loaded. The spec covers auto-`modprobe` and persisting
the module across reboots.

---

## Runtime webcam probe order

With a single webcam add-on the selection is trivial:

```
1. v4l2loopback compiled in AND loopback device available?  → register webcam.Sink
2. Otherwise                                                 → no webcam capability
```

---

## When ready to add a new webcam backend

1. Write the spec at `ADD-ON-SPECS/Linux/webcam/{NAME}_LINUX_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the webcam index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/webcam/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
