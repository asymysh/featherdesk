# Open Questions — Gap Review Triage

Source: a comparative gap review of `specs/` against Selkies (primary) plus
Sunshine/Moonlight, RustDesk, KasmVNC/Guacamole and Parsec, conducted
2026-09-10 on `featherdesk-refactor`, followed by a second pass on
engineering-quality gaps.

Of 31 findings, **8 were kept open for discussion** and are renumbered below as
`OQ-01`…`OQ-08`. Everything else is closed — see "Closed findings" at the end,
which exists so closed rows are not re-raised as new discoveries.

Numbering order is **decision dependency**: OQ-01 changes what the transport is
for, which feeds OQ-05/06; OQ-02 and OQ-03 add the features OQ-07's UI would
expose. It is not a priority order — the recommended sequence is at the end.

| ID | Was | Open question | Recommendation | Lands | Status |
|----|-----|---------------|----------------|-------|--------|
| OQ-01 | C11 | Is the WebSocket carrier a fallback or a supported production path, given Cloudflare + P2P? | Reclassify it, and give it liveness | v1 | ✅ **Specced** — carrier reclassified, carrier-generic liveness, named loss owner, performance budget, reachability recipe |
| OQ-02 | C5+C6 | Does v1 ship with sound? And is client→host mic ever in scope? | Re-trigger audio on *Linux* video working; mic Linux-first, post-v1 | v1 / later | ✅ **Decided** — trigger changed to Linux-only; `[audio] enabled` stays `false` by default. Mic split out as OQ-02b, post-v1 |
| OQ-03 | C4 | Should the host desktop mode follow the client window (and DPI)? | No mode-set on Linux; let the client drive encoder output dims + DPR | v1 | ✅ **Specced** — Linux mode-set recorded permanently out of scope; native is now a per-dimension ceiling, not the aspect ratio; client sends device pixels |
| OQ-04 | G5 | Should capture/encode suspend when nobody is connected? | Yes — pause the loop; release objects behind a config key | v1 | ✅ **Specced** — Stage 1 unconditional, Stage 2 behind `[capture] idle_release_after`, both regression interactions specced |
| OQ-05 | G7 | Can bitrate be capped per user? | Admission-time egress guard + per-session pacing cap | v1 | ✅ **Specced** — `[server] max_egress_bps` + `[transport] per_session_max_bps`; policy drops excluded from the congestion reducer |
| OQ-06 | G6 | Can we multi-encode one capture into per-user tiers? | Not as simulcast in v1; per-session `Sequence` + temporal layers first | v1.x / v2 | 🔓 **Open** — step (1), the role policy, is settled (controller-only, role gate table rows 10-11). Steps 2-4 remain v1.x/v2 and need the ABI-break batching decision |
| OQ-07 | C13 | How much in-session control surface, and where? | Ship the tier that only wires up messages that already exist | v1 | ✅ **Specced** — T1 panel + T1+ Keyboard Lock in `ui.js`; T2 explicitly deferred to OQ-02/03/06 |
| OQ-08 | C2 | What does a headless deployment actually look like? | Fix the docs defect now; decide the product question separately | v1 (docs) | ✅ **Specced** — Xvfb claim corrected, two no-root add-ons specced. The *product* question (does FeatherDesk provision a virtual display?) is still open |

> **Status as of 2026-09-11.** Seven of eight open questions are resolved in the
> spec tree; OQ-06 remains open beyond its first step. Resolved here means
> *specced and internally consistent*, not *built* — no code exists yet
> (`BRANCH.md` "Current Status"). Two product questions survive their OQ and are
> deliberately not closed by spec text: whether FeatherDesk should provision a
> virtual display (OQ-08), and whether Cloudflare's terms permit sustained video
> on the intended plan tier (OQ-01 item 5) — both are owner/business calls, not
> engineering ones.

---

## OQ-01 — Carrier status, Cloudflare and P2P *(was C11)*

**Question.** The two intended paths land on opposite sides of the carrier
split. A P2P / overlay path (WireGuard, Tailscale, hole-punched UDP) carries
QUIC end to end and gets WebTransport — the good case. A Cloudflare Tunnel
cannot: `cloudflared` proxies HTTP/1.1 and HTTP/2 to the origin, so that path is
**always** the WebSocket carrier, which `core/MODULE_TRANSPORT.md` "Carrier
selection" calls *"degraded, on purpose… not a co-equal transport"*. If
Cloudflare is a planned path, that sentence is a product statement the specs
have not caught up with.

**What that costs today.**

| # | Debt | Evidence |
|---|------|----------|
| 1 | **No liveness on WS.** `MODULE_SERVER.md` names QUIC `keepalive_period` / `max_idle_timeout` as *"the real liveness mechanism"*; both are QUIC-only, restart-only knobs. No WebSocket Ping/Pong, no idle timeout, and `ping_interval` is explicitly *not* a liveness check. A half-open TCP session behind a tunnel holds a `max_clients` slot — and possibly **the controller slot** — until the OS TCP timeout. | `MODULE_SERVER.md` "Keepalive, liveness & timeouts"; `MODULE_CONFIG.md:696-708` |
| 2 | **No loss signal.** `PathStats.lost_packets` is *"always 0 on the WebSocket carrier"*, so the adaptive SLOW path (loss-threshold driven) is blind there. | `MODULE_TRANSPORT.md:184-187` |
| 3 | **No performance budget.** `MODULE_TRANSPORT` is the one per-frame-path module with no numeric targets — and it is now the module carrying the non-LAN path. | self-identified in `BRANCH.md` |
| 4 | **No base path / forwarded headers.** Needed only if a tunnel terminates on a path prefix rather than a hostname root. | absent from `MODULE_SERVER.md` HTTP endpoints |

**Recommendation.**
1. Reclassify the carrier in `MODULE_TRANSPORT.md`: *degraded fallback* →
   *supported, lower-performance path*, with a stated budget (debt 3). One
   paragraph, but it changes what every later reviewer treats as optional.
2. Fix debt 1 — this is the only real feature here. Make `keepalive_period` and
   `max_idle_timeout` **carrier-generic** rather than adding `ws_*` twins: the
   semantics are identical, and on WS they map onto WebSocket Ping/Pong control
   frames plus an app-side idle timer. A tunnelled session that vanishes must be
   reaped in ~30 s, not at TCP's discretion.
3. Fix debt 2 by documenting that the SLOW path is **client-`stats`-driven** on
   WS (the client already reports loss) and the FAST path stays `frame_out`-drop
   driven. No new mechanism, just an explicit owner.
4. Decide the P2P mechanism *now* rather than "at v2 start". If P2P is the
   primary non-LAN path, then v1's "the operator supplies reachability" is only
   honest if the docs name a concrete recipe. Cheapest credible answer:
   **document WireGuard/Tailscale as a v1 prerequisite** (zero code, works
   today, carries QUIC), and keep embedded `tsnet` as the v2 convenience.
5. Check Cloudflare's terms for sustained video through the CDN against the
   intended plan tier before designing it in.

---

## OQ-02 — Host audio, and client→host mic *(was C5 + C6)*

**Question.** `MODULE_AUDIO.md` is design-locked and implementation-deferred
behind "video capture+encode working end-to-end on **all three OSes**". Windows
and macOS are specced and not built and gate no cutover, so as written that
trigger defers audio indefinitely. Does v1 ship silent?

**The trade is real, and it is already handled.** Audio-master sync takes
motion-to-photon from ~20 ms to ~65 ms, or ~45 ms at `[audio] frame_ms = 10`,
and `[audio] enabled = false` is already the default. So the latency argument is
an argument about the *default*, not about shipping the feature.

**Recommendation.** Re-trigger audio on **Linux** video working, not all three
OSes — a one-line change to the deferral condition. It lands `pipewire` +
`opus`, which is two add-ons on the platform that has a reference
implementation, and it is the highest product value per unit of work in this
whole list: a remote desktop with no sound reads as a demo regardless of how
good the video path is. Keep the default off; expose the toggle in OQ-07.

**Mic (was C6) is a different shape and should not ride along.** It is the first
client→host *media* direction: new wire types, and a virtual capture device on
the host. Linux is easy (PipeWire/PulseAudio null-sink + loopback). Windows
needs a virtual audio device (driver install). **macOS has no user-space virtual
mic** — it needs a CoreAudio `AudioServerPlugIn` in `/Library/Audio/Plug-Ins/HAL`,
which is an install step plus signing and notarization. Recommendation: mic is
**Linux-first, opt-in, post-v1**, and explicitly not a blocker for the other two
platforms. Track as `OQ-02b`.

---

## OQ-03 — Host-side resolution and HiDPI *(was C4)*

**Question.** Should the host desktop mode follow the client window?

**What the specs do today.** Capture is *always* native on every backend;
scaling happens downstream — the Converter on the SW path, the encoder's VPP on
the HW path (`MODULE_STREAM_PARAMS.md` "Capturers"). `resize` is clamped to
display native. So a 1440p host in a 1280×720 browser window spends bitrate on
detail that is then resampled away, and the client letterboxes any mismatched
aspect ratio.

**The finding that decides this.** Host-side mode-setting is hardest on the
primary platform, *because of* the architecture's main selling point: `kms_egl`
deliberately operates **below the display server**, so it cannot ask X or
Wayland to change mode. Doing so would mean talking to the display server as
well, which forfeits the display-server-agnostic property that justified
rejecting every other Linux capture path. Windows and macOS have clean APIs
(`ChangeDisplaySettingsEx`, `CGDisplaySetDisplayMode`), so this is a
Linux-specific architectural conflict, not a portability chore.

**Recommendation — take the middle path, not the mode-set.**
1. Let the **client drive the encoder's output dimensions freely**, keeping
   native only as a *ceiling* rather than as the aspect ratio. The encoder VPP
   already scales; this removes letterboxing and the wasted bitrate without
   touching the host mode. Aspect ratio follows the client viewport.
2. Have the client multiply its requested dims by `devicePixelRatio`, so a
   HiDPI client asks for its true device pixels instead of CSS pixels.
3. Do real mode-setting **only** for the Windows IddCx headless case, where
   FeatherDesk already owns the virtual display and setting its mode list is
   legitimate and easy.
4. Record explicitly in `MODULE_CAPTURE.md` that host mode-set on Linux is out
   of scope *and why* — otherwise it gets re-proposed every review.

Items 1–2 are a clamp change plus one multiply. That is most of the visible
quality win for a fraction of item 3's cost.

---

## OQ-04 — Idle suspension *(was G5)*

**Question.** No gate on `client_count()` exists anywhere; the frame loop paces,
captures, converts, encodes and broadcasts into empty rings forever.

| | Mechanism | Frees | Wake cost |
|---|---|---|---|
| Stage 1 | Frame thread waits on a `Notify` the server signals at lifecycle step 12, instead of `sleep_to_interval` | encode work, readback bandwidth | sub-frame; objects stay alive |
| Stage 2 | After `[capture] idle_release_after` of zero sessions, drop capture + encoder; rebuild on 0→1 | GPU encode session, DXGI duplication handle, IddCx display | full `FrameLoop::open()` |

**Recommendation.** Stage 1 unconditionally in v1 — it is the cheapest item on
this list. Stage 2 behind a config key, **gated strictly on the 0→1
transition**: on any other transition it is TD-26 ("a join never restarts the
capturer") walking back in. Two interactions must be specced or this regresses:

- **Sustainable-rate control must be reset across a pause, never fed by it.** It
  lowers `params.fps` toward the 5 fps floor based on what the host sustains; an
  idle host would otherwise wake up advertising 5 fps.
- **Count authenticated sessions only.** Counting accepted sessions makes an
  unauthenticated connection a capture-start primitive.

The first-frame-after-idle case needs no new work: the cached IDR will be stale,
so the existing join path forces a fresh one within
`idle_keyframe_ms + join_idr_timeout`.

---

## OQ-05 — Per-user bitrate cap *(was G7)*

**Question.** `[stream.adaptive] max_bitrate_bps` (25 Mbps) is an *encoder*
ceiling. With one encoder every session receives byte-identical access units, so
until OQ-06 exists a per-user cap can only **drop frames**, not lower quality.

**Recommendation — two of the three levels, now.**
1. **Admission-time egress guard.** Check `max_clients × current_bitrate`
   against a new `[server] max_egress_bps` at lifecycle step 9 and refuse the
   excess session with the existing `close::SERVER_FULL (4429)`. Turns silent
   oversubscription of the host uplink into an explicit, debuggable refusal.
   ~10 lines of policy inside a check that already exists.
2. **Per-session pacing cap.** Token bucket at `[transport]
   per_session_max_bps` on each pump; overflow uses the existing drop-oldest
   ring. A capped viewer gets a lower *effective frame rate* at full resolution
   — acceptable for a passive viewer, wrong for a controller, so exempt the
   controller slot by default.
3. True per-user bitrate needs OQ-06.

**The interaction that must be handled.** A `frame_out` drop on the reference
session currently triggers the FAST path's 0.5× cut **for everyone**. Policy
drops must be counted under a separate metric label and excluded from the
congestion reducer — otherwise a per-user cap drags the whole room down, which
is the exact failure the majority-override rule was written to prevent.

---

## OQ-06 — Multi-encode / per-client tiers *(was G6)*

**Question.** Can one capture feed several encodes so each user gets what they
asked for? Technically yes; three current contracts block it, and each is a
bigger change than the pipeline work.

| Blocker | Where | Why it blocks |
|---|---|---|
| Surface single-ownership | CENTRAL_SPEC Contract 6 | `encode_surface(surface: FbInfo)` **consumes** the surface; `Drop` releases it exactly once. Two HW encoders cannot share one acquire, and DXGI will not re-deliver it. Fix = `encode_surface(&FbInfo)`, an `ABI_VERSION` bump invalidating every add-on — or pay the CPU readback for tier 2, losing the zero-copy win the design is built around. |
| One global sequence space per frame type | `MODULE_PROTOCOL.md` R-PRO-06 | Two `VIDEO_H264` tiers share one counter, so each client sees a gap for every frame of the other tier and requests an IDR every frame. |
| The one-encoder reducer | `MODULE_STREAM_PARAMS.md` "Aggregating N clients" | The whole adaptive policy exists to reduce N sessions to one setting. Tiers invert it, and tier lifecycle (create on first subscriber, drop on last, per-tier IDR coalescing and per-tier IDR cache) is new machinery. |

Throughput ceiling too: the frame loop is synchronous, so encodes serialize on
that thread. Two HW tiers ≈ 9 ms of a 16.6 ms budget — fits. Two software tiers
≈ 15.8 ms at the measured 7.9 ms p50 — does not.

**Recommendation — a four-step path, only the first two in v1.**
1. **Decide the role policy first.** `resize` / `set_bitrate` / `set_fps` /
   `set_hdr` are controller-only today, so no viewer can request anything.
   "Depending on what the user requests" is a role decision before it is an
   encoding one.
2. **Make the video `Sequence` per-session.** Purely internal — the server
   already owns the counters and `bootstrap_seq` is already per-session. Cheap,
   and it is the enabling change for everything below.
3. **Temporal-layer subsetting as the v1.x answer.** One encode with a
   hierarchical-P GOP; each session's pump skips non-reference frames for slow
   clients, giving 60 → 30 → 15 fps per client with no extra encode, no ABI
   change and no wire change. With step 2 done, a skipped droppable frame is
   invisible to gap detection. This is what WebRTC and Moonlight do. Caveat to
   verify: no encoder spec currently mentions temporal layers, and `openh264` is
   Constrained-Baseline-only.
4. **True simulcast in v2**, alongside the `encode_surface(&FbInfo)` ABI break.
   **Batch every ABI break into one event** — acceptance is exact equality, so
   each break invalidates every add-on in the field. Plan ABI v2 as a single
   release, not a drip.

If tiers ever differ in resolution, **pin the controller to tier 0** so the input
coordinate space stays single-valued.

---

## OQ-07 — In-session control surface *(was C13)*

**Question.** The wire vocabulary exists and no UI exercises it: `resize`,
`set_bitrate`, `set_fps`, `set_hdr`, `keyframe`, the clipboard and file-transfer
lanes, gamepad, and the `R-CLI-13` HUD's read-only numbers.

**Recommendation — ship the tier that only wires up what already exists.**

| Tier | Contents | Depends on |
|---|---|---|
| **T1 — v1** | Promote the HUD to a panel; controller-gated bitrate/fps controls; fullscreen + pointer-lock buttons; the `files.js` download panel; clipboard status; carrier + effective-role + fingerprint display (all already specced) | nothing — pure client wiring |
| **T1+ — v1, cheap and high value** | A **Keyboard Lock API** "gaming mode" so `Escape` / `Alt+Tab` reach the remote app instead of the browser | nothing server-side |
| **T2** | Audio toggle + volume · resolution/scale control · per-user quality | OQ-02 · OQ-03 · OQ-06 |

T1+ is worth pulling forward: it is small client-only JS and it is the single
thing that makes full-screen app and game use actually work. Chromium-only, so
it degrades to today's behaviour elsewhere.

**Constraint to respect.** "No build step, no framework, ES modules served
as-is" is a stated property of the client. A settings panel is fine in vanilla
ES modules, but it is the piece most likely to grow — keep it in one `ui.js`
with a documented boundary rather than letting it spread across the existing
modules.

---

## OQ-08 — Headless / virtual-display provisioning *(was C2)*

**A documentation defect to fix regardless of the product decision.**
`PLATFORM_COMPAT.md` lists Linux headless support as *"Xvfb / virtual display"*,
and `LINUX_SPEC.md` says KMS+EGL works with *"no display server at all"*. Both
are wrong as written: `kms_egl` captures **DRM/KMS scanout**, and Xvfb renders
to memory and never touches DRM — so on an Xvfb-only host `kms_egl` captures
nothing. With genuinely no display server, nothing is scanning out at all.
Windows (IddCx) and macOS (virtual display driver) are fine; only the Linux
column is wrong.

**Options.**

| | Approach | Verdict |
|---|---|---|
| a | Document the truth: headless Linux needs a real KMS CRTC with a mode set — a GPU with a connected or force-enabled connector (`video=HDMI-A-1:1920x1080e`) plus a display server rendering to it | **Do this now.** Zero code, corrects a false capability claim |
| b | A `vkms` (virtual KMS) path — produces DRM planes with no hardware | Likely dead end: no GPU acceleration and no DMA-BUF export worth encoding. Worth 30 minutes of verification, not more |
| c | A no-root userspace capture add-on (PipeWire portal / wlr-screencopy) | The only real path to headless Wayland — and it reopens a closed decision, narrowly (see below) |

**The distinction worth drawing.** "We are not a Docker application" (closed,
C1) and "we do not support no-root capture" are two different claims that were
closed together. Headless Wayland needs the *second* one relaxed, not the first.
If headless Linux is a target at all, option (c) is the mechanism and it can be
adopted without conceding anything about containers.

---

## Closed findings (do not re-raise)

| Was | Finding | Disposition |
|-----|---------|-------------|
| C1 | Unprivileged / containerized deployment | Closed by design — "remote control software, not a docker application"; root / `CAP_SYS_ADMIN` accepted |
| C3 | Multi-monitor | Closed by choice — v2 / native-client scope |
| C7 | Webcam redirection | Not in plan; wire type `0x50` stays reserved |
| C8 | Browser floor is a cliff (no sub-WebCodecs fallback) | Closed by choice |
| C9 | Single-secret identity model (no per-user identity / OIDC) | Closed by choice |
| C10 | Gamepad injection requires privilege on every OS | Closed by choice |
| C12 | No NAT traversal / relay in v1 | Closed by design — v1 is LAN-only |
| G1–G4, G8–G15 | licensing + repo hygiene, CI + release engineering, add-on ABI exact-equality, add-on supply chain, config schema versioning, `kms_egl` critical-path risk, measurement coverage, single-controller input, audit trail, docs, spec-to-code ratio, accessibility + i18n | Not a concern now |

**Never triaged** (no decision recorded): image/binary clipboard, keyboard
layout + IME + Unicode text entry, packaging + service integration.

---

## Recommended sequence

| When | Items | Why together |
|------|-------|--------------|
| **Now — spec edits, no new mechanism** | OQ-08(a) docs fix · OQ-01(1,3) reclassify + name the loss owner · OQ-03(4) record the Linux mode-set decision · OQ-06(1) role policy | All are decisions or corrections. Cheapest, and OQ-06(1) unblocks later work |
| **v1 build — small, independent** | OQ-04 Stage 1 · OQ-05(1) egress guard · OQ-01(2) carrier-generic keepalive · OQ-03(1,2) client-driven dims + DPR · OQ-07 T1 + T1+ | Each is self-contained, none touches the ABI or the wire format |
| **v1 build — the one real feature** | OQ-02 host audio on the Linux trigger | Highest product value in the list; two add-ons on the platform that has a reference impl |
| **v1.x** | OQ-06(2) per-session `Sequence` → OQ-06(3) temporal layers · OQ-05(2) pacing cap | (2) is the enabler for both; do them in that order |
| **v2, batched** | OQ-06(4) simulcast + `encode_surface(&FbInfo)` · OQ-02b mic · OQ-03(3) IddCx mode-set | Everything requiring an ABI break ships as one ABI v2 event |
