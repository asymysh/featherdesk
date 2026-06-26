# macOS Webcam Add-Ons

## Default binary webcam: NONE

Mirroring the capture and encoder architecture: the default macOS binary ships
with **zero webcam backends**. With no webcam add-on compiled in, the core
binary exposes **no virtual camera** — there is no way to pipe the remote
stream into local apps. Virtual-camera output is an opt-in build-tagged add-on
implementing the `webcam.Sink` interface.

In practice this is the one add-on with real deployment friction on macOS: it
ships a separate system extension that Apple requires to be signed, notarized,
and user-approved. Only deployments that need a virtual webcam take that on.

---

## Available webcam add-ons

| Add-on | Build tag | Spec | When to use | Status |
|--------|-----------|------|------------|--------|
| **CoreMediaIO Camera Extension** | `cmio_ext` | [`./CMIO_EXT_MACOS_SPEC.md`](./CMIO_EXT_MACOS_SPEC.md) | Expose the stream as a system camera (macOS 12.3+) that any app selects as a webcam | 📋 Specced |

### Recommended combinations

| Deployment | Webcam add-on | Build command |
|------------|---------------|---------------|
| Virtual camera into local apps | `cmio_ext` | `go build -tags "sck,vt_hw,cmio_ext"` |
| No virtual camera needed | *(none)* | `go build -tags "sck,vt_hw"` |

---

## Runtime requirement: approved Camera Extension

The modern CoreMediaIO **Camera Extension** (CMIO) replaces the deprecated DAL
plugin model and is mandatory on **macOS 12.3+**. It must be:

- **signed + notarized** with the System Extension entitlement, and
- **approved once** by the user in **System Settings → General → Login Items &
  Extensions** (per machine).

Until the extension is approved, no virtual camera appears to apps. The add-on
detects the approval state and surfaces it to the operator; the spec covers the
activation request flow.

---

## Runtime webcam probe order

With a single webcam add-on the selection is trivial:

```
1. cmio_ext compiled in AND extension approved?  → register webcam.Sink
2. Otherwise                                       → no webcam capability
```

---

## When ready to add a new webcam backend

1. Write the spec at `ADD-ON-SPECS/macOS/webcam/{NAME}_MACOS_SPEC.md`
2. Add a row to the add-on table above
3. Add a row to the webcam index in `specs/CENTRAL_SPEC.md` → "Platform & Add-On Spec Index"
4. Implement under `internal/webcam/{name}/` with a Go build tag
5. Wire the runtime probe order in `MODULE_PIPELINE.md`
