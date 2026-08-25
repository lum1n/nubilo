# Nubilo Architecture

Nubilo is a single-binary personal cloud. The same executable runs as a Linux server, a macOS agent, a generic client, and an administration CLI. Calendar, contacts, files, and photos are adapters around one sync engine and one storage layer.

```text
                  Sync Engine
                       │
       ┌───────────────┼────────────────┐
       │               │                │
     Files           Photos          Calendar / Contacts
       │               │                │
    WebDAV          HTTP/API          CalDAV / CardDAV
       │               │                │
       └───────────────┼────────────────┘
                       │
                    Storage
                 (SQLite + blobs)
```

The binary name is `nubilo`. Command examples in the original requirements that used `personal-cloud` map 1:1 onto `nubilo`.

```text
nubilo server
nubilo agent
nubilo client
nubilo init | status | pair | devices | verify | gc | backup | restore
```

## Design priorities

1. Security
2. Data integrity
3. Correct synchronization
4. Standards compatibility
5. Reliability
6. Simple deployment
7. Performance

Feature count is subordinate to those priorities. If a feature would weaken authentication, authorization, integrity, or crash safety, it is not implemented.

## Runtime modes

| Mode | Typical host | Role |
| --- | --- | --- |
| `server` | Always-on Linux or macOS | System of record. Serves sync protocol, HTTP API, CalDAV, CardDAV, and WebDAV. OS-agnostic. |
| `agent` | macOS | Trusted connector. Reads EventKit / PhotoKit / Contacts / selected files and pushes into the server over the sync protocol. Never stores corporate credentials on Linux. |
| `client` | Linux or macOS | Generic sync client for files and photos without macOS frameworks. |
| CLI | Either | Local administration, pairing, verify, gc, backup, restore. |

All modes share the same packages. Platform-specific APIs are isolated behind build tags (`darwin` for EventKit/PhotoKit). Linux builds of `nubilo agent` refuse to start with an explicit error. `nubilo server install` / `nubilo agent install` register user-level always-on services (systemd `--user` on Linux; LaunchAgent on macOS). Agent install is macOS-only and runs the binary from `~/Applications/Nubilo.app` for TCC attribution. `nubilo setup` / `nubilo doctor` (and the UI health panel) guide first-run hardening: auto-backup, disk encryption checks, and pairing. Agent signing keys live in the macOS Keychain.

## Trust and deployment shape

Intended production topology:

```text
Corporate services
      │
      ▼
    macOS (agent)
      │  EventKit / PhotoKit / permitted APIs
      ▼
Nubilo Agent
      │  TLS + device signatures + encrypted blobs
      ▼
Linux Nubilo Server
      │
      ├── CalDAV
      ├── CardDAV
      ├── WebDAV
      ├── HTTP API
      └── Sync Engine
      │
      ▼
iPhone / Mac / other clients
```

Tailscale is the expected *network* path. It is not an authentication system, not an authorization system, and not encryption-at-rest. See [SECURITY.md](SECURITY.md).

The server default listen address is loopback. Binding beyond loopback is `init --listen 0.0.0.0:8443` (or editing `config.json`). TLS certificates are created automatically (`tls.auto`); operators do not generate them by hand.

## Core subsystems

```text
cmd/nubilo          entrypoint
internal/config     load/validate configuration
internal/crypto     keys, hashing, blob encryption
internal/ids        ULID generation
internal/store      SQLite metadata + filesystem blobs
internal/identity   devices, pairing, credential lifecycle
internal/auth       request authentication, replay cache
internal/authz      per-device, per-collection authorization
internal/syncengine generic objects, journal, cursors, conflicts
internal/protocol   versioned /sync/v1 HTTP mapping
internal/server     HTTP server, TLS, rate limits
internal/service    user-level always-on install (LaunchAgent / systemd --user)
internal/integrity  verify command
internal/backup     encrypted backup and restore
internal/audit      structured audit events
```

Protocol adapters (not implemented until the sync engine and security model are stable):

```text
internal/dav        WebDAV / CalDAV / CardDAV adapters
internal/photos     image metadata, derivatives, PhotoKit mapping
internal/agent      macOS EventKit / Contacts / PhotoKit
```

## Object model (summary)

Everything that synchronizes is a **collection** of **objects**.

- Object IDs are ULIDs. They are stable and independent of filesystem paths, CalDAV hrefs, or PhotoKit local identifiers.
- Every live object has a monotonically increasing per-object `revision`, a `content_hash`, and a `metadata_hash`.
- Deletes are **tombstones**, not silent removals.
- Large payloads live in a content-addressable **blob store** keyed by SHA-256 of plaintext.
- Metadata and the change **journal** live in SQLite.
- Clients follow a **cursor** into the journal and ACK only after applying a batch.

Full rules: [SYNC.md](SYNC.md).

## Storage

```text
$data_dir/
  config.json
  master.key          mode 0600; 32-byte root key
  metadata.db         SQLite (WAL)
  blobs/              encrypted content-addressable objects
  tmp/                crash-discardable write staging
  logs/
```

SQLite is the source of truth for:

- devices and pairing state
- collections, objects, revisions, tombstones
- change journal and client cursors
- blob references and reference counts
- idempotency keys
- audit metadata (no secrets, no payloads)

The filesystem is the source of truth for blob bytes. A blob file may exist without metadata (orphan; recoverable by `verify` / GC). Metadata must never point at a missing blob after a committed transaction.

Write order for a new object:

1. Encrypt and atomically write the blob (temp file, fsync, rename).
2. In one SQLite transaction: insert/update object, insert journal row, increment blob refcount.
3. ACK to the client only after commit.

Never: write metadata, delete the old blob, then write the new blob.

## Authentication split

Native Apple CalDAV/CardDAV/WebDAV clients cannot present Ed25519 request signatures. Nubilo therefore has two client classes, both first-class in the device table:

1. **Signing devices** (agent, client, CLI talking to a remote server). Identity is an Ed25519 keypair. Every HTTP request is signed. Replay is rejected via timestamp window + nonce cache.
2. **Protocol devices** (iPhone Calendar, macOS Finder, etc.). Identity is still a row in `devices`. The credential is a high-entropy **app password** stored as Argon2id. The password is scoped to DAV protocols and selected collections. It is not an admin credential.

There is no global password. Revoking one device does not rotate any other device.

Details: [SECURITY.md](SECURITY.md).

## HTTP surface

Authenticated unless noted:

```text
POST /api/v1/pair/begin      unauthenticated, rate-limited
POST /api/v1/pair/complete   unauthenticated, rate-limited
GET  /api/v1/status
GET  /api/v1/devices
POST /api/v1/devices/{id}/revoke     admin
POST /api/v1/devices/{id}/rename     admin
POST /api/v1/devices/{id}/rotate     the device itself or admin
GET  /api/v1/metrics                 admin or loopback

GET  /api/v1/photos
POST /api/v1/photos
GET  /api/v1/photos/{id}
GET  /api/v1/photos/{id}/original
GET  /api/v1/photos/{id}/preview
GET  /api/v1/photos/{id}/thumb

POST /sync/v1/hello
POST /sync/v1/collections
POST /sync/v1/changes
POST /sync/v1/push
POST /sync/v1/ack
POST /sync/v1/reconcile

/dav/                        WebDAV (HTTP Basic, app password)
/caldav/                     CalDAV
/carddav/                    CardDAV
```

WebDAV display names are URL path segments for Finder/iOS Files. Object identity remains a ULID in the sync engine; paths are never joined onto the blob filesystem. LOCK/UNLOCK are answered for Apple client compatibility and are not exclusive locks.

Administrative mutation is not available to ordinary devices. There is no unauthenticated admin endpoint.

The photo API is an adapter over the same store. Signing devices use Ed25519; protocol devices may use an app password scoped to `photos`.

## Configuration

JSON file generated by `nubilo init`. Override with flags and `NUBILO_*` environment variables.

Secure defaults:

- listen on `127.0.0.1:8443`
- TLS required for any non-loopback bind
- pairing codes expire in 5 minutes and are single-use
- structured logs at `info`, with payload/PII fields disabled
- blob encryption enabled
- no open CORS, no directory listing, no debug pprof on the public mux

## Dependencies

Keep the set small and mature:

| Dependency | Why it exists |
| --- | --- |
| `modernc.org/sqlite` | Pure-Go SQLite; no CGO, same binary on Linux and macOS |
| `golang.org/x/crypto` | Argon2id, HKDF, ChaCha20-Poly1305 |
| `github.com/oklog/ulid/v2` | Stable, time-ordered IDs |
| `github.com/emersion/go-webdav` | WebDAV / CalDAV / CardDAV adapters |
| `golang.org/x/image` | Resize/decode for photo derivatives (JPEG/PNG/GIF/WebP/TIFF/BMP) |

Stdlib covers HTTP, TLS, `log/slog`, testing, and `crypto/ed25519`.

CLI uses a small stdlib command dispatcher rather than Cobra/Viper, to avoid a large dependency tree on a security-sensitive binary.

HEIC/HEIF originals are stored byte-for-byte. Linux does not decode HEIC for derivatives; the macOS agent still uploads the original. JPEG derivatives are produced when the original can be decoded.

## Crash and interruption model

Every mutating path must survive:

- process kill during blob write
- process kill during SQLite commit
- client disconnect mid-batch
- client crash after applying changes but before ACK
- server crash after commit but before response
- restore of a client from backup (stale cursor, duplicate device key)

Rules:

- Blob writes are atomic rename after fsync.
- Journal and object metadata change in one SQLite transaction.
- Push is idempotent via `idempotency_key`.
- Clients may replay a push or a changes fetch safely.
- ACK is client-side: the server may resend journal entries; applying them must be idempotent (object ID + revision).
- `nubilo verify` is the operator's consistency check.
- `nubilo gc --apply` deletes unreferenced blobs (including leftover PUT tmp that made it into the blob dir with a row and zero live refs) and compactable tombstones.

## What this repository implements now vs later

See [IMPLEMENTATION.md](IMPLEMENTATION.md).

**Phases 1–8 are implemented:** foundation, the generic sync engine with `/sync/v1`, WebDAV at `/dav/`, CalDAV at `/caldav/`, CardDAV at `/carddav/`, the macOS EventKit/Contacts agent, photos (`/api/v1/photos` plus PhotoKit), and hardening (`verify`, `gc`, fuzz, corruption, backup drills). Treat as suitable for important personal data only after the operator has run `verify`, taken an encrypted backup that restored cleanly, and put LUKS (or equivalent) under `$data_dir`.
