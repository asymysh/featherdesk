# Specification: Multi-Client, Metrics, and Polish

## Overview

Upgrade the server from single-client to supporting 25 concurrent viewers with role-based access control (one controller, many viewers). Add comprehensive metrics, bandwidth testing, and production-readiness features including systemd integration.

## Dependencies

- Track 1 (KMS capture + software encode + WebSocket viewer) must be complete
- Track 3 (Input injection) should be complete for role enforcement to be meaningful

## Functional Requirements

### FR-1: Multi-Client Broadcast
- Support up to 25 concurrent WebSocket connections
- Broadcast video and audio frames to all clients simultaneously
- Client list managed under sync.RWMutex
- Handle slow clients: drop frames rather than block the broadcast loop
- New client receives IDR frame immediately (checkpoint)
- Oldest client evicted if limit reached

### FR-2: Role-Based Access Control
- WebSocket connects with `?role=control` or `?role=view`
- Only one controller allowed at a time (first to connect with control role)
- Viewers cannot send input (text frames from viewers are ignored)
- Controller disconnect: next control-request gets the role, or stays vacant
- /status reports current controller status

### FR-3: Metrics (Server-Side)
- Track per-second: frames captured, frames encoded, frames broadcast
- Track per-client: bytes sent, frames dropped, connection duration
- Expose via /status JSON endpoint
- Log periodic metrics summary (every 30s at INFO level)

### FR-4: Client Metrics UI
- FPS counter (decoded frames per second)
- Bandwidth display (bytes received per second, formatted KB/s or MB/s)
- RTT measurement (ping/pong JSON round-trip, displayed in ms)
- Input latency display (input ACK round-trip, rolling average)
- Connection uptime display

### FR-5: Bandwidth Test
- Client-initiated bidirectional throughput measurement
- Download test: server sends N MB of test data, client measures speed
- Upload test: client sends N MB of test data, server acknowledges
- Results displayed in client UI
- Non-blocking: test data uses bandwidth_test frame type

### FR-6: Production Readiness
- systemd service unit file (featherdesk.service)
- setcap documentation and helper script
- Graceful shutdown drains all clients
- `--bind` flag for listen address (default 0.0.0.0)
- Startup validation: check KMS access, check PipeWire, report errors clearly

## Non-Functional Requirements

- Broadcast to 25 clients must not increase per-frame encode time
- Memory per client: <2MB (WebSocket send buffer)
- No lock contention on hot path (use RWMutex, writers only on connect/disconnect)
- Client disconnect must be non-blocking for broadcast goroutine

## Acceptance Criteria

1. 25 browser tabs connect simultaneously, all see the stream
2. Only the controller's input is injected; viewers' input is ignored
3. FPS, bandwidth, RTT, input latency all display correctly in all clients
4. Bandwidth test completes and shows results
5. /status endpoint reports all connected clients and their roles
6. systemd service starts and stops cleanly
7. Server handles rapid connect/disconnect without memory leaks

## Out of Scope

- Authentication/login (future WAN track)
- Per-client quality adaptation (all get same stream)
- Client-to-client communication
- Admin panel / web UI for server management
