# User Stories: Cross-Platform Behavior

Derived directly from `specs/PLATFORM_COMPAT.md`'s compatibility matrices.
Stories are written honestly against that file's own status markers
(✅ working / 📋 specced-not-built / ⏸️ design-locked-impl-deferred) — a
story about Windows or macOS host support is explicitly marked as **not yet
satisfiable today** where the matrix says so, rather than implied as done.

---

## US-XP-1: H.264 is a universal baseline video codec on every platform

**As a** developer-operator deploying FeatherDesk on any of the three supported OSes
**I want** H.264 hardware or software encoding to be available regardless of platform or GPU vendor
**So that** every deployment has a guaranteed-working codec path, with fancier codecs layered on top only where available

**Acceptance Criteria:**
- Given any Linux GPU (Intel VA-API Sandy Bridge+, AMD VA-API GCN+, or NVIDIA via nvidia-vaapi-driver), When hardware is probed, Then H.264 HW encode is available (✅ in the Codec Support Matrix).
- Given any Windows GPU (NVENC, AMF, or QSV Sandy Bridge+), When hardware is probed, Then H.264 HW encode is available.
- Given any macOS Apple Silicon or Intel Mac (integrated Skylake+ or discrete), When hardware is probed, Then H.264 HW encode is available.
- Given no GPU at all (any OS) or a pre-Skylake Intel integrated GPU on macOS, When hardware probing finds no H.264 HW path, Then the OpenH264 (Rust FFI) software encoder is used as the universal fallback — this is the only encoder guaranteed on every configuration.
- Given the codec fallback order (HEVC HW -> H.264 HW -> H.264 software), When no HEVC HW is available, Then the server transparently selects H.264, and the client decodes whatever is advertised in `config` with no client-side codec negotiation.

**Validated by:** specs/PLATFORM_COMPAT.md — "Codec Fallback Order (Confirmed)", "Codec Support Matrix"

---

## US-XP-2: AV1 hardware encoding is unavailable on all Apple Silicon

**As a** host user on a Mac
**I want** the system to never attempt AV1 hardware encoding on my machine and to know this is expected, not a bug
**So that** I don't file a false bug report when AV1 HW encode "doesn't work" on Apple Silicon

**Acceptance Criteria:**
- Given any Apple Silicon Mac (M1, M2, or M3+), When the encoder is selected, Then AV1 hardware encode is never chosen because no Apple Silicon has AV1 HW encode (M3+ has AV1 decode only, still no encode).
- Given Windows with RTX 40+ NVIDIA (Ada Lovelace), RDNA3+ AMD (RX 7000+), or Intel Arc, When the encoder is selected, Then AV1 HW encode IS available and may be chosen.
- Given Linux with AMD RDNA3+ (RX 7000+) or Intel Arc, When the encoder is selected, Then AV1 HW encode IS available; given Linux NVIDIA (via nvidia-vaapi-driver), Then AV1 HW encode is NOT available (❌ in the matrix) regardless of GPU generation.

**Validated by:** specs/PLATFORM_COMPAT.md — "Codec Support Matrix", "Protocol — Platform-Agnostic" (AV1 codec string note: "No Apple Silicon has AV1 HW encode")

---

## US-XP-3: HEVC/HDR streams are decodable only by Chromium/WebKit clients

**As a** host user who enables HDR (which requires HEVC)
**I want** to know in advance which browsers can actually display the resulting stream
**So that** I don't enable HDR and then have Firefox viewers see a broken/black screen

**Acceptance Criteria:**
- Given the host selects HEVC (e.g., because HDR was requested), When a Chrome, Edge, or Safari client connects, Then it decodes the HEVC stream successfully (WebCodecs HEVC decode is supported).
- Given the same HEVC/HDR stream, When a Firefox client connects, Then it CANNOT decode it, because Firefox's WebCodecs implementation does not support HEVC — Firefox clients require an H.264 stream instead.
- Given this constraint, When documenting HDR support to users, Then HDR must be described as "effectively Chromium/WebKit-only," not as a universally supported feature.

**Validated by:** specs/PLATFORM_COMPAT.md — "Codec Fallback Order (Confirmed)" (HEVC caveat)

---

## US-XP-4: Minimum browser versions are enforced for the WebCodecs + WebTransport intersection

**As a** viewer connecting from a browser
**I want** to be told clearly if my browser is too old, rather than getting a silent failure
**So that** I know to upgrade instead of assuming FeatherDesk itself is broken

**Acceptance Criteria:**
- Given a viewer on Chrome 107+, Edge 98+, Firefox 130+, or Safari 18.2+, When they connect, Then WebCodecs + WebTransport are both available and the session can be established.
- Given a viewer below any of these minimum versions, When they attempt to connect, Then the connection is expected to fail at the WebCodecs/WebTransport capability level (this is a known platform floor, not a regression to chase per-browser).

**Validated by:** specs/PLATFORM_COMPAT.md — "Codec Fallback Order (Confirmed)" (minimum browser line)

---

## US-XP-5: Self-signed TLS mode works on Chrome/Edge/Firefox but needs a CA cert on Safari

**As a** host user running the default self-hosted/LAN deployment with a self-signed certificate
**I want** to know that Safari viewers may need a different TLS setup than everyone else
**So that** I configure a CA-trusted cert proactively for a mixed-browser audience instead of debugging a Safari-only connection failure later

**Acceptance Criteria:**
- Given the self-signed (LAN) TLS mode, When a Chrome/Edge 107+ or recent Firefox client connects via `serverCertificateHashes`, Then the WebTransport session opens successfully.
- Given the same self-signed mode, When a Safari client connects, Then the connection may fail because Safari's `serverCertificateHashes` support is incomplete, and a CA-trusted cert (`server.tls.cert`/`key`) is required for reliable Safari support.

**Validated by:** specs/PLATFORM_COMPAT.md — minimum-browser note ("Self-signed TLS caveat"); specs/core/MODULE_SERVER.md — "Browser certificate trust" (Browser support caveat)

---

## US-XP-6: Chroma subsampling capability varies by encoder and is negotiated down when needed

**As a** host user wanting sharper text via 4:2:2/4:4:4 chroma
**I want** the system to use the best chroma format my encoder and viewer can actually handle, falling back cleanly when not
**So that** I get the sharpest picture possible without a broken stream on encoders that don't support it

**Acceptance Criteria:**
- Given the OpenH264 (Rust FFI) software encoder is active on any platform, When 4:2:2 or 4:4:4 is requested, Then it is unavailable — OpenH264 is 4:2:0-only, so the stream falls back to 4:2:0.
- Given NVENC on Turing-generation or later, When 4:4:4 is requested, Then it is supported by the encoder.
- Given any encoder/browser combination, When the browser cannot decode the advertised chroma format, Then the client reports `chroma_unsupported` and the stream downgrades to 4:2:0 (universal default) — this fallback is transparent, not a failure state.

**Validated by:** specs/PLATFORM_COMPAT.md — "Protocol — Platform-Agnostic" (Chroma subsampling); specs/core/MODULE_STREAM_PARAMS.md — "Chroma Subsampling (4:2:0 / 4:2:2 / 4:4:4)" section (referenced)

---

## US-XP-7: Windows host support is specced but not yet built — must not be represented as working

**As a** developer-operator evaluating FeatherDesk for a Windows deployment today
**I want** the documentation and test plan to accurately reflect that Windows support is a spec, not a working implementation
**So that** I don't plan a production Windows rollout against a feature that doesn't exist yet

**Acceptance Criteria:**
- Given `specs/PLATFORM_COMPAT.md`'s "Platform Specs" table, When Windows is checked, Then its status is explicitly "📋 Specced, not built" — this story is NOT satisfiable today and any test asserting a working Windows host build should be marked expected-to-fail / not-applicable until implementation lands.
- Given the "Implementation Status" table, When Windows capture (DXGI DD) and HW encode (NVENC/AMF/MF/QSV) are checked, Then they show "✅ ... specced & benchmarked" (design/benchmarking done) while SW encode (OpenH264) shows "✅ ... specced & benchmarked" but overall platform status remains "not built" per the Platform Specs table — i.e., component-level benchmarking existing does NOT imply an integrated, shippable Windows host.
- Given Windows audio (WASAPI loopback), When checked, Then its status is "⏸️ design locked, impl deferred" — the design is finalized but no implementation exists yet.

**Validated by:** specs/PLATFORM_COMPAT.md — "Platform Specs" table, "Implementation Status (current)" table

---

## US-XP-8: macOS host support is specced but not yet built — must not be represented as working

**As a** developer-operator evaluating FeatherDesk for a macOS deployment today
**I want** the documentation and test plan to accurately reflect that macOS support is a spec, not a working implementation
**So that** I don't plan a production macOS rollout against a feature that doesn't exist yet

**Acceptance Criteria:**
- Given `specs/PLATFORM_COMPAT.md`'s "Platform Specs" table, When macOS is checked, Then its status is explicitly "📋 Specced, not built" — this story is NOT satisfiable today.
- Given the "Implementation Status" table, When macOS HW encode (VideoToolbox) is checked, Then it shows "📋 VideoToolbox specced (Hackintosh measured)" — measured only on a Hackintosh, not real Apple hardware, and not yet built as a shipped add-on.
- Given macOS SW encode (OpenH264 and x264), When checked, Then both show "📋 ... specced" only (no "& benchmarked" — unlike the Windows/Linux SW rows), meaning even performance validation is outstanding.
- Given macOS audio (ScreenCaptureKit audio, macOS 13+), When checked, Then its status is "⏸️ design locked, impl deferred", same as every other platform's audio.

**Validated by:** specs/PLATFORM_COMPAT.md — "Platform Specs" table, "Implementation Status (current)" table

---

## US-XP-9: Linux is the only currently working end-to-end platform

**As a** developer-operator deciding where to deploy or test FeatherDesk today
**I want** to know that Linux is the sole platform with a working codebase right now
**So that** I set expectations correctly and don't treat Windows/macOS specs as ready for QA sign-off

**Acceptance Criteria:**
- Given `specs/PLATFORM_COMPAT.md`'s "Platform Specs" table, When Linux is checked, Then its status is "✅ Working codebase" — the only platform marked working, versus "📋 Specced, not built" for both Windows and macOS.
- Given the "Implementation Status" table's "Browser client" row, When checked, Then it shows "✅ built" for Linux and "shared" for Windows/macOS (the browser client itself is platform-independent once the host exists, but that does not make the Windows/macOS host builds themselves complete).
- Given Linux capture (KMS+EGL) and input (enigo default), When checked, Then both show "✅" (working/specced-and-working), matching the platform's overall working status — Linux HW encode (libva/NVENC) itself shows "📋 ... specced" (design done, not yet benchmarked-built at the level the Windows row is), so even the "working" platform has an outstanding item.

**Validated by:** specs/PLATFORM_COMPAT.md — "Platform Specs" table, "Implementation Status (current)" table

---

## US-XP-10: Audio is design-locked but not implemented on any platform

**As a** host user expecting to hear system audio through the remote session
**I want** to know that audio does not work on ANY platform yet, not just the one I'm testing
**So that** I don't spend time debugging "broken audio" on my specific OS when it is a universal gap

**Acceptance Criteria:**
- Given any of the three platforms (Linux/pipewire, Windows/wasapi, macOS/sck_audio), When audio capture is checked in the Implementation Status table, Then all three show "⏸️ design locked, impl deferred" — none is implemented.
- Given this status, When a QA pass encounters no audio in any build, Then this is NOT logged as a platform-specific defect — it is the expected, documented state across the whole product until audio implementation begins.

**Validated by:** specs/PLATFORM_COMPAT.md — "Audio — One Per Platform" section header (🔒 design locked, ⏸️ impl deferred), "Implementation Status (current)" table (Audio row, all three platforms)

---

## US-XP-11: Input injection defaults to a safe, anti-cheat-friendly method on every platform

**As a** host user
**I want** keyboard/mouse injection to use an anti-cheat-safe method by default on whichever OS I run, with more invasive options only as explicit opt-in add-ons
**So that** the default deployment doesn't get flagged by games/anti-cheat, while power users can still opt into deeper injection when they accept the risk

**Acceptance Criteria:**
- Given Linux, When input is injected with no add-on loaded, Then it uses in-core `enigo` via XTEST/libei (X11/Wayland) by default; optionally, `uinput` can be loaded for gaming-grade kernel-level injection.
- Given Windows, When input is injected with no add-on loaded, Then it uses in-core `enigo` via `SendInput` by default; optionally, `interception` (kernel filter driver, explicitly flagged "⚠️ anti-cheat risk" in the spec) can be loaded.
- Given macOS, When input is injected, Then it uses in-core `enigo` via `CGEvent` (requiring Accessibility permission) — there is no kb/mouse override add-on on macOS, `enigo`'s CGEvent path IS the only supported mechanism.

**Validated by:** specs/PLATFORM_COMPAT.md — "Input Injection — `enigo` Default + Optional Overrides"
