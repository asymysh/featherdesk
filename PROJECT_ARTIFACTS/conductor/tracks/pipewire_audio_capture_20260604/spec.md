# Specification: PipeWire Audio Capture + Browser Playback

## Overview

Capture system audio from the host via PipeWire's monitor source and stream raw PCM to connected browser clients. The browser plays back audio via AudioWorklet with minimal latency.

## Dependencies

- Track 1 (KMS capture + software encode + WebSocket viewer) must be complete
- Existing WebSocket binary frame broadcast from Track 1

## Functional Requirements

### FR-1: PipeWire Audio Capture
- Connect to PipeWire as a stream consumer via cgo (libpipewire-0.3)
- Target the default audio sink's monitor port (system audio loopback)
- Receive Float32 PCM buffers (48kHz, stereo)
- Handle buffer callbacks in PipeWire thread, pass to Go via channel
- Handle PipeWire daemon disconnect with automatic reconnection

### FR-2: Audio Frame Protocol
- Define audio frame type in wire protocol
- Header fields for audio: sample_rate, channels, bits_per_sample, format_tag, frame_count
- Wrap raw PCM samples in protocol frame and broadcast

### FR-3: Browser Audio Playback
- Create AudioContext on user gesture (Audio button click)
- AudioWorklet processor: receive PCM chunks, fill output buffers
- Handle sample rate mismatch (resample if AudioContext rate != 48kHz)
- Queue management: cap at ~32 chunks to prevent unbounded growth
- Fill silence on underrun (no clicks/pops)

### FR-4: CLI Control
- `--no-audio` flag to disable audio capture entirely
- Audio starts automatically when enabled (no mode selection needed)
- Log audio format at startup (sample rate, channels, bits)

## Non-Functional Requirements

- Audio latency: <50ms end-to-end on LAN
- No audio compression for MVP (raw PCM, ~1.5 Mbps for 48kHz stereo float32)
- PipeWire thread must not block Go goroutines
- Handle audio device changes (sink switch) without crash

## Acceptance Criteria

1. Audio button in browser starts playback of host system audio
2. Audio and video are roughly in sync (no perceptible drift over 5 minutes)
3. No clicks/pops during normal playback
4. `--no-audio` flag disables audio entirely
5. PipeWire disconnect doesn't crash server
6. Audio works alongside video streaming without performance impact

## Out of Scope

- Audio compression (Opus/AAC)
- Microphone capture (client -> host)
- Per-client audio enable/disable on server side
- Audio routing configuration (always captures default sink monitor)
