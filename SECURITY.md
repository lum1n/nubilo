# Nubilo Security

This document is the threat model and security architecture. It is written *before* protocol adapters (CalDAV/CardDAV/WebDAV) on purpose: those adapters must inherit this model, not invent a parallel one.

## 1. Assets

| Asset | Sensitivity | Location |
| --- | --- | --- |
| Calendar events, including titles, attendees, notes, locations | High | Encrypted blobs; identifiers and hashes in SQLite |
| Contacts / vCards | High | Encrypted blobs |
| Photos / originals and EXIF (including GPS) | High / GPS is explicitly sensitive | Encrypted blobs; GPS handling is configurable |
| Personal files | High | Encrypted blobs |
| Device private keys | Critical | Client-side only (file 0600 or OS keychain) |
| Server master key | Critical | `$data_dir/master.key` mode 0600 |
| App passwords for DAV | Critical | Argon2id hashes in SQLite; plaintext shown once |
| Pairing codes | High, short-lived | Argon2id hashes in SQLite |
| Collection names, object sizes, timestamps | Medium | SQLite metadata |
| Audit logs | Medium | Local log files; redacted |

Corporate credentials are **not** an asset of this system. The macOS agent reads local EventKit/PhotoKit data the OS already made available to the user. Those credentials never leave the Mac. A failed EventKit, Contacts, or PhotoKit enumeration must not be treated as “everything was deleted.”

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
- `GET /.well-known/caldav` (308 to `/caldav/user/`; no calendar data)
- `GET /.well-known/carddav` (308 to `/carddav/user/`; no contact data)

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

### 4.4 Local CLI

CLI commands that need a running server authenticate as a local admin using a token file `$data_dir/admin.token` (0600), accepted **only** on loopback or the Unix socket. The token is not a device and cannot be used on non-loopback binds.

CLI commands may also open `$data_dir/metadata.db` directly when the server is stopped. SQLite WAL + `busy_timeout` is used. Concurrent admin CLI + server is supported for reads; operators should avoid two writers from different processes performing restore at the same time (restore takes an exclusive lock).

## 5. Authorization model

Authentication answers *who*. Authorization answers *what*.

Every resource access goes through `authz.Allow(device, action, resource)`.

Roles:

| Role | Meaning |
| --- | --- |
| `admin` | Device and server administration. Not granted to DAV app passwords. First local init creates a local admin token, not a network-admin device. |
| `agent` | Push/pull on collections the operator selected. Typical macOS agent. |
| `client` | Push/pull on granted collections. |
| `dav` | Protocol-only, scoped. |

Actions: `sync.read`, `sync.write`, `dav.read`, `dav.write`, `device.list`, `device.revoke`, `metrics.read`, `backup.create`, `backup.restore`.

Collection ACL:

- `*` means all current and future collections (agents may have this)
- explicit ULID list otherwise
- tombstoned collections remain in the ACL until the tombstone is itself reconciled; access to tombstones is read-only

Deny by default. A revoked device fails authentication, so authorization is never reached.

## 6. Encryption model

### Transport

TLS 1.2+ (stdlib defaults, HTTP/1.1 and HTTP/2) is required for any non-loopback listener. Loopback may disable TLS only via `tls.allow_insecure_loopback=true` (the init default for `127.0.0.1`). There is no config flag that disables TLS on `0.0.0.0`.

Tailscale does not replace this. If a TLS terminator exists in front, the server should still bind loopback and treat the terminator as part of the host trust domain.

### At rest

- Master key: 32 random bytes, `0600`.
- Per-purpose keys via HKDF-SHA-256:
  - `nubilo-blob-v1` for blob encryption
  - `nubilo-backup-v1` for backup wrapping (further wrapped by a backup passphrase)
- Blobs: `nonce (12) || ciphertext || tag (16)` using ChaCha20-Poly1305. Nonce is random per write; key+nonce reuse is a fatal invariant checked in tests.
- Content addressing uses SHA-256 of **plaintext**. Two identical photos collapse to one blob even if encrypted under different nonces (the store writes once per hash).
- Private keys on clients: `0600` file under the client data dir. macOS Keychain wrapping is not implemented; file permissions remain the control.
- macOS agent map (`agent.db`) is a local EventKit/Contacts identifier ↔ object_id table, not server identity. Object IDs remain ULIDs.
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
| `pair/begin` | 10 / hour / IP; 3 active sessions globally |
| `pair/complete` | 20 / hour / IP |
| App-password failures | 5 / minute / device then 30s cooldown |
| Signed-request failures | 30 / minute / IP |
| Request body | `sync.max_blob_bytes` (default 64 MiB) plus 1 MiB headroom |
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
5. No built-in multi-user isolation. This is a single-owner personal cloud. All devices belong to one operator.
6. Availability is not a security guarantee. An attacker who can DoS the listen port can prevent sync. They should not be able to read or write data.
7. Client private keys are files mode `0600`, not OS keychain items.
8. PhotoKit in-place edits on already-mapped assets are not detected until a future `mod_ms` map field.

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
| Signed-request body cap (33 MiB) below photo max (64 MiB) | Authenticator `MaxBody` follows `sync.max_blob_bytes` |
| Admin token compared with `!=` | SHA-256 then constant-time compare; loopback-only |
| `pair/complete` had no IP rate limit | `pairing.completes_per_hour` (default 20) |
| Signed-request failures unthrottled | 30 / minute / IP |
| DAV password gate lacked the documented 30s cooldown | 5 failures then 30s lock |
| SECURITY.md still said DAV adapters were unimplemented | Corrected |
| SQLCipher | Evaluated; not adopted (see §6) |
| `golang.org/x/image` decode CVEs | Upgraded to v0.45.0; `govulncheck ./...` reports none in this code |

Fuzz seeds cover authorization parsing, canonical strings, pairing-code normalization, device names, photo inspect, and sync JSON push. Storage corruption, WAL reopen, backup tamper, and photo restore drills are tests. `nubilo gc [--apply]` collects unreferenced blobs and compactable tombstones.
