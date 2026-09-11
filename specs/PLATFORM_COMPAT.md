# FeatherDesk — Cross-Platform Compatibility

## Platform Specs

| Platform | Spec | Status |
|----------|------|--------|
| **Linux** | [`./CENTRAL_SPEC.md`](./CENTRAL_SPEC.md) + module specs | ✅ Working codebase |
| **Windows** | [`./addons/windows/WINDOWS_SPEC.md`](./addons/windows/WINDOWS_SPEC.md) | 📋 Specced, not built |
| **macOS** | [`./addons/macos/MACOS_SPEC.md`](./addons/macos/MACOS_SPEC.md) | 📋 Specced, not built |

---

## Encoder Selection and Codec Policy

**Add-on probe order** (the runtime picks the *add-on*; the codec rule below then
picks what it emits). The order is per-OS, because an add-on for another OS cannot
be loaded — see [`./core/MODULE_PIPELINE.md`](./core/MODULE_PIPELINE.md) startup
step 3e, which is authoritative:

```
Linux    HW: nvenc → amf_rocm → libva     SW: openh264   (x264 by force_addon only)
Windows  HW: nvenc → amf → qsv → mf_hw    SW: openh264   (x264 by force_addon only)
macOS    HW: vt_hw                        SW: openh264 → vt_sw  (x264 by force_addon only)
```

Vendor-specific SDKs precede generic abstractions. There is **no software HEVC**
on any platform (libx265 has triple patent-pool exposure). H.264 licensing on the
hardware tiers is covered by the GPU/OS vendor; on the software tier Cisco carries
the MPEG-LA royalty for OpenH264.

**Codec policy.** The encoder advertises **H.264** (`avc1.*`) for every SDR
session, on every platform, regardless of what HEVC hardware is present. **HEVC
Main10** (`hvc1.2.*`) is emitted only for an HDR session, because WebCodecs has no
H.264 HDR profile. HEVC is never selected to save bandwidth: Firefox's WebCodecs
cannot decode it, and a codec no attached client can decode is a black screen, not
a saving. Which HW encoder is *selected* is a separate question from which codec it
*emits* — the probe order picks the add-on, this rule picks the codec.

**Client-side decode:**
The client reports what it can decode in its `auth` message and the server never
advertises a codec no attached client can decode (see
[`./core/MODULE_PROTOCOL.md`](./core/MODULE_PROTOCOL.md) "Decode capability").

> **Minimum browser: Chrome 107+, Edge 98+, Firefox 130+, Safari 16.4+** (the
> WebCodecs floor). The WebTransport carrier additionally needs Safari 26.4+; below
> that the client uses the WebSocket fallback carrier — see
> [`./core/MODULE_TRANSPORT.md`](./core/MODULE_TRANSPORT.md) "Carrier selection".
> See also [`./client/MODULE_WEB_CLIENT.md`](./client/MODULE_WEB_CLIENT.md).
> **Self-signed TLS caveat:** the self-signed (LAN/self-hosted default) mode reaches
> WebTransport via `serverCertificateHashes` (Chrome/Edge 107+, Firefox recent).
> **Safari's support is incomplete** — Safari clients may need a CA-trusted cert
> (`server.tls.cert`/`key`). See [`./core/MODULE_SERVER.md`](./core/MODULE_SERVER.md)
> "Browser certificate trust".
> **HEVC caveat:** Chrome/Edge/Safari decode HEVC; Firefox's WebCodecs does
> **not**. A host that selects HEVC (only ever for HDR) is decodable by Chromium and
> WebKit clients alone, so HDR is Chromium/WebKit-only and the server enforces that
> with the decode-capability gate rather than leaving Firefox viewers black. In the
> browser, an HDR stream is decoded at 10-bit and **tone-mapped** into the canvas's
> sRGB/Display-P3 output; true HDR display output is a native-client (v2) capability.

## Codec Support Matrix

| Platform | Hardware | H.264 HW | HEVC HW | AV1 HW | SW Fallback |
|----------|----------|---------|---------|--------|------------|
| Linux | Intel (VA-API) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Arc+ | OpenH264 (Rust FFI) |
| Linux | AMD (VA-API) | ✅ GCN+ | ✅ Polaris+ | ✅ RDNA3+ (RX 7000+) | OpenH264 (Rust FFI) |
| Linux | NVIDIA (via nvidia-vaapi-driver) | ✅ | ✅ | ❌ | OpenH264 (Rust FFI) |
| Linux | No GPU | ❌ | ❌ | ❌ | OpenH264 (Rust FFI) |
| Windows | NVIDIA (NVENC) | ✅ | ✅ | ✅ RTX40+ | OpenH264 (Rust FFI) |
| Windows | AMD (AMF) | ✅ | ✅ | ✅ RDNA3+ (RX 7000+) | OpenH264 (Rust FFI) |
| Windows | Intel (QSV) | ✅ Sandy Bridge+ | ✅ Skylake+ | ✅ Arc+ | OpenH264 (Rust FFI) |
| Windows | No GPU | ❌ | ❌ | ❌ | OpenH264 (Rust FFI) |
| macOS | Apple Silicon M1 | ✅ | ✅ | ❌ encode | OpenH264 (Rust FFI)* |
| macOS | Apple Silicon M2 | ✅ | ✅ | ❌ | OpenH264 (Rust FFI)* |
| macOS | Apple Silicon M3+ | ✅ | ✅ | ❌ (decode only) | OpenH264 (Rust FFI)* |
| macOS | Intel + AMD discrete | ✅ | ✅ | ❌ | OpenH264 (Rust FFI)* |
| macOS | Intel integrated Skylake+ | ✅ | ✅ | ❌ | OpenH264 (Rust FFI)* |
| macOS | Intel integrated pre-Skylake | ✅ | ❌ | ❌ | OpenH264 (Rust FFI)* |

> *macOS software fallback: `openh264` (Rust FFI) is the cross-platform default;
> `vt_sw` is preferred where it probes available, since VideoToolbox is macOS-native.
> `x264` is opt-in on every OS (`[encode] force_addon = "x264"`).

---

## Capture — One Per Platform (Linux: one default plus two no-root paths)

The default binary has **no capture backend** on any OS; capture is always
an opt-in add-on shared library.

| Platform | Add-on ID | Mechanism | Headless support |
|----------|-----------|-----------|------------------|
| **Linux** | `kms_egl` (+ optional `nvfbc`) | KMS/DRM + EGL DMA-BUF zero-copy | **Needs a real KMS CRTC** — see "Headless on Linux" below |
| **Linux** (no-root / headless Wayland) | `wl_screencopy`, `pw_portal` | `wlr-screencopy` / `zwlr-export-dmabuf`, or the xdg-desktop-portal ScreenCast stream | Yes — no DRM master, no `CAP_SYS_ADMIN` |
| **Windows** | `dxgi_dd` | DXGI Desktop Duplication → ID3D11Texture2D | Integrated IddCx virtual display (auto-installed) |
| **macOS** | `sck` | ScreenCaptureKit → CMSampleBuffer / IOSurface | Virtual display driver |

`kms_egl` remains the **recommended default** wherever root is available: it is
the lowest-latency path and the only one that is display-server agnostic. The
no-root add-ons exist for the deployments it cannot serve (see below), not to
replace it. X11grab / WGC / GDI / Magnification remain rejected — see per-OS
capture READMEs.

### Headless on Linux (corrected)

**`kms_egl` captures DRM/KMS *scanout*.** It therefore requires a CRTC with a
mode set and a display server (or a client) actually rendering to it. Two
consequences that earlier revisions of this file and `LINUX_SPEC.md` stated
incorrectly:

- **Xvfb does not work with `kms_egl`.** Xvfb renders into main memory and never
  touches DRM, so there is no scanout buffer to import and the add-on captures
  nothing. The same is true of Xephyr and any other in-memory X server.
- **"No display server at all" does not work either.** With nothing rendering to
  a CRTC there is no framebuffer to capture — the property `kms_egl` actually has
  is *display-server **agnostic***, not *display-server **optional***.

A genuinely headless `kms_egl` deployment therefore needs **a GPU with a
connected or force-enabled connector** (e.g. kernel parameter
`video=HDMI-A-1:1920x1080e` to force a disconnected output on) **plus a
compositor rendering to it**. That is a supported configuration; it is simply not
"run Xvfb".

For headless where that is not achievable — no forced connector, a nested or
virtual Wayland compositor, or no root — use the `wl_screencopy` or `pw_portal`
add-on instead. `vkms` (the kernel's virtual KMS driver) was evaluated and
rejected: it produces DRM planes but no accelerated, DMA-BUF-exportable
framebuffer worth encoding.

---

## Audio — One Per Platform (host→client only; 🔒 design locked, ⏸️ impl deferred)

Same pluggable, zero-by-default pattern as capture/input — a per-OS **capture**
add-on shared library normalizes the OS device format to the canonical 48 kHz /
S16LE / interleaved, Vorbis channel order, with the **channel count following the
host output layout** up to 7.1 (or forced to 2 by `[audio] channels = "stereo"`),
and a separate **codec** add-on (`opus`, else raw PCM) sets the wire format
(advertised in the `config` message). **No subprocess** (`pw-cat` gone), no
driver, no mic. Realtime, **audio-master** A/V sync. See
[`./media/MODULE_AUDIO.md`](./media/MODULE_AUDIO.md).

| Platform | Add-on ID | Capture mechanism | Spec |
|----------|-----------|-------------------|------|
| **Linux** | `pipewire` | PipeWire monitor (native libpipewire; Pulse/ALSA fallback) | [`linux/audio/PIPEWIRE_LINUX_SPEC.md`](./addons/linux/audio/PIPEWIRE_LINUX_SPEC.md) |
| **Windows** | `wasapi` | WASAPI loopback (default render endpoint) | [`windows/audio/WASAPI_WINDOWS_SPEC.md`](./addons/windows/audio/WASAPI_WINDOWS_SPEC.md) |
| **macOS** | `sck_audio` | ScreenCaptureKit audio on the shared `sck` stream (macOS 13+) | [`macos/audio/SCK_AUDIO_MACOS_SPEC.md`](./addons/macos/audio/SCK_AUDIO_MACOS_SPEC.md) |

Codec: `opus` (BSD libopus, in-process; ~96–128 kbps) — **recommended** — or raw
S16LE PCM (1.536 Mbps at stereo, more for surround; no concealment) when the `opus`
add-on isn't loaded. The Opus encoder always runs with in-band FEC on, but what a
receiver can do with it differs by path: the wasm-libopus and native-client paths
get full Opus FEC + PLC, while the browser's WebCodecs `AudioDecoder` exposes
neither, so that path gets bounded, clock-preserving worklet-side concealment
instead — see [`./media/MODULE_AUDIO.md`](./media/MODULE_AUDIO.md)
"Loss concealment, by path".

---

## Input Injection — `enigo` Default + Optional Overrides

kb/mouse is injected by the in-core **`enigo`** default on every OS — anti-cheat-safe, like Sunshine. Optional add-ons **override/extend** it:

| Platform | Default (in-core `enigo`) | Optional override / extension add-on |
|----------|---------------------------|----------------------------------------|
| **Linux** | XTEST / libei (X11 / Wayland) | **uinput** (kernel virtual device, gaming-grade + force-feedback; needs `/dev/uinput`) |
| **Windows** | `SendInput` | **interception** (kernel filter driver + `SendSAS`, below UIPI / reaches elevated apps; ⚠️ anti-cheat risk) · `win_touch` (touch) · `vigem` (gamepad) |
| **macOS** | `CGEvent` (needs Accessibility) | `gcvirtual` (gamepad) — no kb/mouse add-on (`enigo`'s CGEvent **is** the default) |

---

## Protocol — Platform-Agnostic

The wire protocol (`./core/MODULE_PROTOCOL.md`) is identical on all platforms.
Reachability is not part of it: v1 performs no NAT traversal and operates no
relay — the host binds a UDP port and the operator supplies the path to it (LAN,
port-forward, or a tunnel that carries QUIC end to end). See
[`./v2/MODULE_NETWORK.md`](./v2/MODULE_NETWORK.md) for the v2 plan.

The protocol itself:
- 22-byte media `FrameHeader` (Version, Type, Sequence, Timestamp, W, H, PayloadSize) — media channels only (datagram fragment 0 + bootstrap stream)
- `config` JSON message on the **control stream** as handshake — carries codec string, dims, cursorMode (the binary type-6 Config frame is retired)
- `FrameTypeVideoH264` (type 1) for video, `FrameTypeVideoHEVC` (type 7) for an HDR session — slot 5 reserved (formerly VP8, rejected)
- `FrameTypeAudioOpus` (type 8, default) / `FrameTypeAudioPCM` (type 4) for audio — host→client, stereo / 5.1 / 7.1 (impl deferred)
- **Binary** `[u16 RecLen]`-prefixed input records on the **input stream** (C→S); JSON on the **control stream** for keyframe/resize/etc. — input is NOT JSON

The codec in the `config` message is the **full WebCodecs codec string**, and its
**level is computed from the active resolution and frame rate**, never a constant —
see [`./core/MODULE_ABI.md`](./core/MODULE_ABI.md) "Codec-string computation" for
the algorithm and the reference table (1080p60 H.264 High is `avc1.64002A`, not the
Level-3.1 string a fixed constant would produce). AV1 (`av01.0.*`) is reserved for a
future `av1` add-on; **no Apple Silicon has AV1 HW encode** (M3+ has decode only).

**Chroma subsampling** (`[stream] chroma`): `420` (universal default) / `422` / `444`.
4:2:2/4:4:4 sharpen text but are capability-negotiated with a transparent fall-back
to 4:2:0 (encoder support varies — OpenH264 is 4:2:0-only, x264 does all, NVENC 4:4:4
on Turing+; and browser decode of 4:4:4 is best-effort, reliable on the native
client). See [`./core/MODULE_STREAM_PARAMS.md`](./core/MODULE_STREAM_PARAMS.md).

---

## Implementation Status (current)

> **Legend — one glyph, one meaning.** 📋 *specced*: the design is written; nothing
> has been measured or built. 📈 *measured*: a benchmark artifact **in this repo**
> records numbers for it (`PROJECT_ARTIFACTS/bench_out/`, all Windows). ✅ *runs
> today*: it executes in the Go reference implementation on `feature-libav-vp8s8`.
> ⏸️ *design locked, impl deferred*. ❌ *not available* — with the reason.
> A cell may carry more than one glyph; "specced" alone never implies "measured".

| Feature | Linux | Windows | macOS |
|---------|-------|---------|-------|
| Capture | 📋 KMS+EGL specced · ❌ not the path that runs (see note) | 📋 DXGI DD specced · ❌ never measured (every `bench_out` session reports `available: false`) | 📋 SCK specced · ❌ no artifact in this repo (the Hackintosh CSVs `MACOS_SPEC.md` cites live under `/tmp`, off-repo) |
| HW encode | 📋 libva/NVENC/AMF specced | 📋 NVENC/AMF/MF specced · 📈 measured · QSV 📋 specced only (no Intel silicon on the bench machine) | 📋 VideoToolbox specced · ❌ no artifact in this repo (Hackintosh only, off-repo) |
| SW encode (BSD) | ✅ OpenH264 runs today · 📈 measured on Windows (7.9 ms p50 @ 1080p, 4 threads) | 📋 OpenH264 specced · 📈 measured | 📋 OpenH264 specced |
| SW encode (GPL, opt-in) | 📋 x264 subprocess specced · 📈 measured on Windows (4.3 ms p50 @ 1080p, ultrafast, 4 threads) | 📋 x264 specced · 📈 measured | 📋 x264 specced |
| Audio | ⏸️ design locked, impl deferred | ⏸️ design locked, impl deferred | ⏸️ design locked, impl deferred |
| Input | 📋 `enigo` default specced · ✅ uinput runs today · 📋 uinput add-on specced | 📋 `enigo` default specced · 📋 interception/win_touch/vigem specced | 📋 `enigo` default specced · 📋 gcvirtual specced |
| Browser client | 📋 WebTransport + WebCodecs client specced · ❌ not built (the Go branch's client is WebSocket-based) | shared | shared |

> **Linux capture note.** `kms_egl` is the specced Linux default and the only
> Linux capture add-on in the recommended set, but it is **not** what runs on
> `feature-libav-vp8s8` today: `cmd/server/main.go:137` constructs
> `capture.NewX11Capturer`, and `NewKMSCapturer` has no non-test caller. Commit
> `deb99d1` moved the default off KMS+EGL after the EGLImage backing a
> `GL_TEXTURE_2D` proved not framebuffer-attachable on Intel HD 630 under GNOME
> Wayland (XR30 10-bit framebuffers with Y-tiled modifiers). The path that runs
> instead — `x11grab.go` + `screencast.py` — is explicitly rejected by
> [`./media/MODULE_CAPTURE.md`](./media/MODULE_CAPTURE.md) "What Was Rejected".
> Making `kms_egl` work on tiled 10-bit framebuffers is therefore in scope for
> the Rust rewrite, not a port of already-working code.
