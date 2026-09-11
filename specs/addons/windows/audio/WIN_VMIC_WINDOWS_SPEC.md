# Add-On Spec: `win_vmic` — virtual microphone (Windows, client→host)

## Overview

The Windows half of the client→host microphone path
([`../../../media/MODULE_AUDIO.md`](../../../media/MODULE_AUDIO.md)
"Microphone (client→host)"). It presents a virtual audio **capture** endpoint
that host applications select as a microphone, and writes the decoded client
audio into it.

Windows sits between the two other platforms in difficulty. Unlike Linux there
is no user-space null-sink trick — a virtual capture endpoint requires a
**driver**. Unlike macOS, that driver does not need to be notarized by a
platform gatekeeper, and FeatherDesk already sets the precedent for this exact
shape of dependency: the gamepad add-on `vigem` depends on ViGEmBus, a
separately-installed driver, and is documented as such rather than bundled.

`AddonKind::AudioSink` (`0x0A`).

---

## Requirements

| | |
|---|---|
| Runtime | A virtual audio capture driver providing a render endpoint whose audio appears on a capture endpoint. FeatherDesk does **not** ship one in v1 |
| Privilege | None at runtime. The **driver install** is one-time and requires admin, exactly as ViGEmBus does for gamepad |
| Links | `mmdeviceapi`, `audioclient` (WASAPI render side) |
| Probe fails when | No suitable endpoint is present → `ProbeReport { available: false, reason: "no_virtual_mic_device" }`. This is a **warning path**, never an error: host→client audio and the rest of the session are unaffected |

> **v1 does not bundle or install a driver.** The add-on targets an endpoint the
> operator has provisioned, and the probe reason names exactly what is missing.
> Bundling a signed kernel driver is a distribution decision of the same weight
> as the macOS plug-in, and it is deliberately not made here — the difference is
> that on Windows an operator *can* satisfy it today, which is why the mic ships
> on this platform and not on macOS.

---

## ABI surface

Implements `AddonKind::AudioSink` (`0x0A`) per
[`../../../core/MODULE_ABI.md`](../../../core/MODULE_ABI.md) "Root module
surface". As required of every add-on: `init(host)` calls
`featherdesk_abi::install_log_sink(host.log)` first; `descriptor()` fills
`os` / `arch` from `abi::HOST_OS` / `abi::HOST_ARCH`, `id = "win_vmic"`, empty
`codecs`; every entry point and trait method is wrapped in
`std::panic::catch_unwind` returning `AbiErr::Unrecoverable` (`7`); the crate is
built `panic = "unwind"`.

### Capability bits

`AddonCaps(0)` — no audio capability bits are defined in v1. The trait surface
(`write_chunk`, `format`) is fully mandatory.

---

## Configuration

```toml
[addon_module_win_vmic]
device = ""                # "" = the first endpoint whose friendly name matches a virtual
                           #   capture device. A non-empty value is an exact MMDevice id,
                           #   which is what an operator with several virtual devices uses.
                           #   A named device that is absent is a PROBE FAILURE, never a
                           #   silent fallback to a different one — writing a user's voice
                           #   into the wrong device is the error this rule prevents.
share_mode = "shared"      # "shared" | "exclusive". Shared is correct: exclusive mode locks
                           #   the endpoint away from every other host application, which
                           #   defeats the purpose of presenting a microphone.
```

Decoded in `construct()` against the add-on's own
`#[serde(deny_unknown_fields)]` struct; an unknown key returns
`AbiErr::BadConfig` (`10`).

---

## Device lifecycle

The endpoint is **not owned by FeatherDesk** — the driver provides it and it
outlives the process. That inverts the Linux obligation:

- `construct()` opens an `IAudioClient` on the render side of the virtual
  device and starts it; `Drop` stops and releases it. Nothing is created or
  destroyed.
- There is therefore **no orphan risk** from FeatherDesk, and equally **no
  cleanup FeatherDesk can do**: a virtual mic device present on the host is the
  operator's, and it stays. The privacy note in MODULE_AUDIO applies with more
  force here — the device is visible to every application whether or not
  FeatherDesk is running.
- A device removed while the session runs (driver uninstall, endpoint disabled)
  surfaces as `AUDCLNT_E_DEVICE_INVALIDATED` → `StreamError::Unrecoverable`.
  The pipeline poisons the sink and the session continues without a mic.

## Frame acquisition

This add-on is a **consumer**; the host drives it.

- `write_chunk(&RPcmChunk)` is called from the audio loop's mic half, on one
  thread, in timestamp order, and copies into the WASAPI render buffer.
- `format()` reports the endpoint's mix format. The **host** resamples to it —
  one resampler on the mic path, not one per backend.
- Underrun is the host's business: the audio loop writes silence when no
  controller is connected, so this add-on never repeats a buffer.
- COM is initialised per-thread (`COINIT_MULTITHREADED`) in `construct()` on
  the audio thread, which is the only thread that touches this object.

---

## Performance

| Metric | Target | Note |
|---|---|---|
| `write_chunk` (p99) | < 0.5 ms | A memcpy into the WASAPI render buffer |
| Added mouth-to-host-application latency | one `mic_frame_ms` period + the endpoint period | ~20-40 ms; the shared-mode engine period dominates |
| Idle cost with no controller | one silent period per `mic_frame_ms` | Never starve the endpoint — a starved shared-mode client is dropped by the engine |

📋 **Not measured** — no implementation exists. Budgets, not observations.

---

## Testing Strategy

| Type | What | Hardware |
|------|------|----------|
| Unit | `device = ""` selects a virtual capture endpoint when one exists; with none, `probe()` reports `available:false` reason `no_virtual_mic_device` and startup emits a warning, not an error | Yes (Windows) |
| Unit | A `device` id that does not resolve is a probe failure — the add-on never falls back to a different endpoint | Yes (Windows) |
| Integration | A tone written through `write_chunk` is readable by a second application recording from the virtual mic, same frequency, no channel swap | Yes (Windows) |
| Unit | `AUDCLNT_E_DEVICE_INVALIDATED` mid-session maps to `Unrecoverable`; the sink is poisoned and the audio/video session continues | Yes (Windows) |
| Unit | `share_mode = "exclusive"` is accepted but logs a warning naming the consequence (other applications lose the device) | No |
| Unit | `format()` is stable across the add-on's life | No |

---

## Status

📋 **Specced — in v1, not yet built.** Added when the microphone was brought
into audio scope for v1. The add-on targets an operator-provisioned virtual
audio device; FeatherDesk bundles no driver in v1, the same stance it takes for
`vigem` / ViGEmBus.
