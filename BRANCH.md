# Branch: `featherdesk-refactor`

## Purpose

This branch is the **architectural redesign** of FeatherDesk, targeting a **Rust** rewrite. The working Go codebase on `feature-libav-vp8s8` has all five original tracks implemented and running, but accumulated several structural problems during iterative development. This branch contains the full modular redesign spec.

**This is a SPEC-ONLY branch — it contains no source code or build artifacts.** Only `specs/`, `PROJECT_ARTIFACTS/`, `README.md`, `BRANCH.md`, and `.gitignore` live here. The Go implementation (and its POC/benchmark tooling and vendored binaries) was removed from this branch and remains preserved on `feature-libav-vp8s8`; the specs cite it as the working reference. Implementation of the Rust rewrite happens in its own crates once the specs are finalized.

---

## Why the Refactor Is Needed

The current codebase has these confirmed issues (tracked as TD-01 through TD-40 in `specs/CENTRAL_SPEC.md` — TD-35 through TD-40 were added following a second round of implementation-history review across all five original tracks):

**High severity (bugs, crashes, data loss):**
- `server.go` — broadcasts one WebSocket message **per NAL unit**, not per frame; multi-NAL H.264 frames produce partial access units that break WebCodecs (TD-23)
- `server.go` — IDR cache stores only the IDR NAL; SPS and PPS (sent as separate messages) are lost; new clients receive an undecodable keyframe (TD-24)
- `kms.go` + `x11grab.go` + `main.go` — video timestamps are stamped in the capturers and audio at consumption, both with `time.Now().UnixMilli()` (wall-clock, milliseconds); A/V sync is broken by design (TD-25)
- `main.go` — new-client handler forces a keyframe AND restarts the capture subprocess; disrupts all existing viewers on every join (TD-26)

**Medium severity (architectural gaps):**
- `kms.go` — DMA-BUF fd lifetime is a two-site manual discipline (close-previous-on-next-call plus close-in-`Close`); correct today, but nothing enforces it (TD-01)
- `x11grab.go` — the capturer `main.go` actually constructs retries a dead subprocess by recursing into `NextFrame()` with no depth limit (TD-02)
- `egl.go` — file-scope C statics allow only one EGL readback context per process; latent under the single pinned capture thread, fatal on the first concurrent second context (TD-03)
- `ffmpeg.go` — `ForceKeyframe()` sets a flag nothing ever reads; the IDR is never forced (TD-04)
- All modules are tightly coupled through `main.go`; no module boundaries or interfaces
- No protocol versioning; no connection handshake; client must guess codec
- VP8 was the default encoder for part of the branch's history and was reverted to OpenH264 (H.264 Baseline) at commit `fae7e45`, the branch tip; `internal/encode/vp8.go` survives with no callers outside itself. The refactor drops VP8 outright — no add-on exists for it and wire slot 5 is permanently retired.
- GPU→CPU→GPU round-trip on the hardware encode path wastes ~36MB/frame in memory bandwidth
- No frame-drop strategy; latency accumulates unboundedly under load
- No orchestrator spec; wiring logic lives inline in `main.go`

---

## What This Branch Contains

```
specs/
├── CENTRAL_SPEC.md       # Master architecture doc: module contracts, dependency graph, TD catalog
├── PLATFORM_COMPAT.md    # Cross-platform capability matrix
├── core/                 # Protocol, Transport, Server, Pipeline, Config, Auth, Stream-Params
├── media/                # Capture, Encode (SW), Hardware Encode, Audio
├── interaction/          # Input, Clipboard, File Transfer, Gamepad
├── client/               # Web client (v1) + Native client (v2)
├── v2/                   # Network connectivity (NAT traversal / relay — v2-deferred)
└── addons/               # Per-OS add-on shared libraries:
                          #   {linux,macos,windows}/{capture,encoders,input,audio}/
```

---

## Key Design Decisions (recorded here for future implementors)

| Decision | Choice | Reason |
|----------|--------|--------|
| NAL framing | Annex B per-frame (start codes retained) | WebCodecs consumes whole access units; length-prefix adds AVCC complexity for no benefit |
| Software encoder priority | OpenH264 → VideoToolbox SW (macOS only) → `x264` subprocess (**opt-in**, never chosen by `mode = "auto"`) | OpenH264 is in-process FFI with no external dependency, and measures 7.9 ms p50 at 1080p / 13.7 ms at 1440p on 4 threads — realtime at 60 fps with headroom. `x264` is faster still (3.3 ms p50 at 1080p at its default auto thread count, 12 on the bench machine; 4.3 ms on the same 4 threads as the openh264 figure) but needs an external GPL ffmpeg binary and cannot force an IDR cheaply: its only mechanism is killing and respawning the child process. It is the right choice for a measured CPU-bound deployment and the wrong default. VP8 is rejected outright — no add-on exists for it |
| Hardware path | Zero-copy DMA-BUF → VA-API (no glReadPixels) | Eliminates ~36MB/frame GPU↔CPU copies |
| Encoder tiers | Two only: hardware OR software (no ffmpeg-vaapi middle tier) | Clean separation; ffmpeg-vaapi still did CPU copies |
| A/V sync clock | CLOCK_MONOTONIC nanoseconds, stamped at capture | Single epoch across all capturers and audio; wall-clock ms is broken |
| Frame drop | Skip-before-capture, 5 FPS minimum floor | Stay realtime even at 1 FPS rather than accumulate latency |
| Cursor (hardware path) | Client-side overlay: a 14-byte latest-wins position datagram, with the bitmap on a reliable per-session cursor stream (tag `0x11`) | Frame never touches CPU on zero-copy path; cursor moves without waiting for a video frame, and a bitmap that would not fit a datagram is not sent as one |
| Protocol versioning | Version byte (byte 0) + Config handshake | Handshake sends the full WebCodecs codec string with its computed level; client never hardcodes codec |
| Input latency | InputMessage carries `seq`; server echoes in InputAck | Enables measured round-trip latency display |
| Carriers | TCP HTTPS (page, `/cert-hashes`, `/auth`, `Alt-Svc`) + HTTP/3 WebTransport, with a WebSocket fallback on the same port | No browser navigates an origin over HTTP/3 first, so the TCP listener is mandatory; the fallback keeps UDP-blocked networks and pre-26.4 Safari working, reusing the same stream tags and framing rather than a second channel model |

---

## How to Use These Specs

Spec files share a common shape but are not uniform. Two sections are mandatory
and present in every module spec that describes runtime behaviour:

- **Public interface (or an equivalently-named contract section)** — the Rust
  trait, struct, or wire format the module owns. `MODULE_ABI`, `MODULE_AUTH`,
  `MODULE_CONFIG` and `MODULE_STREAM_PARAMS` carry theirs under
  domain-specific headings (`Root module surface`, the auth-mode flows, the TOML
  schema, `Params`) rather than a literal "Public Interface".
- **Testing strategy** — a table of unit / integration / hardware rows.

Three more appear where they apply, and their absence is a real gap, not a
template variation:

- **Internal architecture** — 4 of 19 specs (Web Client, Pipeline, Server, Clipboard).
- **Refactoring directives** — numbered `R-XXX-NN` items, in 5 of 19; only the
  `R-PIP`/`R-SRV`/`R-PRO`/`R-CLI`/`R-AUD` prefixes exist.
- **Performance targets** — numeric latency/CPU/memory goals, in 9 of 19
  (Capture, Encode, Hardware Encode, Stream Params, Server, Web Client,
  Pipeline, Audio, Transport). **`MODULE_TRANSPORT` was the one gap on the
  per-frame path and no longer is**: promoting the WebSocket carrier to a
  supported path made a per-carrier budget necessary, so it now carries one
  ("Performance Targets"). Two of its rows are explicitly *derived, not
  measured*, and labelled as such; the parity gate (step 3 below) replaces them
  with real numbers under the same harness.

**For an agent starting implementation:**

1. Read `CENTRAL_SPEC.md` first — it defines all cross-module contracts and the Module Dependency Graph.
2. Pick a leaf module: `featherdesk-stream` (the shared foundation — `Params`/`EncodedFrame`/`StreamError` — every other module depends on it) is the natural starting point, followed by `protocol` (pure data, shared with the client, no logic deps) or `clipboard` (core, per-OS, minimal surface). There is no dedicated Logger module — every module uses the `tracing` crate directly (see `CENTRAL_SPEC.md` "Removed from the module map").
3. Implement the module's crate: the public `trait` plus its internal implementation (e.g. `featherdesk-<module>`).
4. Write tests against the trait, not the implementation.
5. The Pipeline module is last — it wires everything together (the sole crate that imports all others).

**Codec set (reconciled with `MODULE_ENCODE.md` / `MODULE_HARDWARE_ENCODE.md` / `PLATFORM_COMPAT.md` / `CENTRAL_SPEC.md`):**

| Codec | Tier | Status |
|-------|------|--------|
| H.264 | Hardware (all platforms) + Software (`x264`, `openh264`) | ✅ Fully specced, universal default |
| HEVC (H.265) | Hardware only — no software encoder (`libx265` has triple patent-pool exposure, see `PLATFORM_COMPAT.md`) | ✅ Fully specced, used for HDR |
| AV1 | Hardware only (NVENC Ada Lovelace+, AMD RDNA3+, Intel Arc/QSV; **no Apple Silicon has AV1 HW encode**) | 🚧 Per-vendor capability detection already specced (see `specs/addons/*/encoders/HW/`); wire `frame_type` now reserved (`VIDEO_AV1 = 16`, see `CENTRAL_SPEC.md`) — add-on *implementation* is future work |
| VP8 | — | ❌ Rejected — legacy codec, no demand, superseded by AV1 (no add-on exists; wire slot 5 permanently retired, do not reuse) |
| VP9 | — | ❌ Never adopted — AV1 supersedes it with better compression, and every hardware vendor converged on AV1 rather than VP9 for the royalty-free tier |

AV1 is the deliberate modern replacement for the old VP8 slot — VP8 was dropped for its age and because no current-generation encode silicon targets it, not swapped for another aging codec.

Encoder selection is a fixed runtime probe order, not a data file: `[encode] mode = "auto"` walks HW (per OS — Linux `nvenc → amf_rocm → libva`, Windows `nvenc → amf → qsv → mf_hw`, macOS `vt_hw`) then SW, and `[encode] force_addon` is the only override (`MODULE_CONFIG.md`, `MODULE_PIPELINE.md` step 3e). The `feature-benchmark` tool informs which add-ons are worth shipping; it does not select at runtime.

---

## Migration Strategy (Go → Rust Cutover)

This is a full rewrite of working software (`feature-libav-vp8s8`), not an incremental patch, so it needs an explicit cutover plan rather than an implicit "swap it in when done":

1. **Parallel existence, not a hard cutover.** `feature-libav-vp8s8` (Go) remains the production branch and rollback target for the entire duration of the Rust rewrite — it is not touched, deprecated, or feature-frozen until the Rust implementation reaches parity (step 3).
2. **Module-by-module validation, not one big-bang merge.** The dependency graph in `CENTRAL_SPEC.md` guarantees no cycles, so crates land and get reviewed independently — starting from `featherdesk-stream` outward — well before `pipeline` wires them into a runnable binary.
3. **Parity gate before cutover.** Two measurements, both of which exist today:
   (a) **Encoder throughput** — the Rust `openh264` add-on must reach a p50
   encode latency within 15 % of the recorded software baseline for the same
   resolution and thread count in `PROJECT_ARTIFACTS/bench_out` (1920×1080,
   4 threads: 7.9 ms p50; 2560×1440, 4 threads: 13.7 ms p50), measured with the
   same `feature-benchmark` harness on the same class of machine.
   (b) **End-to-end latency** — the Rust `pipeline` binary's
   `featherdesk_frame_time_seconds` p99 (capture→broadcast, 60-sample window,
   `MODULE_PIPELINE.md` metrics catalog) must be at or below the Go binary's
   equivalent measured on the same host and display, over a 60 s session. There
   is no CPU or memory baseline to match: `bench_out` records neither, so those
   are recorded for the first time by this gate rather than compared against.
4. **Cutover trigger.** `master` only points at the Rust implementation once (a) all core + media modules pass their module-spec Testing Strategy, (b) the parity gate in step 3 passes, and (c) a manual end-to-end session (capture → encode → transport → browser client) has run on **Linux**, the only platform with a working reference codebase on `feature-libav-vp8s8`. Windows and macOS are specced and not built; they gate no cutover.
5. **Rollback.** Until step 4's criteria are met, `feature-libav-vp8s8` stays deployable at any time — the Go implementation is never deleted or made non-functional as a fallback.

---

## Relationship to Other Branches

| Branch | Purpose |
|--------|---------|
| `feature-libav-vp8s8` | Main working codebase (all 5 tracks implemented) |
| `featherdesk-refactor` | **This branch** — spec-first redesign, no code yet |
| `feature-benchmark` | Standalone GPU/encoder benchmark tool; its output is the parity gate's baseline, not a runtime input |

The refactored code should eventually replace `feature-libav-vp8s8` as the main branch. The benchmark tool (`feature-benchmark`) is run against the refactored codebase to produce the parity-gate numbers; neither binary reads its output at runtime.

---

## Current Status

| | |
|-|-|
| Specs written | ✅ 19 module specs + per-OS add-ons |
| Specs reviewed | ✅ All 5 tracks — see below |
| Code written | ❌ None yet |
| Tests written | ❌ None yet |

All 40 catalogued TD issues have a traced fix mapped to a specific module/decision in `CENTRAL_SPEC.md` — no row still reads "GAP" — and cross-module contracts were verified against the actual Go source on `feature-libav-vp8s8`; the architectural decisions in the Key Design Decisions table above were finalized with the project owner.

All five original tracks now have review + summary artifacts on disk under `PROJECT_ARTIFACTS/review/<track>/` and `PROJECT_ARTIFACTS/summaries/<track>/`: `kms_capture_software_encode` (the original three-phase pass), plus `input_injection_uinput`, `multiclient_metrics_polish`, `pipewire_audio_capture`, and `vaapi_hardware_encoding` (commit-level audits of the implementation history, which is where TD-35–TD-40 came from).

Implementation-readiness analyses — per-feature clarity and wiring assessments — are in `PROJECT_ARTIFACTS/IMPLEMENTATION_READINESS_CORE_MEDIA.md` and `PROJECT_ARTIFACTS/IMPLEMENTATION_READINESS_INTERACTION_CLIENT_ADDONS.md`, each with a **Final Review Pass addendum** recording what was found and fixed afterwards. The QA acceptance suite (90 user stories across 8 areas, no open `GAP` citations) is in `PROJECT_ARTIFACTS/user_stories/`, indexed by `INDEX.md`.

Two artifacts sit alongside those and answer different questions:

- **`PROJECT_ARTIFACTS/GAP_TRIAGE.md`** — the competitive/feature gap review, triaged into eight open questions (`OQ-01`…`OQ-08`, each recording the decision and its consequences), a **closed findings** table that exists so settled decisions are not re-raised, and a recommended sequence. The OQ-08 and OQ-01 passes are what produced the two no-root Linux capture add-ons, the `kms_egl` headless correction, and the WebSocket carrier's promotion to a supported path.
- **`QA/`** — release checklists for a **human on real hardware**, indexed by `QA/README.md`. Deliberately not the same thing as the user stories: those are acceptance criteria a test suite can assert, these are the 12 areas (preflight → platform sign-off) that only a person with a real GPU, a real network and a real second machine can confirm.

**Final cross-module review.** The last pass audited *across* module boundaries rather than within them — for every consumer, asking who produces it. That is a different question from "is this module internally consistent", and it found three defects the per-module passes structurally could not: the cursor overlay had a wire type, a `send_cursor` method, and a client renderer but **no producer** (`send_cursor` had zero callers anywhere); the host→client clipboard direction had **no `Server` method and no drainer** despite two specs each describing the other's half; and the ABI had **no mechanism** for the host to learn which optional traits an add-on implements, since a `#[sabi_trait]` object cannot be downcast. All three are fixed (`CursorCapturer` + Contract 8, `send_clipboard` + Contract 9, `ProbeReport.caps`).

**Mechanically verified invariants.** Each is a command, not an assertion — re-run them, do not re-read them:

| Invariant | Check | Status |
|---|---|---|
| Every relative markdown link resolves | walk all 194 `.md` files, resolve every non-`http` link target | 0 broken (614 relative links, re-run 2026-09-11 after the OQ-08 pass and the QA checklists landed) |
| TD ids are unique and none reads `GAP` | `grep -c '^| TD-' specs/CENTRAL_SPEC.md` = 40, distinct; `grep -rn GAP specs/ \| grep -v GAP_TRIAGE` — the exclusion is required since the OQ pass, because five specs now cite `PROJECT_ARTIFACTS/GAP_TRIAGE.md` by filename and a bare `grep GAP` matches the path, not an unresolved gap | 40 unique, 0 hits |
| `StreamError` ↔ `AbiErr` mapping is total | `MODULE_ABI.md` "Testing Strategy" row 1 — a bijection within each of the four `AbiErr` domains, so it is total in both directions | total |
| Every `server::Config` field has a named constructor | for each field in `MODULE_SERVER.md` `struct Config`, grep `MODULE_PIPELINE.md` step 7 for a bullet naming it; and for each step-7 bullet, grep `struct Config` for the field | all fields |
| Every control-stream message type, wire frame type, and `AddonCaps` bit appears in ≥1 acceptance criterion | for each `pub const` in `MODULE_PROTOCOL.md` `mod frame_type`, each `"type"` in its "Control-stream JSON messages" table, and each `pub const` in `MODULE_ABI.md` `impl AddonCaps` except `KNOWN`, confirm a row in `PROJECT_ARTIFACTS/user_stories/COVERAGE.md` **and** a `- Given …` line naming it | 9/9 frame types, 16/16 messages, 8/8 caps bits |
| Every user story has exactly one `**Validated by:**` line citing a heading that exists | per file, `grep -c '^## US-'` = `grep -c '^\*\*Validated by:\*\*'`; then, for every double-quoted string in a `**Validated by:**` line, grep the cited file for that string | 90 = 90; 211 citations checked, 0 dangling |
