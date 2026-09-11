# QA 06 — Resilience: loss, reconnect, crash recovery, idle

Matching user stories: `PROJECT_ARTIFACTS/user_stories/07_resilience_and_reconnection.md`.

This area is almost entirely **failure injection**. Nothing here is observed by
using the product normally, which is exactly why it needs a checklist.

## Network interruption

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-RR-1 | Drop the client's network for 5 s, restore it | Auto-reconnect resumes without re-auth; the picture returns within a second of restoration; the HUD showed `reconnecting` throughout | `MODULE_SERVER` "Session Cache" | ☐ |
| QA-RR-2 | Drop it for longer than `cache_ttl_seconds` (300 default) | Falls back to full re-auth cleanly — never a silent hang or a bare 401 | `MODULE_AUTH` resume | ☐ |
| QA-RR-3 | Suspend the client laptop, resume 10 minutes later | Reconnects or re-authenticates cleanly. No zombie session left counted on the host | `MODULE_SERVER` | ☐ |
| QA-RR-4 | Reconnect storm: 10 clients all lose the network and return at once | No keyframe storm (one IDR per `keyframe_min_interval_ms`), no accept-queue overflow crash — a full queue closes new sessions with `4429`, reason `server_busy` | `MODULE_SERVER` DoS | ☐ |
| QA-RR-5 | Client reconnects showing a close code | HUD distinguishes `4429` (server full, wait) from `4401` (credential rejected, re-auth) and shows the countdown to the next attempt | R-CLI-13 "Reconnect policy" | ☐ |
| QA-RR-6 | Roam between Wi-Fi and Ethernet mid-session | QUIC survives the path change where possible; otherwise a clean resume | `MODULE_TRANSPORT` | ☐ |

## Adaptive bitrate

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-RR-7 | Throttle the link to well below the configured bitrate | Bitrate steps down (FAST path on send-queue drops, then SLOW on sustained loss) and the picture stays usable rather than stalling | `MODULE_STREAM_PARAMS` "The policy" | ☐ |
| QA-RR-8 | Remove the throttle | Recovers upward after the recovery window; does not oscillate | `MODULE_STREAM_PARAMS` | ☐ |
| QA-RR-9 | The **shipped default** config: `bitrate_bps = 0` (constant-QP) with `adaptive = true` | The first congestion signal converts the session to bitrate mode, one way, as specced. This is the one configuration that had no story before | US-RR-11 "Cold start" | ☐ |
| QA-RR-10 | Adaptation on the **WebSocket** carrier | Still adapts, driven by client `stats` (not `lost_packets`, which is 0 there) plus send-queue drops | `MODULE_TRANSPORT` "The loss signal" | ☐ |
| QA-RR-11 | A bitrate change mid-stream | No `config` message is sent and the decoder is not reconfigured — a bitrate change is transparent. The HUD's number moves because it measures throughput | `MODULE_STREAM_PARAMS` | ☐ |

## Add-on crash and fallback

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-RR-12 | Kill the `x264` ffmpeg child once | Restarts on the backoff ladder (100/200/400/800/1600 ms ± jitter); the stream resumes; frames during the gap are **dropped, never buffered** | `MODULE_PIPELINE` "Add-On Crash Recovery" Level 1 | ☐ |
| QA-RR-13 | Make it crash repeatedly and persistently | Gives up on the 6th failure rather than respawning once per frame forever. Logs do not flood | TD-39; US-RR-9 | ☐ |
| QA-RR-14 | Let it run healthy for 60 s after failures, then crash again | The counter has decayed — the ladder restarts from the bottom | `MODULE_PIPELINE` Level 1 | ☐ |
| QA-RR-15 | An add-on returns `StreamError::Unrecoverable` | Poisoned for the session; the pipeline walks the probe order to the next candidate, forces an IDR and pushes a fresh `config` on a successful swap | Level 2 | ☐ |
| QA-RR-16 | No candidate remains after a poison | Clean shutdown with a clear reason — not a hang and not a panic | Level 2 terminal case | ☐ |
| QA-RR-17 | Panic inside an add-on trait method | Caught by `catch_unwind`, surfaces as `AbiErr::Unrecoverable` (7). **The host process survives** | CENTRAL_SPEC add-on registration step 4 | ☐ |
| QA-RR-18 | GPU reset / driver restart mid-session (`nvidia-smi -r`, or a TDR on Windows) | Either recovers or degrades to software; does not wedge. Record which | `MODULE_HARDWARE_ENCODE` | ☐ |

## Idle suspension (OQ-04)

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-RR-19 | Start the host, connect nobody, watch CPU/GPU for 5 minutes | **Near zero.** Capture and encode are suspended with no authenticated session. Compare against the pre-OQ-04 behaviour of encoding into empty rings | OQ-04 Stage 1 | ☐ |
| QA-RR-20 | First client connects to a suspended host | Video appears promptly — sub-second, with the fresh IDR arriving via the stale-cache join path | OQ-04 | ☐ |
| QA-RR-21 | Last client disconnects, then a new one connects a minute later | Suspends and resumes correctly. With Stage 2 enabled, the rebuild happens only on the 0→1 transition | OQ-04 Stage 2 | ☐ |
| QA-RR-22 | fps advertised **after** an idle period | Back at the configured rate — sustainable-rate control was **reset** across the pause, not fed by it. A host that wakes advertising 5 fps is a `FAIL` | OQ-04 interaction 1 | ☐ |
| QA-RR-23 | An **unauthenticated** connection attempt against a suspended host | Does **not** start capture. Only authenticated sessions count, or connecting becomes a resource-exhaustion primitive | OQ-04 interaction 2 | ☐ |
| QA-RR-24 | GPU encode session / DXGI duplication handle after Stage 2 release | Actually released — verify with `nvidia-smi` or the Windows handle count, not just by log line | OQ-04 Stage 2 | ☐ |

## Server lifecycle

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-RR-25 | Graceful shutdown with clients attached | Every client receives `server_shutdown`, then close `4503`. Clients show a sensible message rather than a generic disconnect | R-SRV-05 | ☐ |
| QA-RR-26 | `SIGKILL` the host | Clients detect the loss within `max_idle_timeout` on **both** carriers and enter reconnect | `MODULE_TRANSPORT` liveness | ☐ |
| QA-RR-27 | Run a 24-hour session | No memory growth, no fd leak, no handle leak, no log rotation failure. Check RSS and fd count at start, 1 h and 24 h | CENTRAL_SPEC goals | ☐ |
