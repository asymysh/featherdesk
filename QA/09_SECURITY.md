# QA 09 — Security boundaries

No matching user-story area. These verify the boundaries the specs claim, from
the outside. **No `FAIL` may ship.**

Scope note: this is verification of *specced* controls. It is not a penetration
test, and it does not cover the items closed in `PROJECT_ARTIFACTS/GAP_TRIAGE.md`
(no per-user identity, no add-on signing) — those are accepted product
positions, and a checklist cannot un-accept them.

## Authentication and authorisation

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-SEC-1 | Credential in a URL query **or fragment** on `/wt` or `/ws` | Treated as unauthenticated. Never an alternate auth path — the fragment reaches browser history and profile sync just as a query does | `MODULE_AUTH` security table | ☐ |
| QA-SEC-2 | Timing of a wrong token differing in the **first** byte vs. the **last** | Statistically indistinguishable. `==` on a credential is a `FAIL` even if the timing test is noisy | `MODULE_AUTH` | ☐ |
| QA-SEC-3 | Password brute force from many fresh connections | Per-identity backoff, a global 30/min failed bucket and a per-IP 5/min limit all hold — **process-global**, so reconnecting gains nothing | `MODULE_AUTH` | ☐ |
| QA-SEC-4 | PIN brute force, one attempt per fresh connection | Global counter and doubling delay hold (QA-CON-29) | `MODULE_AUTH` | ☐ |
| QA-SEC-5 | Every generated token (auth, session, device) | From a CSPRNG. Collect many and check for structure; confirm no `thread_rng`-class source is on the path | `MODULE_AUTH` "Token generation" | ☐ |
| QA-SEC-6 | Session tokens at rest on the host | Stored as `SHA-256(token)`, never in the clear | `MODULE_AUTH` "Session fixation" | ☐ |
| QA-SEC-7 | Client-supplied `session_token` on a **fresh** auth | Never accepted as an identity — resume is the only path that takes one, and it consumes it | `MODULE_SERVER` "Two token kinds" | ☐ |
| QA-SEC-8 | Role escalation attempt: a patched client sending controller-only messages as a viewer | Refused server-side at the role gate. **Authority is server-enforced**, never client-asserted | `MODULE_SERVER` "Role gate table" | ☐ |
| QA-SEC-9 | A viewer opening `0x01` (input), `0x02` (clipboard), `0x03` (file transfer) | All three reset at stream scope before any payload is read | Role gate rows 1–7 | ☐ |
| QA-SEC-10 | `require_auth_for_view = false` | Anonymous is admitted at `View` only, is never promotable, and cannot take over | `MODULE_AUTH` "admits, never promotes" | ☐ |
| QA-SEC-11 | Cross-origin connection with `allow_origin = ""` (default) | Rejected on **both** carriers, from the very first connection (the check is armed before any reload) | `MODULE_AUTH` "Origin hijacking" | ☐ |
| QA-SEC-12 | File permissions on `token_file`, `paired_devices_file`, the self-signed key, `pw_portal` restore token | 0600 / owner-only ACL. Verified at startup, not assumed | `MODULE_AUTH`; `PW_PORTAL_LINUX_SPEC` | ☐ |

## Input validation (the bytes-from-the-network surface)

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-SEC-13 | Fuzz the 22-byte header, datagram reassembly and binary input records | No panic, no OOM, no hang. These are the three surfaces `cargo-fuzz` is specced for — run them, do not just configure them | CENTRAL_SPEC "Fuzzing" | ☐ |
| QA-SEC-14 | `PayloadSize` declaring 4 GiB | Rejected **before allocation** (16 MiB cap) | `MODULE_SERVER` "Reassembly bounds" | ☐ |
| QA-SEC-15 | Clipboard `[u32 Len]` declaring 4 GiB | Rejected before allocation; that stream reset; session survives | `MODULE_SERVER` DoS | ☐ |
| QA-SEC-16 | Control-stream line larger than `max_message_bytes` | Stream closed with `PROTOCOL_ERROR`; no unbounded buffering | `MODULE_SERVER` DoS | ☐ |
| QA-SEC-17 | Input record with a valid type but wrong length, or out-of-range coordinates | Rejected before injection; coordinates clamped to stream dims; nothing malformed reaches the kernel input layer | `MODULE_INPUT` "Security Considerations" | ☐ |
| QA-SEC-18 | Flood of input events far above `input_rate_limit` | Excess dropped at the reader; the session is not starved and other sessions are unaffected | `MODULE_SERVER` DoS | ☐ |
| QA-SEC-19 | Flood of unauthenticated connections | Accept queue bounded at 16; excess closed with `4429`/`server_busy`; **authenticated users are not evicted** (the `max_clients` check is after auth for exactly this reason) | `MODULE_SERVER` DoS | ☐ |
| QA-SEC-20 | Clipboard HTML containing `<script>`, `onerror=`, `javascript:` URLs — **both directions** | Sanitised both ways. H→C is sanitised too: the host process is not trusted to put safe markup on its own clipboard | CENTRAL_SPEC Contract 9 gates 5–6 | ☐ |
| QA-SEC-21 | File upload named `../../etc/passwd`, an absolute path, or a symlink escaping the sandbox | Confined to the Incoming folder in all cases | `MODULE_FILETRANSFER` | ☐ |

## Exposure and disclosure

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-SEC-22 | Metrics endpoint | Loopback by default; a non-loopback bind prints the startup warning. It carries **no** administrative surface | `MODULE_CONFIG` `[metrics]` | ☐ |
| QA-SEC-23 | Every exported metric series and label | No label carries an address, token, user, device or file name | `MODULE_PIPELINE` "The `client` label" | ☐ |
| QA-SEC-24 | Logs at every level, including `debug` | No credential, no clipboard content, no file content. Clipboard logs record **byte length only** | Contract 9; `MODULE_AUTH` | ☐ |
| QA-SEC-25 | Error responses on `/auth`, `/pair`, `/logout` | Do not reveal whether a token, device or session existed | `MODULE_SERVER` endpoints | ☐ |
| QA-SEC-26 | `GET /pair` when not pairing | `404` for both verbs — the endpoint's existence does not advertise the window | `MODULE_AUTH` | ☐ |
| QA-SEC-27 | TLS version negotiated | 1.3 only. A downgrade attempt fails — impossible under QUIC, verify on the TCP listener too | `MODULE_SERVER` TLS | ☐ |
| QA-SEC-28 | Self-signed first contact | Trust-on-first-use is documented and the startup banner prints the fingerprint the user can compare out of band; a later SPKI change is a hard stop client-side | `MODULE_AUTH` "LAN MITM" | ☐ |

## Add-on trust boundary

| ID | Check | Expected result | Spec | Status |
|----|-------|-----------------|------|--------|
| QA-SEC-29 | Confirm the documented posture is what happens: a hostile `.so` in the add-ons directory **is** loaded and executed | This is **expected** — the loader deliberately does not police the directory, and it does not refuse a world-writable one. The check exists so the risk is verified and documented, not discovered later. `FAIL` only if the behaviour differs from the spec's advisory | CENTRAL_SPEC "Security note (advisory, not enforced)" | ☐ |
| QA-SEC-30 | Operator guidance is actually present in shipped docs | Install docs state plainly: put only trusted add-ons there, and secure the folder yourself if running elevated | CENTRAL_SPEC | ☐ |
| QA-SEC-31 | An add-on that **claims a capability bit it does not implement** | Host handles the lie without crashing — see the `AddonCaps` capability-lie behaviour, e.g. a `CURSOR` liar hides the pointer rather than freezing it | `MODULE_ABI` "Misbehaving add-ons"; US-XP-13 | ☐ |
| QA-SEC-32 | An add-on that panics or hangs in a trait method | Panic is caught (`AbiErr::Unrecoverable`); the host survives. Record what a **hang** does — `catch_unwind` does not cover it, and that gap should be known rather than assumed away | CENTRAL_SPEC step 4; `MODULE_ABI` fault recovery | ☐ |
