# Implementation Plan: PipeWire Audio Capture + Browser Playback

## Phase 1: PipeWire Stream Setup

- [ ] Task: Implement PipeWire initialization and stream connection (cgo)
    - [ ] cgo bindings: pw_init, pw_main_loop_new, pw_stream_new
    - [ ] Connect to default sink monitor (PW_KEY_MEDIA_CLASS = "Audio/Sink")
    - [ ] Configure stream: Float32, 48kHz, stereo, low latency
    - [ ] Register process callback to receive audio buffers
    - [ ] Run PipeWire main loop in dedicated goroutine
    - [ ] Implement clean shutdown (pw_stream_destroy, pw_main_loop_destroy)
- [ ] Task: Implement buffer handling and Go channel bridge
    - [ ] In PipeWire process callback: copy buffer data, send to Go channel
    - [ ] Channel buffer: 32 frames (prevent blocking PipeWire thread)
    - [ ] Drop oldest frame if channel is full (prefer fresh audio)
    - [ ] Convert PipeWire spa_data pointer to Go []byte slice safely
- [ ] Task: Write audio capture tests
    - [ ] Test PipeWire init/destroy lifecycle (integration, build-tag gated)
    - [ ] Test channel doesn't block when full (drop behavior)
    - [ ] Test buffer sizes are consistent (48000 * 2 * 4 bytes per second)
- [ ] Task: Conductor - User Manual Verification 'PipeWire Stream Setup' (Protocol in workflow.md)

## Phase 2: Audio Protocol & Broadcast

- [ ] Task: Add audio frame type to wire protocol
    - [ ] Define FrameTypeAudioPCM constant
    - [ ] Extend header or define audio-specific sub-header: sample_rate(u32), channels(u16), bits(u16), frame_count(u32)
    - [ ] Implement audio frame marshal function
    - [ ] Write unit tests for audio frame encoding
- [ ] Task: Implement audio broadcast in server
    - [ ] Receive audio frames from capture channel in server goroutine
    - [ ] Wrap in protocol frame (header + raw PCM payload)
    - [ ] Broadcast to all connected clients (same as video broadcast)
    - [ ] Audio and video share the same WebSocket connection
- [ ] Task: Implement --no-audio flag
    - [ ] Skip PipeWire initialization when flag is set
    - [ ] Log "Audio disabled" at startup
    - [ ] /status endpoint reports audio state
- [ ] Task: Conductor - User Manual Verification 'Audio Protocol & Broadcast' (Protocol in workflow.md)

## Phase 3: Browser Audio Playback

- [ ] Task: Implement AudioWorklet processor
    - [ ] Create worklet code inline (no separate file, use Blob URL)
    - [ ] Processor: receives PCM chunks via port.postMessage, fills output buffers
    - [ ] Handle stereo (2 channels) playback
    - [ ] Queue cap: 32 chunks max, drop oldest on overflow
    - [ ] Fill zeros (silence) when queue is empty (underrun)
- [ ] Task: Implement audio UI and lifecycle
    - [ ] Audio button: create AudioContext on click (user gesture required)
    - [ ] AudioWorklet addModule, create AudioWorkletNode, connect to destination
    - [ ] Parse audio binary frames: extract sample_rate, channels, bits, frame_count
    - [ ] Convert raw bytes to Float32Array (handle both Float32 native and Int16 PCM)
    - [ ] Transfer to worklet via postMessage with Transferable buffers
- [ ] Task: Handle edge cases
    - [ ] Sample rate mismatch: if AudioContext.sampleRate != 48000, log warning (acceptable for MVP)
    - [ ] Tab hidden: pause AudioContext to save resources
    - [ ] Reconnect: re-create AudioContext on new WebSocket connection
    - [ ] Fallback: ScriptProcessorNode if AudioWorklet unavailable
- [ ] Task: Conductor - User Manual Verification 'Browser Audio Playback' (Protocol in workflow.md)

## Phase 4: Error Handling & Integration

- [ ] Task: Implement PipeWire reconnection
    - [ ] Detect PipeWire disconnect (stream state change to ERROR/UNCONNECTED)
    - [ ] Wait 2 seconds, attempt reconnection
    - [ ] Log reconnection attempts
    - [ ] Cap retries (after 10 failures, stop trying, log permanent failure)
- [ ] Task: End-to-end audio verification
    - [ ] Play audio on host, verify it's heard in browser
    - [ ] Verify no perceptible A/V drift over 5 minutes
    - [ ] Verify --no-audio flag works (no audio frames sent)
    - [ ] Verify audio doesn't impact video FPS
- [ ] Task: Conductor - User Manual Verification 'Error Handling & Integration' (Protocol in workflow.md)
