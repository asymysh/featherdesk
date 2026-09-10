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
- Given the codec policy, When any SDR session starts on any platform, Then the encoder advertises **H.264** (`avc1.*`) regardless of what HEVC hardware is present — HEVC Main10 is emitted only for an HDR session, because WebCodecs has no H.264 HDR profile and a codec no attached client can decode is a black screen, not a saving. Which HW add-on is *selected* is a separate question from which codec it *emits*.

**Validated by:** specs/PLATFORM_COMPAT.md — "Encoder Selection and Codec Policy", "Codec Support Matrix"

---

## US-XP-2: AV1 hardware encoding is unavailable on all Apple Silicon

**As a** host user on a Mac
**I want** the system to never attempt AV1 hardware encoding on my machine and to know this is expected, not a bug
**So that** I don't file a false bug report when AV1 HW encode "doesn't work" on Apple Silicon

**Acceptance Criteria:**
- Given any Apple Silicon Mac (M1, M2, or M3+), When the encoder is selected, Then AV1 hardware encode is never chosen because no Apple Silicon has AV1 HW encode (M3+ has AV1 decode only, still no encode).
- Given Windows with RTX 40+ NVIDIA (Ada Lovelace), RDNA3+ AMD (RX 7000+), or Intel Arc, When the encoder is selected, Then AV1 HW encode IS available and may be chosen.
- Given Linux with AMD RDNA3+ (RX 7000+) or Intel Arc, When the encoder is selected, Then AV1 HW encode IS available; given Linux NVIDIA (via nvidia-vaapi-driver), Then AV1 HW encode is NOT available (❌ in the matrix) regardless of GPU generation.

**Validated by:** specs/PLATFORM_COMPAT.md — "Codec Support Matrix", "Protocol — Platform-Agnostic" (AV1 codec string note: "no Apple Silicon has AV1 HW encode")

---

## US-XP-3: HEVC/HDR streams are decodable only by Chromium/WebKit clients

**As a** host user who enables HDR (which requires HEVC)
**I want** to know in advance which browsers can actually display the resulting stream
**So that** I don't enable HDR and then have Firefox viewers see a broken/black screen

**Acceptance Criteria:**
- Given the host selects HEVC (e.g., because HDR was requested), When a Chrome, Edge, or Safari client connects, Then it decodes the HEVC stream successfully (WebCodecs HEVC decode is supported).
- Given the same HEVC/HDR stream, When a Firefox client connects, Then it CANNOT decode it, because Firefox's WebCodecs implementation does not support HEVC — Firefox clients require an H.264 stream instead.
- Given this constraint, When an HDR session is requested while a Firefox viewer is attached, Then the host refuses it with `{"type":"hdr_unavailable"}` carrying `reason: attached_client_cannot_decode` rather than switching to a codec that viewer cannot decode — HDR is effectively Chromium/WebKit-only and the server enforces that instead of leaving Firefox viewers black.

**Validated by:** specs/PLATFORM_COMPAT.md — "Encoder Selection and Codec Policy" (HEVC caveat)

---

## US-XP-4: Minimum browser versions are enforced for the WebCodecs + WebTransport intersection

**As a** viewer connecting from a browser
**I want** to be told clearly if my browser is too old, rather than getting a silent failure
**So that** I know to upgrade instead of assuming FeatherDesk itself is broken

**Acceptance Criteria:**
- Given a viewer on Chrome 107+, Edge 98+, Firefox 130+, or Safari 26.4+, When they connect, Then WebCodecs and WebTransport are both available and the session runs on the preferred QUIC carrier.
- Given a viewer on Safari 16.4–26.3 (WebKit shipped WebTransport enabled-by-default only in 26.4), or on any browser whose network blocks UDP, When they connect, Then WebCodecs is still available and the session runs on the degraded WebSocket fallback carrier instead — the product floor is the **WebCodecs** floor, Chrome 107 / Edge 98 / Firefox 130 / Safari 16.4.
- Given a viewer below the WebCodecs floor, When they attempt to connect, Then the client gates on the capability rather than on a version string — `"VideoDecoder" in window` fails and it renders an explicit unsupported notice naming the minimum versions, rather than failing silently.

**Validated by:** specs/PLATFORM_COMPAT.md — "Encoder Selection and Codec Policy" (minimum browser line)

---

## US-XP-5: Self-signed TLS mode works on Chrome/Edge/Firefox but needs a CA cert on Safari

**As a** host user running the default self-hosted/LAN deployment with a self-signed certificate
**I want** to know that Safari viewers may need a different TLS setup than everyone else
**So that** I configure a CA-trusted cert proactively for a mixed-browser audience instead of debugging a Safari-only connection failure later

**Acceptance Criteria:**
- Given the self-signed (LAN) TLS mode, When a Chrome/Edge 107+ or recent Firefox client connects via `serverCertificateHashes`, Then the WebTransport session opens successfully.
- Given the same self-signed mode, When a Safari client connects, Then its WebTransport attempt may fail because Safari's `serverCertificateHashes` support is incomplete — the client then falls through to the WebSocket carrier, which is ordinary TLS over TCP and is covered by the one-time interstitial the user already clicked through for `GET /`. A CA-trusted cert (`server.tls.cert`/`key`) is what puts Safari back on the QUIC carrier.

**Validated by:** specs/PLATFORM_COMPAT.md — minimum-browser note ("Self-signed TLS caveat"); specs/core/MODULE_SERVER.md — "Browser certificate trust" (Browser support caveat)

---

## US-XP-6: Chroma subsampling capability varies by encoder and is negotiated down when needed

**As a** host user wanting sharper text via 4:2:2/4:4:4 chroma
**I want** the system to use the best chroma format my encoder and viewer can actually handle, falling back cleanly when not
**So that** I get the sharpest picture possible without a broken stream on encoders that don't support it

**Acceptance Criteria:**
- Given the OpenH264 (Rust FFI) software encoder is active on any platform, When 4:2:2 or 4:4:4 is requested, Then it is unavailable — OpenH264 is 4:2:0-only, so the stream falls back to 4:2:0.
- Given NVENC on Turing-generation or later, When 4:4:4 is requested, Then it is supported by the encoder.
- Given any encoder/browser combination, When `VideoDecoder.isConfigSupported()` rejects the advertised chroma format, Then the client reports `decode_unsupported` — without ever calling `configure()` on a config that probed unsupported — and the stream downgrades to 4:2:0 (universal default); this fallback is transparent, not a failure state.

**Validated by:** specs/PLATFORM_COMPAT.md — "Protocol — Platform-Agnostic" (Chroma subsampling); specs/core/MODULE_STREAM_PARAMS.md — "Chroma Subsampling (4:2:0 / 4:2:2 / 4:4:4)" section (referenced)

---

## US-XP-7: Windows host support is specced but not yet built — must not be represented as working

**As a** developer-operator evaluating FeatherDesk for a Windows deployment today
**I want** the documentation and test plan to accurately reflect that Windows support is a spec, not a working implementation
**So that** I don't plan a production Windows rollout against a feature that doesn't exist yet

**Acceptance Criteria:**
- Given a Windows host build, When it is attempted today, Then it does not exist — this story is NOT satisfiable, and any test asserting a working Windows host is marked expected-to-fail until an implementation lands.
- Given the `dxgi_dd` add-on when it does land, When it is benchmarked on hardware with a real display, Then a recorded acquisition p50 exists in `PROJECT_ARTIFACTS/bench_out` — no session there records one today (every DXGI row is `available: false`, `fps_actual: 0`), so any claim of a measured DXGI acquisition cost is unsupported until one appears.
- Given the Windows encoder add-ons, When their measured status is checked, Then NVENC, AMF and MF have recorded p50s in `bench_out` (e.g. `nvenc-1080ti-h264-qp26`: 4.68 ms p50 @ 1080p) and **QSV has none** — the bench machine has no Intel silicon, so QSV is specced, not measured.
- Given Windows audio (WASAPI loopback), When checked, Then no implementation exists and none is expected before the audio milestone.

**Validated by:** specs/PLATFORM_COMPAT.md — "Platform Specs" table, "Implementation Status (current)" table

---

## US-XP-8: macOS host support is specced but not yet built — must not be represented as working

**As a** developer-operator evaluating FeatherDesk for a macOS deployment today
**I want** the documentation and test plan to accurately reflect that macOS support is a spec, not a working implementation
**So that** I don't plan a production macOS rollout against a feature that doesn't exist yet

**Acceptance Criteria:**
- Given a macOS host build, When it is attempted today, Then it does not exist — this story is NOT satisfiable.
- Given macOS performance claims, When their provenance is checked, Then **no macOS benchmark session exists in this repository** — every session in `PROJECT_ARTIFACTS/bench_out` is `"platform":"windows"`, and the VideoToolbox and ScreenCaptureKit numbers cited in `MACOS_SPEC.md` come from Hackintosh CSVs under `/tmp`, outside the tree and unreproducible from it.
- Given macOS SW encode (OpenH264 and x264), When checked, Then neither has been measured on any Apple hardware, Hackintosh included.
- Given macOS audio (ScreenCaptureKit audio, macOS 13+), When checked, Then it is design-locked and deferred, like every other platform's audio.

**Validated by:** specs/PLATFORM_COMPAT.md — "Platform Specs" table, "Implementation Status (current)" table

---

## US-XP-9: Linux is the only currently working end-to-end platform

**As a** developer-operator deciding where to deploy or test FeatherDesk today
**I want** to know that Linux is the sole platform with a working codebase right now
**So that** I set expectations correctly and don't treat Windows/macOS specs as ready for QA sign-off

**Acceptance Criteria:**
- Given the three platforms, When "has a working end-to-end codebase" is evaluated, Then only Linux does — and what runs there is the Go reference implementation on `feature-libav-vp8s8`, not this branch's design.
- Given Linux capture, When the running path is inspected, Then it is `x11grab.go` + `screencast.py` (`cmd/server/main.go:137` constructs `NewX11Capturer`), **not** the specced `kms_egl`: `NewKMSCapturer` has no non-test caller, and the path that runs is explicitly rejected by `MODULE_CAPTURE.md` "What Was Rejected". Linux capture is therefore an open implementation item, not a port.
- Given Linux input, When the running path is inspected, Then it is uinput (`internal/input/device.go` opens `/dev/uinput`), while the specced default is the in-core `enigo` injector, which exists nowhere in the Go tree.
- Given the browser client, When the running one is inspected, Then it is a WebSocket client (`internal/server/server.go` imports `github.com/coder/websocket`); none of the specced WebTransport + WebCodecs client is built.

**Validated by:** specs/PLATFORM_COMPAT.md — "Platform Specs" table, "Implementation Status (current)" table (Linux capture note); specs/media/MODULE_CAPTURE.md — "What Was Rejected"

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

---

## US-XP-12: Incompatible add-ons are rejected at load, never partially loaded

**As a** host operator dropping add-on libraries into the add-ons directory
**I want** a mismatched library to be refused with a reason
**So that** a stale or wrong-architecture `.so` cannot corrupt a running session

**Acceptance Criteria:**
- Given a `cdylib` whose `CapabilityDescriptor.abi_version` differs from the host's `ABI_VERSION` (currently 1), When the loader scans the directory, Then the library is skipped with one `WARN` naming the file, the expected version and the found version — and no symbol from it is called.
- Given a `cdylib` built for a different OS or CPU arch (an x86_64 `.so` offered to an arm64 host), When the loader attempts it, Then it is skipped with a `WARN` and no partially-initialized add-on enters the registry.
- Given a library whose `abi_version` matches but whose `abi_stable` structural layout hash does not, When the loader checks it, Then it is rejected on the hash alone — a forgotten version bump must not produce a silently miscompiled load.
- Given `[addons] abi_strict = true`, When **any** row of the load-failure taxonomy is hit (corrupt library, not an add-on, constructor error, version/layout mismatch, unknown `AddonKind`, bad `[addon_module_<id>]` config, missing add-ons dir), Then startup aborts non-zero instead of warning and continuing.
- Given the add-ons directory does not exist, When the host starts with `abi_strict = false`, Then it is treated as empty with one `WARN` and the host still starts — a host with no add-ons is a valid configuration, not a crash.
- Given a panic inside `probe()` or `construct()`, When it crosses the boundary, Then it is caught by `catch_unwind` and surfaces as `AbiErr::Unrecoverable` (code 7) — an unwind never crosses `dlopen`, and the add-on is dropped rather than retried, because a panic is deterministic and permanent.

**Validated by:** specs/core/MODULE_ABI.md — "Load-failure taxonomy", "ABI versioning", "AbiErr registry", and the Testing Strategy rows for version/layout mismatch, wrong OS/arch, `catch_unwind`, and `abi_strict`

---

## US-XP-13: Optional add-on capabilities are declared, honoured, and safe when misdeclared

**As a** host operator mixing add-ons from different sources
**I want** the host to call only what an add-on says it implements
**So that** a third-party library cannot be driven into a method it never wrote

**Acceptance Criteria:**
- Given a capture add-on whose `ProbeReport.caps` clears `SURFACE`, When the pipeline selects it, Then the zero-copy path is never attempted for that session and the software path is chosen even if a hardware encoder probed successfully.
- Given `[capture] cursor_mode = "auto"` and a capture add-on whose `caps` clears both `CURSOR` and `EMBED_CURSOR`, When the pipeline runs capture selection, Then that add-on is **skipped in the dispatch order** exactly like one whose probe reported `available = false` — the check runs during selection at startup step 3d, inside `pipeline::new()`, and is re-verified on the frame thread inside `FrameLoop::open()` against the constructed handle's authoritative `caps()`, so no capturer that can deliver no pointer at all is ever chosen; if none remains, startup fails naming each add-on it rejected and why.
- Given an add-on that sets `EMBED_CURSOR` but clears `EMBED_CURSOR_SURF`, When the resolved mode is `"embedded"`, Then it is paired with the software path — it can burn the pointer into a CPU frame but not into a GPU surface, and the pipeline must not hand it one.
- Given a capture add-on whose `caps` sets `CONFIGURABLE`, When a hot parameter change (FPS, bit depth, HDR) arrives, Then `update_stream_params` is called in place; given an add-on that clears it, Then the pipeline tears the capturer down and recreates it instead, and the session sees one IDR rather than a stall.
- Given an encoder add-on whose `caps` sets `ENC_CONFIGURABLE`, When a bitrate or resolution change arrives, Then it is applied in place with no `config` message for bitrate; given one that clears it, Then the encoder is recreated and a fresh `config` plus IDR is pushed.
- Given an input add-on that clears `SECURE_ATTENTION` (or `RUMBLE`), When the operator enables the corresponding feature, Then the host never calls `send_sas` (or installs a rumble sink), logs once that the knob is inert, and continues — an unclaimed method is never called speculatively.
- Given an add-on that sets a capability bit but returns `AbiErr::Unsupported` (code 8) from the corresponding method, When the host calls it once, Then the host logs at `error` with the add-on id and the method, clears that bit for the rest of the session, and continues on the non-optional path — it does not shut down, does not retry per frame, and does not advance the capture/encode error ladder.
- Given an add-on that sets a bit the host does not recognise, When `caps` is read, Then the unknown bit is ignored (one `WARN` naming the mask) and the add-on loads normally — forward compatibility is not a load failure.

**Validated by:** specs/core/MODULE_ABI.md — "Optional-method capability flags" (`AddonCaps`, unknown bits ignored, `AbiErr::Unsupported` for unclaimed methods), "Misbehaving add-ons"; specs/core/MODULE_PIPELINE.md — startup step 3d (cursor eligibility, inside `pipeline::new()`) and step 3g (capability recording), step 6a (records the `[capture] cursor_mode` policy and the resolved mode on the `SelectionPlan` — it constructs nothing), and `FrameLoop::open()` at step 13, which constructs the capturer, re-resolves the cursor mode against its `caps()` and builds `CursorPublisher`; specs/media/MODULE_CAPTURE.md — "Cursor delivery and the" section (the `"separate"` / `"embedded"` decision), `ConfigurableCapturer`; specs/media/MODULE_ENCODE.md — `ConfigurableEncoder`
