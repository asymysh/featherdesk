# Implementation Readiness — Core + Media Modules

Scope: the 13 `specs/core/*` and `specs/media/*` module specs plus
`specs/CENTRAL_SPEC.md`, scored on **Interface Clarity** (is the public
contract unambiguous enough to implement without guessing?) and **Wiring
Clarity** (is it clear which other module constructs/owns/calls this one,
cross-checked against `CENTRAL_SPEC.md`'s Module Dependency Graph). This
does **not** cover `specs/interaction/*`, `specs/client/*`, or
`specs/addons/*` — those are out of scope for this pass.

Legend: 🟢 Clear · 🟡 Clear with a noted gap · 🔴 Blocking gap

## Scorecard

| Module | Interface | Wiring | Notes |
|---|---|---|---|
| `CENTRAL_SPEC.md` | 🟢 | 🟢 | Module map, dependency graph, and TD catalog are internally consistent as of this pass. |
| `MODULE_ABI.md` | 🟢 | 🟢 | Stable-ABI registries and load-failure taxonomy are concrete; pipeline's `AddonRegistry` in `MODULE_PIPELINE.md` matches. |
| `MODULE_PROTOCOL.md` | 🟢 | 🟡 | Wire formats are exhaustive and internally consistent (frame types, `InputAck`, close codes). Gap: defines `InputAck`'s wire shape but not its consumer — see Q1 below. |
| `MODULE_TRANSPORT.md` | 🟢 | 🟢 | `Transport`/`Session` trait contract is concrete; channel model and stream-identification rules are unambiguous. Consumed cleanly by `MODULE_SERVER.md`. |
| `MODULE_SERVER.md` | 🟢 | 🟡 | Interface and internal architecture (broadcast, keyframe cache, session lifecycle) are thorough. Gaps: (1) `broadcast()`'s doc-comment ("VP8 was rejected; slot 5 reserved") wasn't updated for the `VIDEO_AV1=16` addition — cosmetic, not blocking. (2) See Q2 below re: `stream_mgr` wiring. |
| `MODULE_PIPELINE.md` | 🟢 | 🟡 | The orchestrator is the most thorough spec in the set — startup sequence, frame loop, shutdown order, error recovery, and the metrics catalog are all concrete. Gap: see Q2 below — the construction/injection point for the `stream::Manager` handed to `MODULE_SERVER.md`'s `Config.stream_mgr` is never shown, so it's unclear how `Manager::apply()`'s effective `Params` actually reaches `Pipeline.param_tx`. |
| `MODULE_AUTH.md` | 🟢 | 🟢 | All four modes (none/token/password/pin) are fully specced with concrete flows; `Authenticator` trait is a clean single-method contract consumed by `MODULE_SERVER.md`. |
| `MODULE_CONFIG.md` | 🟢 | 🟢 | Schema, validation table, and hot-reload matrix are exhaustive; phase-A/phase-B validation split is explicit and matches `MODULE_PIPELINE.md`'s startup sequence. |
| `MODULE_STREAM_PARAMS.md` | 🟢 | 🟡 | The `Params` contract and per-add-on translation tables are excellent. Same wiring gap as above (Q2) — `Manager` is defined but never explicitly constructed/wired to a `Params` sender. |
| `MODULE_ENCODE.md` | 🟢 | 🟢 | SW encoder dispatch order and `ConfigurableEncoder` contract are concrete (read earlier this session; Testing Strategy added and verified specific). |
| `MODULE_HARDWARE_ENCODE.md` | 🟢 | 🟢 | HW encoder contract, AV1 OBU-framing note, and `FbInfo` consumption are concrete (read earlier this session; Testing Strategy added and verified specific). |
| `MODULE_AUDIO.md` | 🟢 | 🟢 | Interface is fully specced (design locked); wiring to `MODULE_PIPELINE.md`'s `run_audio_loop` is explicit. Implementation is *intentionally* deferred behind the video milestone — this is a documented status, not a spec gap. |
| `MODULE_CAPTURE.md` | 🟢 | 🟢 | `Capturer`/`SurfaceCapturer` contracts, buffer-ownership rules, and per-OS selection are concrete; matches `MODULE_PIPELINE.md`'s capability-probing section exactly. |

**Overall verdict: ready, with 0 hard blockers and 1 real clarity gap worth closing before implementation starts** (Q2 below — everything else is either cosmetic or out of this pass's scope).

## Blocking / notable gaps

### Q1 — Does any core/media module describe a client-side consumer of `InputAck`?
**No.** `MODULE_PROTOCOL.md` defines `InputAck`'s wire format only (13-byte
echo of seq + server timestamp on the input stream). `MODULE_TRANSPORT.md`
lists it only as `[u16 RecLen] InputAck S → C` in the channel-model table —
no consumer logic. `MODULE_SERVER.md` states the *intent* ("the server then
emits an InputAck(seq) so the client can measure round-trip latency") but
that's server-side framing, not a spec of what the client does with it. No
module in this scope owns "measure this and show it to the user." That
logic would belong to `MODULE_WEB_CLIENT.md` (client scope, not reviewed in
this pass) — flagging so whoever reviews the client spec checks for it
there; if it's absent there too, it's a real end-to-end gap, not just an
out-of-scope one.

### Q2 — Does any module own bandwidth-testing / metrics / RTT display?
**Yes, for server-side adaptive-bitrate telemetry and operator-facing
metrics** — this is well-owned:
- `MODULE_SERVER.md` R-SRV-08: derives RTT from QUIC `smoothed_rtt` + app
  ping/pong, derives loss from the server's own datagram-drop rate + client
  `stats` messages, feeds both to `stream::Manager` every 100ms.
- `MODULE_STREAM_PARAMS.md` "Bandwidth Adaptation": the full two-tier
  (fast/slow path) adaptation policy and its config knobs.
- `MODULE_PIPELINE.md` R-PIP-02: exports `featherdesk_rtt_seconds`,
  `featherdesk_effective_bitrate_kbps`, and 10 other series on a Prometheus
  endpoint.

**But the wiring from `Manager::apply()` to the pipeline's frame loop has a
gap.** `MODULE_STREAM_PARAMS.md` says `Manager` "hands the effective
`Params` to the pipeline's `param_ch`," and `MODULE_SERVER.md`'s `Config`
holds `stream_mgr: Box<dyn stream::Manager>`. But `Pipeline.param_tx` (the
sending half of that channel) is a private field on `Pipeline`
(`MODULE_PIPELINE.md`), and neither spec's startup/wiring sequence shows
*who constructs the concrete `Manager` and hands it a clone of
`param_tx`*. `MODULE_PIPELINE.md`'s "Wire callbacks" step (step 12) lists
every `server.set_*` callback but not this one. This is the one concrete
"how do these two modules actually connect" question in the whole core+media
set that isn't answered by either spec — worth a one-line addition to
`MODULE_PIPELINE.md`'s startup sequence (e.g. "step 6c: construct
`stream::Manager` with a clone of `param_tx`, pass it into `server::Config`")
before implementation starts, since it's the kind of thing that gets
guessed differently by whoever writes `MODULE_PIPELINE.md`'s `new()` vs.
whoever writes the `Manager` impl.

There is **no user-initiated "bandwidth test" feature** in any core/media
spec (e.g. a button that runs a one-off throughput probe) — only continuous
passive telemetry feeding the adaptive-bitrate loop. If a "bandwidth test"
UI feature is expected (this phrase came up in the earlier track review of
the old Go `multiclient_metrics_polish` work), it isn't in the new spec at
all and would need a fresh design, not just a fix.

### Cosmetic (non-blocking)
- `MODULE_SERVER.md`'s `broadcast()` doc-comment still reads "VP8 was
  rejected; slot 5 reserved" without mentioning the `VIDEO_AV1=16` slot
  added to `CENTRAL_SPEC.md`/`MODULE_PROTOCOL.md`/`MODULE_ABI.md` earlier
  in this branch's work. Doesn't block anything (AV1 isn't wired into the
  pipeline's encode path yet either — it's reserved, not implemented) but
  worth a one-line touch-up for consistency.

---

# Addendum — Final Review Pass

Everything above is the record of the **first** readiness assessment. This
addendum records what changed afterwards. Read the two together: the body says
what was wrong, this says what was done about it.

## Items above that are now CLOSED

| Finding above | Resolution |
|---|---|
| `stream::Manager` has no constructor/owner — "the one concrete 'how do these two modules actually connect' question in the whole core+media set" | **Closed.** `MODULE_PIPELINE.md` step 12 now constructs the Manager with a clone of `param_tx` and hands it to `server.set_stream_params_callback`. ⚠️ **The fix above suggested passing it via `server::Config` — do NOT do that.** `Manager::apply` takes `&mut self`, which does not fit a field on a `Send + Sync` server without adding a lock, and the Manager cannot exist until `param_tx` does (after the server is built). `server::Config` now carries an explicit note saying there is deliberately no `stream_mgr` field. |
| `broadcast()` doc-comment omits `VIDEO_AV1 = 16` (cosmetic) | **Closed.** Doc-comment updated; reference points at BRANCH.md "Codec set". |
| No user-initiated bandwidth test in any spec | **Still true, now recorded as a deliberate decision** rather than an open question — see user story US-MC-8. The adaptive loop measures bandwidth passively (QUIC `smoothed_rtt` + server datagram-drop rate + client `stats` deltas); an active probe would steal throughput from the stream it measures. Adding one later needs a new frame type and a new directive; it must not be folded into the R-CLI-13 HUD. |

## New findings from the final pass (all fixed)

The first pass audited each module against its own contract. The final pass
audited **across** modules — asking, for every consumer, "who produces this?".
That found three defects of a shape the per-module reads structurally could not
catch, because each module described its own half correctly:

1. **The cursor overlay had no producer.** `frame_type::CURSOR_UPDATE` (11),
   `server.send_cursor`, the client's `cursor.js`, and every capture add-on's
   "cursor is sent separately" note all existed. Nothing captured a cursor.
   `send_cursor` had **zero callers anywhere in the spec tree**. Fixed with
   `capture::CursorCapturer`, CENTRAL_SPEC **Contract 8**, pipeline frame-loop
   step (0b) and startup step 6a, and the `cursorMode` resolution truth table.
2. **The host→client clipboard direction had no method and no drainer.**
   MODULE_CLIPBOARD's flow diagram said "changes() → server" and MODULE_SERVER
   said `clipboardReader()` "writes host→client the same way" — but no `Server`
   trait method accepted host content and no pipeline task drained
   `Monitor::changes()`. Fixed with `Server::send_clipboard`, CENTRAL_SPEC
   **Contract 9**, and pipeline steps 12/13.
3. **The ABI had no way to express optional traits.** The pipeline said "set
   `surf_cap` when the chosen capturer implements `SurfaceCapturer`", but a
   `#[sabi_trait]` object cannot be downcast, so the host had no mechanism to
   ask. Fixed with `ProbeReport.caps` / `AddonCaps` bitflags (MODULE_ABI), which
   also carries the new `CURSOR` bit and the per-field
   `stream_params_capability()` hookup at pipeline step 3g.

Also closed here: **TD-39** (add-on crash-recovery backoff + circuit breaker,
`StreamError::Unrecoverable`, `AbiErr` code 7) and **TD-40** (audio buffer
conservation tests). No TD row still reads "GAP".

## Verdict (revised)

**Implementation-ready for core + media.** Every `Server` trait method has at
least one pipeline caller, every `server::Config` field has a named constructor
at step 7, `StreamError` and `AbiErr` are 1:1, all 40 TD rows have traced fixes,
and every cross-module contract now names both a producer and a consumer.
