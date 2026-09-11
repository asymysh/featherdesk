# QA 08 — Performance and the parity gate

No matching user-story area — performance is asserted in spec tables and gated
by `BRANCH.md` "Migration Strategy" step 3.

**Rule for this file: a number you did not measure is not a result.** Several
targets in `specs/` are derived rather than observed (`MODULE_TRANSPORT`
"Performance Targets" says which of its own are). Record what you measured, on
what, and leave the rest blank rather than copying the spec's figure across.

## Every run records

| Field | Value |
|-------|-------|
| Commit | |
| Host OS / kernel | |
| CPU / GPU | |
| Loaded add-ons | |
| Browser + version | |
| Network class (N1–N5) | |
| Resolution / fps / codec | |

## Motion-to-photon budget

Each row is one line of CENTRAL_SPEC "Motion-to-photon budget". A module whose
Performance Targets table contradicts that budget is a `FAIL` on both.

| ID | Stage | Target (1080p60, LAN, HW encode) | Measured | Status |
|----|-------|----------------------------------|----------|--------|
| QA-PERF-1 | Capture acquire (p50) | 7.0 ms | | ☐ |
| QA-PERF-2 | Encode, HW (p50) | 4.5 ms | | ☐ |
| QA-PERF-3 | Pipeline bookkeeping | 0.1 ms | | ☐ |
| QA-PERF-4 | Broadcast fan-out, 25 clients | 2.0 ms | | ☐ |
| QA-PERF-5 | Transport enqueue → last byte, p99, one session | 1.0 ms (both carriers) | | ☐ |
| QA-PERF-6 | Reassembly + decode (client) | 5.0 ms | | ☐ |
| QA-PERF-7 | Present (drawImage) | 1.0 ms | | ☐ |
| QA-PERF-8 | **End-to-end, audio disabled** | **~20 ms** | | ☐ |
| QA-PERF-9 | End-to-end, audio enabled | ~65 ms (~45 ms at `frame_ms = 10`) | | ☐ |

> **How to measure QA-PERF-8 honestly.** Not by summing the rows above — that
> hides queueing. Point a high-frame-rate camera at the host display and the
> client display together and count frames between a physical input and the
> pixel changing on the client. Everything else is a component measurement.

## Software path

| ID | Check | Target | Measured | Status |
|----|-------|--------|----------|--------|
| QA-PERF-10 | `openh264` encode p50 @ 1080p, 4 threads | 7.9 ms (recorded baseline) | | ☐ |
| QA-PERF-11 | `openh264` encode p50 @ 1440p, 4 threads | 13.7 ms (recorded baseline) | | ☐ |
| QA-PERF-12 | `x264` encode p50 @ 1080p, 4 threads | 4.3 ms (recorded baseline) | | ☐ |
| QA-PERF-13 | Sustained software-path frame rate | 30 fps target, not 60 | | ☐ |
| QA-PERF-14 | CPU-path readback cost @ 1440p | Record it — the ~24 MB/frame the zero-copy path avoids | | ☐ |

## Parity gate (blocks cutover)

| ID | Check | Pass condition | Status |
|----|-------|----------------|--------|
| QA-PERF-15 | Rust `openh264` add-on vs. the recorded baseline | p50 within **15 %** of `PROJECT_ARTIFACTS/bench_out` for the same resolution and thread count, same harness, same class of machine | ☐ |
| QA-PERF-16 | Rust pipeline `featherdesk_frame_time_seconds` p99 vs. the Go binary | At or below, same host and display, 60 s session | ☐ |
| QA-PERF-17 | CPU and memory baseline | **Recorded for the first time** — `bench_out` has no baseline to compare against, so this establishes one rather than passing or failing | ☐ |

## Resource and capacity

| ID | Check | Target | Measured | Status |
|----|-------|--------|----------|--------|
| QA-PERF-18 | Host RSS, 1 client, 10 min | < 50 MB, not growing | | ☐ |
| QA-PERF-19 | Host RSS, 25 clients | < 50 MB + ~640 KB/client | | ☐ |
| QA-PERF-20 | Idle host, **no clients** (OQ-04) | Near-zero CPU/GPU | | ☐ |
| QA-PERF-21 | Bandwidth per viewer at default settings | 5–15 Mbps | | ☐ |
| QA-PERF-22 | **Total host egress, 25 viewers** | Record it. `clients × bitrate` is what leaves the NIC, and it can exceed a 1 GbE link at the 25 Mbps ceiling | | ☐ |
| QA-PERF-23 | Connection setup (TLS 1.3 + upgrade) | < 100 ms, both carriers | | ☐ |
| QA-PERF-24 | Ping RTT on LAN | < 5 ms | | ☐ |
| QA-PERF-25 | Input latency, LAN, HUD-reported | Consistent with QA-PERF-8; no drift over a 30-minute session | | ☐ |

## Carrier comparison

| ID | Check | Expectation | Measured | Status |
|----|-------|-------------|----------|--------|
| QA-PERF-26 | End-to-end latency, WebTransport vs. WebSocket, **0 % loss** | ≤ 1 ms apart — the carrier is not the cost on a clean path | | ☐ |
| QA-PERF-27 | Same at **1 % loss** | WebSocket adds 30–60 ms (head-of-line blocking). If it does not, question the loss injection before believing the result | | ☐ |
| QA-PERF-28 | Same at 2 % loss | WebSocket visibly unpleasant; WebTransport degraded but usable | | ☐ |
