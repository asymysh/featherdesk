# Module Spec: Authentication

## Overview

The Authentication module gates WebTransport sessions on the first control-stream
message. It is a **base feature** — present on every FeatherDesk binary
regardless of which add-ons are loaded.

Auth runs in the server layer (`MODULE_SERVER.md`) on the **first message of
the control stream** (see [`MODULE_TRANSPORT.md`](./MODULE_TRANSPORT.md)
"Connection Lifecycle"), identically on both carriers — the WebTransport session
and the WebSocket fallback carry the same auth message, in the same place, and
neither reads a credential from the URL or an HTTP header. After successful auth
the SERVER mints a **session token** that the client stores for reconnection
without re-authentication — a different token, in a different store, from the
credential this crate validates (see "Two token kinds").

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

`mode` is re-read on `SIGHUP` like every other `[auth]` key (MODULE_CONFIG "Hot reload
behavior"): the `fd-config` task builds a replacement Authenticator for the reloaded
section with `auth::reload`. Because `reload` performs none of `new`'s one-time startup
work, a mode change gates new connections without generating a token, rewriting
`token_file` or re-opening the PIN pairing window — so switching INTO `pin` still needs a
restart to get a pairing window.

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
token              = ""              # explicit token; "" = read token_file, else auto-generate
                                     # a 32-byte CSPRNG token at startup. If set, must be
                                     # >= 32 chars.
token_file         = ""              # supply a token here, or receive the generated one (mode 0600)
session_ttl_minutes = 60             # successful auth lifetime before re-auth
```

### Behavior

- **Token source, in order.** (1) `token` non-empty → that is the token. (2) `token`
  empty and `token_file` names an existing file with non-empty contents → the token is
  the file's first line, trimmed of trailing whitespace (this is how systemd / Docker /
  k8s **supply** one). (3) `token` empty and `token_file` absent or empty → the server
  generates 32 bytes from `OsRng` (**CSPRNG mandatory**), encodes base64url unpadded
  (43 chars), prints it once, and — if `token_file` is set — writes it there with mode
  0600 (Unix) / owner-only ACL (Windows). Case (3) generates **exactly once per process**;
  a SIGHUP reload never re-generates (see "Token rotation and device revocation").
- **Length.** A token from source (1) or (2) must be ≥ 32 characters, else
  `AuthError::Config` at startup.
- **Comparison is constant-time.** The presented bearer is compared to the expected token
  with `subtle::ConstantTimeEq` over the raw bytes, after a length check that is itself
  performed on the constant-time path (compare `SHA-256(presented)` to
  `SHA-256(expected)` with `ConstantTimeEq` so unequal lengths do not short-circuit).
  `==` on `String`/`[u8]` is prohibited for any credential.
- The token is printed to stdout once at startup:
  ```
  Auth token: G3vK9xTHRvUu3yz1BqLmPnRoSt6wYzAbCdEfGhIjKlMnO
  ```
- Client authenticates by sending a JSON message as the **first write on the
  control stream** (the first bidirectional stream opened after the session is
  established):
  ```json
  {"type":"auth","token":"<bearer>","role":"control|view|player",
   "resume":false,"takeover":false,
   "decode":{"h264":true,"h264_422":false,"h264_444":false,"hevc":false,"hevc10":false}}
  ```
  `resume` and `takeover` both default to `false`; `role` defaults to `control`
  (then arbitration demotes it if the slot is taken). `decode` is optional and is
  **not a security input** — a client that lies about what it can decode only
  harms itself, and the field is never consulted for authorization (see
  MODULE_PROTOCOL "Decode capability").
  Browsers cannot set arbitrary headers (e.g. `Authorization`) on the
  `WebTransport()` constructor; first-frame auth on the control stream is the
  standard browser-compatible pattern and works identically for native clients
  and on the WebSocket fallback carrier.
  Server reads the first message, validates, replies `{"type":"auth_ok",...}`
  or `{"type":"auth_failed",...}` + `CloseWithError(4401)`.
- **NOT** in a URL query parameter — query params leak into proxy access logs,
  Referer headers, and browser history.
- The `[transport] auth_deadline` timer (5 s) ends in
  `CloseWithError(4408 CloseAuthTimeout)` if the client doesn't authenticate in time.
- Wrong/missing/expired token → `close::AUTH_FAILED (4401)`, unless
  `require_auth_for_view = false`, in which case the session is admitted with
  `max_role = View` and can never be promoted (see "Identity ceilings per mode").

### Token rotation and device revocation

Both are the same operation: edit the file the authenticator reads, then send `SIGHUP`
(Windows: service control code 128). `config::watch` publishes the reloaded section, and
the pipeline's `fd-config` task (`pipeline::config_applier`, MODULE_PIPELINE step 12b) —
one of the two receiving halves of that watch — applies it as one call:

```rust
Server::set_auth_policy(auth::reload(&cfg.auth)?, cfg.auth.allow_takeover)
```

`auth::reload` builds a replacement Authenticator without repeating any of `new`'s
one-time startup work; `set_auth_policy` swaps it in, refreshes the one `[auth]` policy
gate the server itself evaluates (`allow_takeover`), and clears the session cache
internally — `SessionCache::clear` is Server-private on `Config.session_cache`, which has
no accessor, so no applier calls it and there is no separate call site anywhere
(MODULE_SERVER):

| Re-read on SIGHUP | Effect |
|---|---|
| `[auth] token` (or the contents of `token_file`) | New connections and new `POST /auth` requests must present the new token. **Existing sessions survive** and are not re-validated, but they cannot **resume** afterwards: the session cache is cleared, so a resume token minted under a leaked credential does not outlive the rotation. To cut live sessions off immediately, also `POST /logout` each session token, or restart. |
| the contents of `paired_devices_file` | A device removed from the file can no longer authenticate. With `[reconnect] require_same_auth = true` (the default) its cached sessions cannot resume either. |

Nothing else in `[auth]` changes behaviour under a live session set without a restart's
scrutiny: `mode`, `password_hash`, the `pin_*` keys, `session_ttl_minutes`,
`require_auth_for_view` and `allow_takeover` are re-read by the same applier —
`allow_takeover` as the second argument of `set_auth_policy`, `require_auth_for_view`
inside the replacement Authenticator (never as an applier argument, which would give one
gate two evaluation sites) — and a reload that fails validation is rejected whole, with
the previous config still in effect (MODULE_CONFIG "Hot reload behavior").

**`token = ""` does not re-generate on reload.** A generated token is minted once per
process (see "Token source, in order"). A SIGHUP with `token` still empty keeps the
existing token; the naive "call `auth::new` again" implementation would mint a fresh one
on every unrelated config reload and lock out every operator who had the old one.

The retained value lives in a crate-level `OnceLock<String>`, written by `auth::new` when
it generates a token and read by `auth::reload` when the reloaded `token` is empty.
`reload` takes no handle to the running Authenticator: there is no `retained_token`
accessor on the trait, no downcast and no `Any` supertrait — the `OnceLock` is the whole
mechanism.

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
- Client POSTs the password to `/auth` (JSON body — see "The `/auth` endpoint" below).
- Server verifies with `argon2::Argon2::new(Algorithm::Argon2id, Version::V0x13,
  Params::new(65536, 3, 4, None))` — **m = 65536 KiB (64 MiB), t = 3, p = 4**, 32-byte
  output — via `PasswordVerifier::verify_password`, which is constant-time in the hash
  comparison. The parameters are read from the stored PHC string, so an operator who
  generated a hash with different parameters still verifies; `featherdesk hash-password`
  always emits the parameters above. A `password_hash` that is not a well-formed PHC
  `$argon2id$` string is `AuthError::Config` at startup.
- On success the server issues a session token and returns
  `{"session_token":"…","ttl_sec":…,"role_ceiling":"control"}`.
- Client then opens a session and sends the session token in the first message of the
  control stream — same first-frame-auth pattern as Mode `token` above. Works
  identically in browsers and native clients.
- **Rate limiting (connection-independent).** Three counters, all process-global and all
  keyed on the credential rather than on the peer, so a fresh QUIC connection, a fresh
  TCP connection, or a rotated source IP resets none of them:
  1. **Per-identity:** `password` mode has exactly one identity. After **5 consecutive**
     failures the endpoint sleeps `min(2^(n-5), 60)` seconds before *answering* the next
     attempt (1 s, 2 s, 4 s, …, capped at 60 s). The delay is applied before the reply is
     written, so it cannot be sidestepped by abandoning the connection.
  2. **Global failure bucket:** a token bucket of 30 failed `/auth` attempts per minute
     across all peers. When empty, `/auth` answers `429` with `Retry-After: 60` without
     evaluating the KDF (this also bounds the 64 MiB × 4-lane memory cost of the KDF as
     a DoS vector).
  3. **Per-IP:** 5 attempts per source IP per minute; on exceed, that IP is refused for
     60 s. Advisory only — (1) and (2) are the load-bearing limits.
  A **successful** authentication resets (1) and refills (3) for that IP; (2) is never
  reset early.
- The password is NEVER sent on the session URL or on any stream — only in the `/auth`
  request body over TLS.

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
5. **Brute-force protection.** The PIN is `pin_length` digits (default 8 → 10^8 = 100 M
   values; min 6 → 1 M, max 12 → 10^12) generated from `OsRng` by rejection sampling
   over the full decimal range — **never** `rand % 10^n` on a non-CSPRNG source, and
   never a timestamp or counter. `max_pin_attempts` (default 10) is a single
   **process-global counter for the whole pairing window**: it is not per-IP, not
   per-connection, and not per-session, so opening a fresh QUIC or TCP connection for
   each guess consumes attempts at exactly the same rate as reusing one. After the 3rd
   failure the server delays its *response* by 1 s, 2 s, 4 s, 8 s … (doubling, capped at
   60 s) before answering; the delay is applied before the reply is written, so
   abandoning the connection does not skip it. After `max_pin_attempts` total failures
   the window closes immediately and no further pairing is possible without operator
   action. The PIN is compared with `subtle::ConstantTimeEq`. With the default 8 digits
   and 10 attempts the brute-force success probability is 10 / 10^8 = 0.00001 %.
6. Future connections from that client use the device token (same
   mechanism as `mode = "token"` but per-client).

### After the pairing window

- `Authenticator::handle_pair` answers `404` to both verbs, so the endpoint's existence
  does not advertise the window — the same answer it gives outside `mode = "pin"`.
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
  "type": "config",
  "version": 1,
  "codec": "avc1.64002A",
  "width": 1920,
  "height": 1080,
  "fps": 60,
  "hdr": false,
  "color_space": "bt709",
  "chroma": "420",
  "audio": false,
  "audioCodec": "",
  "audioSampleRate": 0,
  "audioChannels": 0,
  "audioLayout": "",
  "audioDescription": "",
  "cursorMode": "separate",
  "clipboard": "bidirectional",
  "fileStreamBudget": 12,
  "carrier": "webtransport",
  "session_token": "Yhgz...43chars...AbCd",
  "session_ttl_sec": 3600,
  "resumed": false
}
```

(See [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) `ConfigBase` / `ConfigPayload` for the
full field list. The pipeline supplies only the `ConfigBase` half; `carrier`,
`session_token`, `session_ttl_sec` and `resumed` are **stamped by the server, per
recipient**, on every `config` message, because they describe one session and the
pipeline holds no session state. `session_ttl_sec` is the remaining life of THIS
session's token, recomputed per recipient — see "Session token properties".)

The client stores `session_token` (in-memory; not localStorage — avoid
persistent token leakage) and replaces it on every `config` message. On reconnect
within `session_ttl_sec`:

```
https://host:port/wt   (bearer carried in the control-stream first frame,
                        with "resume":true; never in the URL)
```

Server-side flow (the authoritative sequence is MODULE_SERVER "WebTransport Session
Lifecycle" steps 8-13; this is the auth-crate view of it):
1. Key the presented token: `key = hex(SHA-256(token))`.
2. `SessionCache::take(key)` — a CONSUMING read. A miss, an entry past
   `absolute_expiry`, an entry past `idle_expiry`, or (with `[reconnect]
   require_same_auth`) an entry whose paired device has since been revoked all fall
   through to full auth.
3. On a hit the server re-runs admission control and controller-slot arbitration with
   the CACHED role as the ceiling — resume is not a shortcut past them — mints a FRESH
   token, and sends `{"type":"config","resumed":true,…}` carrying it, then seeds the
   decoder over a fresh bootstrap stream (see
   [`MODULE_PROTOCOL.md`](./MODULE_PROTOCOL.md) resume flow). The client replaces its
   cached token with the new one on every `config` message.
4. On fall-through the client re-authenticates with its stored credential. If that also
   fails the session is closed with `close::AUTH_FAILED (4401)`.
   NOTE: resume happens post-upgrade, so an HTTP 401 is not possible
   here — always use the QUIC application close code 4401.

### Two token kinds

**Two token kinds, two stores, and they never mix.**

1. A **credential token** is minted by `POST /auth` (and, as a permanent device token, by
   `POST /pair`). It lives in the Authenticator's own credential store, keyed
   `hex(SHA-256(token))`, expiring at `[auth] session_ttl_minutes`. It is what
   `authenticate()` validates. It is presented exactly once, on the control stream, with
   `"resume": false`. It is NEVER written to `server::SessionCache`.
2. A **session token** is minted by the SERVER at lifecycle step 12, after arbitration. It
   lives in `server::SessionCache` (`featherdesk-host::server`), is delivered only in
   `config.session_token`, and is the ONLY token accepted with `"resume": true`. It is
   single-use: resume consumes the entry and mints a replacement.

A client therefore presents its `POST /auth` token with `resume:false` and its most recent
`config.session_token` with `resume:true`, and never the other way round. `POST /logout`
revokes a session token (kind 2).

Neither wire field is renamed: the two never appear in the same JSON object, and the
`resume` flag is the discriminator. Everything in "Session token properties" below
describes kind 2 unless it says otherwise.

### Session token properties

- **Generation.** 32 bytes from the OS CSPRNG (`getrandom::getrandom` / `rand::rngs::OsRng`
  — **CSPRNG mandatory**, `thread_rng` and any userspace PRNG are prohibited), encoded
  base64url **without padding** → exactly 43 ASCII characters. 256 bits of entropy.
- **Opaque.** The token carries no structure: no role, no user id, no expiry, no
  signature, no HMAC. Everything about a session is looked up **server-side** by the
  token, which is what makes demotion, revocation and rotation effective the instant
  they are written. A client-supplied `role` is never trusted on any path.
- **Storage.** The session store is keyed by `hex(SHA-256(token))`, never by the token
  itself, so neither a heap dump nor a log of the key set yields a usable credential.
  Lookup is a map hit on that digest — there is no secret comparison on this path, and
  therefore no timing side channel. The token value itself is never written to disk or
  to any log (log the first 6 characters of the digest when a session must be
  identified in a log line).
- **One live holder.** A token is bound to at most one live session. Resume consumes it
  (`SessionCache::take`) and mints a fresh one, so two connections can never present the
  same token concurrently — see MODULE_SERVER "WebTransport Session Lifecycle" step 8. A
  key consumed by resume is never re-created, not even by the closing session that minted
  it: both write-if-present sites (`SessionCache::update_if_present`, at lifecycle steps
  11 and 21) return `false` on a consumed key and the session's state is discarded.
- **Lifetime (absolute, not sliding).** `[auth] session_ttl_minutes` (default 60) is the
  **absolute** lifetime measured from the successful full authentication that created
  the session. It is never extended by activity, by reconnection, or by resume. A
  resumed session inherits the ORIGINAL `created_at` and the original absolute expiry.
  `[reconnect] cache_ttl_seconds` (default 300) is a second, **idle** bound: how long a
  disconnected session's state survives before eviction. Resume succeeds only if BOTH
  hold. Because the two are independent, the effective remaining life of a token is
  `min(absolute_expiry, idle_expiry) - now`, and that is the value sent on the wire as
  `session_ttl_sec` — recomputed per recipient on every `config` message, never a
  constant 3600.
- **Multiple sessions per identity.** Concurrent connections from one authenticated
  identity are allowed (mirrored views); each gets its own token, its own absolute
  expiry, and its own arbitrated role.
- **Revocation.** `POST /logout` with `{"session_token":"…"}` deletes the entry and, if a
  live session holds it, closes that session with `close::AUTH_FAILED`. In `pin` mode,
  revoking a device additionally invalidates every cached session whose `device_id`
  matches, when `[reconnect] require_same_auth = true`.
- **Server-side storage is in-memory only** (lost on restart). Operators wanting durable
  session persistence handle that externally.

---

## The `/auth` endpoint

`POST /auth` is the **only** way a mode credential (password, `[auth] token`, device
token) is exchanged for a **bearer the control stream will accept**. It is available in
every mode except `none`, and it is the only credential entry point the web client has —
no credential is ever read from a URL, a fragment, a query parameter, or an HTTP header
(see "Security Considerations").

The bearer it returns is a **credential token** (kind 1 of "Two token kinds"): it lives in
this Authenticator's own credential store, keyed `hex(SHA-256(bearer))`, and it is NEVER
written to `server::SessionCache`. It is presented exactly once, with `"resume": false`.
The resume token is a different value the SERVER mints at lifecycle step 12.

**Request** — `Content-Type: application/json`:

```json
{"mode": "password", "password": "hunter2"}
{"mode": "token",    "token": "G3vK9xTHRvUu3yz1BqLmPnRoSt6wYzAbCdEfGhIjKlMnO"}
{"mode": "device",   "device_token": "…"}
```

`mode` must equal the server's configured `[auth] mode`, except `"device"`, which is valid
only when the configured mode is `pin`. Exactly one credential field may be present. A
body over 4096 bytes, malformed JSON, an unknown field, or a `mode` mismatch is `400` with
`{"error":"bad_request"}` — never a hint about which part was wrong.

**Response** — `200`, `Content-Type: application/json`, `Cache-Control: no-store`:

```json
{"session_token": "Yhgz…43chars…AbCd", "ttl_sec": 3600, "role_ceiling": "control"}
```

The `session_token` field name is the wire spelling and is not renamed; the value in it is
a credential token, not a `server::SessionCache` entry. `ttl_sec` is
`min(absolute_expiry, idle_expiry) − now` in whole seconds at the moment of issue (see
"Session token properties"). `role_ceiling` tells the SPA which UI to offer; it is
advisory — the effective role is settled at control-stream auth and reported in
`auth_ok`.

**Errors.** `401 {"error":"bad_credentials"}` for every credential failure — the same body
and the same timing for "wrong password", "unknown token" and "revoked device", so the
endpoint does not enumerate. `429 {"error":"rate_limited"}` with `Retry-After` when a
limiter from "Mode: `password`" fires. `503 {"error":"auth_disabled"}` when
`[auth] mode = "none"` (there is nothing to exchange). No other status is returned.

**Handler ownership.** The handler is `Authenticator::handle_auth` — a METHOD on the
Authenticator, not a free function, because the bearer it mints is validated later by this
same object's `authenticate` and lives in this same object's credential store, so an
`[auth]` reload rotates both at once. `featherdesk-host::server` routes `/auth` to it and
contributes the route and the `bytes::Bytes` → body conversion, and nothing else (see
`MODULE_SERVER.md` "HTTP Endpoints" and R-SRV-01). `/pair` is the same shape:
`Authenticator::handle_pair`, in `pin.rs`.

---

## Authorization (Role Model)

Beyond authentication (who you are), authorization (what you can do) is
controlled by the **`role` field of the control-stream auth message** — NOT a
URL query param. The `/wt` URL is identical for every client; browsers can't set
headers on the WebTransport constructor, so role travels in-band with the token.

The `role` field is a **request**. The server computes the effective role as
`min(requested, identity.max_role)` (see "Identity ceilings per mode"), applies the
`allow_coop` clamp, arbitrates the controller slot, and echoes the result in `auth_ok`.
A client is never granted a role it asked for; it is told the role it received.

| Requested role | Effective role when granted | What that role may do |
|---|---|---|
| `"control"` | `Control` — the first `control` connection wins the slot; a later one without an honoured `takeover` is arbitrated to `View` | Everything a viewer may do, **plus** binary keyboard/mouse/touch input, gamepad records for slot 0 and for every other slot no player owns, clipboard C→H and H→C, file-transfer streams, and `resize`/`set_bitrate`/`set_fps`/`set_hdr` |
| `"player"` | `Player` when `[gamepad] allow_coop`, else `View` (reported in `auth_ok.role`) | Everything a viewer may do, **plus** gamepad records on the input stream for one virtual-pad slot (1…`max_controllers-1`). No keyboard/mouse/touch, no clipboard, no file transfer, no parameter changes. See MODULE_GAMEPAD "Co-op". |
| `"view"` | `View` | Receive video / audio / cursor. Send `keyframe`, `pong`, `stats`, `decode_unsupported` — **loss recovery and capability negotiation are available to every authenticated role.** A viewer that cannot request an IDR can never recover from a lost fragment, so these are bounded by the per-session rate limiter in MODULE_SERVER "Keyframe-Request Rate Limiting", not by role. **Never** receives host clipboard pushes. |
| omitted | parsed as `"control"`, then arbitrated as above | — |

This table describes capabilities. The **enforcement points** are the rows of
[`MODULE_SERVER.md`](./MODULE_SERVER.md) "Role gate table", which is normative; if the
two ever disagree, that table wins.

### Effective role

The server computes it, and only the server:

```
requested = if resumed { the cached role } else { the auth message's role }
effective = min(requested, identity.max_role)      // the ceiling clamp
if effective == Role::Player && ![gamepad] allow_coop { effective = Role::View }
```

The clamp is a total function because `Role` is ordered by privilege
(`View < Player < Control`, MODULE_PROTOCOL), so there is no pair of roles for which
"the lesser of the two" is undefined. That ordering is a statement about ROLES, not about
instances: where a gate turns on which gamepad slot a session OWNS, that is instance
state, not a privilege a lower role holds and a higher one does not — no rule anywhere
grants `Player` an operation `Control` is refused. It runs at lifecycle step 10, before the
controller slot is arbitrated at step 11, and it runs identically on the fresh-auth
and resume paths. A client-supplied role is a request, never a grant; the result is
reported back in `auth_ok.role`.

**One controller per session.** Subsequent `role:"control"` connections are arbitrated to
`View` (the first controller keeps the slot until it disconnects). An **authenticated**
client may set `"takeover":true` in the auth message to seize the slot, honored only when
`[auth] allow_takeover = true`; the displaced session's cached role is rewritten to
`Role::View` with `SessionCache::update_if_present(key, Role::View, None)` (so its own
auto-reconnect resumes as a viewer; a no-op if that key has already been consumed), its
held input is released, and it is closed with `close::CONTROLLER_TAKEOVER (4410)`. The downgraded client is told: `auth_ok.role` is `view` while
`auth_ok.requested_role` is `control`, and `auth_ok.takeover_allowed` reflects
`[auth] allow_takeover`.

Per-mode role permissions can be locked down via:

```toml
[auth]
require_auth_for_view = true     # SECURE default (true). Set false only to allow unauth'd viewers.
allow_takeover        = false    # SECURE default (false). Set true to permit any authed controller to seize.
```

**`require_auth_for_view = false` admits, it never promotes.** When no valid credential is
presented and this key is `false`, `authenticate` returns
`Identity { max_role: Role::View, authenticated: false, .. }`. Because the effective role
is `min(requested_role, max_role)` (see "Identity ceilings per mode"), an anonymous client
that asks for `"role":"control"`, asks for `"role":"player"`, or omits `role` entirely is
admitted as a **viewer** in all three cases. An anonymous session is never eligible for the
controller slot, the input stream (`0x01`), the clipboard stream (`0x02`), a file-transfer
stream (`0x03`), `resize`/`set_*`, or `"takeover":true` — and `takeover` is additionally
gated on `identity.authenticated`, so it can never be honoured on this path even if
`allow_takeover = true`.

---

## Implementation Sketch

```rust
// crate: featherdesk-auth
use featherdesk_protocol::Role;   // the ONE role definition (MODULE_PROTOCOL)

pub enum Mode {
    None,
    Token,
    Password,
    Pin,
    OAuth,
}

/// The parsed first control-stream message. Constructed by the server after it reads
/// and size-checks the line; never constructed by an Authenticator implementation.
pub struct AuthRequest<'a> {
    pub token: &'a str,       // "" when the client sent no token
    pub requested_role: Role, // absent `role` parses as Role::Control (see MODULE_PROTOCOL)
    pub takeover: bool,       // default false
    pub resume: bool,         // default false
}

/// Non-credential connection facts the authenticator may use for rate limiting and
/// ACLs. It is what the server knows about the peer BEFORE any credential is read,
/// and it is never itself a credential.
pub struct PeerInfo {
    pub remote_addr: std::net::SocketAddr,
    pub origin: Option<String>, // the validated Origin header, if any (R-SRV-06)
}

pub trait Authenticator: Send + Sync {
    /// Validates the credential carried in the first JSON message on the control
    /// stream. `peer` carries Origin / remote_addr for rate limiting and ACLs
    /// and MUST NOT be treated as a credential.
    ///
    /// Returns the authenticated identity, whose `max_role` is a CEILING, not a grant:
    /// the SERVER computes the effective role as `min(req.requested_role, max_role)`
    /// and then arbitrates the controller slot (MODULE_SERVER step 11). An
    /// implementation MUST NOT inspect `req.requested_role` to decide success/failure,
    /// and MUST NOT return a `max_role` above what the credential proves.
    ///
    /// The SERVER creates the SESSION token — the Authenticator neither issues nor
    /// inspects a `server::SessionCache` entry, and `SessionCache` never holds a
    /// credential this trait minted (see "Two token kinds"). `req.resume` is
    /// informational (a rate-limiting exemption); the resume path is adjudicated by the
    /// server (MODULE_SERVER step 8).
    fn authenticate(&self, req: &AuthRequest<'_>, peer: &PeerInfo) -> Result<Identity, AuthError>;

    /// Serves `POST /auth` — the ONLY place a mode credential (password, `[auth] token`,
    /// device token) is exchanged for a bearer the control stream will accept. It is a
    /// method on the Authenticator, not a free function, because the bearer it mints is
    /// validated later by this same object's `authenticate` and lives in this same
    /// object's credential store, so an `[auth]` reload rotates both at once.
    ///
    /// `req` is the already-size-checked request (the router caps the body at 4096
    /// bytes); `peer` is the same non-credential `PeerInfo` `authenticate` receives, for
    /// rate limiting. The implementation returns the complete response — `200
    /// {"session_token","ttl_sec","role_ceiling"}`, `400 {"error":"bad_request"}`,
    /// `401 {"error":"bad_credentials"}`, `429 {"error":"rate_limited"}` with
    /// `Retry-After`, or `503 {"error":"auth_disabled"}` in `mode = "none"` — so the
    /// mode's limiters and its constant-time answering stay inside one implementation
    /// (see "The `/auth` endpoint"). `featherdesk-host::server` contributes the route and
    /// the `bytes::Bytes` -> body conversion, and nothing else.
    ///
    /// The bearer returned in the `session_token` field is a CREDENTIAL token: it lives
    /// in THIS Authenticator's credential store, keyed `hex(SHA-256(bearer))`, and it is
    /// NEVER written to `server::SessionCache`. It is presented exactly once, with
    /// `"resume": false`. The resume token is a different value the SERVER mints at
    /// lifecycle step 12 (see "Two token kinds").
    fn handle_auth(&self, req: &http::Request<bytes::Bytes>, peer: &PeerInfo)
      -> http::Response<bytes::Bytes>;

    /// Serves BOTH verbs of `/pair`, in `mode = "pin"` only.
    ///   - `GET`  → the pairing form, plus the CSRF token it mints
    ///     (`Set-Cookie: SameSite=Strict; Secure; HttpOnly`). This is where the CSRF
    ///     token comes from; without it `POST` has nothing to check.
    ///   - `POST` → validates the CSRF cookie, then the PIN, consuming one
    ///     `[auth] max_pin_attempts` slot from the process-global window counter; on
    ///     success mints a permanent DEVICE token, appends it to `paired_devices_file`
    ///     (mode 0600) and returns it as `Set-Cookie: device_token=…`.
    /// `404` for every request outside `mode = "pin"` or outside the pairing window, so
    /// the endpoint's existence does not advertise the window. Like `handle_auth`, the
    /// device token is a CREDENTIAL token in this Authenticator's own store, never a
    /// `server::SessionCache` entry.
    fn handle_pair(&self, req: &http::Request<bytes::Bytes>, peer: &PeerInfo)
      -> http::Response<bytes::Bytes>;
}

pub struct Identity {
    pub user_id: String,   // empty for token/none modes; populated for password/OAuth
    pub device_id: String, // populated for PIN mode (paired device)
    /// Highest role this credential may hold. See "Identity ceilings per mode".
    pub max_role: Role,
    /// false only on the `require_auth_for_view = false` anonymous path. An
    /// unauthenticated identity is never eligible for takeover (MODULE_SERVER step 11).
    pub authenticated: bool,
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
pub fn new(cfg: &config::AuthSection) -> Result<Box<dyn Authenticator>, AuthError>;

/// reload builds a replacement Authenticator from a reloaded `[auth]` section. The
/// pipeline's `fd-config` task calls it and hands the result to
/// `Server::set_auth_policy(a, cfg.auth.allow_takeover)`, which swaps it in,
/// refreshes the one server-side policy gate (`allow_takeover`) and clears the
/// session cache internally (MODULE_CONFIG "Hot reload behavior").
/// `require_auth_for_view` is NOT refreshed here or there: it is evaluated inside
/// the Authenticator this function returns.
///
/// Unlike `new`, it performs NO one-time startup work: it never generates a token,
/// never rewrites `token_file`, and never re-opens the PIN pairing window. A
/// reloaded section whose `token` is empty KEEPS the token `new` generated at
/// startup — otherwise every unrelated SIGHUP (a `[log]` level change, say) would
/// silently invalidate every operator's credential.
///
/// It takes NO handle to the running Authenticator, and the trait has no
/// `retained_token` accessor. The generated token is minted once per process and
/// retained INSIDE this crate — a `OnceLock<String>` written by `new` when it
/// generates one and read by `reload`. That is what makes this function callable at
/// all: the running Authenticator was moved into `server::Config` at MODULE_PIPELINE
/// step 7 and the Server exposes no getter, so a `current: &dyn Authenticator`
/// parameter had no expression any caller could write for it.
pub fn reload(cfg: &config::AuthSection) -> Result<Box<dyn Authenticator>, AuthError>;
```

> The control-stream auth handshake, the resume path and session-token issuance are NOT
> sketched here. They belong to the server, not to this crate: the authoritative sequence
> is MODULE_SERVER "WebTransport Session Lifecycle" steps 7-13, the state it writes is
> `server::SessionState`, and the store is `server::SessionCache`. This crate holds
> credential tokens only (see "Two token kinds").

### Identity ceilings per mode

`max_role` is what the presented credential proves, never what the client asked for.
The effective role is `min(requested_role, max_role)`, computed by the server.

| Mode | Credential presented | `max_role` | `authenticated` |
|------|----------------------|-----------|-----------------|
| `none` | — (accepted unconditionally) | `Control` | `true` |
| `token` | matching `[auth] token` (constant-time) | `Control` | `true` |
| `token` | absent/wrong, `require_auth_for_view = true` | — (`Err(BadCredentials)`) | — |
| `token` | absent/wrong, `require_auth_for_view = false` | `View` | `false` |
| `password` | the credential token issued by `POST /auth` | `Control` | `true` |
| `password` | absent/wrong, `require_auth_for_view = false` | `View` | `false` |
| `pin` | device token present in `paired_devices_file` | `Control` | `true` |
| `pin` | absent/unknown/revoked, `require_auth_for_view = false` | `View` | `false` |
| `oauth` | — | — (`Err(AuthError::Config)` at `new()`) | — |

---

## File Structure

```
featherdesk-auth/src/
├── lib.rs             // Authenticator trait (`authenticate`, `handle_auth`, `handle_pair`),
│                      // Identity struct, Mode enum, `new()` + `reload()`, and the
│                      // `OnceLock<String>` holding the token `new()` generated
├── none.rs            // Mode::None implementation (prints security warning)
├── token.rs           // Mode::Token implementation (bearer in control-stream msg, not URL params)
├── password.rs        // Mode::Password implementation + argon2id hashing
├── pin.rs             // Mode::Pin implementation + /pair handler + CSRF tokens
├── devices.rs         // Paired device storage, per-device revocation
├── oauth_stub.rs      // Mode::OAuth stub (cargo feature: oauth)
├── credentials.rs     // Credential-token store: CSPRNG tokens minted by POST /auth and
│                      // the device tokens minted by POST /pair, keyed by
│                      // hex(SHA-256(token)), TTL [auth] session_ttl_minutes. NOT a
│                      // session store — session state lives in server::SessionCache
│                      // (MODULE_SERVER "Session Cache (Reconnect)")
├── ratelimit.rs       // Per-IP attempt limiting + global PIN attempt counter
└── tests.rs
```

---

## Security Considerations

| Concern | Mitigation |
|---------|-----------|
| Token leakage in URL | Token sent in the first control-stream message (JSON body, inside the encrypted stream) after being obtained from `POST /auth`. NEVER as a URL query parameter, **URL fragment**, or HTTP header — the fragment reaches browser history and profile sync just as a query does. |
| Replay attack on token | Session tokens have an **absolute** TTL that resume never extends, and resume **consumes** the token (single-use, rotated on every reconnect), so a captured token is usable at most once and only until the original absolute expiry. |
| Brute-force password | Argon2id `m=65536, t=3, p=4`; per-identity exponential response delay after 5 failures, a global 30/min failed-attempt bucket, and a per-IP 5/min limit — all process-global, so a fresh connection per attempt gains nothing. |
| Brute-force PIN | 8-digit CSPRNG default (100M space), **process-global** max 10 attempts per window, doubling response delay after 3, constant-time compare. |
| Session fixation | Server generates session_token via a CSPRNG (`getrandom` / `OsRng`) (CSPRNG mandatory), never accepts client-supplied, and stores it under `SHA-256(token)`, never in the clear. |
| CSRF on /pair | CSRF token issued on GET `/pair`, required on POST. Cookie: `SameSite=Strict; Secure; HttpOnly` |
| Cleartext over HTTP | TLS 1.3 mandatory (enforced by QUIC; weaker negotiation is impossible), AEAD ciphers only (see [`MODULE_SERVER.md`](./MODULE_SERVER.md)) |
| Token generation | **All** random tokens (auth, session, device) MUST use a CSPRNG (`getrandom` / `OsRng`). Non-cryptographic RNGs (e.g. `rand::thread_rng` for non-crypto use) are prohibited for tokens. |
| TLS key storage | Self-signed cert private key cached with mode 0600 (Unix) / restrictive ACL (Windows). Permissions verified on startup. |
| Device revocation | `featherdesk revoke-device <id> --config <path>` removes the device from `paired_devices_file`; `SIGHUP` makes it effective (see "Token rotation and device revocation"). There is deliberately **no** HTTP admin endpoint — the media port carries no administrative surface. |
| Metrics endpoint | Plain HTTP, unauthenticated, loopback by default; a non-loopback `metrics.bind` prints a startup warning. No label in any series carries an address, token, user, device or file name — see MODULE_PIPELINE "The `client` label". |
| LAN MITM on first contact (self-signed mode) | Trust-on-first-use. The stable `spki_sha256` is pinned by the client on first successful connection and a later change is a hard stop; the first contact itself is unauthenticated unless the operator compares the startup-banner fingerprint out of band. CA-trusted mode removes the window entirely. |
| Viewer-only attacks | `require_auth_for_view = true` by default. With `false`, the unauthenticated identity's `max_role` is `View`, so an anonymous client that requests `control` or `player` is admitted as a viewer — it is never promoted. |
| Controller takeover | `allow_takeover = false` by default. When enabled, displaced controller receives QUIC application close code 4410 (`close::CONTROLLER_TAKEOVER`). |
| Origin hijacking | `allow_origin = ""` by default (same-origin only). Wildcard `"*"` requires explicit opt-in. It is a field of `server::SessionDefaults`, filled at MODULE_PIPELINE step 7 and replaced wholesale by `Server::set_session_defaults` on reload, and it is loaded from the `ArcSwap` on every WebTransport or WebSocket upgrade — i.e. before a session exists — so the check is armed from the first connection, not from the first reload. |

---

## Testing Strategy

| Level | What | Hardware |
|-------|------|----------|
| Unit | `mode = token`: correct/missing/expired/malformed-JSON first-message → `auth_ok` vs `close::AUTH_FAILED (4401)` | No |
| Unit | `mode = password`: constant-time argon2id verify rejects a wrong password without a timing difference vs. a correct one, and a 6th consecutive failure is answered no earlier than 1 s after the request | No |
| Unit | `mode = pin`: PIN brute-force backoff timing (1s, 2s, 4s, 8s... after the 3rd failure) and hard stop at `max_pin_attempts`, **with each attempt on a fresh connection** (proves the counter is process-global) | No |
| Unit | Bearer comparison is constant-time: a token differing in the first byte and one differing in the last byte take statistically indistinguishable time | No |
| Unit | Credential-token generation and lookup use the CSPRNG path only and are keyed by `hex(SHA-256(token))`; a test double `Authenticator` proves no code path in this crate mints a credential token from a non-CSPRNG source, and none writes into `server::SessionCache` (see "Two token kinds") | No |
| Integration | Resume flow: valid unexpired `session_token` + `resume:true` skips auth and emits `{"resumed":true,...}`; expired/unknown token falls through to full re-auth, never a bare HTTP 401 (QUIC close 4401 instead) | No |
| Integration | `mode = pin` full pairing flow end-to-end: `GET /pair` → CSRF token issued → `POST /pair` with correct PIN → device token stored in `paired_devices_file` → subsequent connection authenticates via device token | No |
| Integration | Role assignment: first connection with role omitted becomes `control`; a second `role:"control"` connection without `takeover:true` becomes `view`; `takeover:true` only succeeds when `[auth] allow_takeover = true` | No |
| Integration | Resume re-runs admission control and the controller-slot CAS: with the slot held by another session, a resuming ex-controller is granted `view` and its `auth_ok.role` says so | No |
| Integration | Resume is single-use: two connections presenting the same token concurrently → exactly one resumes, the other falls through to full auth | No |
| Integration | Absolute TTL: a session reconnecting every 60 s is refused at `session_ttl_minutes` after its original authentication, and `session_ttl_sec` on the wire counts down to it | No |
| Integration | `require_auth_for_view = true` (default) rejects an unauthenticated viewer; `= false` admits one — and an unauthenticated client requesting `control`, requesting `player`, or omitting `role` is admitted as `view` in all three cases and is refused the `0x01`/`0x02`/`0x03` streams | No |
| Security | Token/PIN/password are never accepted from a URL query parameter — a request smuggling credentials in the `/wt` URL **query or fragment** is treated as unauthenticated, not as an alternate auth path | No |
| Unit | `new()` fails startup on every inconsistent `[auth]` section — `mode="token"` with a token under 32 chars, `mode="password"` with no/malformed `password_hash`, an unwritable `token_file` path, `mode="oauth"` without the `oauth` feature — returning `AuthError::Config`. **A misconfigured server must not start**; the failure mode being guarded against is one that boots and silently under-gates. | No |
| Unit | `new()` side effects happen exactly once and only for the selected mode: `token=""` with a populated `token_file` READS it instead of generating; `token=""` with no `token_file` generates exactly one CSPRNG token per process, writes `token_file` at mode 0600, and never a second on SIGHUP; `mode="none"` emits the warning line; no other mode touches those paths | No |

---

## Status

📋 **Specced.** Implementation pending. Token, password, and PIN modes are
in scope for v1. OAuth is deferred until requested.

Implementation order (recommended):
1. `mode = none` + `mode = token` (covers headless server deploys)
2. `mode = password` (covers single-user installs)
3. `mode = pin` (covers Sunshine-style first-launch UX)
4. `mode = oauth` (deferred until requested)
