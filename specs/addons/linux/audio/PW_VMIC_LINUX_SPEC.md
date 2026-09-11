# Add-On Spec: `pw_vmic` — PipeWire virtual microphone (Linux, client→host)

## Overview

The Linux half of the client→host microphone path
([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)
"Microphone (client→host)"). It creates a virtual input device that host
applications can select as a microphone, and writes the decoded client audio
into it.

Linux is the easy platform for this, which is why the mic ships here in v1: a
PipeWire **null-sink** plus its automatically-exposed monitor source is exactly
a virtual microphone, it needs **no kernel driver, no root and no install step**,
and it is created and destroyed by an ordinary client of the session bus. This
is the same property that made `pipewire` the host→client capture add-on, used
in the other direction.

`AddonKind::AudioSink` (`0x0A`). It is a **sink**, not a capturer: it consumes
PCM rather than producing it, which is why it is a separate kind rather than a
direction flag on `pipewire`.

---

## Requirements

| | |
|---|---|
| Runtime | PipeWire ≥ 0.3.40 with a running user session (`pipewire` + `wireplumber`) |
| Privilege | **None.** No root, no `CAP_SYS_ADMIN`, no driver |
| Links | `libpipewire-0.3` |
| Probe fails when | No PipeWire session bus reachable → `ProbeReport { available: false, reason: "no_pipewire_session" }`. PulseAudio-only hosts are **not** supported in v1 (`pactl load-module module-null-sink` would work, but a second mechanism is a second thing to maintain) |

---

## ABI surface

Implements `AddonKind::AudioSink` (`0x0A`) per
[`../../../core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module
surface". As required of every add-on: `init(host)` calls
`featherdesk_abi::install_log_sink(host.log)` first; `descriptor()` fills
`os` / `arch` from `abi::HOST_OS` / `abi::HOST_ARCH`, `id = "pw_vmic"`, empty
`codecs`; every entry point and trait method is wrapped in
`std::panic::catch_unwind` returning `AbiErr::Unrecoverable` (`7`); the crate is
built `panic = "unwind"`.

### Capability bits

`AddonCaps(0)` — no audio capability bits are defined in v1, on this kind or on
`AudioCapture`. The trait surface (`write_chunk`, `format`) is fully mandatory,
so there is nothing for a bit to gate.

---

## Configuration

```toml
[addon_module_pw_vmic]
target = ""                # "" = create and own a null-sink named "FeatherDesk Mic",
                           #   exposing its monitor as the virtual microphone. This is
                           #   the normal mode and the add-on destroys what it created.
                           #   A non-empty value names an EXISTING sink to write into;
                           #   the add-on then never creates or destroys anything, which
                           #   is the escape hatch for a host with its own audio routing.
node_name = "FeatherDesk Mic"  # The name host applications see in their input picker.
```

Decoded in `construct()` against the add-on's own
`#[serde(deny_unknown_fields)]` struct; an unknown key returns
`AbiErr::BadConfig` (`10`).

---

## Device lifecycle

**The device is owned, not leaked.** This is the add-on's main correctness
obligation, because an orphaned "FeatherDesk Mic" on a host nobody is connected
to is both confusing and a standing privacy surface:

- Created in `construct()`, destroyed in `Drop`. The pipeline drops the sink in
  its shutdown sequence, on the audio thread that built it.
- On an **abnormal** exit (SIGKILL, panic in another thread reaching abort) the
  node dies with the process anyway: a PipeWire node is owned by its client
  connection, so there is no persistent registry entry to clean up. This is a
  property of the mechanism, not of the code, and it is why the null-sink
  approach is preferred over a kernel-level virtual device on this platform.
- `target != ""` never destroys a sink it did not create.

## Frame acquisition

This add-on is a **consumer**; the host drives it.

- `write_chunk(&RPcmChunk)` is called from the audio loop's mic half, on one
  thread, in timestamp order. The add-on never reorders and never buffers beyond
  the node's own period — latency added here is latency the speaker hears.
- `format()` reports the node's format. The **host** resamples the client's
  stream to it before calling `write_chunk`; the add-on never resamples, so
  there is exactly one resampler on the mic path and it is not per-backend.
- An underrun (the host stops writing — no controller, a stalled client) is the
  host's business: the audio loop feeds silence rather than letting the node
  starve, so this add-on never synthesizes its own fill and never repeats a
  buffer.
- Loss of the PipeWire connection is `StreamError::Unrecoverable`. The pipeline
  poisons the sink and the session **keeps running without a mic**
  (MODULE_AUDIO "Failure behaviour") — losing the microphone must never take
  down the desktop.

---

## Performance

Slower paths than this exist; none of them matter, because the mic is not on
the A/V-sync path and nothing is slaved to it.

| Metric | Target | Note |
|---|---|---|
| `write_chunk` (p99) | < 0.5 ms | A memcpy into the node's ring at `mic_frame_ms` = 20 ms cadence |
| Added mouth-to-host-application latency | one `mic_frame_ms` period + the node period | ~20-30 ms, dominated by the client's own encode and the network |
| Idle cost with no controller | one silent period per `mic_frame_ms` | The host writes silence; the node is never starved |

📋 **Not measured** — no implementation exists. These are budgets, not
observations.

---

## Testing Strategy

| Type | What | Hardware |
|------|------|----------|
| Unit | `construct()` with `target = ""` creates a node named `node_name`; `Drop` removes it. A second construct after a drop reuses the name without collision | Yes (Linux) |
| Unit | `target` naming an existing sink neither creates nor destroys a node | Yes (Linux) |
| Integration | A tone written through `write_chunk` is readable at the monitor source at the same frequency, with no channel swap at stereo and 5.1 | Yes (Linux) |
| Integration | **No orphan:** after a clean shutdown and after SIGKILL, `pw-cli ls Node` shows no `FeatherDesk Mic` | Yes (Linux) |
| Unit | Losing the PipeWire connection mid-session returns `Unrecoverable`; the host poisons the sink and the audio/video session continues | Yes (Linux) |
| Unit | `format()` is stable across the add-on's life — the host's resampler is configured once | No |

---

## Status

📋 **Specced — in v1, not yet built.** Added when the microphone was brought
into audio scope for v1. Linux and Windows ship the mic; macOS is specced and
gated on a signed CoreAudio plug-in (MODULE_AUDIO "Microphone (client→host)").
