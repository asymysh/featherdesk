# Windows Webcam Add-Ons

## Default binary webcam: NONE

Mirroring the capture and encoder architecture: the default Windows binary
ships with **zero webcam backends**. With no webcam add-on compiled in, the
core binary exposes **no virtual camera** — there is no way to pipe the remote
stream into local apps. Virtual-camera output is an opt-in build-tagged add-on
implementing the `webcam.Sink` interface.

This keeps the default build free of COM-registration side effects; only
deployments that actually need a virtual webcam register the filter.

---

## Available webcam add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **DirectShow virtual camera** | `dshow_vcam` | [`./DSHOW_VCAM_WINDOWS_SPEC.md`](./DSHOW_VCAM_WINDOWS_SPEC.md) | Expose the stream as a DirectShow capture source (OBS-style) that apps select as a webcam | 📋 Specced |

### Recommended combinations

| Deployment | Webcam add-on | Build command |
|------------|---------------|---------------|
| Virtual camera into local apps | `dshow_vcam` | `go build -tags "dxgi_dd,mf_hw,openh264,dshow_vcam"` |
| No virtual camera needed | *(none)* | `go build -tags "dxgi_dd,mf_hw,openh264"` |

---

## Runtime requirement: register the DirectShow filter

The virtual camera is a DirectShow **source filter** (a COM server). Windows
must have it registered once, with admin rights:

```
regsvr32 featherdesk-vcam.dll        # one-time, elevated
```

Same model as OBS's virtual camera. After registration the filter persists; the
add-on probes for it at startup and fails closed with a clear error if the
filter isn't registered. The spec covers the installer-driven `regsvr32` step.

---

## Runtime webcam probe order

With a single webcam add-on the selection is trivial:

```
1. dshow_vcam compiled in AND filter registered?  → register webcam.Sink
2. Otherwise                                        → no webcam capability
```

---

## When ready to add a new webcam backend

1. Write the spec at `ADD-ON-SPECS/Windows/webcam/{NAME}_WINDOWS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the webcam index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/webcam/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
