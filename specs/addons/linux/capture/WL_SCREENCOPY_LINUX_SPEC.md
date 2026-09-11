# Add-On Spec: `wl_screencopy` — wlroots screencopy capture (Linux, no root)

## Overview

`wl_screencopy` is a **no-root** Linux capture add-on that asks the compositor
for the contents of a `wl_output` over the wlroots-family Wayland protocols. It
exists for the two deployments [`KMS_EGL_LINUX_SPEC.md`](./KMS_EGL_LINUX_SPEC.md)
structurally cannot serve:

1. a host where `CAP_SYS_ADMIN` / root is not grantable, and
2. **headless Wayland** with no forceable KMS connector — a nested or virtual
   compositor whose output never reaches a CRTC, which `kms_egl` cannot see at
   all (see [`../../../PLATFORM_COMPAT.md`](../../../PLATFORM_COMPAT.md)
   "Headless on Linux").

It is **not** a replacement for `kms_egl` and is never selected ahead of it
(`LINUX_SPEC.md` "Runtime probe order" step 3). It is also **not** a container
story: FeatherDesk is not a containerized application (`GAP_TRIAGE.md` closed
finding C1). *No-root capture* and *run in Docker* are separate claims and only
the first is in scope here.

**Requires no privilege at all** — no DRM master, no `CAP_SYS_ADMIN`, no
`/dev/dri` write access beyond what the compositor already granted the user. It
must run as the **same user** as the compositor and needs `WAYLAND_DISPLAY` (or
`WAYLAND_SOCKET`) in its environment.

---

## Protocols used

Two protocols, in preference order. The add-on binds whichever the compositor
advertises and reports the choice in `probe()`'s `reason` field for
observability.

| Protocol | Buffer | Path | Notes |
|----------|--------|------|-------|
| `ext-image-copy-capture-v1` | dmabuf or shm | preferred | The standardized successor; present on recent wlroots, Hyprland and others. Use where advertised — this is the forward path and the only one likely to outlive the `wlr-*` prefix. |
| `zwlr_export_dmabuf_unstable_v1` | dmabuf | zero-copy | Delivers the output's dmabuf directly per frame. Fastest, but export-dmabuf gives no damage information and no cursor compositing control. |
| `zwlr_screencopy_unstable_v1` | shm or dmabuf `wl_buffer` | fallback | Universally available on wlroots-family compositors; supports `overlay_cursor` and `copy_with_damage`. |

**Compositor support is the hard limit.** These are wlroots-family protocols —
sway, Hyprland, river, labwc, Wayfire, and the nested capture compositors used
for headless. **GNOME (Mutter) and KDE (KWin) implement none of them**; on those,
`probe()` reports `available: false` with a reason naming the missing global, and
selection falls through to [`PW_PORTAL_LINUX_SPEC.md`](./PW_PORTAL_LINUX_SPEC.md).

---

## ABI surface

Implements `AddonKind::Capture` (`0x01`) per
[`../../../core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module
surface". As required of every add-on: `init(host)` calls
`featherdesk_abi::install_log_sink(host.log)` first; `descriptor()` fills `os` /
`arch` from `abi::HOST_OS` / `abi::HOST_ARCH`, `id = "wl_screencopy"`, empty
`codecs`; every entry point and trait method is wrapped in
`std::panic::catch_unwind` returning `AbiErr::Unrecoverable` (`7`); the crate is
built `panic = "unwind"`.

### Capability bits

| Bit | Set? | Why |
|-----|------|-----|
| `SURFACE` (1<<0) | **yes**, when the bound protocol delivers a dmabuf (export-dmabuf always; screencopy/image-copy where the compositor accepts a linux-dmabuf `wl_buffer`) | `next_surface()` returns `SurfaceHandle::DmaBuf` with the compositor's stride, fourcc and modifier. Cleared when only shm is available, in which case the add-on is CPU-path only. |
| `CURSOR` (1<<1) | **no** | There is **no wlroots protocol that reports pointer position or the cursor bitmap to a screencopy client.** The pointer is either composited in or absent; it cannot be delivered separately. This is the add-on's one real functional deficit versus `kms_egl` (DRM cursor plane) and `pw_portal` (portal `Metadata` cursor mode). |
| `CONFIGURABLE` (1<<2) | **no** in v1 | Output selection is resolved once at construction. A mode change on the selected output is reported through the resolution-change flow, not through `update_stream_params`. |
| `EMBED_CURSOR` (1<<4) | **yes** | `zwlr_screencopy`'s `overlay_cursor` argument, and `ext-image-copy-capture-v1`'s equivalent, ask the compositor to composite the pointer. Honoured on every `next_frame()`. |
| `EMBED_CURSOR_SURF` (1<<5) | **yes**, with the same condition as `SURFACE` | The compositor composites before handing over the buffer, so an embedded pointer is present on the surface path too. **Not set** under `zwlr_export_dmabuf`, which has no cursor-overlay argument. |

---

## Cursor Handling

**This add-on can deliver the pointer in exactly one way: composited into the
frame.** There is no wlroots protocol that reports pointer position or the cursor
bitmap to a screencopy client, so `CURSOR` (1<<1) is never set and `next_cursor()`
is not implemented. `EMBED_CURSOR` (1<<4) is always set — `zwlr_screencopy`'s
`overlay_cursor` argument and `ext-image-copy-capture-v1`'s equivalent ask the
compositor to composite the pointer, and the add-on honours
`CaptureConfig.embed_cursor` on every `next_frame()`.

| `[capture] cursor_mode` | `CursorMode::resolve` | Result |
|---|---|---|
| `"embedded"` | `Embedded` | Selected; `overlay_cursor = true`, pointer composited by the compositor |
| `"auto"` | `Embedded` | Selected; resolves to embedded because `CURSOR` is clear |
| `"separate"` | `None` | **Ineligible at selection** — the pipeline skips this add-on exactly as it skips one whose probe reported `available:false`, and names it in the startup rejection list if no candidate remains |

The `"separate"` row is the correct outcome, not a degradation: an add-on that
can satisfy the requested mode in no way is not an eligible capturer
(CENTRAL_SPEC Contract 8, MODULE_PIPELINE step 3d). The pointer is therefore
never silently missing — either it is in the frame, or this backend was not
chosen.

Under `zwlr_export_dmabuf` there is no cursor-overlay argument at all, so
`EMBED_CURSOR_SURF` (1<<5) stays clear on that protocol and the add-on reports
the pointer as unavailable on the surface path rather than claiming an overlay
it cannot produce.

---

## Configuration

```toml
[addon_module_wl_screencopy]
output_name    = ""        # wl_output name, e.g. "HEADLESS-1", "DP-1". "" = the
                           #   compositor's first advertised output. This is the
                           #   per-capturer display selector (cf. dxgi_dd
                           #   output_index, sck display_id). v1 captures exactly
                           #   one display — MODULE_CAPTURE "Display selection".
protocol       = "auto"    # "auto" | "image_copy" | "export_dmabuf" | "screencopy"
                           #   "auto" = the preference order in "Protocols used".
                           #   Forcing one that is not advertised is a probe
                           #   failure, not a silent fallback.
prefer_dmabuf  = true      # false forces the shm path (diagnostic only — it
                           #   costs the zero-copy path and clears SURFACE)
```

Decoded in `construct()` against the add-on's own
`#[serde(deny_unknown_fields)]` struct; an unknown key returns
`AbiErr::BadConfig` (`10`). The host has no schema for this section.

---

## Frame acquisition

`zwlr_screencopy` and `ext-image-copy-capture-v1` are **request/response per
frame**: the client asks for a capture, the compositor signals `ready`. The
add-on keeps exactly one capture in flight and a **latest-only slot** for the
completed buffer, the same shape `sck` uses:

| Queue / buffer | Type | Capacity | Overflow policy | Producer → consumer |
|---|---|---|---|---|
| completed-frame slot | `Mutex<Option<Buf>>` inside the add-on | **1** | latest-wins: the callback **drops the occupant it replaces**, releasing that buffer back to the pool — an unreleased dmabuf pins compositor memory | compositor event loop → frame thread |

This row belongs in CENTRAL_SPEC "Queues and buffers" alongside row 22 (`sck`).

- `next_frame()` / `next_surface()` take the slot. Empty slot → `Ok(None)`
  ("no new frame"), which the pipeline treats as pacing, not error.
- The compositor's own frame cadence bounds the achievable rate; the pipeline
  still owns pacing, and sustainable-rate control lowers the advertised fps to
  what is actually delivered.
- **`rotation`** comes from the `wl_output` transform and is **reported, never
  applied** — the add-on never rotates (MODULE_CAPTURE "Display rotation").
- **`timestamp_ns`** is `CLOCK_MONOTONIC`, sampled when the compositor's `ready`
  event names the frame's presentation time where the protocol supplies one, and
  at slot-fill otherwise. It shares the process's single monotonic epoch
  (CENTRAL_SPEC "Canonical Media Clock").
- A `failed` event, or the loss of the Wayland connection, is
  `StreamError::Unrecoverable` — the compositor has gone away and no retry inside
  the add-on can recover it. The pipeline poisons the add-on and walks the probe
  order (MODULE_PIPELINE "Add-On Crash Recovery" Level 2).

---

## Performance

Slower than `kms_egl` on the same hardware, and the spec says so rather than
implying parity:

| Path | Expected cost | Why |
|------|--------------|-----|
| `export_dmabuf` / dmabuf `wl_buffer` | `kms_egl` + one compositor round-trip | Zero-copy still, but each frame is a request/response through the compositor's event loop rather than a direct scanout read |
| shm | dmabuf cost + a full CPU copy of the framebuffer | The compositor copies into shared memory; at 1440p that is the ~24 MB/frame the zero-copy path exists to avoid |

No numeric budget is asserted here. This add-on is **not measured** — like every
other Linux capture path in this repo — and a target invented without a
measurement would be a fiction the motion-to-photon budget then inherits. The
first implementation records the number; until then the budget line is
`kms_egl`'s plus an unquantified round-trip.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | Config decode: unknown key → `AbiErr::BadConfig`; `protocol` naming an unadvertised protocol → probe failure with a reason, never a silent fallback | No |
| Unit | `probe()` on a compositor advertising none of the three globals returns `ROk(ProbeReport{available:false, reason})` naming the missing global — **not** `RErr` | No |
| Unit | `caps()` equals the set of optional methods the constructed object serves: `SURFACE`/`EMBED_CURSOR_SURF` clear on the shm path and under `export_dmabuf` respectively; `CURSOR` never set (the ABI's capability-lie row) | No |
| Unit | The completed-frame slot drops the buffer it replaces exactly once — a fake compositor delivering N frames with no consumer leaks no buffers | No |
| Integration | Against a headless `wlroots` compositor (`WLR_BACKEND=headless`): a frame round-trips with correct dims, stride and fourcc; output transform 90/180/270 is **reported** and the buffer is not rotated | Yes (wlroots) |
| Integration | Selection: with `kms_egl` also loaded and available, `kms_egl` wins; with `kms_egl` loaded but reporting `available:false` (no CRTC), this add-on is selected | Yes |
| Integration | `[capture] cursor_mode = "separate"` makes this add-on ineligible at selection; `"auto"` resolves `"embedded"` and the pointer is present in the frame | Yes |
| Integration | Compositor exit mid-session surfaces `StreamError::Unrecoverable`, the add-on is poisoned for the session, and the pipeline shuts down cleanly when no candidate remains | Yes |

---

## Status

📋 **Specced, not built, not measured.** Added in the OQ-08 pass to give headless
and no-root Linux a path that actually works, replacing the incorrect claim that
`kms_egl` captures an Xvfb or display-server-less host.

---

## Stream Params Translation

This add-on does **not** implement `capture::ConfigurableCapturer` and does not
set `AddonCaps::CONFIGURABLE` in v1 (see "Capability bits"): the `wl_output` and
its protocol binding are resolved once, at `construct()`. Any parameter change
the table below marks "requires re-init" is therefore handled by the pipeline
tearing the capturer down and rebuilding it, which is the documented path for a
non-configurable capturer (see
[`../../../core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)).
Capture is at the output's native resolution; the pipeline owns scaling.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Output is captured at the `wl_output`'s current mode — the pipeline scales (GL blit or libyuv). No capturer change needed | n/a (pipeline) |
| `FPS` | Pipeline pacing. Capture is request/response per frame, so the achievable ceiling is the compositor's own cadence; sustainable-rate control lowers the advertised fps to what is actually delivered | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | Not supported — the compositor chooses the buffer format and the wlroots protocols expose no format request. The add-on reports its actual fourcc and the pipeline's HDR admission gate declines, yielding `hdr_unavailable` with reason `no_ten_bit_capture` rather than a silent 8-bit stream | n/a (declined) |
| `ColorSpace` | Read from the `wl_output` / buffer metadata and reported per surface; the pipeline annotates the encoder | n/a (read-only) |
| `output_name` (display change) | Not a `stream::Params` field — v1 captures exactly one display and the selection is config-only | requires re-init |
