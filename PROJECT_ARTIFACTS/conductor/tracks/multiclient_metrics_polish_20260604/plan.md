# Implementation Plan: Multi-Client, Metrics, and Polish

## Phase 1: Multi-Client Infrastructure

- [ ] Task: Refactor server for multi-client broadcast
    - [ ] Replace single-client connection with client list ([]Client, max 25)
    - [ ] Protect with sync.RWMutex (RLock for broadcast, Lock for add/remove)
    - [ ] Implement per-client write goroutine with buffered channel
    - [ ] Drop frames when client channel is full (non-blocking send)
    - [ ] Remove client on write error or disconnect
    - [ ] Send IDR checkpoint to new client on connect
- [ ] Task: Implement role-based access control
    - [ ] Parse `?role=control|view` from WebSocket upgrade URL
    - [ ] Track controller client (only one, first to request)
    - [ ] Ignore text frames (input) from non-controller clients
    - [ ] On controller disconnect: release controller slot
    - [ ] Log role assignments and changes
- [ ] Task: Write multi-client tests
    - [ ] Test 25 concurrent connections accepted
    - [ ] Test 26th connection evicts oldest
    - [ ] Test controller role exclusivity
    - [ ] Test viewer text frames are ignored
    - [ ] Test broadcast doesn't block on slow client
- [ ] Task: Conductor - User Manual Verification 'Multi-Client Infrastructure' (Protocol in workflow.md)

## Phase 2: Metrics & Monitoring

- [ ] Task: Implement server-side metrics collection
    - [ ] Atomic counters: frames_captured, frames_encoded, frames_broadcast per second
    - [ ] Per-client tracking: bytes_sent, frames_dropped, connected_at
    - [ ] Periodic log (every 30s): "25 clients, 60fps, 12.5 MB/s total"
    - [ ] Update /status endpoint with full metrics JSON
- [ ] Task: Implement client-side metrics UI
    - [ ] FPS counter: increment on each decoded frame, reset every second
    - [ ] Bandwidth: sum bytes received per second, format as KB/s or MB/s
    - [ ] RTT: send ping JSON every second, measure pong round-trip
    - [ ] Input latency: rolling average of last 20 ACK round-trips
    - [ ] Display all in status bar (existing bar from Track 1)
- [ ] Task: Implement bandwidth test
    - [ ] Define bandwidth_test frame type in protocol
    - [ ] Client sends request: `{"type":"bandwidth_test","size_kb":102400}`
    - [ ] Server sends N MB in chunks (4MB each) with bandwidth_test frame type
    - [ ] Client measures download speed from timestamps
    - [ ] Client sends upload test (N MB of zeros), server acknowledges total
    - [ ] Display results in UI: "DL: X MB/s | UL: Y MB/s"
- [ ] Task: Write metrics tests
    - [ ] Test counters increment correctly
    - [ ] Test /status JSON schema is valid
    - [ ] Test bandwidth test frame type round-trip
- [ ] Task: Conductor - User Manual Verification 'Metrics & Monitoring' (Protocol in workflow.md)

## Phase 3: Production Readiness

- [ ] Task: Create systemd service unit
    - [ ] Write featherdesk.service (Type=simple, Restart=on-failure)
    - [ ] Document: install path, setcap command, enable/start commands
    - [ ] Test: systemctl start/stop/restart lifecycle
    - [ ] Add After=pipewire.service dependency
- [ ] Task: Implement startup validation
    - [ ] Check /dev/dri/card* access (fail early with clear error if no CAP_SYS_ADMIN)
    - [ ] Check /dev/uinput access (warn if input group missing)
    - [ ] Check PipeWire connectivity (warn if unavailable, continue without audio)
    - [ ] Check ffmpeg in PATH (warn if missing, software encode still works)
    - [ ] Report all capabilities at startup in a summary block
- [ ] Task: Implement --bind flag and final CLI polish
    - [ ] Add `--bind` flag (default "0.0.0.0")
    - [ ] Print startup banner: "FeatherDesk listening on http://0.0.0.0:30084/"
    - [ ] Print detected capabilities: KMS ok, VA-API ok/missing, PipeWire ok/missing, uinput ok/missing
    - [ ] `--version` flag prints build version
- [ ] Task: Final memory and stability verification
    - [ ] Stream for 30 minutes with 5 clients, verify stable RSS
    - [ ] Rapid connect/disconnect 100 times, verify no goroutine leaks
    - [ ] Kill/reconnect clients during bandwidth test, verify no crash
    - [ ] Verify all fds are closed after shutdown (check /proc/self/fd)
- [ ] Task: Conductor - User Manual Verification 'Production Readiness' (Protocol in workflow.md)
