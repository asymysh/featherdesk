# Module Spec: Authentication

## Overview

The Authentication module gates WebTransport sessions on the first control-stream
message. It is a **base feature** — present on every FeatherDesk binary
regardless of which add-ons are loaded.

Auth runs in the server layer (`MODULE_SERVER.md`) on the **first message of
the WebTransport control stream** (see [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md)
"Connection Lifecycle"). The previous WebSocket-upgrade-time check
boundary. Successful auth produces a **session token** that the client stores
for reconnection without re-authentication.

---

## Auth Modes

The `[auth] mode` config key selects one of:

| Mode | Use case | Status |
|------|----------|--------|
| `none` | Local dev, trusted LAN | ✅ supported (dev only) |
| `token` | Headless servers, automation | ✅ supported |
| `password` | Single-user installs | ✅ supported |
| `pin` | First-launch pairing (Sunshine-style) | ✅ supported |
| `oauth` | Enterprise SSO | ⏸️ deferred — interface defined, no implementations |

Mode is set at server startup; changing it requires restart.

---

## Mode: `none`

```toml
[auth]
mode = "none"
```

Server accepts every WebTransport session. **For local dev or trusted-LAN
deployments only.** Logs a warning at startup so operators know auth is off.

The server prints:
```
⚠ AUTH DISABLED — all WebTransport sessions accepted unauthenticated.
  This is intended for local development. Do NOT use in production.
```

---

## Mode: `token`

```toml
[auth]
mode               = "token"
token              = ""              # explicit token; "" = auto-generate on startup
token_file         = ""              # write generated token here for ops to find
session_ttl_minutes = 60             # successful auth lifetime before re-auth
```

### Behavior

- At startup, if `token` is empty, server generates a random 32-byte token
  via a CSPRNG (the `getrandom` crate / `OsRng`) (**CSPRNG mandatory**), base64url encoded (43 chars).
  If set explicitly, must be ≥ 32 characters.
- Token is printed to stdout once at startup:
  ```
  Auth token: G3vK9xTHRvUu3yz1BqLmPnRoSt6wYzAbCdEfGhIjKlMnO
  ```
- If `token_file` is set, token is also written to that path (mode 0600).
  This is how systemd / Docker / k8s pick it up.
- Client authenticates by sending a JSON message as the **first write on the
  WebTransport control stream** (the first bidirectional stream opened after
  the WebTransport session is established):
  ```json
  {"type":"auth","token":"<bearer>","role":"control|view|player"}
  ```
  Browsers cannot set arbitrary headers (e.g. `Authorization`) on the
  `WebTransport()` constructor; first-frame auth on the control stream is the
  standard browser-compatible pattern and works identically for native clients.
  Server reads the first message, validates, replies `{"type":"auth_ok",...}`
  or `{"type":"auth_failed",...}` + `CloseWithError(4401)`.
- **NOT** in a URL query parameter — query params leak into proxy access logs,
  Referer headers, and browser history.
- The 5-second auth timer ends in `CloseWithError(4408 CloseAuthTimeout)` if the
  client doesn't authenticate in time.
- Wrong/missing/expired token → `close::AUTH_FAILED (4401)`.

### Token rotation

Manual: edit config, send `SIGHUP`. The new token takes effect for new
connections; existing connections survive until they disconnect.

Automatic rotation is **not** in scope for v1 — operators handle this
externally (config management, secret stores).

---

## Mode: `password`

```toml
[auth]
mode           = "password"
password_hash  = ""                  # argon2id hash (required)
session_ttl_minutes = 60
```

### Behavior

- Operator runs `featherdesk hash-password` once to generate an argon2id
  hash:
  ```
  $ featherdesk hash-password
  Password: ********
  Confirm:  ********
  $argon2id$v=19$m=65536,t=3,p=4$RyVKczQy...
  ```
- Hash goes in `password_hash`.
- Client POSTs to `/auth` with HTTP Basic Auth:
  `Authorization: Basic <base64(":password")>` (username field empty).
- Server verifies via constant-time argon2id comparison.
- On success, server returns `{"session_token":"...","ttl_sec":3600}`.
- Client then opens a WebTransport session and sends the session token in
  the first message of the control stream — same first-frame-auth pattern as
  Mode `token` above. Works identically in browsers and native clients.
- Failed attempts are rate-limited (5 attempts per IP per minute);
  exceeding triggers a 60-second IP block.
- The password is NEVER sent on the WebTransport URL or stream — only on the HTTPS `/auth` POST.

### Why argon2id

- Memory-hard (resistant to GPU brute-force)
- Side-channel resistant
- Crate support via the `argon2` crate (RustCrypto)
- Industry standard for password hashing (winner of PHC 2015)

---

## Mode: `pin`

```toml
[auth]
mode                   = "pin"
pin_length             = 8           # 8-digit PIN (100M possibilities). Min 6, max 12.
pairing_window_minutes = 5           # accept new pairings for N min after start
max_pin_attempts       = 10          # GLOBAL limit per window. Exponential backoff after 3.
paired_devices_file    = "/var/lib/featherdesk/paired.json"
session_ttl_minutes    = 60
```

### Behavior

Sunshine-style first-launch pairing:

1. Server starts. If `paired_devices_file` is empty or missing, opens a
   **pairing window** for `pairing_window_minutes`.
2. During the window, server prints an N-digit PIN (default 8) to stdout:
   ```
   PAIRING MODE -- enter PIN at https://host:port/pair
     PIN: 47293816  (valid for 4:58 more)
   ```
3. Client browses to `/pair`, enters the PIN. Server issues a CSRF token
   on GET `/pair`; POST must include the token. Cookie `SameSite=Strict;
   Secure; HttpOnly`.
4. Server validates the PIN, issues a permanent **device token** for that
   client, stores in `paired_devices_file` (mode 0600).
5. Brute-force protection: `max_pin_attempts` is a **global** counter per
   pairing window. After 3 failures, exponential backoff (1s, 2s, 4s, 8s...)
   between allowed attempts. After `max_pin_attempts` total, window closes.
   An 8-digit PIN with 10 allowed attempts = 0.00001% brute-force probability.
6. Future connections from that client use the device token (same
   mechanism as `mode = "token"` but per-client).

### After the pairing window

- New devices cannot pair without operator intervention (restart with
  pairing window re-opened, or remove `paired_devices_file`).
- Already-paired devices continue working — they use their stored device
  token.

### Pairing flow

```
[client]                     [server /pair]
   │                              │
   │ GET /pair                    │
   ├──────────────────────────────>
   │ HTML form: <input name="pin"/>
   │ <─────────────────────────────
   │                              │
   │ POST /pair pin=4729          │
   ├──────────────────────────────>
   │                              │ validate PIN
   │                              │ generate device token
   │                              │ append to paired.json
   │ Set-Cookie: device_token=... │
   │ <─────────────────────────────
   │ JS redirects to /            │
   │                              │
   │ https://.../wt (control stream auth)│
   ├──────────────────────────────>
   │                              │ device token validated
   │                              │ stream begins
```

---

## Mode: `oauth` (deferred)

```toml
[auth]
mode             = "oauth"
oauth_provider   = "google" | "github" | "azure" | "okta"
oauth_client_id  = ""
oauth_client_secret = ""
oauth_redirect_url = ""
oauth_allowed_emails = []    # whitelist; empty = anyone authenticated
```

### Status

⏸️ **Deferred.** Interface and config schema are defined; no implementation
in v1. Trigger to un-defer:
- A real customer requests SSO
- We adopt an OIDC library (likely the `openidconnect` crate)

The deferred status is documented so external integrations can plan around it.

---

## Session Tokens (Shared Across Modes)

After successful auth in any mode, the server issues an **opaque session
token** in the Config handshake:

```json
{
  "version": 1,
  "codec": "avc1.42E01F",
  "width": 1920,
  "height": 1080,
  "fps": 60,
  "hdr": false,
  "color_space": "bt709",
  "audio": false,
  "audioSampleRate": 0,
  "audioChannels": 0,
  "cursorMode": "separate",
  "session_token": "Yhgz...43chars...AbCd",
  "session_ttl_sec": 3600,
  "resumed": false
}
```

(See [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) `ConfigPayload` for the full
field list. `session_ttl_sec` here is the auth session lifetime from
`[auth] session_ttl_minutes`; reconnect state caching uses the separate
`[reconnect] cache_ttl_seconds`.)

The client stores `session_token` (in-memory; not localStorage — avoid
persistent token leakage). On reconnect within `session_ttl_sec`:

```
https://host:port/wt   (WebTransport; bearer carried in control-stream first frame)
```

Server-side flow:
1. Look up session_token in active session cache
2. If found AND not expired: skip auth, send the "resumed" config message
   (`{"type":"config","resumed":true,…}`) then seed the decoder over a fresh
   bootstrap stream (see [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) resume flow)
3. If not found / expired: session closed with close::AUTH_FAILED (4401). Client falls back to
   full auth re-flow with stored credentials (token / password / device token).
   NOTE: resume happens post-WebTransport-upgrade, so an HTTP 401 is not possible
   here -- always use the QUIC application close code 4401.

### Session token properties

- 32 bytes random, base64url-encoded (43 characters, no padding).
- Each WebTransport session gets its own token from the initial auth. Multiple
  concurrent connections from the same authenticated identity are allowed
  (mirrored streams), but each has a distinct token.
- Server-side storage: in-memory only (lost on restart). Operators wanting
  durable session persistence handle that externally.
- TTL: configurable `[auth] session_ttl_minutes` (default 60). This is the
  **auth session lifetime** -- how long the token remains valid for new
  WebTransport sessions. Distinct from `[reconnect] cache_ttl_seconds`
  (default 300), which is how long the server caches stream state for
  fast-resume after a disconnect.

---

## Authorization (Role Model)

Beyond authentication (who you are), authorization (what you can do) is
controlled by the **`role` field of the control-stream auth message** — NOT a
URL query param. The `/wt` URL is identical for every client; browsers can't set
headers on the WebTransport constructor, so role travels in-band with the token.

| Auth message | Role | Permissions |
|-----|------|------------|
| `{"type":"auth","token":…,"role":"control"}` | Controller | Binary input, keyframe req, `resize`/`set_*`, clipboard C→H, file-transfer streams, gamepad slot 0 |
| `{"type":"auth","token":…,"role":"player"}` | Player (co-op) | **Gamepad only** — claims one virtual-pad slot. Honored only when `[gamepad] allow_coop`; otherwise treated as `view`. See MODULE_GAMEPAD "Co-op". |
| `{"type":"auth","token":…,"role":"view"}` | Viewer | Receive video/audio/cursor/clipboard-pushes only |
| `{"type":"auth","token":…}` (role omitted) | Auto: first connection = controller, rest = viewer | — |

**One controller per session.** Subsequent `role:"control"` connections become
viewers (the first controller keeps the slot until they disconnect; if
authenticated, they can set `"takeover":true` in the auth message to forcibly
seize, honored only when `[auth] allow_takeover = true`).

Per-mode role permissions can be locked down via:

```toml
[auth]
require_auth_for_view = true     # SECURE default (true). Set false only to allow unauth'd viewers.
allow_takeover       = false     # SECURE default (false). Set true to permit any authed controller to seize.
```

---

## Implementation Sketch

```rust
// crate: featherdesk-auth

pub enum Mode {
    None,
    Token,
    Password,
    Pin,
    OAuth,
}

pub trait Authenticator: Send + Sync {
    /// Validates the first JSON message on the WebTransport control stream. The
    /// token + role (and optional resume) fields are parsed from that message
    /// before authenticate is invoked; `req` is the original WebTransport upgrade
    /// request (carries Origin, remote_addr, etc. — useful for rate limiting and
    /// ACLs). Returns the authenticated identity on success, or an error. The
    /// SERVER creates the session token — the Authenticator only validates creds.
    fn authenticate(&self, req: &UpgradeRequest) -> Result<Identity, AuthError>;
}

pub struct Identity {
    pub user_id: String,   // empty for token/none modes; populated for password/OAuth
    pub device_id: String, // populated for PIN mode (paired device)
    pub role: String,      // "control" | "view" | "player" (gamepad co-op) | "" (auto)
}

pub struct Session {
    pub token: String,
    pub created: std::time::Instant,
    pub last_seen: std::time::Instant,
    pub user_id: String,   // empty for token/PIN modes; populated for password/OAuth
    pub role: String,      // "control" | "view" | "player" (gamepad co-op)
    pub device_id: String, // populated for PIN mode
}

// One thiserror-derived error enum for the crate.
#[derive(Debug, thiserror::Error)]
pub enum AuthError {
    #[error("bad credentials")] BadCredentials,
    #[error("expired")] Expired,
    #[error("rate limited")] RateLimited,
    /// Startup-only: the [auth] section is internally inconsistent (e.g. a
    /// token shorter than 32 chars, mode="password" with no password_hash,
    /// an unreadable token_file path). Returned by `new`, never by
    /// `authenticate` — a misconfigured server must fail to start, not accept
    /// connections it cannot correctly gate.
    #[error("auth config: {0}")] Config(String),
}

/// Builds the Authenticator for the configured mode. This is the pipeline's
/// single entry point into this crate (MODULE_PIPELINE step 7) — it selects
/// between `none.rs` / `token.rs` / `password.rs` / `pin.rs` / `oauth_stub.rs`
/// and performs the mode-specific startup work that must happen exactly once:
///   - `none`     → prints the loud "AUTH DISABLED" warning to stdout
///   - `token`    → generates the 32-byte CSPRNG token if `token = ""`, prints
///                  it once, and writes `token_file` with mode 0600 if set
///   - `password` → validates the argon2id hash is present and well-formed
///   - `pin`      → opens the pairing window and loads the paired-device store
///   - `oauth`    → returns `AuthError::Config` unless the `oauth` cargo
///                  feature is enabled (deferred)
pub fn new(cfg: &config::AuthConfig) -> Result<Box<dyn Authenticator>, AuthError>;

/// Server's auth pipeline
pub struct Server {
    auth: Box<dyn Authenticator>,
    sessions: tokio::sync::RwLock<HashMap<String, Session>>, // by session_token
    session_ttl: std::time::Duration,
}

impl Server {
    async fn handle_web_transport(&self, req: UpgradeRequest) {
        // Upgrade the WebTransport session. NO credentials are read from the URL
        // query or HTTP headers — they arrive in-band on the control stream
        // (see Security Considerations: NEVER a URL query parameter).
        let session = match self.upgrade(&req).await {
            Ok(s) => s,
            Err(_) => return,
        };

        // The client's FIRST control-stream message carries credentials in-band
        // (JSON body inside the encrypted QUIC stream): {token, role, resume?}.
        let (ctrl, msg) = match self.accept_auth_message(&session).await {
            Ok(v) => v,
            Err(_) => {
                let _ = session.close_with_error(close::PROTOCOL_ERROR, "auth handshake");
                return;
            }
        };

        // 1. Try resume first — gated by the in-band `resume` boolean flag; the
        //    session `token` is itself the resume credential (matches the
        //    {token, role, resume:bool} shape in TRANSPORT/PROTOCOL/SERVER).
        if msg.resume {
            if let Some(sess) = self.resume_session(&msg.token) {
                self.attach_resume(session, ctrl, sess).await;
                return;
            }
            // stale / invalid session token → fall through to full auth
        }

        // 2. Fall back to full auth. token + role are already parsed from `msg`;
        //    `req` is passed only for Origin / remote_addr (rate limiting, ACLs).
        let identity = match self.auth.authenticate(&req) {
            Ok(id) => id,
            Err(e) => {
                self.respond_auth_error(&ctrl, e).await;
                let _ = session.close_with_error(close::AUTH_FAILED, "auth failed");
                return;
            }
        };

        // Server creates the session token (Authenticator only validates).
        let token = generate_session_token(); // CSPRNG (getrandom) 32-byte base64url
        let sess = Session {
            token: token.clone(),
            user_id: identity.user_id,
            device_id: identity.device_id,
            created: Instant::now(),
            last_seen: Instant::now(),
            role: identity.role,
        };
        self.store_session(sess);
        self.attach_stream(session, ctrl, &token).await;
    }
}
```

---

## File Structure

```
featherdesk-auth/src/
├── lib.rs             // Authenticator trait, Identity struct, Mode enum, `new()` factory
├── none.rs            // Mode::None implementation (prints security warning)
├── token.rs           // Mode::Token implementation (bearer in control-stream msg, not URL params)
├── password.rs        // Mode::Password implementation + argon2id hashing
├── pin.rs             // Mode::Pin implementation + /pair handler + CSRF tokens
├── devices.rs         // Paired device storage, per-device revocation
├── oauth_stub.rs      // Mode::OAuth stub (cargo feature: oauth)
├── sessions.rs        // Session cache, TTL, lookup (CSPRNG tokens only)
├── ratelimit.rs       // Per-IP attempt limiting + global PIN attempt counter
└── tests.rs
```

---

## Security Considerations

| Concern | Mitigation |
|---------|-----------|
| Token leakage in URL | Token sent in the first WebTransport control-stream message (JSON body, inside the encrypted QUIC stream). NEVER as a URL query parameter or HTTP header. |
| Replay attack on token | Session tokens are scoped to TTL -- once expired, must re-auth |
| Brute-force password | Argon2id (memory-hard), per-IP rate limit (5/min), 60s block on exceed |
| Brute-force PIN | 8-digit default (100M space), global max 10 attempts per window, exponential backoff after 3 |
| Session fixation | Server generates session_token via a CSPRNG (`getrandom` / `OsRng`) (CSPRNG mandatory), never accepts client-supplied |
| CSRF on /pair | CSRF token issued on GET `/pair`, required on POST. Cookie: `SameSite=Strict; Secure; HttpOnly` |
| Cleartext over HTTP | TLS 1.3 mandatory (enforced by QUIC; weaker negotiation is impossible), AEAD ciphers only (see [`MODULE_SERVER.md`](./MODULE_SERVER.md)) |
| Token generation | **All** random tokens (auth, session, device) MUST use a CSPRNG (`getrandom` / `OsRng`). Non-cryptographic RNGs (e.g. `rand::thread_rng` for non-crypto use) are prohibited for tokens. |
| TLS key storage | Self-signed cert private key cached with mode 0600 (Unix) / restrictive ACL (Windows). Permissions verified on startup. |
| Device revocation | `DELETE /devices/{device_id}` admin endpoint (requires controller auth) revokes individual paired devices. CLI: `featherdesk revoke-device <id>`. |
| Metrics endpoint | If `metrics.bind` is not a loopback address, server prints a security warning at startup. Consider adding bearer token auth to scrape endpoint for exposed deployments. |
| Viewer-only attacks | `require_auth_for_view = true` by default. Unauthenticated viewing requires explicit opt-in. |
| Controller takeover | `allow_takeover = false` by default. When enabled, displaced controller receives QUIC application close code 4410 (`close::CONTROLLER_TAKEOVER`). |
| Origin hijacking | `allow_origin = ""` by default (same-origin only). Wildcard `"*"` requires explicit opt-in. |

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `mode = token`: correct/missing/expired/malformed-JSON first-message → `auth_ok` vs `close::AUTH_FAILED (4401)` | No |
| Unit | `mode = password`: constant-time argon2id comparison rejects a wrong password without a timing difference vs. a correct one | No |
| Unit | `mode = pin`: PIN brute-force backoff timing (1s, 2s, 4s, 8s... after the 3rd failure) and hard stop at `max_pin_attempts` | No |
| Unit | `session_token` generation and lookup use the CSPRNG path only — a test double `Authenticator` proves no code path can construct a `Session` with a non-CSPRNG token | No |
| Integration | Resume flow: valid unexpired `session_token` + `resume:true` skips auth and emits `{"resumed":true,...}`; expired/unknown token falls through to full re-auth, never a bare HTTP 401 (QUIC close 4401 instead) | No |
| Integration | `mode = pin` full pairing flow end-to-end: `GET /pair` → CSRF token issued → `POST /pair` with correct PIN → device token stored in `paired_devices_file` → subsequent connection authenticates via device token | No |
| Integration | Role assignment: first connection with role omitted becomes `control`; a second `role:"control"` connection without `takeover:true` becomes `view`; `takeover:true` only succeeds when `[auth] allow_takeover = true` | No |
| Integration | `require_auth_for_view = true` (default) rejects an unauthenticated viewer; `= false` allows it | No |
| Security | Token/PIN/password are never accepted from a URL query parameter — a request smuggling credentials in the `/wt` URL is treated as unauthenticated, not as an alternate auth path | No |
| Unit | `new()` fails startup on every inconsistent `[auth]` section — `mode="token"` with a token under 32 chars, `mode="password"` with no/malformed `password_hash`, an unwritable `token_file` path, `mode="oauth"` without the `oauth` feature — returning `AuthError::Config`. **A misconfigured server must not start**; the failure mode being guarded against is one that boots and silently under-gates. | No |
| Unit | `new()` side effects happen exactly once and only for the selected mode: `token=""` generates one CSPRNG token and writes `token_file` at mode 0600; `mode="none"` emits the warning line; no other mode touches those paths | No |

---

## Status

📋 **Specced.** Implementation pending. Token, password, and PIN modes are
in scope for v1. OAuth is deferred until requested.

Implementation order (recommended):
1. `mode = none` + `mode = token` (covers headless server deploys)
2. `mode = password` (covers single-user installs)
3. `mode = pin` (covers Sunshine-style first-launch UX)
4. `mode = oauth` (deferred until requested)
