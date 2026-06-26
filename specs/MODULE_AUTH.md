# Module Spec: Authentication

## Overview

The Authentication module gates WebSocket connections before they're upgraded
to streams. It is a **base feature** — present on every FeatherDesk binary
regardless of which add-ons are compiled in.

Auth runs in the server layer (`MODULE_SERVER.md`) at the WebSocket upgrade
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

Server accepts every WebSocket upgrade. **For local dev or trusted-LAN
deployments only.** Logs a warning at startup so operators know auth is off.

The server prints:
```
⚠ AUTH DISABLED — all WebSocket connections accepted unauthenticated.
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
  (base64url, 43 chars).
- Token is printed to stdout once at startup:
  ```
  ✓ Auth token: G3vK9xTHRvUu3yz1BqLmPnRoSt6wYzAbCdEfGhIjKlMnO
  ```
- If `token_file` is set, token is also written to that path (mode 600).
  This is how systemd / Docker / k8s pick it up.
- Client must supply the token as a query parameter on the WebSocket
  upgrade: `wss://host:port/ws?token=<the-token>`
- Wrong/missing token → 401 Unauthorized, no WebSocket upgrade.

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

- Operator runs `viewport-rds hash-password` once to generate an argon2id
  hash:
  ```
  $ viewport-rds hash-password
  Password: ********
  Confirm:  ********
  $argon2id$v=19$m=65536,t=3,p=4$RyVKczQy...
  ```
- Hash goes in `password_hash`.
- Client POSTs to `/auth` with HTTP Basic Auth:
  `Authorization: Basic <base64(":password")>` (username field empty).
- Server verifies via constant-time argon2id comparison.
- On success, server returns `{"session_token":"...","ttl_sec":3600}`.
- Client then upgrades to WebSocket with `Authorization: Bearer <session_token>`.
- Failed attempts are rate-limited (5 attempts per IP per minute);
  exceeding triggers a 60-second IP block.
- The password is NEVER sent on the WebSocket URL -- only on the HTTPS `/auth` POST.

### Why argon2id

- Memory-hard (resistant to GPU brute-force)
- Side-channel resistant
- Stdlib support via `golang.org/x/crypto/argon2`
- Industry standard for password hashing (winner of PHC 2015)

---

## Mode: `pin`

```toml
[auth]
mode                  = "pin"
pairing_window_minutes = 5           # accept new pairings for N min after start
paired_devices_file   = "/var/lib/viewport-rds/paired.json"
session_ttl_minutes   = 60
```

### Behavior

Sunshine-style first-launch pairing:

1. Server starts. If `paired_devices_file` is empty or missing, opens a
   **pairing window** for `pairing_window_minutes`.
2. During the window, server prints a 4-digit PIN to stdout:
   ```
   ✓ PAIRING MODE — enter PIN at https://host:port/pair
     PIN: 4729  (valid for 4:58 more)
   ```
3. Client browses to `/pair`, enters the PIN.
4. Server validates the PIN, issues a permanent **device token** for that
   client, stores in `paired_devices_file`.
5. Future connections from that client use the device token (same
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
   │ wss://.../ws (cookie sent)   │
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
- We adopt an OIDC library (likely `github.com/coreos/go-oidc`)

The deferred status is documented so external integrations can plan around it.

---

## Session Tokens (Shared Across Modes)

After successful auth in any mode, the server issues an **opaque session
token** in the Config handshake:

```json
{
  "version": 1,
  "codec": "avc1.42E01E",
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
wss://host:port/ws?resume=<session_token>
```

Server-side flow:
1. Look up session_token in active session cache
2. If found AND not expired: skip auth, jump straight to "resumed" Config
   handshake + cached IDR replay (see [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md)
   resume flow)
3. If not found / expired: WebSocket close code 4401. Client falls back to
   full auth re-flow with stored credentials (token / password / device token).
   NOTE: resume happens post-WS-upgrade, so HTTP 401 is not possible here --
   always use WS close code 4401.

### Session token properties

- 32 bytes random, base64url-encoded (43 characters, no padding).
- Each WebSocket connection gets its own token from the initial auth. Multiple
  concurrent connections from the same authenticated identity are allowed
  (mirrored streams), but each has a distinct token.
- Server-side storage: in-memory only (lost on restart). Operators wanting
  durable session persistence handle that externally.
- TTL: configurable `[auth] session_ttl_minutes` (default 60). This is the
  **auth session lifetime** -- how long the token remains valid for new
  WebSocket connections. Distinct from `[reconnect] cache_ttl_seconds`
  (default 300), which is how long the server caches stream state for
  fast-resume after a disconnect.

---

## Authorization (Role Model)

Beyond authentication (who you are), authorization (what you can do) is
controlled by URL query param:

| URL | Role | Permissions |
|-----|------|------------|
| `wss://host/ws?role=control&token=...` | Controller | Send input events, request keyframes |
| `wss://host/ws?role=view&token=...` | Viewer | Receive video/audio only |
| `wss://host/ws?token=...` (no role) | Auto: first connection = controller, rest = viewer | — |

**One controller per session.** Subsequent `?role=control` connections become
viewers (the first controller keeps the slot until they disconnect; if
authenticated, they can `?role=control&takeover=true` to forcibly seize).

Per-mode role permissions can be locked down via:

```toml
[auth]
require_auth_for_view = true     # default: false (lets unauth'd viewers connect)
allow_takeover       = false     # default: true (any authed controller can take over)
```

---

## Implementation Sketch

```go
package auth

type Mode int
const (
    ModeNone Mode = iota
    ModeToken
    ModePassword
    ModePIN
    ModeOAuth
)

type Authenticator interface {
    // Authenticate validates a WebSocket upgrade request.
    // Returns the authenticated identity on success, or error with HTTP
    // status code. The SERVER creates the session token -- the Authenticator
    // only validates credentials.
    Authenticate(r *http.Request) (identity Identity, err error)
}

type Identity struct {
    UserID   string // empty for token/none modes; populated for password/OAuth
    DeviceID string // populated for PIN mode (paired device)
    Role     string // "control" | "view" | "" (auto)
}

type Session struct {
    Token       string
    Created     time.Time
    LastSeen    time.Time
    UserID      string  // empty for token/PIN modes; populated for password/OAuth
    Role        string  // "control" | "view"
    DeviceID    string  // populated for PIN mode
}

// Server's auth pipeline
type Server struct {
    auth          Authenticator
    sessions      map[string]*Session  // by session_token
    sessionMu     sync.RWMutex
    sessionTTL    time.Duration
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
    // 1. Try resume first
    if tok := r.URL.Query().Get("resume"); tok != "" {
        if sess := s.resumeSession(tok); sess != nil {
            s.upgradeAndResume(w, r, sess)
            return
        }
    }

    // 2. Fall back to full auth
    newToken, err := s.auth.Authenticate(r)
    if err != nil {
        s.respondAuthError(w, err)
        return
    }

    sess := &Session{
        Token:   newToken,
        Created: time.Now(),
        Role:    determineRole(r),
    }
    s.storeSession(sess)
    s.upgradeAndStream(w, r, sess)
}
```

---

## File Structure

```
internal/auth/
├── auth.go            // Authenticator interface, Session struct, ModeXxx constants
├── none.go            // ModeNone implementation
├── token.go           // ModeToken implementation
├── password.go        // ModePassword implementation + argon2id hashing
├── pin.go             // ModePIN implementation + /pair handler
├── oauth_stub.go      // ModeOAuth stub (build tag: oauth)
├── sessions.go        // Session cache, TTL, lookup
├── ratelimit.go       // Per-IP attempt limiting
└── auth_test.go
```

---

## Security Considerations

| Concern | Mitigation |
|---------|-----------|
| Token leakage in URL | Server logs strip `?token=` query param before logging |
| Replay attack on token | Session tokens are single-use within TTL — once expired, must re-auth |
| Brute-force password | Argon2id (slow), per-IP rate limit (5/min), 60s block on exceed |
| Brute-force PIN | 5-minute pairing window; PIN is 4 digits but window is bounded; rate limit 1 attempt/sec |
| Session fixation | Server generates session_token, never accepts client-supplied |
| CSRF on /pair | Same-origin only; rejects cross-origin POSTs |
| Cleartext over HTTP | TLS is mandatory (see [`MODULE_SERVER.md`](./MODULE_SERVER.md)) — auth is never sent in clear |

---

## Status

📋 **Specced.** Implementation pending. Token, password, and PIN modes are
in scope for v1. OAuth is deferred until requested.

Implementation order (recommended):
1. `mode = none` + `mode = token` (covers headless server deploys)
2. `mode = password` (covers single-user installs)
3. `mode = pin` (covers Sunshine-style first-launch UX)
4. `mode = oauth` (deferred until requested)
