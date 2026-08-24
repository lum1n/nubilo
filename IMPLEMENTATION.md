# Implementation plan

Correctness and security over speed. CalDAV/WebDAV/CardDAV and the macOS agent are not started until Phase 1–2 are internally consistent and reviewed.

## Phase 1 — Foundation (this milestone)

- [x] Design: ARCHITECTURE.md, SECURITY.md, SYNC.md
- [x] Go module, CLI dispatcher, config, structured logging
- [x] SQLite metadata store (WAL, parameterized queries, busy timeout)
- [x] Encrypted content-addressable blob store with atomic writes
- [x] Master key + HKDF
- [x] Device table, pairing, revoke/rename/rotate
- [x] Request signatures + nonce replay cache
- [x] Authorization
- [x] Audit logging (redacted)
- [x] `nubilo verify`
- [x] `nubilo backup` / `nubilo restore` (encrypted, with `--verify`)

No CalDAV, CardDAV, WebDAV, EventKit, or PhotoKit in this phase.

## Phase 2 — Sync engine (this milestone)

- [x] Objects, collections, revisions, hashes, tombstones
- [x] Change journal and cursors
- [x] `/sync/v1` HELLO, GET_COLLECTIONS, GET_CHANGES, blob GET/PUT, PUSH, ACK, RECONCILE
- [x] Conflict detection, object history
- [x] Idempotent push
- [x] Tests: create/update/delete, concurrent updates, duplicate delivery, stale cursor, replay, partial batch, crash recovery, reconciliation

## Phase 3 — WebDAV

- [x] Adapter from engine objects to `github.com/emersion/go-webdav`
- [x] Path mapping via collection/file display names; object IDs remain ULIDs
- [x] ETags from content hashes; range reads via `io.ReadSeeker`
- [x] App-password HTTP Basic auth for Apple clients
- [x] Security tests: path traversal, oversized bodies, unauthenticated access, revoked passwords
- Finder / iOS Files: operator verification (mount `http(s)://host:port/dav/` with device id + app password)

## Phase 4 — CalDAV

- [x] Calendar collections as engine collections (`kind=calendar`)
- [x] VEVENT/VTODO payloads as blobs; UID in metadata; object IDs remain ULIDs
- [x] ETags from content hashes; calendar-query / calendar-multiget
- [x] Recurrence stored as the client sent it (query matching may expand in memory only)
- [x] App-password HTTP Basic auth scoped to `caldav`; `/.well-known/caldav`
- Verification: iOS/macOS Calendar (operator)

## Phase 5 — CardDAV

- [x] Address book collections as engine collections (`kind=addressbook`)
- [x] vCard payloads as blobs; UID in metadata; object IDs remain ULIDs
- [x] ETags from content hashes; addressbook-query / addressbook-multiget
- [x] App-password HTTP Basic auth scoped to `carddav`; `/.well-known/carddav`
- Verification: Apple Contacts (operator)

## Phase 6 — macOS agent

- [x] EventKit calendar selection; periodic reconcile (interval ticker; EK notifications not required for v1)
- [x] Contacts via the macOS Contacts framework
- [x] Client-side EventKit/Contacts identifier ↔ object_id map (`agent.db`); object IDs stay ULIDs
- [x] Failed local enumeration never PUSHes deletes; EventKit listing is a time window (default ±730 days)
- [x] Linux `nubilo agent` refuses to start; corporate credentials never stored on Linux
- [x] Pair as a signing device (`nubilo pair --role agent`); not a DAV app password

## Phase 7 — Images

- [x] Originals preserved (content-addressable blob; no transcoding)
- [x] Preview and thumbnail JPEG derivatives referenced from metadata
- [x] MIME, dimensions, EXIF orientation/camera/taken-at, checksum
- [x] GPS stays in the encrypted original; `photos.strip_gps_from_derivatives` (default true)
- [x] Hash dedup via SHA-256 of plaintext
- [x] Optional perceptual hash field (`photos.perceptual_hash`)
- [x] PhotoKit sources: all / albums / date ranges; failed enumeration never PUSHes deletes
- [x] HTTP API `/api/v1/photos` (signing device or `--scope photos` app password)

## Phase 8 — Hardening

- [x] Threat-model review against the running code
- [x] Dependency audit (`go.mod` / `govulncheck` when available)
- [x] Fuzz pairing, signatures, sync JSON, photo inspect
- [x] Storage corruption tests
- [x] Crash/recovery tests (WAL reopen, leftover tmp, idempotent push)
- [x] Permission tests
- [x] Backup restore drills (including photos + tamper reject)
- [x] SQLite-at-rest encryption evaluation (SQLCipher not adopted; LUKS remains the control)
- [x] Blob GC and tombstone compaction that keep photo derivative metadata refs

Host LUKS (or equivalent) under `$data_dir` remains an operator control, not something the binary can provide. After Phase 8, treat the system as suitable for important personal data only once `verify` is clean, an encrypted backup has been restored in a drill, and the volume is encrypted.

## Definition of done (production milestone)

Tracked against the original 20-point list. After Phase 2, items 3 (pairing), 16 (backup), 17 (verify), 18 (interrupted sync), 19 (no corporate credentials) have an implementation path. Items that need DAV/agent/photos remain open until their phase.

| # | Requirement | Earliest phase |
| --- | --- | --- |
| 1 | One binary on Linux | 1 |
| 2 | Reachability via Tailscale (operator) | 1 |
| 3 | Pair Mac securely | 1 |
| 4 | Sync selected macOS calendars | 6 (implemented; EventKit agent over `/sync/v1`) |
| 5–7 | iPhone CalDAV | 4 (implemented; `/caldav/` with app password) |
| 8–9 | Offline phone then reconnect | 2 + 4 |
| 10 | WebDAV Finder / iOS Files | 3 (implemented; mount `/dav/` with app password) |
| 11–13 | Images + PhotoKit | 7 (implemented; `/api/v1/photos` + macOS PhotoKit agent) |
| 14 | CardDAV | 5 (implemented; `/carddav/` with app password) |
| 15 | Revoke one device | 1 |
| 16 | Backup / restore | 1 |
| 17 | Integrity check | 1 |
| 18 | Interrupted sync recovery | 2 |
| 19 | No corporate creds on Linux | architecture + 6 (implemented; Linux agent refuses to start) |
| 20 | No iPhone app for Calendar/Contacts/Files | 3–5 (implemented) |

## Repository structure

```text
nubilo/
├── ARCHITECTURE.md
├── SECURITY.md
├── SYNC.md
├── IMPLEMENTATION.md
├── README.md
├── go.mod
├── go.sum
├── cmd/nubilo/main.go
├── internal/
│   ├── audit/
│   ├── auth/
│   ├── authz/
│   ├── backup/
│   ├── config/
│   ├── crypto/
│   ├── identity/
│   ├── ids/
│   ├── integrity/
│   ├── logging/
│   ├── agent/
│   ├── photos/
│   ├── protocol/
│   ├── server/
│   ├── store/
│   └── syncengine/
└── testdata/
```
