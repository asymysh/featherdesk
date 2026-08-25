# User Stories: Connection & Authentication

QA acceptance-criteria stories covering WebTransport/QUIC session establishment,
the four supported auth modes (`none` / `token` / `password` / `pin`), session
resume, and role/controller assignment. Source specs: `specs/core/MODULE_AUTH.md`,
`specs/core/MODULE_TRANSPORT.md` ("Connection Lifecycle"), `specs/CENTRAL_SPEC.md`.

---

## US-CONN-1: Establish a WebTransport session

**As a** viewer
**I want** my browser to open a low-latency remote-desktop session over WebTransport/QUIC
**So that** I can start watching/controlling the host with a fast, modern transport instead of WebSocket

**Acceptance Criteria:**
- Given a running FeatherDesk server, When a client does `GET /` and then opens `new WebTransport("https://host:port/wt")`, Then the WebTransport handshake completes over TLS 1.3 in 1 RTT.
- Given the WebTransport session is established, When the client opens a bidirectional stream and writes the `0x00` (control) StreamType tag followed by the auth JSON line, Then the server's accept loop routes it to the control handler and reads the auth message.
- Given a client that never opens any stream after the session is accepted, When 5 seconds elapse, Then the server closes the session with `close::AUTH_TIMEOUT` (4408).
- Given a successful auth, When the server replies, Then the client receives `{"type":"auth_ok",...}` followed by a `{"type":"config",...}` line, and a bootstrap uni stream (tag `0x10`) carrying the seed IDR is opened.

**Validated by:** specs/core/MODULE_TRANSPORT.md — "Connection Lifecycle"

---

## US-CONN-2: Auth mode `none` for local development

**As a** developer-operator
**I want** the server to accept every WebTransport session unauthenticated when `[auth] mode = "none"`
**So that** I can iterate quickly on a trusted LAN/dev box without configuring credentials

**Acceptance Criteria:**
- Given `[auth] mode = "none"` in config, When the server starts, Then it prints `⚠ AUTH DISABLED — all WebTransport sessions accepted unauthenticated. This is intended for local development. Do NOT use in production.` to stdout.
- Given `mode = "none"` is active, When any client connects without credentials, Then the session is accepted.

**Validated by:** specs/core/MODULE_AUTH.md — "Mode: none"

---

## US-CONN-3: Auth mode `token` for headless/automation deployments

**As a** developer-operator
**I want** a bearer-token auth mode with an auto-generated or explicit CSPRNG token
**So that** I can secure headless servers and automation pipelines without interactive login

**Acceptance Criteria:**
- Given `[auth] mode = "token"` and `token = ""`, When the server starts, Then it generates a random 32-byte CSPRNG token (base64url, 43 chars) and prints it once to stdout.
- Given `token_file` is set, When the server starts, Then the token is also written to that path with file mode 0600.
- Given a client sends `{"type":"auth","token":"<bearer>","role":"control|view|player"}` as the first control-stream message, When the token is valid and unexpired, Then the server replies `{"type":"auth_ok",...}`; When it is wrong, missing, or expired, Then the server replies `auth_failed` and closes with `close::AUTH_FAILED` (4401).
- Given the token is set explicitly, When it is shorter than 32 characters, Then the configuration is invalid (token must be ≥ 32 characters).
- Given the token is never accepted from a URL query parameter, When a request attempts to smuggle it in the `/wt` URL, Then the session is treated as unauthenticated, not as an alternate valid auth path.

**Validated by:** specs/core/MODULE_AUTH.md — "Mode: token", "Testing Strategy" (Unit: token first-message outcomes; Security: no URL-param credentials)

---

## US-CONN-4: Auth mode `password` for single-user installs

**As a** host user
**I want** to protect my single-user FeatherDesk instance with a password
**So that** only someone who knows my password can view or control my desktop

**Acceptance Criteria:**
- Given an operator has run `featherdesk hash-password` and stored the resulting argon2id hash in `password_hash`, When a client POSTs to `/auth` with HTTP Basic Auth (`Authorization: Basic <base64(":password")>`), Then the server verifies the password via constant-time argon2id comparison.
- Given the password is correct, When verification succeeds, Then the server returns `{"session_token":"...","ttl_sec":3600}` and the client uses that token in the first control-stream message to open the WebTransport session.
- Given 5 failed attempts from the same IP within one minute, When a 6th attempt arrives, Then the server rate-limits with a 60-second IP block.
- Given the password is never sent on the WebTransport URL or stream, When credentials are exchanged, Then they only ever appear in the HTTPS `/auth` POST body.

**Validated by:** specs/core/MODULE_AUTH.md — "Mode: password", "Security Considerations" (Brute-force password)

---

## US-CONN-5: Auth mode `pin` first-launch pairing

**As a** host user
**I want** a Sunshine-style PIN pairing flow the first time I set up FeatherDesk
**So that** I can pair a new viewing device without pre-sharing a password or token

**Acceptance Criteria:**
- Given `paired_devices_file` is empty or missing at startup, When the server starts, Then it opens a pairing window for `pairing_window_minutes` (default 5) and prints an N-digit PIN (default 8) to stdout.
- Given a client browses to `/pair` during the pairing window and submits the correct PIN via `POST /pair` (with the CSRF token issued on `GET /pair`), When the PIN validates, Then the server issues a permanent device token, stores it in `paired_devices_file` (mode 0600), and the client can subsequently connect using that token.
- Given 3 consecutive PIN failures within the pairing window, When further attempts are made, Then the server applies exponential backoff (1s, 2s, 4s, 8s, ...) between allowed attempts, and the window closes after `max_pin_attempts` total failures.
- Given the pairing window has closed, When a new (unpaired) device attempts to pair, Then pairing fails and requires operator intervention (restart with a re-opened window, or removing `paired_devices_file`); already-paired devices continue to work via their stored device token.

**Validated by:** specs/core/MODULE_AUTH.md — "Mode: pin", "Testing Strategy" (Unit: PIN brute-force backoff; Integration: full pairing flow)

---

## US-CONN-6: OAuth is deferred, not silently broken

**As a** developer-operator
**I want** a clear signal that `mode = "oauth"` is not yet implemented, rather than a confusing failure
**So that** I don't waste time debugging enterprise SSO that was never built

**Acceptance Criteria:**
- Given the config schema defines `oauth_provider`, `oauth_client_id`, `oauth_client_secret`, `oauth_redirect_url`, and `oauth_allowed_emails`, When an operator sets `[auth] mode = "oauth"`, Then the interface exists but no implementation ships in v1 (⏸️ deferred).
- Given OAuth is deferred, When documentation is consulted, Then it states the de-fer triggers explicitly: a real customer requesting SSO, or adoption of an OIDC library (`openidconnect` crate).

**Validated by:** specs/core/MODULE_AUTH.md — "Mode: oauth (deferred)"

---

## US-CONN-7: Resume a session with a valid, unexpired token

**As a** viewer
**I want** a brief network blip to reconnect me without a full re-authentication
**So that** my session continues seamlessly instead of forcing me to re-enter credentials

**Acceptance Criteria:**
- Given a client previously authenticated and holds an in-memory `session_token`, When it opens a new WebTransport session and sends `{"type":"auth","token":"<session_token>","role":"control","resume":true}` on the control stream, Then the server looks up the token in the session cache.
- Given the token is found and not expired, When resume proceeds, Then the server skips full auth, replies with `{"type":"config","resumed":true,...}`, and seeds the decoder over a fresh bootstrap stream.
- Given the resumed config is sent, When the client receives it, Then no `last_video_seq` hint is needed or sent — the bootstrap stream always seeds a fresh decodable keyframe.

**Validated by:** specs/core/MODULE_AUTH.md — "Session Tokens (Shared Across Modes)"; specs/core/MODULE_TRANSPORT.md — "Connection Lifecycle" → "Resume"

---

## US-CONN-8: Expired or unknown resume token falls back to full auth (never a bare HTTP 401)

**As a** developer-operator
**I want** a stale or invalid resume token to fail cleanly via the QUIC application close code, not an ambiguous HTTP error
**So that** client implementations have one well-defined failure path to handle after the WebTransport upgrade

**Acceptance Criteria:**
- Given a client sends `resume:true` with a token not present in the session cache (or past its TTL), When the server processes it, Then the session is closed with `close::AUTH_FAILED` (4401) — never a bare HTTP 401, since resume happens post-WebTransport-upgrade.
- Given the resume attempt failed, When the client handles the close, Then it falls back to the full auth re-flow using its stored credentials (token / password / device token).

**Validated by:** specs/core/MODULE_AUTH.md — "Session Tokens" ("Server-side flow"), "Testing Strategy" (Integration: resume flow)

---

## US-CONN-9: Automatic role assignment for connecting clients

**As a** viewer
**I want** the first person to connect to automatically become the controller and everyone after to be a viewer
**So that** control access is unambiguous without requiring every client to explicitly request a role

**Acceptance Criteria:**
- Given no client is currently the controller, When a client sends an auth message with `role` omitted (or first connects), Then it is assigned the `control` role.
- Given a controller is already assigned, When a second client authenticates with `role` omitted or `role:"view"`, Then it is assigned the `view` role (receives video/audio/cursor/clipboard-pushes only, no input).
- Given a client authenticates with `role:"player"`, When `[gamepad] allow_coop` is enabled, Then it claims one virtual-pad slot (gamepad-only); When `allow_coop` is disabled, Then it is treated as `view`.

**Validated by:** specs/core/MODULE_AUTH.md — "Authorization (Role Model)", "Testing Strategy" (Integration: role assignment)

---

## US-CONN-10: Controller takeover only when explicitly enabled

**As a** host user
**I want** a second authenticated user to be able to forcibly seize the controller slot only when I've opted in to allow it
**So that** control of my desktop cannot be hijacked by default

**Acceptance Criteria:**
- Given `[auth] allow_takeover = false` (the secure default), When a second `role:"control"` client sends `"takeover":true`, Then the takeover is rejected and the client remains a viewer.
- Given `[auth] allow_takeover = true`, When an authenticated client sends `role:"control","takeover":true`, Then it forcibly seizes the controller slot and the displaced controller's session receives QUIC application close code 4410 (`close::CONTROLLER_TAKEOVER`).
- Given a controller disconnects normally (no takeover), When the next `role:"control"` connection with `takeover` unset arrives, Then it may become the new controller since the slot is free.

**Validated by:** specs/core/MODULE_AUTH.md — "Authorization (Role Model)", "Security Considerations" (Controller takeover)

---

## US-CONN-11: Unauthenticated viewers are rejected by default

**As a** host user
**I want** viewing my screen to require authentication unless I explicitly opt out
**So that** I don't accidentally expose my desktop to anonymous viewers

**Acceptance Criteria:**
- Given the secure default `[auth] require_auth_for_view = true`, When an unauthenticated client attempts to connect with `role:"view"`, Then the connection is rejected.
- Given an operator explicitly sets `require_auth_for_view = false`, When an unauthenticated client connects as a viewer, Then it is allowed.

**Validated by:** specs/core/MODULE_AUTH.md — "Authorization (Role Model)", "Testing Strategy" (Integration: `require_auth_for_view`)
