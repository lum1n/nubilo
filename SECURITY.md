# Nubilo Security

This document is the threat model and security architecture. It is written *before* protocol adapters (CalDAV/CardDAV/WebDAV) on purpose: those adapters must inherit this model, not invent a parallel one.

## 1. Assets

| Asset | Sensitivity | Location |
| --- | --- | --- |
| Calendar events, including titles, attendees, notes, locations | High | Encrypted blobs; identifiers and hashes in SQLite |
| Contacts / vCards | High | Encrypted blobs |
| Photos / originals and EXIF (including GPS) | High / GPS is explicitly sensitive | Encrypted blobs; GPS handling is configurable |
| Personal files | High | Encrypted blobs |
| Device private keys | Critical | Client-side only (macOS Keychain for agent; else file 0600) |
| Server master key | Critical | `$data_dir/master.key` mode 0600 |
| App passwords for DAV | Critical | Argon2id hashes in SQLite; plaintext shown once |
| Pairing codes | High, short-lived | Argon2id hashes in SQLite |
| Collection names, object sizes, timestamps | Medium | SQLite metadata |
| Audit logs | Medium | Local log files; redacted |

Corporate credentials are **not** an asset of this system. The macOS agent reads only the folders the user selected; photos are backed up by the Nubilo app, and calendars and contacts reach the server through the built-in CalDAV/CardDAV accounts. Those credentials never leave the Mac. A failed photo library or folder enumeration must not be treated as “everything was deleted.”

## 2. Trust boundaries

```text
┌─────────────────────────────────────────────────────────────┐
│ Untrusted network (Internet, LAN, Tailscale overlay)        │
│   Attackers: packet capture, MITM if TLS failed, replay,    │
│   malicious peers, port scanners                            │
└───────────────────────────┬─────────────────────────────────┘
                            │ TLS
┌───────────────────────────▼─────────────────────────────────┐
│ Application boundary: Nubilo HTTP mux                       │
│   Authenticates every request. Authorizes every resource.   │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Server process + $data_dir                                  │
│   Master key, SQLite, encrypted blobs                       │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ Host OS / disk / backups / operator shell                   │
│   Full compromise of the Linux host is in the threat model  │
└─────────────────────────────────────────────────────────────┘

Separate boundary:

┌─────────────────────────────────────────────────────────────┐
│ Device (Mac, iPhone, CLI laptop)                            │
│   Compromise of a device is outside the server's ability    │
│   to protect that device's already-synced data.             │
│   Server CAN revoke the device going forward.               │
└─────────────────────────────────────────────────────────────┘
```

### Network security

Tailscale (or WireGuard, or a private LAN) provides **reachability and a coarse host filter**. It reduces who can open a TCP connection.

It does **not**:

- authenticate a Nubilo device
- authorize access to a calendar or file
- encrypt blobs on disk
- protect against a compromised Tailscale peer
- protect against a stolen node key

A Tailscale peer that can reach port 8443 is still an unauthenticated network client until it completes Nubilo pairing and request authentication.

### Application security

Nubilo authenticates every API, sync, and DAV request independently of the network. Unauthenticated endpoints are only:

- `POST /api/v1/pair/begin`
- `POST /api/v1/pair/complete`
- `GET /.well-known/caldav` (301 to `/caldav/user/`; no calendar data)
- `GET /.well-known/carddav` (301 to `/carddav/user/`; no contact data)

Pairing endpoints are rate-limited, brute-force resistant, and cannot read or mutate user data. Well-known redirects only reveal DAV principal paths. There are no unauthenticated admin endpoints.

### Data security

Blobs are encrypted at rest with ChaCha20-Poly1305. The master key never leaves the server host except inside an encrypted backup.

SQLite metadata is **not** full-payload encrypted. It contains IDs, hashes, sizes, device names, collection names, and journal operations. It does not contain event descriptions, vCard payloads, or file bytes. Disk encryption (LUKS) is the recommended control; see §6.

### Device security

A compromised Mac or iPhone can read anything that device is authorized to sync, and can push malicious or destructive changes within its permissions. The server cannot prevent that. The operator can revoke the device. Other devices keep their credentials.

## 3. Attacker capabilities

The design assumes all of the following are possible:

1. The Linux host is eventually compromised (root on the server).
2. A LAN device is malicious.
3. A Tailscale peer is compromised.
4. Pairing codes, app passwords, or log files leak.
5. Clients disappear for months, then return with stale state.
6. Networks drop, replay, reorder, or truncate requests.
7. The process crashes during any write.
8. Clients send malformed DAV/HTTP/sync bodies, huge bodies, or path-traversal hrefs.
9. Clients are buggy or hostile, including the operator's own phone.
10. Backups are stolen.
11. An operator runs `verify` / `restore` incorrectly.

Out of scope for *prevention* (but in scope for *containment*):

- An attacker with the live master key and disk access (they can decrypt blobs). Mitigation: host hardening, LUKS, backup encryption with a separate passphrase, limited SSH.
- An attacker on a paired Mac (they are the user of that device). Mitigation: least privilege, revoke, audit.

## 4. Authentication model

### 4.1 Device identity

Each device has:

```text
device_id      ULID
device_name    operator-chosen, non-unique, sanitised
public_key     Ed25519 (signing devices)
created_at
last_seen
permissions    role + collection ACL + protocol ACL
revoked_at     null or timestamp; non-null means immediate deny
```

Signing devices also hold a private key **only on the device**. The server stores the public key.

Protocol (DAV) devices may have no public key. They authenticate with an app password.

### 4.2 Request signatures (signing devices)

Every authenticated request carries:

```text
Authorization: Nubilo-Sig v1
  device=<device_id>
  ts=<unix milliseconds>
  nonce=<32 hex bytes>
  sig=<base64 Ed25519 signature>
```

Canonical string (byte-exact):

```text
nubilo-sig/v1
<device_id>
<ts>
<nonce>
<HTTP method uppercase>
<path including query exactly as received>
<sha256 hex of raw body>
```

Server checks, in order:

1. Header parses.
2. `|now - ts| ≤ 60s` (configurable, default 60).
3. Nonce has not been seen for that device (replay cache, TTL 2× window).
4. Device exists and `revoked_at IS NULL`.
5. Signature verifies against the stored public key.
6. Body size ≤ configured maximum.

Clock skew beyond the window is a hard failure. Devices should use NTP.

This is **not** a bearer token. Stealing a single signed request does not allow forging others after the nonce TTL, and does not reveal the private key.

### 4.3 App passwords (protocol devices)

Generated as 24 bytes CSPRNG, encoded Crockford Base32, shown once.

Stored as Argon2id (`m=64MiB, t=3, p=1`) of the full secret. Verification is done in constant-time comparison of the derived key. HTTP Basic over TLS is the on-the-wire mapping for future DAV.

App passwords:

- are scoped (`caldav`, `carddav`, `webdav`, optionally collection IDs)
- never confer `admin`
- are revocable independently of signing keys
- are rate-limited on failure

Since schema v2 every app password belongs to a **user**. HTTP Basic accepts either the user's username (each of the user's active app passwords is tried; at most 10 per user, which bounds Argon2 work) or, for accounts created before multi-user support, the password's device ULID. Successful logins are cached for 10 minutes keyed by `sha256(username, password)` because Apple clients send Basic on every request; revoking the password, revoking its device, or disabling the user invalidates the cache entry on the next request. Failures lock the username (5/min, 30 s) and the client IP (20/min, 30 s); a client whose credentials are in the success cache is not affected by a username lock, so others' wrong guesses cannot lock out a working phone.

### 4.4 Local CLI

CLI commands that need a running server authenticate as a local admin using a token file `$data_dir/admin.token` (0600), accepted **only** on loopback or the Unix socket. The token is not a device and cannot be used on non-loopback binds.

CLI commands may also open `$data_dir/metadata.db` directly when the server is stopped. SQLite WAL + `busy_timeout` is used. Concurrent admin CLI + server is supported for reads; operators should avoid two writers from different processes performing restore at the same time (restore takes an exclusive lock).

## 5. Authorization model

Authentication answers *who*. Authorization answers *what*.

Every request is checked in two steps:

1. `authz.Allow(device, action)` — protocol scope and role (a WebDAV-only app password cannot use CalDAV; only admins administer).
2. `authz.AllowCollection(device, action, access)` — the device's **user** access to the collection: reads need `ro`, writes need `rw`, deleting, renaming and sharing a collection need ownership.

Roles:

| Role | Meaning |
| --- | --- |
| `admin` | Device and server administration. Not granted to DAV app passwords. First local init creates a local admin token, not a network-admin device. |
| `agent` | Push/pull on collections the operator selected. Typical macOS agent. |
| `client` | Push/pull on granted collections. |
| `dav` | Protocol-only, scoped. |

Actions: `sync.read`, `sync.write`, `dav.read`, `dav.write`, `device.list`, `device.revoke`, `metrics.read`, `backup.create`, `backup.restore`.

Users and roles (schema v2): every device belongs to one user. Users are `admin` or `member`; a device of an admin user (other than an app password) may use the admin API. Disabling a user revokes all their devices and app passwords. The last active admin cannot be demoted or disabled. The per-device `permissions.collections` list from v1 is no longer consulted.

Collection access: each collection has an owner and a root (`root_id`; children inherit both). A user's access is `owner` if they own it, else the share on its root (`ro` or `rw`), else none. Shares exist only on roots.

### 5.1 Multi-user isolation invariants

These are enforced in the sync engine (not only in HTTP handlers) and covered by `syncengine/access_test.go`, `server/isolation_test.go` and `dav/share_test.go`:

1. `GET /sync/v1/changes` filters by access in SQL **before** `LIMIT`, so a user never sees another user's journal entries and pages stay full.
2. A push needs `rw` on the target collection and, when an object moves, `rw` on its current collection; both must have the same owner (no handing data to another user, no pulling it out of a shared collection).
3. Blobs are deduplicated across users by plaintext hash, so knowing a hash must not grant access: a push may only reference a blob the **pusher** holds a `blob_refs` row for (created by uploading the bytes, or by adding a photo they can read to an album), even when writing into a collection shared with them. `GET /sync/v1/blob/{hash}` answers 404 unless the caller holds a ref or can read a collection with a live object whose `blob_id` or blob metadata keys (`preview_hash`, `thumb_hash`, `live_pair_hash`, matched case-insensitively everywhere) name it; photo renditions only serve hashes the object references.
8. Changes are filtered by where an object was when the journal entry was written; if it has since moved somewhere the caller cannot read, the entry is delivered as a deletion without metadata or blob.
9. Shares of a disabled user stop working until the user is re-enabled. DAV lookups always resolve the caller's own collections before shared ones, so a shared collection cannot shadow an own calendar path. Usernames `owner`, `admin`, `root` and `system` are reserved.
4. Creating an object whose ID exists in a collection the caller cannot read returns a generic error, not its revision or hash.
5. Photos, albums, DAV homes and WebDAV roots are resolved per user; another user's principal path (`/caldav/u/<other>/…`) is 403 and shared collections appear only through the caller's own home (`/dav/shared/<owner>/…` for files).
6. Idempotency keys are scoped per device.
7. System callers (local admin token, local operator) are explicit and user-less; the admin UI acts *as* a chosen user (`X-Nubilo-Act-As`) so its writes go through the same checks.

Deny by default. A revoked device (or a device of a disabled user) fails authentication, so authorization is never reached.

## 6. Encryption model

### Transport

TLS 1.2+ is required for any non-loopback listener. `tls.auto` (default true) writes a self-signed certificate at init/server start covering localhost and local interface IPs. Pairing pins that leaf (TOFU) in `device.json`; later agent requests require the same cert. Public CA or Tailscale Serve certificates are verified against system roots and are not pinned.

Loopback may disable TLS only if there is no certificate on disk and `tls.allow_insecure_loopback=true`. There is no config flag that disables TLS on `0.0.0.0`.

Tailscale does not replace this. If a TLS terminator exists in front (Tailscale Serve, Caddy, …), the server should still bind loopback and treat the terminator as part of the host trust domain. That is the path Apple CalDAV/CardDAV clients should use, because they will not honor Nubilo's pairing pin.

### At rest

- Master key: 32 random bytes, `0600`.
- Per-purpose keys via HKDF-SHA-256:
  - `nubilo-blob-v1` for blob encryption
  - `nubilo-backup-v1` for backup wrapping (further wrapped by a backup passphrase)
- Blobs: `nonce (12) || ciphertext || tag (16)` using ChaCha20-Poly1305. Nonce is random per write; key+nonce reuse is a fatal invariant checked in tests.
- Content addressing uses SHA-256 of **plaintext**. Two identical photos collapse to one blob even if encrypted under different nonces (the store writes once per hash).
- Private keys on clients: on macOS the agent signing key is stored in the **Keychain** (`dev.nubilo.agent`); a leftover `device.key` file is migrated on load and removed. Other platforms use a `0600` file under the client data dir.
- macOS agent map (`agent.db`) is a local file path ↔ object_id table, not server identity. Object IDs remain ULIDs.
- Photo preview/thumb blobs are extra hashes in object metadata (`preview_hash`, `thumb_hash`). `verify` and `gc` count them as live references so derivatives are not deleted while the photo object exists.

### SQLite at rest (Phase 8 evaluation)

SQLite pages are **not** encrypted. Payloads (ICS, vCards, file bytes, photo originals) live in the blob store, which is ChaCha20-Poly1305. SQLite holds IDs, hashes, sizes, names, and journal ops.

SQLCipher was evaluated and **not** adopted:

- The server driver is `modernc.org/sqlite` (pure Go). SQLCipher is a C fork and would force CGO on Linux or a different driver.
- Encrypting selected columns still leaks the object graph unless almost every column is ciphertext, at which point SQLite is a poor fit.
- Recommended controls: LUKS (or equivalent) on the data volume, `0700` on `$data_dir`, encrypted backups whose passphrase is **not** the master key.

Revisit if a maintained pure-Go SQLCipher-compatible driver appears.

### Not encrypted without full-disk encryption

SQLite pages. Mitigations above. This is an accepted limitation, not an unfinished checkbox.

## 7. Device pairing

There is no shared password.

```text
Operator on server:  nubilo pair --role agent
                     → prints XXXXX-XXXXX
                     → stores Argon2id(code), expiry 5 min, attempts=0

Device:              nubilo pair --server https://host:8443 --code XXXXX-XXXXX --name "Studio Mac"

                     1. Generate Ed25519 keypair locally
                     2. POST /api/v1/pair/begin
                          {code, name, public_key, client_nonce}
                     3. Server hashes code, looks up active session,
                        rejects on expiry / too many attempts / mismatch
                     4. Server returns {pairing_id, challenge, server_id}
                     5. Client signs challenge with its private key
                     6. POST /api/v1/pair/complete
                          {pairing_id, signature}
                     7. Server verifies, inserts device, burns the code
                     8. Client writes key material to its data dir
```

Pairing code properties:

- 10 Crockford Base32 characters (50 bits), displayed as `XXXXX-XXXXX`
- single use
- 5 minute TTL
- 5 attempts then the session is burned
- global rate limit: 10 begin attempts / hour / IP, 20 complete attempts / hour / IP, 3 concurrent active sessions
- codes are never logged; only a truncated session id is logged

Out-of-band local pairing (recovery / first admin workstation):

```text
nubilo devices enroll --pubkey device.pub --name "..." --role agent
```

This requires filesystem access to `$data_dir` (or the admin token on loopback). It is the recovery path if pairing HTTP is unavailable.

## 8. Credential lifecycle

| Event | Effect |
| --- | --- |
| Pair complete | Device row created; public key stored; last_seen set |
| Successful request | last_seen updated (rate-limited to once per minute) |
| `devices revoke` | `revoked_at=now`; replay cache dropped; app passwords revoked; subsequent requests 401 |
| `devices rotate` | New keypair (signing) or new app password; old credential immediately invalid; device_id unchanged |
| `devices rename` | Name change only |
| Pairing expiry | Session row remains for audit with no code hash reuse |

Revoking device A does not change device B's keys. There is no global secret to rotate.

Compromised pairing code (unused): wait 5 minutes or run pairing again (which can cancel outstanding sessions).

Compromised app password: revoke that protocol device; other devices continue.

Compromised device private key: revoke that device.

Compromised master key: assume all blobs readable. Generate a new data directory, restore from an encrypted backup that used a **backup passphrase** not equal to the master key, or re-encrypt (Phase 8 tooling). Rotate all device credentials because the attacker may also have SQLite.

## 9. Compromise scenarios

| Scenario | Impact | Operator response |
| --- | --- | --- |
| Stolen iPhone | Attacker uses DAV app password until revoked | `nubilo devices revoke <id>`; iCloud/Find My as appropriate |
| Stolen Mac with agent | Attacker can push/pull granted collections | Revoke device; inspect journal for unexpected writes; restore collections from backup if needed |
| Stolen Tailscale node of an unrelated peer | Can reach TCP port if ACL allows | Still needs Nubilo credentials; tighten Tailscale ACLs; keep TLS |
| Stolen `master.key` + disk | Full plaintext of blobs | New server identity; restore from passphrase-protected backup onto clean host; revoke all devices and re-pair |
| Stolen encrypted backup | Unreadable without backup passphrase | Rotate passphrase on next backup; treat as leak of ciphertext only |
| Malicious CalDAV client | Can mutate calendars it is authorized for; cannot admin-revoke other devices | Scope DAV devices narrowly; revoke |
| Log leak | Should contain no secrets or payloads | If `log.sensitive_metadata=true` was enabled, treat as metadata leak |
| Path traversal in future DAV | Must be impossible | Adapters map hrefs to ULIDs; never `filepath.Join(root, urlPath)` |

## 10. Recovery scenarios

| Situation | Procedure |
| --- | --- |
| Server process crash | Restart `nubilo server`. WAL recovers. `nubilo verify`. `nubilo gc --apply` for orphans. |
| Disk corruption | `nubilo verify` reports missing blobs / hash mismatch. Restore from backup. |
| Lost device | Revoke it. Do not re-use IDs. |
| Lost all devices plus server, backups remain | `nubilo restore --passphrase ...` then re-pair every device. |
| Lost backup passphrase | Backup is not recoverable. This is intentional. |
| Client restored from Time Machine with old cursor | Hello reports cursor; server may flag `need_reconcile`; client runs RECONCILE. Duplicate pushes are idempotent. |
| Split brain two writers | Conflict detection on `base_revision`; no silent overwrite. |

## 11. Backup implications

`nubilo backup` produces an encrypted archive containing:

- SQLite snapshot (consistent: `VACUUM INTO` or backup API)
- blob directory
- key-wrapping metadata (not the raw master key in plaintext)
- device public keys and permission rows
- journal, tombstones, revisions

The backup is wrapped with a passphrase via scrypt/Argon2id + ChaCha20-Poly1305. Restoring applies to an empty data dir by default. Restoring over a live database is refused.

A backup that has not been restored in a test is not trusted. `nubilo backup --verify` restores into a temp directory and runs `verify`.

Backups contain everything needed to reconstruct user data. They are as sensitive as the live server. Store them off-host.

## 12. Logging rules

Never log:

- passwords, pairing codes, app passwords
- authorization headers or signatures
- master keys, private keys, DEKs
- complete calendar descriptions, vCards, file bytes
- GPS coordinates
- full photo EXIF

Default logs may include: device_id, collection_id, object_id, sequence numbers, content hashes, HTTP status, error classes, durations.

`log.sensitive_metadata=true` may add collection names and object kinds. It still must not add payloads.

## 13. Rate limiting and abuse

| Endpoint | Limit (defaults) |
| --- | --- |
| `pair/begin` | 10 / hour / IP; 3 active sessions per user |
| `pair/complete` | 20 / hour / IP |
| `invites/redeem` | 5 / hour / IP |
| App-password failures | 5 / minute / username and 20 / minute / IP, then 30s cooldown |
| Signed-request failures | 30 / minute / IP |
| Request body | `sync.max_blob_bytes` (default 256 MiB) plus 1 MiB headroom |
| Sync batch | 500 objects / request |

## 14. Input validation

- Paths: URL path is never used as a filesystem path. Objects are ULIDs. Collection hrefs in DAV are a reversible encoding of IDs, not user filenames.
- Filenames in metadata are display names; they are sanitised for logs and rejected if they contain NUL.
- SQLite access is parameterized only.
- JSON decoders use `DisallowUnknownFields` on pairing and sync request structs.
- Maximum collection name length, maximum batch size, and body limits are enforced at the mux and adapters.

## 15. Known limitations

1. SQLite metadata is not encrypted at rest (see §6 evaluation). Use LUKS.
2. GPS in image originals is preserved (required). Stripping GPS from *derivatives* is configurable (`photos.strip_gps_from_derivatives`, default true). Originals are never silently mutated. Coordinates are not stored in SQLite metadata.
3. A root attacker on the server can decrypt blobs and impersonate the server to new clients until clients pin the server identity (server Ed25519 identity is returned at pair and should be pinned by the client).
4. DAV app passwords are bearer secrets (unavoidable with Apple Calendar/Files). TLS and revocation are the controls.
5. Multi-user isolation is enforced per user (§5.1), but blobs share one master-derived key and are deduplicated across users; deleting a user does not crypto-shred their data. Server-side encryption, not end-to-end: the server (and its operator) can read everything, which Apple CalDAV/CardDAV require anyway.
9. Behind the wired relay, the relay prepends a PROXY protocol v1 header with the client address and the home server trusts it **only** on the overlay listener (overlay members are the operator's own nodes). A compromised overlay member could therefore spoof client IPs for rate limiting and logs; it still cannot authenticate.
6. Availability is not a security guarantee. An attacker who can DoS the listen port can prevent sync. They should not be able to read or write data.
7. Client private keys: macOS agent uses Keychain (file fallback); server `master.key` remains a `0600` file.

## 15a. Public exposure (relay and direct)

`nubilo setup` offers these modes: **relay** (a VPS runs `nubilo relay`, an anchor of a [wired](https://github.com/lum1n/wired) overlay that forwards public TCP 443/80 to the home server as raw bytes), **direct** (this host listens on :443/:80), **proxy** (§15c), **tailscale** (§15d) and **lan** (self-signed, LAN only).

- TLS terminates on the home server with a Let's Encrypt certificate obtained by built-in ACME (TLS-ALPN-01 on :443, HTTP-01 on :80). The VPS never holds the master key, the database, or the certificate key; it sees TLS ciphertext only. It **can** deny service (drop or delay traffic) and see connection metadata (timing, sizes, client IPs). It **cannot** read or modify requests.
- The relay invite is a credential for joining the overlay; the home node only listens for the `https`/`http` services. `nubilo relay revoke NAME` removes a node and rotates the invite.
- Public listeners send `Strict-Transport-Security: max-age=31536000`. Plain HTTP only answers ACME HTTP-01 and redirects to HTTPS.
- The LAN listener keeps its self-signed certificate (and serves the ACME certificate when the SNI is the public domain), so phones on Wi-Fi keep working when the relay is down.

## 15c. Relay behind a TLS-terminating proxy (exe.dev)

In **proxy** mode the relay runs on a host whose provider proxy (exe.dev) terminates TLS for the public name and forwards plain HTTP to the relay. This is weaker than §15a:

- The proxy operator holds the certificate key and sees every request and response in plaintext, including app passwords in CalDAV/CardDAV Basic auth, calendar and contact data, files and photos. It can also modify traffic. App API requests stay signed by the device key (a modified request fails verification), but responses are not signed.
- The relay VM sees the same plaintext. wired encrypts the hop from the VM to the home server, which listens only on the overlay `web` service (plain HTTP) in this mode.
- The home server takes the client address from `X-Nubilo-Client-IP`, only on that overlay listener. The relay always overwrites the header with the last `X-Forwarded-For` entry its proxy appended, so clients cannot set it.
- Blobs and payloads stay encrypted at rest at home, so data at rest is protected as in other modes. The exposure is data in transit.
- `nubilo doctor` reports this as a `TLS termination` warning. Use the VPS relay (§15a) when TLS must end at home.

## 15d. Tailscale mode

- Devices reach the LAN listener over the tailnet by the node's MagicDNS name. The certificate comes from `tailscale cert` (Let's Encrypt, via tailscaled) and is stored with its key in `<data-dir>/tailscale/` (directory `0700`). TLS ends in the Nubilo process, as on the LAN.
- The server runs the `tailscale` CLI: `status --json` at setup and in doctor, `cert` at start and daily. Letting a non-root user fetch certificates means making it the tailscaled operator (`tailscale set --operator`), which also lets that user change other tailscaled settings.
- The name is published in Certificate Transparency logs when the certificate is issued.
- With Funnel forwarding raw TCP (docs/RELAY.md), the internet reaches the listener with TLS still ending at home, but every public client appears as `127.0.0.1`, so per-IP rate limits and the audit log cannot tell clients apart.

## 15b. Invites, upload tokens and configuration profiles

- **Invites** (`nbi-` + 20 Crockford base32 characters, 100 bits) are stored as Argon2id hashes, expire (default 24 h, max 30 days), are single-use unless an admin sets `max_uses`, and are admin-only to create. Redeeming creates the user (or pairs a device for an existing one) and the pairing challenge in one transaction; a failed redeem (e.g. username taken) does not consume a use. QR links are `https://<domain>/join#<code>`: the code stays in the URL fragment and never reaches the server's logs.
- **Upload tokens** (`POST /api/v1/me/upload-token`) are 256-bit bearer secrets bound to one device, valid 24 h, stored as SHA-256, and accepted only for `PUT /sync/v1/blob/*`, `POST /api/v1/photos/check` and `/commit`. They exist because iOS background uploads cannot be signed at send time (signatures expire after 60 s). A stolen token lets an attacker add photos to that user's library until it expires or the device is revoked; it cannot read anything.
- **Configuration profiles** (`POST /api/v1/me/profile`) contain a freshly created CalDAV/CardDAV app password in plaintext (Apple requires it). The `.mobileconfig` is encrypted with the blob key at rest, downloadable **once** within 15 minutes from an unguessable URL, then wiped. Profiles are unsigned for now (iOS shows "Unverified"); revoke the app password to cut off a lost device.

## 16. Security review gates

A subsystem is not "production-ready" if any of the following are known:

- authentication bypass
- authorization flaw
- arbitrary filesystem access or path traversal
- credential exposure in logs, backups without encryption, or source control
- unsafe deserialization
- command injection
- SQL injection
- a race that can corrupt or silently drop data
- an integrity failure that can lose objects without `verify` detecting it

## 17. Threat model summary

| Question | Answer |
| --- | --- |
| Who is the enemy? | Network attackers, malicious LAN/Tailscale peers, stolen devices, future host compromise, buggy clients |
| What do we protect first? | Confidentiality of payloads, integrity of the object graph, isolation between devices |
| What do we not claim? | Protection after host+master-key compromise; protection of a compromised paired device's own data; anonymity |
| What is the primary control? | Per-device Ed25519 identity, explicit authz, encrypted blobs, crash-safe journaled sync |
| What is Tailscale? | Transport. Not a security boundary. |

## 18. Phase 8 review (running code)

Reviewed against Phases 1–7 as implemented:

| Finding | Resolution |
| --- | --- |
| Preview/thumb blobs had `refcount=0` and were invisible to GC | Engine increments extra metadata hashes; `verify`/`gc` count `preview_hash`/`thumb_hash` |
| Signed-request body cap below photo max | Authenticator `MaxBody` follows `sync.max_blob_bytes` (default 256 MiB) |
| Admin token compared with `!=` | SHA-256 then constant-time compare; loopback-only |
| `pair/complete` had no IP rate limit | `pairing.completes_per_hour` (default 20) |
| Signed-request failures unthrottled | 30 / minute / IP |
| DAV password gate lacked the documented 30s cooldown | 5 failures then 30s lock |
| SECURITY.md still said DAV adapters were unimplemented | Corrected |
| SQLCipher | Evaluated; not adopted (see §6) |
| `golang.org/x/image` decode CVEs | Upgraded to v0.45.0; `govulncheck ./...` reports none in this code |

Fuzz seeds cover authorization parsing, canonical strings, pairing-code normalization, device names, photo inspect, and sync JSON push. Storage corruption, WAL reopen, backup tamper, and photo restore drills are tests. `nubilo gc [--apply]` collects unreferenced blobs and compactable tombstones.
