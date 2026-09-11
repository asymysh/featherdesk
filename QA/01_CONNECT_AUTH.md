# QA 01 — Connection, carriers, TLS and authentication

Matching user stories: `PROJECT_ARTIFACTS/user_stories/01_connection_and_auth.md`.

## Carrier selection and reachability

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CON-1 | **N1 LAN**, modern Chrome | Connects on the **WebTransport** carrier; HUD says so; `featherdesk_clients{carrier="webtransport"}` is 1 | `MODULE_TRANSPORT` "Carrier selection" | ☐ |
| QA-CON-2 | **N4 UDP blocked** (blackholed, no ICMP reject — packets simply vanish) | `wt.ready` never settles, the **3 s** deadline fires, the client completes auth on `/ws` and receives a bootstrap IDR. Total time to first frame stays reasonable | `MODULE_TRANSPORT` "How the client selects" | ☐ |
| QA-CON-3 | Reconnect in the same tab after a WebSocket fallback | The sticky preference skips the WebTransport probe for **10 minutes**, then re-probes. Confirm it is a latency optimisation only — forcing UDP open mid-window must still work on the next probe | `MODULE_TRANSPORT` sticky carrier | ☐ |
| QA-CON-4 | **N2 overlay/VPN forwarding UDP** (WireGuard or Tailscale) | Keeps the **WebTransport** carrier end to end. This is the recommended non-LAN path and must not silently degrade | `MODULE_TRANSPORT` "Reachability recipe" | ☐ |
| QA-CON-5 | **N3 Cloudflare Tunnel** | Connects, on the **WebSocket** carrier, and stays there. Video, audio, input, clipboard and file transfer all work | `MODULE_TRANSPORT` "Carrier selection" | ☐ |
| QA-CON-6 | Both carriers fail (UDP blocked **and** `/ws` returning 501) | The client renders a connection-failed notice naming **both** errors. It never silently retries a third way | `MODULE_TRANSPORT` | ☐ |
| QA-CON-7 | `[transport] websocket_fallback = false` | `/ws` returns `501`; a UDP-blocked client cannot connect and is told why | `MODULE_CONFIG` | ☐ |
| QA-CON-8 | Carrier parity spot-check: same session, both carriers | A control line, an input record, a clipboard message and an access unit produce identical application behaviour. Only the HUD's carrier label differs | `MODULE_TRANSPORT` "Wire format on the fallback carrier" | ☐ |

## Liveness (both carriers)

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CON-9 | **WebTransport dead peer:** kill the client's network abruptly (pull the cable / `iptables -j DROP`), do not close the tab | Session dropped within `max_idle_timeout` (30 s default). Client count returns to its prior value | `MODULE_SERVER` "Keepalive, liveness & timeouts" | ☐ |
| QA-CON-10 | **WebSocket dead peer** — the important one. Half-open the TCP connection (drop packets, keep the socket) while that client **holds the controller slot** | Session closed within `max_idle_timeout`, the `max_clients` slot is freed, **and the controller slot is released**. Without the app-side timer this hangs for hours | `MODULE_TRANSPORT` "Liveness on both carriers" | ☐ |
| QA-CON-11 | Held-key release on the same timeout | A key held down by the timed-out controller is released on the host (`release_all`), not left stuck | `MODULE_INPUT` `release_all`; TD-35 | ☐ |
| QA-CON-12 | **Idle but alive:** a viewer sends no input for 30 minutes | Never disconnected. Keepalives flow on either carrier; there is no idle-input disconnect in v1 | `MODULE_SERVER` "No idle-input disconnect" | ☐ |
| QA-CON-13 | `ping_interval = 0` | Keepalive/liveness still works on both carriers; only the RTT sample is gone. Confirms the WebSocket **Ping control frame** and the app-level **`Ping` datagram** are separate mechanisms | `MODULE_TRANSPORT` | ☐ |

## TLS and trust

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CON-14 | **Self-signed mode** (both `cert` and `key` empty), Chrome | Connects via `serverCertificateHashes` — **no** TLS click-through interstitial for the WebTransport session. `/cert-hashes` serves current + previous hashes and a stable `spki_sha256` | `MODULE_SERVER` "Browser certificate trust" | ☐ |
| QA-CON-15 | Self-signed cert rotation while a session is live | Session unaffected; the next handshake presents the new cert; the **SPKI fingerprint is unchanged** (key pair retained across rotation) | `MODULE_SERVER` TLS | ☐ |
| QA-CON-16 | Client pins `spki_sha256`, then the host's cached key file is deleted and regenerated | The client hard-stops on the fingerprint change rather than connecting silently. Confirm the recovery path is documented and usable | `MODULE_AUTH` "LAN MITM on first contact" | ☐ |
| QA-CON-17 | **Safari**, self-signed mode | Documented behaviour: `serverCertificateHashes` support is incomplete, so Safari may need a CA-trusted cert. Verify the client's failure message says that rather than failing opaquely | `PLATFORM_COMPAT` | ☐ |
| QA-CON-18 | **CA-trusted mode** with a real chain | Normal browser trust; `/cert-hashes` returns `{"hashes":[]}` with no `spki_sha256` | `MODULE_SERVER` | ☐ |
| QA-CON-19 | `reload_tls` on SIGHUP with a **bad** cert file at the same path | Previous key stays in place, error logged, listener stays up | `MODULE_CONFIG` hot reload | ☐ |

## Authentication

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CON-20 | `mode = "none"` | Startup prints the ⚠ AUTH DISABLED warning. Every session admitted | `MODULE_AUTH` | ☐ |
| QA-CON-21 | `mode = "token"`, correct token as first control-stream message | `auth_ok`, then `config`, then media | `MODULE_AUTH` | ☐ |
| QA-CON-22 | Wrong / missing / malformed-JSON first message | `auth_failed` + close `4401`. **Not** an HTTP 401 | `MODULE_AUTH` | ☐ |
| QA-CON-23 | Token generated at startup (`token = ""`, `token_file` set) | Printed once, written mode 0600, and **not regenerated** on SIGHUP | `MODULE_AUTH` "Token source" | ☐ |
| QA-CON-24 | Token supplied via `token_file` shorter than 32 chars | `AuthError::Config` at startup — the server does not boot under-gated | `MODULE_AUTH` | ☐ |
| QA-CON-25 | Credential smuggled in the `/wt` URL **query or fragment** | Treated as unauthenticated. Never an alternate auth path | `MODULE_AUTH` security table | ☐ |
| QA-CON-26 | `mode = "password"` via `POST /auth`, then the credential token on the control stream | Works. 6th consecutive wrong password is answered no earlier than 1 s later, and the delay applies even across fresh connections | `MODULE_AUTH` | ☐ |
| QA-CON-27 | `mode = "pin"` full pairing: `GET /pair` → CSRF token → `POST /pair` with the PIN | Device token stored in `paired_devices_file` (mode 0600); the next connection authenticates with it and never sees the form again | `MODULE_AUTH` "Pairing flow" | ☐ |
| QA-CON-28 | `POST /pair` **without** the CSRF token | Rejected | `MODULE_AUTH` | ☐ |
| QA-CON-29 | PIN brute force: wrong PIN repeatedly, **each on a fresh connection** | Backoff after the 3rd (1s, 2s, 4s, 8s…), hard stop at `max_pin_attempts`, window closes. Proves the counter is process-global, not per connection | `MODULE_AUTH` | ☐ |
| QA-CON-30 | `GET /pair` outside `mode = "pin"` or after the window | `404` for both verbs — the endpoint's existence does not advertise the window | `MODULE_AUTH` | ☐ |
| QA-CON-31 | `revoke-device` + SIGHUP | The revoked device can no longer authenticate, and (with `require_same_auth`) cannot resume | `MODULE_AUTH` "Token rotation and device revocation" | ☐ |
| QA-CON-32 | Token rotation + SIGHUP with sessions live | Existing sessions survive; none of them can **resume** afterwards (session cache cleared) | `MODULE_AUTH` | ☐ |
| QA-CON-33 | `POST /logout` with a session token | That session is closed; the response reveals nothing about whether the token existed | `MODULE_SERVER` endpoints | ☐ |
| QA-CON-34 | `mode = "oauth"` | Startup fails with `AuthError::Config` — deferred, not silently permissive | `MODULE_AUTH` "Status" | ☐ |

## Resume

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CON-35 | Brief network blip, client reconnects with `resume:true` and its `session_token` | Resumes without re-auth; `config` carries `"resumed":true`; stream continues | `MODULE_SERVER` "Session Cache" | ☐ |
| QA-CON-36 | Present the **same** session token twice concurrently | Exactly one resumes; the other falls through to full auth. Resume is single-use and rotated | `MODULE_AUTH` | ☐ |
| QA-CON-37 | Reconnect every 60 s for longer than `session_ttl_minutes` | Refused at the **absolute** expiry — resume never extends it. `session_ttl_sec` on the wire counts down to it | `MODULE_AUTH` | ☐ |
| QA-CON-38 | Resume while another session now holds the controller slot | Resuming ex-controller is granted `view`, and `auth_ok.role` says so | `MODULE_AUTH` | ☐ |

## Subpath deployment

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-CON-39 | `base_path = "/desk/"`, reached through a proxy on that prefix | Every route answers under the prefix — `/desk/`, `/desk/cert-hashes`, `/desk/healthz`, `/desk/wt`, `/desk/ws`, `/desk/auth`, `/desk/pair`, `/desk/logout` — and the SPA connects without any hardcoded `/` | `MODULE_SERVER` HTTP endpoints | ☐ |
| QA-CON-40 | Same, but check each **bare** path | Returns 404. Proves no route was left behind at the old prefix | `MODULE_SERVER` | ☐ |
| QA-CON-41 | Invalid `base_path` (no leading or trailing `/`, contains `..`) | Startup error | `MODULE_CONFIG` | ☐ |
