# Phase 2: Metrics & Monitoring - Summary

## T4 - Implement server-side metrics collection
- **Commit:** cdf3021
- **Changes:** `/status` extended with `clients`, `max_clients`, `encoder`, `audio`, `has_controller`, `frames_broadcast`, `bytes_broadcast` (the latter two as atomic counters).
- **Files:** `internal/server/server.go`, `cmd/server/main.go`
- **Why:** Operators and the client UI need visibility into server state.
- **Deviation:** no per-second rates, no `frames_captured`/`frames_encoded` counters, no per-client tracking, no periodic 30s log — spec's FR-3 is only partially met.

## T5 - Implement client-side metrics UI
- **Commit:** (part of the client work under `ea88bbb`/earlier `feature-libav-vp8s8` history — exact commit not isolated, logic present in `compositor.js`)
- **Changes:** FPS and bandwidth computed every ~1s and rendered in the status bar.
- **Files:** `cmd/server/client/compositor.js`
- **Why:** Viewers need feedback on stream health.
- **Deviation:** RTT (ping/pong) and input-latency (ACK round-trip) displays from the spec were not built.

## T6 - Implement bandwidth test
- **Commit:** none — not implemented
- **Changes:** No `bandwidth_test` frame type, server chunk-sender, or client-side measurement exists anywhere in the repo.
- **Files:** N/A
- **Why:** N/A — task not started.

## T7 - Write metrics tests
- **Commit:** none — no new tests added
- **Changes:** Only the pre-existing `TestStatusEndpoint` touches `/status`, and it predates/doesn't assert on this track's added fields.
- **Files:** N/A
- **Why:** N/A — task not completed.
