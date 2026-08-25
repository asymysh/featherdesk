# Implementation Readiness — Interaction, Client, and Add-on Specs

Scope: `specs/interaction/*`, `specs/client/*`, `specs/addons/**`. Assessed for
whether a competent engineer could implement each spec without needing to ask a
clarifying question first.

## Summary Table

| Module | Interface Clarity | Wiring Clarity | Blocking Questions |
|---|---|---|---|
| `interaction/MODULE_INPUT.md` | Clear | Clear | 0 (interface itself is unambiguous — but see cross-cutting gap below) |
| `interaction/MODULE_CLIPBOARD.md` | Clear | Clear | 0 |
| `interaction/MODULE_FILETRANSFER.md` | Clear | Clear | 0 |
| `interaction/MODULE_GAMEPAD.md` | Clear | Clear | 0 |
| `client/MODULE_WEB_CLIENT.md` | Minor gaps | Clear | 1 (see cross-cutting gap below) |
| `client/MODULE_NATIVE_CLIENT.md` | Clear (intentionally deferred) | Clear | 0 — connectivity is explicitly flagged as "the open question," not a hidden gap |
| `addons/linux/LINUX_SPEC.md` (full read) | Clear | Clear | 0 |
| `addons/macos/MACOS_SPEC.md` (structural read) | Clear | Clear | 0 |
| `addons/windows/WINDOWS_SPEC.md` (structural read) | Clear | Clear | 0 |
| `addons/linux/encoders/HW/LIBVA_LINUX_SPEC.md` (spot-check) | Clear | Clear | 0 |
| `addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md` (spot-check, full) | Clear | Clear | 0 |

Addon specs use their own valid template (`Purpose`/`Implementation Plan`/`File
Structure`/`Status` etc. rather than module-spec's `Public Interface` heading) —
an automated heading-name check initially flagged all ~28 addon files as
"incomplete," but this is a false positive from comparing against the wrong
template, not a real gap (verified by reading actual content). A repo-wide scan
for `TBD`/`TODO`/`FIXME`/"to be determined" across all of `specs/addons/`,
`specs/client/`, `specs/interaction/` returned zero hits.

Addon spot-check coverage: read `LINUX_SPEC.md`, `MACOS_SPEC.md`,
`WINDOWS_SPEC.md` (per-OS overviews) in full or near-full; deep-read
`LIBVA_LINUX_SPEC.md` and `INTERCEPTION_WINDOWS_SPEC.md`; the remaining ~26
per-vendor/per-device addon specs were not individually deep-read (time-boxed
per the lighter-touch instruction) but are covered indirectly — every one is
referenced by name with a consistent add-on-ID/license/capability table from
its owning module spec (`MODULE_ENCODE.md`, `MODULE_HARDWARE_ENCODE.md`,
`MODULE_AUDIO.md`, `MODULE_INPUT.md`, `MODULE_GAMEPAD.md`), and none showed up
in the TBD/placeholder scan.

## Cross-Cutting Blocking Gap: Stuck Input on Tab Blur (confirmed real, matches an actual old-code bug)

**This is the most important finding.** The old Go client had a confirmed bug
(see `PROJECT_ARTIFACTS/review/input_injection_uinput/phase4/T9-keyboard-capture.md`):
no `blur`/`visibilitychange` handling meant a key held down when the browser tab
lost focus stayed "stuck" pressed on the host indefinitely, because the browser
stops delivering `keyup` events but the connection itself doesn't close.

**The new spec reproduces the same gap, verified across three files:**

1. `client/MODULE_WEB_CLIENT.md` "Input Handling (binary)" — the example JS wires
   `keydown`/`keyup`/`pointerdown`/`pointerup`/`wheel` listeners with zero mention
   of `blur` or `visibilitychange` anywhere in the file (`grep -i "blur\|visibilitychange\|focus"` — zero matches).
2. `interaction/MODULE_INPUT.md`'s `Dispatcher::release_all()` is real and
   correctly designed — but its doc comment and every caller explicitly says it
   fires "when the controller slot is released or seized (takeover)" and on
   `Drop`. **Tab blur is neither** — the WebTransport session stays open, so
   neither trigger fires.
3. `addons/windows/input/INTERCEPTION_WINDOWS_SPEC.md`'s "Held-input release (no
   stuck keys)" section confirms the entire release-on-disconnect design
   (`Dispatcher::release_all` + defensive per-add-on `Drop`) is scoped to
   connection lifecycle only, not client-side focus events.

**Concrete fix needed (not yet in any spec):** `MODULE_WEB_CLIENT.md`'s
`input.js` needs to track a pressed-keys/buttons `Set` client-side and, on
`window.blur` / `document.visibilitychange` (hidden), synthesize up-records for
everything currently held (or send one dedicated "release all" wire message) —
this is a client-side responsibility since the server has no way to know the
tab lost focus. This gap should be added to `MODULE_WEB_CLIENT.md`'s Internal
Architecture and cross-referenced from `MODULE_INPUT.md`.

## Scroll-Wheel Magnitude — CONFIRMED FIXED (not a gap)

The old Go code collapsed `deltaY` to a fixed `±1` at the server call site
(`internal/input/protocol.go`, see
`PROJECT_ARTIFACTS/review/input_injection_uinput/phase2/T4-event-injection.md`).
The new spec fixes this cleanly: `MODULE_INPUT.md`'s wire format defines Scroll
(Type `0x23`) as **signed 16-bit high-resolution `Dx`/`Dy`** (not a boolean/step),
explicitly says "positive = scroll right/down (W3C deltaX/deltaY)," and the
`KeyMouseInjector::inject_scroll(dx, dy, unit)` interface takes the real
magnitude through to injection, with a documented per-add-on sign-negation rule
and a unit-translation table (pixel/line/page across all three OSes). No add-on
spec was found deviating from this. This is a genuine, verified improvement — no
further action needed.

## Verdict

**Not fully implementation-ready** — one concrete, well-understood blocking gap
(tab-blur stuck-input handling) needs a `MODULE_WEB_CLIENT.md` addition before
this area can be called done; everything else in interaction/client/addons is
clear, consistent, and free of placeholders.

---

# Addendum — Final Review Pass

## The blocking gap in the verdict above is CLOSED

The verdict says "one concrete, well-understood blocking gap (tab-blur
stuck-input handling) needs a `MODULE_WEB_CLIENT.md` addition." That addition
was made: **R-CLI-12 "Held-Input Release on Focus Loss"** specifies
`heldKeys`/`heldButtons` Sets, tracking in all four key/pointer handlers, and
`releaseAllHeld()` bound to `window.blur` and `visibilitychange`. It is covered
by user story US-INP-9, which no longer carries a `GAP` citation.

## Further additions since that verdict

| Directive | What it closes |
|---|---|
| **R-CLI-13** — On-Screen Connection/Stats HUD | `stats.js` and the "status overlay" were named in the file listing but never specced beyond outbound telemetry — nothing was ever specced to be shown **to the user**, which is why RTT/latency visibility was never built pre-refactor. F9-toggleable HUD showing FPS, `inputLatencyMs`, resolution+codec+bitrate, connection state — all values the client already holds. Explicitly **not** an active bandwidth probe. |
| **R-CLI-14** — Deterministic Teardown and Backgrounded-Tab Behavior | Nothing specified when the `AudioContext`, `AudioWorklet`, `VideoDecoder`, in-flight `VideoFrame`s, or the session itself are released. Now: one idempotent `teardown()` off `pagehide` (not `unload` — bfcache + mobile reliability), in reverse-setup order with `releaseAllHeld()` **first** while the transport is still open. Plus a per-resource backgrounding table: **audio keeps playing, video stops decoding**, keyframe requested on return. Closes user story US-AUD-7. |

Cross-module fixes in the final pass that touch this area: the client's
control-stream dispatch summary omitted `chroma_unsupported` from its C→S list
even though the client sends it (line 167) — corrected; and CENTRAL_SPEC
**Contract 9** now specifies the clipboard's host→client half, which the client
consumed but no host-side module produced (see the core+media addendum).

## Verdict (revised)

**Implementation-ready for interaction + client + add-ons.** The blocking gap is
closed, the client's file listing no longer names unspecced files, and every
user story in `PROJECT_ARTIFACTS/user_stories/` covering this area cites a real
directive or spec heading.
