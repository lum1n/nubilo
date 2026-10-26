# Nubilo as an iCloud replacement for families and small teams

## Context

Nubilo today is a single-operator personal cloud (`README.md:3`, `GAPS.md:75`, `SECURITY.md:394`). The user wants it to replace iCloud for a family or workplace: calendar, contacts, reminders, files and photos per user, with sharing, privacy first, and setup that happens from a phone instead of the CLI. Exploration showed the only isolation unit is the *device*; there is no user, owner or principal anywhere (schema, authz, DAV URLs, photos library, journal). The sibling project `../wired` (`/home/vegard/repos/wired`) is a self-hosted encrypted overlay on iroh with a Go binding; it is a transport, not a sync or identity layer.

Decisions already made with the user:
- Home server stays closed to the internet; a small VPS runs a wired relay that forwards TCP 443/80 to the home server, which terminates TLS itself with Let's Encrypt. Direct public hosting (port forward or VPS) with the same built-in ACME is also supported. LAN + self-signed stays as fallback.
- Server-side encryption (not end-to-end). Apple CalDAV/CardDAV need plaintext on the server anyway.
- Server runs on a Mac at home (macOS app bundles it) *and* on Linux/VPS (one command prints a QR).
- iPhone calendar/contacts/reminders stay **native CalDAV/CardDAV accounts** (Apple syncs in the background); the app installs them via a `.mobileconfig`. Photos: best-effort background camera-roll backup plus in-app gallery and shared albums. Files: WebDAV in Files app first, File Provider later.

**One deviation from the earlier answer:** per-user *blob keys* are dropped. Blobs are content-addressed and deduplicated by plaintext hash (`internal/store/blobs.go:15`), which cannot work with a different key per user, and moving blobs into per-user directories would be the riskiest migration in the plan. Instead blobs stay global under the existing master-derived key and ownership is tracked in a `blob_refs(user_id, hash)` table. Per-user keys can come later as an optional "crypto-shred on user delete" feature.

Honest limits to keep in mind: iOS third-party apps cannot match iCloud Photos' always-on sync (Background Tasks + background `URLSession` only), and Apple's native apps cannot use an in-app wired tunnel, so CalDAV/CardDAV must reach the server over plain HTTPS with an Apple-trusted cert.

## Phases

Each phase is shippable and tested on its own. A is the foundation; B makes it reachable; C is the app; D is polish.

---

## Phase A — Multi-user core (backend, Go)

### A1. Migration framework
- `internal/store/store.go:57-71` seeds `schema_meta` version 1 and never reads it. Add `internal/store/migrate.go`:
  `type migration struct{ Version int; Name string; Up func(ctx, *sql.Tx, *Store) error }`, `func (s *Store) Migrate(ctx) error` (reads `MAX(version)`, runs each pending migration in `WithWrite`, inserts the version row). `Open` calls it; fresh DBs take the same path.
- Reuse the column-exists / `ALTER TABLE` pattern from `internal/agent/idmap.go:77-118`.
- `store.Open` gains the master key (currently receives only the derived blob key) so the migration can wrap keys later; update `internal/app/app.go` and the test harnesses (`server/server_test.go:28`, `dav/*_test.go`, `ui/ui_test.go:23`, `syncengine/engine_test.go:26`, `agent/agent_test.go:270`).

### A2. Schema v2
```sql
CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
  role TEXT NOT NULL /*admin|member*/, status TEXT NOT NULL DEFAULT 'active', created_at INTEGER NOT NULL);
ALTER TABLE devices ADD COLUMN user_id TEXT REFERENCES users(id);
ALTER TABLE app_passwords ADD COLUMN user_id TEXT REFERENCES users(id);
ALTER TABLE app_passwords ADD COLUMN label TEXT NOT NULL DEFAULT '';
ALTER TABLE pairing_sessions ADD COLUMN user_id TEXT;
ALTER TABLE collections ADD COLUMN owner_user_id TEXT REFERENCES users(id);
ALTER TABLE collections ADD COLUMN root_id TEXT;          -- = id for roots; children inherit
ALTER TABLE audit ADD COLUMN user_id TEXT;
CREATE TABLE shares (collection_id TEXT NOT NULL, principal_type TEXT NOT NULL /*user|group*/, principal_id TEXT NOT NULL,
  access TEXT NOT NULL /*ro|rw*/, created_by TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (collection_id, principal_type, principal_id));
CREATE TABLE invites (id TEXT PRIMARY KEY, code_hash BLOB NOT NULL, code_salt BLOB NOT NULL, role TEXT NOT NULL, user_id TEXT,
  max_uses INTEGER NOT NULL DEFAULT 1, uses INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL, created_by TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE blob_refs (user_id TEXT NOT NULL, hash TEXT NOT NULL, PRIMARY KEY (user_id, hash));
CREATE TABLE collection_prefs (user_id TEXT NOT NULL, collection_id TEXT NOT NULL, color TEXT, sort_order INTEGER, PRIMARY KEY (user_id, collection_id));
CREATE TABLE profile_tokens (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, payload BLOB NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER);
CREATE TABLE upload_tokens (id TEXT PRIMARY KEY, device_id TEXT NOT NULL, hash BLOB NOT NULL, expires_at INTEGER NOT NULL);
DROP TABLE blob_uploads;   -- written at server.go:503, never read
CREATE UNIQUE INDEX collections_owner_name ON collections(owner_user_id, kind, COALESCE(parent_id,''), name) WHERE deleted_at IS NULL;
CREATE INDEX shares_principal ON shares(principal_type, principal_id);
CREATE INDEX collections_root ON collections(root_id);
```
Data migration (only when `devices` or `collections` is non-empty): create user `owner` (admin); set `user_id`/`owner_user_id` on all devices, app passwords, collections; fill `root_id` with a recursive CTE over `parent_id`; insert `blob_refs(owner, hash)` for every blob row; copy calendar `metadata.color/order` into `collection_prefs(owner, …)`. Blob files do not move.

### A3. Identity: users, invites, user-bound pairing
Files: `internal/identity/identity.go`, `password.go`, new `users.go`, `invites.go`.
- `Device` gains `UserID`. `Permissions.Collections` is no longer consulted (keep the JSON field for old rows).
- `CreateUser`, `GetUser`, `GetUserByName`, `ListUsers`, `SetUserStatus/Role`.
- `CreateInvite(ctx, by, role, forUserID, ttl, maxUses) (code, Invite)` and `RedeemInvite(ctx, code, RedeemRequest)` which creates the user when the invite has no `user_id`, then runs `StartPairing`+`Begin` in one transaction. Code format `nbi-` + 20 base32 chars, argon2-hashed like pairing codes (`identity.go:128-163`).
- `StartPairing(ctx, userID, role)`: session stores `user_id`; `MaxActive` counted per user. `Complete` copies it into `devices.user_id`. `Enroll(ctx, userID, …)`.
- `CreateAppPassword(ctx, userID, label, scope)` replaces `CreateDAVDevice` (`password.go:20`). Cap 10 per user.
- `AuthenticateBasic(ctx, username, password)`: if the username parses as a ULID keep the old device-ID path (existing iPhone accounts keep working); else resolve the user and verify against their active app passwords. Add a 10-minute success cache keyed by `sha256(username|password)` in `internal/dav/auth.go` because Apple sends Basic on every request.

### A4. Authorization and engine isolation
Files: `internal/authz/authz.go`, `internal/syncengine/{engine.go,lookup.go,types.go}`, new `syncengine/access.go`, `internal/store/blobs.go`.
- `Access(ctx, userID, collectionID) (AccessNone|Read|Write|Owner)` and `accessFilterSQL`: owner, or a share on `root_id`. Disabled users get None.
- `authz.Allow(d, action)` keeps protocol/role checks; new `authz.AllowCollection(d, action, access)` (read ≥Read, write ≥Write, delete-collection/share = Owner). Remove the `collectionID` parameter from the 15 call sites in `server/server.go` and `server/photos.go` that always pass `""`.
- `Collection` gains `OwnerUserID`, `RootID`, `Access` (computed on list). `GetCollections(ctx, userID)`, `ChildCollections(ctx, userID, kind, parent)` (roots = user's own), new `SharedRoots(ctx, userID, kind)`, `CollectionCTags(ctx, userID, kind)`. Update callers listed at `dav/caldav.go:85`, `carddav.go:85`, `fs.go:157`, `color.go:186,207`, `ui/handlers.go:32,294,545`, `cli/cli.go:1263,1311,1359`.
- `Create/EnsureNamedCollection/EnsureChildCollection` take `ownerUserID`; children inherit owner and `root_id`.
- `GetChanges` (`engine.go:132-200`): join `collections` with `accessFilterSQL` **before** `LIMIT`; delete the post-filter loop. `Hello` adds `collections_rev` so clients skip polling `/sync/v1/collections` when unchanged. Collection-level ops stay unjournaled (clients reconcile newly visible collections).
- `applyOne` (`engine.go:266-462`): require ≥Write on the target collection **and** on the object's current collection for update/delete (fixes the cross-collection move hole at line 393); both must share an owner.
- Idempotency key stored as `<device_id>:<key>` (`engine.go:246-264`).
- Blobs: `ensureBlob` (`engine.go:499-512`) requires a `blob_refs` row for the collection owner; `PutBlob` inserts `blob_refs(dev.UserID, hash)`; `GET /sync/v1/blob/{hash}` (`server.go:507`) requires a ref for the caller's user or ≥Read on a collection referencing it. GC removes `blob_refs` with the blob.
- `operator.go` synthetic admin device gets a `UserID` ("act as") so UI writes land in a user.

### A5. Server API
Files: `internal/server/server.go`, `photos.go`, new `admin.go`, `me.go`, `profile.go`.
Existing: `handleEnsureCollection` creates under `dev.UserID`; `handleDevices` lists only the caller's user unless admin `?all=1`.

| Method / path | Auth | Body → Response |
|---|---|---|
| `POST /api/v1/invites/redeem` | none, IP-limited | `{code, username?, display_name?, device_name, public_key}` → `{pairing_id, challenge, server_id, user}`; then existing `pair/complete` |
| `GET /api/v1/me` | any | `{user, device, storage, urls:{caldav,carddav,webdav,photos}}` |
| `GET/POST /api/v1/me/passwords`, `DELETE …/{id}` | any | `{label, scope}` → `{id, username, password}` (plaintext once) |
| `POST /api/v1/me/profile` | any | `{services:[caldav,carddav]}` → `{url, expires_at}` single-use |
| `GET /api/v1/profile/{token}` | none | `application/x-apple-aspen-config`, burns token |
| `POST /api/v1/me/upload-token` | any | → `{token, expires_at}` (24 h; valid only for blob PUT + photos commit) |
| `GET /api/v1/collections` | any | accessible collections with `owner`, `access`, `shares` (owner only) |
| `GET/PUT/DELETE /api/v1/collections/{id}/shares` | owner | `{principal_type, principal_id, access}` |
| `GET /api/v1/users` | any | `{id, username, display_name}` directory for share pickers |
| `GET/POST /api/v1/admin/users`, `PATCH /{id}` | admin | create / role / status |
| `GET/POST /api/v1/admin/invites`, `DELETE /{id}` | admin | → `{id, code, url, expires_at}`; `url = https://<domain>/join#<code>` for QR |
| `GET /api/v1/admin/stats` | admin | per-user storage, objects, devices |
| `POST /api/v1/admin/devices/{id}/revoke` | admin | cross-user revoke |

`/api/v1/status` gains `{api_version, server_name, domain, features}`. Audit events carry `user_id` (`internal/audit`).

### A6. DAV per-user layout
Files: `internal/dav/{caldav.go,carddav.go,compat.go,color.go,fs.go,auth.go}`, `server/server.go:90-93`.
```
/.well-known/caldav  → (inside auth middleware) 301 to /caldav/u/<username>/
/caldav/u/<username>/                          principal
/caldav/u/<username>/calendars/                home: own + shared
/caldav/u/<username>/calendars/<colID>/<uid>.ics
/carddav/u/<username>/addressbooks/<colID>/<uid>.vcf
/dav/files/<name>/…                            own files roots
/dav/shared/<owner-username>/<name>/…          shared files roots
```
- `calUserSeg`/`cardUserSeg` become `u`; `parseCalPath` (`caldav.go:424-454`) parses `u/<username>/calendars/<colID>`; username must match the authenticated device's user, else 403. Hrefs at `caldav.go:136,284,420,465` and `carddav.go:147,285,401,439` use `col.ID` (stable, no collisions with shared calendars; Apple shows `displayname`). Shared calendars display as `"<Name> (<owner>)"`.
- Create/delete calendar requires Owner; PUT/DELETE objects require ≥Write. PROPFIND injects `current-user-privilege-set` (read-only shares expose only `<D:read/>` so Apple shows the lock) and color/order from `collection_prefs` for non-owners (`color.go:182-201`); PROPPATCH (`color.go:100-153`) writes metadata for owners, prefs for others.
- `fs.go` resolve (`63-120`): root lists `files` and `shared`; write ops on `ro` shares → 403. Camera Upload ingest (`fs.go:337-341`) targets the uploader's photo library.
- `dav/auth.go` middleware → `AuthenticateBasic`; fail gate keyed by username and IP.

### A7. Photos per user, albums
Files: `internal/photos/service.go`, `internal/server/photos.go`.
- `Library(ctx, userID)` replaces the global `Collection()` (`service.go:41`). `Ingest` writes into the caller's library. `List(ctx, userID, after, limit)` with paging. `Get` checks ≥Read on the object's collection.
- New: `POST /api/v1/photos/check {hashes}` → `{missing}`; `POST /api/v1/photos/commit {blob_id, name, taken_at, local_id, live_video_blob?}` → ingests from an already-uploaded blob (derivatives generated server-side); both accept the upload token.
- Albums: root collection `kind=album`; items are objects `kind=album_item` whose metadata lists blob hashes (refcounted via `ensureExtraBlobs`, `engine.go:464`); shares via the generic shares endpoint. `GET/POST /api/v1/albums`, `GET/POST /api/v1/albums/{id}/items`. Thumb/preview endpoints accept album items.

### A8. Agent, UI, CLI
- `internal/agent/sync.go ensureCollection` (`1316-1331`) prefers collections owned by the agent's user; `device.json` stores `user_id` (returned by `pair/complete`); `bindRoutes` reconciles collections that newly appear. `protocol/client.go` decodes the new fields.
- `nubilo ui` stays an admin console: new Users tab (list, create, disable, invites with QR via `github.com/skip2/go-qrcode`, per-user storage) and an act-as user switcher (`X-Nubilo-Act-As` header set on the operator device). `handleDevicePassword` (`ui/handlers.go:244-285`) moves to `CreateAppPassword`.
- CLI: `nubilo users {list,add,disable,invite}`, `nubilo pair --user`, `nubilo devices password --user`.

### A9. Verification
- `go test ./...` plus new tests: `store/migrate_test.go` (v1 fixture → v2, owner owns everything, `nubilo verify` clean), `syncengine/access_test.go` (owner/ro/rw/none, child inheritance, cross-owner move rejected, full pages when another user has 1000 changes), `server/isolation_test.go` (user B cannot read A's blobs, push into A's collections, list A's photos or devices; rw share allows push; ro share rejects), `dav/caldav_test.go` (per-user principal, shared calendar in home, PUT on ro → 403, privilege-set XML), `dav/fs_test.go` (`/dav/shared/<owner>/`), `identity/invites_test.go` (redeem creates user, max_uses, expiry).
- Manual: two iPhones with CalDAV accounts as `alice` and `bob`; share Alice's calendar read-only; Bob sees it with a lock and cannot edit. Existing ULID-username accounts keep working after migration (calendars re-download once because hrefs changed).

---

## Phase B — Setup and reachability

### B1. wired integration (`internal/wiredx`)
- `node.go` interface (`Listen`, `Dial`, `Invite`, `Members`, `Revoke`, `RotateInvite`, `Path`, `Close`); `node_wired.go` (`//go:build !nowired`) wraps `github.com/lum1n/wired/bindings/go` (`Join`, `Anchor`, `Listen` returns a `net.Listener`, `Dial`); `node_stub.go` returns `ErrUnavailable`.
- `forward.go`: `Forward(ctx, ln net.Listener, node, target)` — accept, `Dial(target)`, `io.Copy` both ways with half-close. The Go binding has no `Forward`/`Expose` (only the CLI does), so this is ours.
- Build: the binding declares `go 1.26` (Nubilo is `1.25.0`); bump Nubilo. Release script builds `libwired_ffi.a` via `bindings/go/scripts/build.sh` for linux amd64/arm64 and darwin arm64 (only `linux_amd64` is checked in today). Dev and CI default to `-tags nowired`; `nubilo version` prints `wired: yes/no`. Fallback if cgo on macOS becomes painful: spawn the `wired` CLI as a sidecar.

### B2. Config, ACME, listeners
Files: `internal/config/config.go`, `internal/app/app.go`, `internal/server/server.go:116-128`, `internal/cli/cli.go:298`, new `internal/server/tlsconf.go`, `internal/acme/acme.go`.
- Config: `Public{Domain, ACME{Enabled, Email, Directory}, Relay{Invite, Name, StateDir}, DirectListen}`; paths `dataDir/acme`, `dataDir/wired`.
- `golang.org/x/crypto/acme/autocert` with `HostWhitelist(domain)`, `DirCache`; TLS-ALPN-01 primary (raw TCP forward preserves ALPN), HTTP-01 fallback over the forwarded `:80`.
- `tlsconf.Build`: `GetCertificate` serves the ACME cert when SNI matches the domain, else the existing self-signed cert (`internal/crypto/tls.go GenerateTLS`).
- Listeners: LAN (`cfg.Listen`, always); `DirectListen` `:443` + `:80` when the host is public; wired `node.Listen("https")` wrapped in `tls.NewListener` plus `node.Listen("http")` when `Relay.Invite` is set. Served concurrently with an errgroup.

### B3. `nubilo relay` (VPS)
Files: new `internal/cli/relay.go`, `internal/relay/relay.go`, systemd unit `dev.nubilo.relay` in `internal/service`.
- `nubilo relay init --public-host <ip|dns>` → wired root anchor with public relay on 7443, prints invite as text and terminal QR, writes `relay.json {forward:[":443"→"https@home", ":80"→"http@home"]}`.
- `nubilo relay run|install|invite --rotate|members|revoke <name>`.
- The VPS holds no master key and no DB; it sees TLS ciphertext only. Ports: UDP/TCP 7443, UDP 7842, TCP 80/443. `scripts/install-relay.sh` one-liner.

### B4. `nubilo setup` wizard with QR
Files: `internal/cli/setup_doctor.go:65`, `internal/setup/setup.go`, new `internal/setup/qr.go`.
1. Init data dir (existing `EnsureServerInitialized`).
2. "How will devices reach this server?" → `relay` (paste invite) | `direct` | `lan`.
3. Domain + ACME email; for relay, join once to validate and print the path kind.
4. Install service (existing `InstallServerService`), wait for `https://<domain>/api/v1/status` end to end, report cert issuer.
5. Create a 24 h admin invite and print the QR `nubilo://setup?url=https://<domain>&invite=<code>&fp=<server pubkey>`.
`--json --step=<n>` keeps it drivable by the Mac app.

### B5. Doctor and security
- `internal/doctor`: `dns`, `relay` (path direct/relayed/none), `cert` expiry, `public_reach`, `ports`.
- Limits: DAV Basic fail gate per username **and** per IP; invites/redeem 5/hour/IP; `Strict-Transport-Security` on public listeners.
- `SECURITY.md`: "Public exposure" (VPS can deny service, cannot read), "Multi-user isolation invariants" (A4 list), invite and upload-token lifecycle, `.mobileconfig` token model.

### B6. Verification
- `go test -tags nowired ./...` in CI; `go test ./internal/wiredx` with the Rust toolchain locally.
- Relay smoke test, documented in `docs/RELAY.md`: VPS `relay init && relay run` → home `nubilo setup` → from a phone on LTE `curl https://<domain>/api/v1/status` returns 401 with a Let's Encrypt chain; `nubilo doctor` green; stop the relay → doctor reports `relay: unreachable`; LAN URL still works. Run against LE staging first.

---

## Phase C — iOS/macOS app (SwiftUI, one target)

Layout: `apps/Nubilo/` Xcode project (iOS 17+, macOS 14+), Swift packages `NubiloKit` (API client, signing, models) and `NubiloUI`.

### C1. Core client and onboarding
- Ed25519 device key in Keychain (`AfterFirstUnlockThisDeviceOnly`, CryptoKit `Curve25519.Signing`).
- `RequestSigner` reproduces `internal/auth/auth.go:104 Canonical`; a Go test writes JSON vectors into `apps/Nubilo/Tests/Fixtures`; document the canonical string in `docs/API.md`.
- Onboarding: scan QR (`nubilo://setup?…` or `https://<domain>/join#<code>`) → `invites/redeem` → sign challenge → `pair/complete` → TOFU-pin the server key.

### C2. Calendar, Contacts, Reminders, Files
- "Set up Calendar & Contacts" → `POST /api/v1/me/profile` → open the URL in Safari (iOS only installs profiles downloaded through Safari) → guide the user through Settings → Install. Payloads: `com.apple.caldav.account` (host, 443, SSL, username, password, principal `/caldav/u/<username>/`, description "Nubilo") and `com.apple.carddav.account`. Reminders ride on the CalDAV account. Same URL works on macOS.
- Files screen: WebDAV URL, username, copy-password, short "Files → Connect to Server" guide.

### C3. Photos backup and gallery
- PhotoKit enumeration with per-asset state (`localIdentifier, sha256, status, server_id`) in SwiftData.
- Pipeline: stream-hash → `photos/check` → background `URLSession.uploadTask(fromFile:)` `PUT /sync/v1/blob/<hash>` with the upload token (signed requests expire after 60 s, so they cannot be deferred by the OS) → `photos/commit`. Live Photo pairs upload the movie as a second blob.
- `BGProcessingTask` `dev.nubilo.photos.backup` (power/network options in settings); status screen with counts, last run, errors.
- Gallery: paged `GET /api/v1/photos`, `LazyVGrid` with thumb/preview, 500 MB `URLCache`; albums, shared albums, share picker from `/api/v1/users`.

### C4. Family and admin screens
- Admin: members, invite member (QR + share link), devices per user with revoke, storage per user.
- Member: my devices, app passwords, storage.

### C5. macOS extras
- `MenuBarExtra` mode: server status and start/stop (bundled `nubilo` binary, LaunchAgent via `internal/service`), agent status over the existing loopback agent API (`internal/ui/agent.go`, replaces `nubilo agent ui`), setup wizard driving `nubilo setup --json`. The app bundle replaces the TCC-only `Nubilo.app` from `internal/setup/setup_darwin.go`; extend `scripts/mac-sign.sh`.
- On Mac, Calendar/Contacts/Reminders use the same profile; the agent keeps Photos.app and folder sync. Agent calendar sync stays available but off by default.

### C6. Verification
- Swift signer tests against Go-generated vectors.
- Manual: fresh server → scan admin QR → paired; invite a member → second phone pairs; install profile → accounts appear; enable photo backup → 50 photos upload with airplane mode mid-way and resume; gallery shows them; shared album visible on the second phone.
- macOS: install app, run wizard with a relay invite, server reachable at the domain, Calendar.app via profile.

---

## Phase D — Polish (later)
File Provider extension over `/sync/v1`; app direct peer path via the wired Swift binding (bypasses the VPS); groups (`shares.principal_type='group'` already in the schema); per-user quotas; per-user web UI login; `.mobileconfig` CMS signing with the ACME cert; optional per-user crypto-shred keys; DNS check in the wizard.

## Risks and open questions
- **Href change to collection IDs** makes existing iPhone accounts re-download calendars once. Accepted for collision-free sharing; note in release notes.
- **Argon2 on every Basic request** with several app passwords per user: the success cache in A3 is mandatory.
- **wired is 8 days old**; the `nowired` build stays the tested default until release builds exist for all targets. TLS-ALPN-01 through the raw TCP forward must be verified on LE staging before relying on it.
- **Open:** should members be able to invite other members (multi-use links)? Default: admin-only, single-use. Should the agent's calendar sync be removed in favour of the profile? Default: keep, off by default.

## Order of work
A1 → A2 → A3 → A4 → A5 → A6 → A7 → A8 → A9, then B (B1 and B2 can run in parallel), then C. Phase A alone makes the server family-ready for anyone who still uses Tailscale or a LAN; B makes it reachable without exposing the home; C removes the CLI from the user's path.
