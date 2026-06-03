# Phase 2: Wire Protocol - Summary

## T5 - Design and implement binary frame header
- **Commit:** 2ce20c22
- **Changes:** Created `internal/protocol/` with 17-byte binary frame header (Type uint8, Timestamp uint64, Width uint16, Height uint16, PayloadSize uint32). Little-endian, zero-alloc marshal/unmarshal into pre-allocated buffers. Benchmarks: 0.55ns marshal, 0.28ns unmarshal.
- **Files:** `internal/protocol/protocol.go`, `internal/protocol/protocol_test.go`
- **Why:** Wire protocol is the contract between server and web client; needed before WebSocket and client work.
