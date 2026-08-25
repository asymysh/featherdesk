# Branch: `featherdesk-refactor`

## Purpose

This branch is the **architectural redesign** of FeatherDesk, targeting a **Rust** rewrite. The working Go codebase on `feature-libav-vp8s8` has all five original tracks implemented and running, but accumulated several structural problems during iterative development. This branch contains the full modular redesign spec.

**This is a SPEC-ONLY branch — it contains no source code or build artifacts.** Only `specs/`, `PROJECT_ARTIFACTS/`, `README.md`, `BRANCH.md`, and `.gitignore` live here. The Go implementation (and its POC/benchmark tooling and vendored binaries) was removed from this branch and remains preserved on `feature-libav-vp8s8`; the specs cite it as the working reference. Implementation of the Rust rewrite happens in its own crates once the specs are finalized.

---

## Why the Refactor Is Needed

The current codebase has these confirmed issues (tracked as TD-01 through TD-40 in `specs/CENTRAL_SPEC.md` — TD-35 through TD-40 were added following a second round of implementation-history review across all five original tracks):

**High severity (bugs, crashes, data loss):**
- `kms.go` — DMA-BUF file descriptor leak on EGL import failure (TD-01)
- `x11grab.go` — recursive `NextFrame()` retry has no depth limit; stack overflow on sustained failure (TD-02)
- `egl.go` — static C globals mean EGL state is not thread-safe (TD-03)
- `ffmpeg.go` — `ForceKeyframe()` stores a flag but never signals ffmpeg; IDR never actually forced (TD-04)
- `server.go` — broadcasts one WebSocket message **per NAL unit**, not per frame; multi-NAL H.264 frames produce partial access units that break WebCodecs (TD-23)
- `server.go` — IDR cache stores only the IDR NAL; SPS and PPS (sent as separate messages) are lost; new clients receive an undecodable keyframe (TD-24)
- `main.go` + `x11grab.go` — video and audio timestamps use `time.Now().UnixMilli()` (wall-clock, milliseconds); A/V sync is broken by design (TD-25)
- `main.go` — new-client handler forces a keyframe AND restarts the capture subprocess; disrupts all existing viewers on every join (TD-26)

**Medium severity (architectural gaps):**
- All modules are tightly coupled through `main.go`; no module boundaries or interfaces
- No protocol versioning; no connection handshake; client must guess codec
- VP8 is the default encoder despite OpenH264 being planned (VP8 was switched in to avoid SPS/PPS complexity, which is now properly handled in the new spec)
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
| Software encoder priority | `x264` subprocess → VideoToolbox SW (macOS only) → OpenH264 (universal fallback) | Order defined by `MODULE_PIPELINE` dispatch (see `MODULE_ENCODE.md`); OpenH264 is the last resort because it's pure in-process FFI with no external dependency. VP8 is rejected outright — no add-on exists for it |
| Hardware path | Zero-copy DMA-BUF → VA-API (no glReadPixels) | Eliminates ~36MB/frame GPU↔CPU copies |
| Encoder tiers | Two only: hardware OR software (no ffmpeg-vaapi middle tier) | Clean separation; ffmpeg-vaapi still did CPU copies |
| A/V sync clock | CLOCK_MONOTONIC nanoseconds, stamped at capture | Single epoch across all capturers and audio; wall-clock ms is broken |
| Frame drop | Skip-before-capture, 5 FPS minimum floor | Stay realtime even at 1 FPS rather than accumulate latency |
| Cursor (hardware path) | Client-side overlay via CursorUpdate frames | Frame never touches CPU on zero-copy path; cursor moves without waiting for video frame |
| Protocol versioning | Version byte (byte 0) + Config handshake | Handshake sends full WebCodecs codec string; client never hardcodes codec |
| Input latency | InputMessage carries `seq`; server echoes in InputAck | Enables measured round-trip latency display |

---

## How to Use These Specs

Each spec file is self-contained with:
- **Public interface** — Rust trait definition the module must implement
- **Internal architecture** — data flow, components, system interactions
- **Refactoring directives** — numbered `R-XXX-NN` items, each a concrete task
- **Testing strategy** — unit/integration/benchmark breakdown
- **Performance targets** — measurable latency/CPU/memory goals

**For an agent starting implementation:**

1. Read `CENTRAL_SPEC.md` first — it defines all cross-module contracts and the Module Dependency Graph.
2. Pick a leaf module: `featherdesk-stream` (the shared foundation — `Params`/`EncodedFrame`/`StreamError` — every other module depends on it) is the natural starting point, followed by `protocol` (pure data, shared with the client, no logic deps) or `clipboard` (core, per-OS, minimal surface). There is no dedicated Logger module — every module uses the `tracing` crate directly (see `CENTRAL_SPEC.md` "Removed from the module map").
3. Implement the module's crate: the public `trait` plus its internal implementation (e.g. `featherdesk-<module>`).
4. Write tests against the trait, not the implementation.
5. The Pipeline module is last — it wires everything together (the sole crate that imports all others).

**Codec set (final — reconciled with `MODULE_ENCODE.md` / `MODULE_HARDWARE_ENCODE.md` / `PLATFORM_COMPAT.md`):**

| Codec | Tier | Status |
|-------|------|--------|
| H.264 | Hardware (all platforms) + Software (`x264`, `openh264`) | ✅ Fully specced, universal default |
| HEVC (H.265) | Hardware only — no software encoder (`libx265` has triple patent-pool exposure, see `PLATFORM_COMPAT.md`) | ✅ Fully specced, used for HDR |
| AV1 | Hardware only (NVENC Ada Lovelace+, AMD RDNA3+, Intel Arc/QSV; **no Apple Silicon has AV1 HW encode**) | 🚧 Per-vendor capability detection already specced (see `specs/addons/*/encoders/HW/`); wire `frame_type` now reserved (`VIDEO_AV1 = 16`, see `CENTRAL_SPEC.md`) — add-on *implementation* is future work |
| VP8 | — | ❌ Rejected — legacy codec, no demand, superseded by AV1 (no add-on exists; wire slot 5 permanently retired, do not reuse) |
| VP9 | — | ❌ Never adopted — AV1 supersedes it with better compression, and every hardware vendor converged on AV1 rather than VP9 for the royalty-free tier |

AV1 is the deliberate modern replacement for the old VP8 slot — VP8 was dropped for its age and because no current-generation encode silicon targets it, not swapped for another aging codec.

Encoder selection is driven by the `feature-benchmark` output file (`.featherdesk-bench.json`) when present.

---

## Migration Strategy (Go → Rust Cutover)

This is a full rewrite of working software (`feature-libav-vp8s8`), not an incremental patch, so it needs an explicit cutover plan rather than an implicit "swap it in when done":

1. **Parallel existence, not a hard cutover.** `feature-libav-vp8s8` (Go) remains the production branch and rollback target for the entire duration of the Rust rewrite — it is not touched, deprecated, or feature-frozen until the Rust implementation reaches parity (step 3).
2. **Module-by-module validation, not one big-bang merge.** The dependency graph in `CENTRAL_SPEC.md` guarantees no cycles, so crates land and get reviewed independently — starting from `featherdesk-stream` outward — well before `pipeline` wires them into a runnable binary.
3. **Parity gate before cutover.** The existing `feature-benchmark` tooling (`.featherdesk-bench.json`) is the acceptance bar: the Rust `pipeline` binary must match or beat the recorded Go encode latency/CPU/memory numbers, not just "compiles and runs."
4. **Cutover trigger.** `master` only points at the Rust implementation once (a) all core + media modules pass their module-spec Testing Strategy, (b) the parity gate in step 3 passes, and (c) a manual end-to-end session (capture → encode → transport → browser client) has run on each platform currently marked "Working" in `PLATFORM_COMPAT.md` (Linux only, today).
5. **Rollback.** Until step 4's criteria are met, `feature-libav-vp8s8` stays deployable at any time — the Go implementation is never deleted or made non-functional as a fallback.

---

## Relationship to Other Branches

| Branch | Purpose |
|--------|---------|
| `feature-libav-vp8s8` | Main working codebase (all 5 tracks implemented) |
| `featherdesk-refactor` | **This branch** — spec-first redesign, no code yet |
| `feature-benchmark` | Standalone GPU benchmark tool; its output drives encoder auto-selection |

The refactored code should eventually replace `feature-libav-vp8s8` as the main branch. The benchmark tool (`feature-benchmark`) integrates with the refactored codebase, not the current one.

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

Implementation-readiness analyses — per-feature clarity and wiring assessments — are in `PROJECT_ARTIFACTS/IMPLEMENTATION_READINESS_CORE_MEDIA.md` and `PROJECT_ARTIFACTS/IMPLEMENTATION_READINESS_INTERACTION_CLIENT_ADDONS.md`, each with a **Final Review Pass addendum** recording what was found and fixed afterwards. The QA acceptance suite (81 user stories across 8 areas, no open `GAP` citations) is in `PROJECT_ARTIFACTS/user_stories/`, indexed by `INDEX.md`.

**Final cross-module review.** The last pass audited *across* module boundaries rather than within them — for every consumer, asking who produces it. That is a different question from "is this module internally consistent", and it found three defects the per-module passes structurally could not: the cursor overlay had a wire type, a `send_cursor` method, and a client renderer but **no producer** (`send_cursor` had zero callers anywhere); the host→client clipboard direction had **no `Server` method and no drainer** despite two specs each describing the other's half; and the ABI had **no mechanism** for the host to learn which optional traits an add-on implements, since a `#[sabi_trait]` object cannot be downcast. All three are fixed (`CursorCapturer` + Contract 8, `send_clipboard` + Contract 9, `ProbeReport.caps`). Mechanical invariants now verified green: all 175 docs' relative links resolve, every `Server` trait method has a pipeline caller, every `server::Config` field has a named constructor, `StreamError` ↔ `AbiErr` is 1:1, and all 81 user stories cite a real spec heading.
