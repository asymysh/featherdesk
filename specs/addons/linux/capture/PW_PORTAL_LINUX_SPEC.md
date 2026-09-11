# Add-On Spec: `pw_portal` — xdg-desktop-portal ScreenCast capture (Linux, no root)

## Overview

`pw_portal` is a **no-root** Linux capture add-on that obtains a screen-capture
stream through `org.freedesktop.portal.ScreenCast` and consumes the resulting
**PipeWire** node. It is the companion to
[`WL_SCREENCOPY_LINUX_SPEC.md`](./WL_SCREENCOPY_LINUX_SPEC.md) and covers the
compositors that one cannot: **GNOME (Mutter) and KDE (KWin)**, which implement
no wlroots screencopy protocol but do ship portal backends.

It is never selected ahead of `kms_egl` or `wl_screencopy`
(`LINUX_SPEC.md` "Runtime probe order" step 4), for one reason that dominates
every performance consideration: **the portal requires interactive consent.**

> **The consent prompt is this add-on's defining constraint.** The first
> `ScreenCast` session opens a compositor-drawn dialog asking the local user to
> pick a screen and approve sharing. Nobody can answer it over FeatherDesk —
> the session being authorized *is* the one that would display it. So this
> add-on is only usable where either (a) somebody is physically at the machine
> for first launch, or (b) a **restore token** from a previous approval is
> available. Plan around that or the add-on cannot start.

Like `wl_screencopy`, this is **not** a container story — FeatherDesk is not a
containerized application (`GAP_TRIAGE.md` closed finding C1); *no-root capture*
and *run in Docker* are separate claims and only the first is in scope.

---

## Requirements

- A session D-Bus and `xdg-desktop-portal` **plus a backend** that implements
  `ScreenCast` (`xdg-desktop-portal-gnome`, `-kde`, `-wlr`, `-hyprland`).
- PipeWire running, and `libpipewire-0.3` available to the add-on. Note the
  audio add-on already links it (`PIPEWIRE_LINUX_SPEC.md`); this add-on links it
  independently — an add-on's native dependencies are linked into **that
  library**, never the host (CENTRAL_SPEC "Add-on loading model").
- The same user session as the compositor. No root, no `CAP_SYS_ADMIN`, no DRM
  master.

---

## Session establishment and the restore token

```
CreateSession  → session handle
SelectSources  → types=MONITOR, multiple=false,
                 cursor_mode = Metadata | Embedded (see caps below),
                 persist_mode = 2 ("persist until revoked")
Start          → [consent dialog, unless restore_token is accepted]
                 → response carries streams[] (node id, size, position)
                   and, with persist_mode=2, a NEW restore_token
OpenPipeWireRemote → fd → pw_stream connected to the node id
```

- `persist_mode = 2` plus a stored `restore_token` is what makes launch 2..N
  non-interactive. The token is **single-use and rotated**: every successful
  `Start` returns a fresh one, and the add-on MUST write the new value back or
  the next launch prompts again.
- The token is stored at `[addon_module_pw_portal] restore_token_file`, created
  **mode 0600**. It is a capability to capture this user's screen; treat it with
  the same care as a credential. It is **not** written to the main config file,
  because that file is operator-authored and hot-reloaded.
- A rejected or expired token is not an error: the add-on falls back to a normal
  `Start` (which prompts). A **denied** dialog is
  `StreamError::Unrecoverable` — retrying re-prompts a user who just said no.

---

## ABI surface

Implements `AddonKind::Capture` (`0x01`) per
[`../../../core/MODULE_ABI.md`](../../../core/MODULE_ABI.md). `init(host)` calls
`featherdesk_abi::install_log_sink(host.log)` first; `descriptor()` fills `os` /
`arch` from `abi::HOST_OS` / `abi::HOST_ARCH`, `id = "pw_portal"`, empty
`codecs`; every entry point and trait method is wrapped in
`std::panic::catch_unwind` returning `AbiErr::Unrecoverable` (`7`); built
`panic = "unwind"`.

### Capability bits

| Bit | Set? | Why |
|-----|------|-----|
| `SURFACE` (1<<0) | **yes**, when the negotiated `pw_stream` format is `SPA_DATA_DmaBuf` | `next_surface()` returns `SurfaceHandle::DmaBuf` from the buffer's fd, stride, fourcc and modifier. PipeWire may renegotiate to `MemFd`/`MemPtr` mid-session (a GPU change, or a compositor that stops exporting); on that transition the add-on returns `StreamError::FallbackToSoftware` **once** and serves the CPU path thereafter, rather than lying about the bit. |
| `CURSOR` (1<<1) | **yes**, when the portal grants `cursor_mode = Metadata` | This is the add-on's advantage over `wl_screencopy`: portal Metadata mode delivers pointer position **and** the cursor bitmap as PipeWire buffer metadata (`spa_meta_cursor`), which is exactly what `next_cursor()` needs. Cleared when the backend grants only `Embedded` or `Hidden`. |
| `CONFIGURABLE` (1<<2) | **no** in v1 | The portal owns source selection; a change means a new session and a new prompt. Mode changes on the selected output arrive as a PipeWire format renegotiation and are reported through the resolution-change flow. |
| `EMBED_CURSOR` (1<<4) | **yes**, when the portal grants `cursor_mode = Embedded` | Requested at `SelectSources` time from `CaptureConfig.embed_cursor`, so the mode is fixed for the session's life. |
| `EMBED_CURSOR_SURF` (1<<5) | **yes**, with the same conditions as `SURFACE` + `EMBED_CURSOR` | The compositor composites before export. |

---

## Cursor Handling

**This is the only no-root Linux add-on that can deliver the pointer
separately**, and the reason to prefer it over `wl_screencopy` where an
interactive prompt is acceptable. The portal offers three cursor modes and the
add-on maps them straight onto the capability bits:

| Portal `cursor_mode` | Bits set | `next_cursor()` | Notes |
|---|---|---|---|
| `Metadata` | `CURSOR` (1<<1) | implemented | Pointer position **and** the cursor bitmap arrive as PipeWire buffer metadata (`spa_meta_cursor`) — exactly the shape `next_cursor()` needs |
| `Embedded` | `EMBED_CURSOR` (1<<4), plus `EMBED_CURSOR_SURF` (1<<5) on the dmabuf path | not implemented | The compositor composites the pointer before export |
| `Hidden` | neither | not implemented | No pointer at all; the add-on is ineligible under both `"separate"` and `"embedded"` |

**`cursor_mode` is chosen once, before the session starts.** Unlike every other
capture add-on, this one cannot switch pointer delivery after construction: it is
a `SelectSources` argument. The pipeline therefore resolves `cursorMode` from
`[capture] cursor_mode` **before** `construct()`, passes it in
`CaptureConfig.embed_cursor`, and the add-on requests the matching portal mode.

A backend that grants something other than what was requested causes the add-on
to clear the bit it cannot honour — a granted-mode mismatch must never become a
capability lie (MODULE_ABI "Misbehaving add-ons"). Because the grant is only
known after the session is established, the add-on re-reports its authoritative
caps from the constructed object, and the pipeline re-resolves `cursorMode`
against them at step 4: if the resolution now returns `None`, or a mode whose
`embed_cursor()` differs from what the constructor was handed, the pipeline falls
through to the next candidate rather than running a session whose pointer
behaviour is not what was asked for (MODULE_PIPELINE step 3d / step 4).

---

## Configuration

```toml
[addon_module_pw_portal]
restore_token_file = ""     # Where the portal restore token is cached (mode 0600).
                            # "" = <state dir>/featherdesk/pw_portal.token.
                            # Deleting it forces a fresh consent prompt.
                            # This is a capability to capture the screen — not a
                            # value to put in the operator-authored main config.
allow_prompt       = true   # false = never open a consent dialog: if no valid
                            # restore token exists, probe() reports
                            # available:false with reason "consent_required".
                            # Set false on an unattended host so selection falls
                            # through instead of hanging on a dialog nobody can see.
output_name        = ""     # Preferred monitor name to pre-select where the
                            # backend honours a hint; the portal remains the
                            # authority. "" = let the portal/user choose.
                            # v1 captures exactly one display.
```

Decoded in `construct()` against the add-on's own
`#[serde(deny_unknown_fields)]` struct; an unknown key returns
`AbiErr::BadConfig` (`10`).

---

## Frame acquisition

PipeWire is **push**: the stream's `process` callback fires on PipeWire's own
thread. The add-on therefore keeps a latest-only slot, as `sck` and
`wl_screencopy` do:

| Queue / buffer | Type | Capacity | Overflow policy | Producer → consumer |
|---|---|---|---|---|
| latest-only buffer slot | `Mutex<Option<PwBuf>>` inside the add-on | **1** | latest-wins: the callback **drops the occupant it replaces**, which returns the buffer to PipeWire via `pw_stream_queue_buffer` — a buffer not requeued starves the stream | PipeWire `process` callback → frame thread |

This row belongs in CENTRAL_SPEC "Queues and buffers" alongside row 22 (`sck`)
and `wl_screencopy`'s slot.

- `next_frame()` / `next_surface()` take the slot; empty → `Ok(None)`.
- `next_cursor()` reads `spa_meta_cursor` off the most recent buffer. Per
  CENTRAL_SPEC Contract 8 the **first call after construction MUST return
  `Ok(Some(..))`** carrying current position, visibility and bitmap even though
  nothing changed; from the second call on, "only if it changed" applies.
- `rotation` is reported from the stream's format metadata and never applied.
- `timestamp_ns` is `CLOCK_MONOTONIC` from the buffer's own PTS where PipeWire
  supplies one in that domain, else sampled at slot-fill; it shares the
  process's single monotonic epoch.
- Stream disconnect (`PW_STREAM_STATE_ERROR`, or the portal session closing) is
  `StreamError::Unrecoverable`: the session and its consent are gone, and
  re-establishing means re-prompting.

---

## Performance

The slowest of the three Linux capture paths, by construction: the frame crosses
the compositor, then PipeWire, before the add-on sees it. On the dmabuf path it
is still zero-copy in the sense that matters (no CPU round-trip), but it adds a
buffer hand-off and PipeWire's own scheduling latency.

**Not measured.** No numeric target is asserted here for the same reason as
`wl_screencopy`: inventing one would put a fiction into the motion-to-photon
budget. The first implementation records it.

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | Config decode: unknown key → `AbiErr::BadConfig`; `allow_prompt = false` with no token → `probe()` reports `available:false`, reason `"consent_required"`, **never** a dialog and never `RErr` | No |
| Unit | Restore-token round-trip: a `Start` response's new token replaces the old one on disk at mode 0600; a rejected token falls back to an interactive `Start`; a **denied** dialog is `Unrecoverable`, not a retry | No |
| Unit | `caps()` equals what the constructed object serves: `CURSOR` set only when the backend granted `Metadata`, `EMBED_CURSOR` only when it granted `Embedded`, both clear when it granted `Hidden` | No |
| Unit | The buffer slot requeues to PipeWire exactly once per buffer — a fake stream pushing N buffers with no consumer neither leaks nor starves | No |
| Unit | A mid-session renegotiation from `DmaBuf` to `MemFd` returns `FallbackToSoftware` exactly once and serves the CPU path afterwards, rather than continuing to advertise `SURFACE` | No |
| Integration | Against `xdg-desktop-portal-wlr` and `-gnome`: a frame round-trips with correct dims/stride/fourcc; with `cursor_mode = Metadata` the first `next_cursor()` returns position + visibility + bitmap | Yes (portal backend) |
| Integration | Selection: with `kms_egl` available it wins; with `kms_egl` unavailable and `wl_screencopy` unavailable (Mutter/KWin), this add-on is selected | Yes |
| Integration | Portal session revoked mid-stream → `Unrecoverable` → add-on poisoned for the session, clean shutdown when no candidate remains | Yes |

---

## Status

📋 **Specced, not built, not measured.** Added in the OQ-08 pass. Of the two
no-root add-ons this is the broader-compatibility one (it is the only path on
GNOME and KDE Wayland) and the more operationally awkward one (consent).

---

## Stream Params Translation

This add-on does **not** implement `capture::ConfigurableCapturer` and does not
set `AddonCaps::CONFIGURABLE` in v1 (see "Capability bits"): the portal owns
source selection, and changing it means a new session and — unless a restore
token covers it — a new consent prompt. Parameter changes the table marks
"requires re-init" are handled by the pipeline rebuilding the capturer, the
documented path for a non-configurable capturer (see
[`../../../core/MODULE_STREAM_PARAMS.md`](../../../core/MODULE_STREAM_PARAMS.md)).
Capture is at the source's native resolution; the pipeline owns scaling.

| Param change | Mechanism | Hot? |
|--------------|-----------|------|
| `Width`, `Height` | Captured at the portal source's own size — the pipeline scales (GL blit or libyuv). A size change initiated by the compositor arrives as a PipeWire **format renegotiation** and is reported through the resolution-change flow, not through `update_stream_params` | n/a (pipeline) |
| `FPS` | Pipeline pacing. PipeWire pushes on its own thread at the negotiated rate; sustainable-rate control lowers the advertised fps to what is actually delivered | n/a (pipeline) |
| `BitDepth=10` / `HDR=true` | Not supported — the negotiated `pw_stream` format is the compositor's choice and v1 requests no 10-bit format. The pipeline's HDR admission gate declines, yielding `hdr_unavailable` with reason `no_ten_bit_capture` rather than a silent 8-bit stream | n/a (declined) |
| `ColorSpace` | Read from the negotiated SPA format and reported per surface; the pipeline annotates the encoder | n/a (read-only) |
| Buffer type (`DmaBuf` → `MemFd`/`MemPtr`) | Not a `stream::Params` change, but the one mid-session transition this add-on must survive: it returns `StreamError::FallbackToSoftware` **once** and serves the CPU path thereafter, clearing `SURFACE` rather than lying about the bit | hot (handled in-add-on) |
| `output_name` (source change) | Not a `stream::Params` field — v1 captures exactly one display, and re-selecting the source is a new portal session | requires re-init |
