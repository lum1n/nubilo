# Nubilo Sync Engine

The sync engine is the product. CalDAV, CardDAV, WebDAV, the HTTP photo API, and the macOS agent are adapters that translate to and from this model. They must not keep their own notion of revision, cursor, or identity.

## Goals

- Incremental sync via a durable change journal
- Full reconciliation when a cursor is stale or a client is restored from backup
- Idempotent pushes and fetches
- Conflict *detection* before conflict *resolution*
- Crash safety: a kill in the middle of a batch cannot silently drop or duplicate committed objects
- Globally unique object IDs that are not filesystem paths

## Domain types

### Collection

A named container of objects of one kind.

```text
id              ULID (stable)
kind            files | calendar | addressbook | photos
name            display name
parent_id       optional ULID (album / folder)
revision        collection-level revision (changes when membership changes)
metadata        small JSON (color, sort order) — not payload
created_at
updated_at
deleted_at      null or tombstone time
```

### Object

A synchronizable item. Identity is `id`, never a filename or href.

```text
id              ULID
collection_id   ULID
kind            file | directory | event | contact | photo | album
revision        uint64, starts at 1, increments on every committed mutation
content_hash    SHA-256 of canonical payload bytes
metadata_hash   SHA-256 of canonical metadata bytes
blob_id         SHA-256 of plaintext payload (null for metadata-only)
size            plaintext size
origin_device   device_id that last committed a change
created_at
updated_at
deleted_at      null or tombstone time
metadata        small JSON (filename, MIME, width, height, uid for iCal UID, …)
```

`content_hash` covers the payload only. `metadata_hash` covers the canonical JSON of `metadata`. Adapters that need "did this ICS change" compare `content_hash`. Adapters that need "was it renamed" compare `metadata_hash`.

### Revision

Per-object counter. It is **not** the journal sequence. Two objects can both be at revision 4. Journal sequence 104 might be "object A revision 4".

A push must include `base_revision`:

- create: `base_revision = 0`
- update/delete: `base_revision` equals the revision the client last observed
- if server revision ≠ `base_revision`, the push of that item is a **conflict**

### Change

One journal row.

```text
seq             INTEGER PRIMARY KEY AUTOINCREMENT  (global, monotonic)
object_id
collection_id
op              create | update | delete
revision        object revision after the op
content_hash    after the op (empty for delete)
metadata_hash   after the op
device_id       who committed it
ts              unix ms
idempotency_key optional, from the originating push
```

### Tombstone

A deleted object remains a row with `deleted_at` set. The journal records `op=delete`. Clients that have never seen the object still receive the tombstone if their cursor is before that seq. `nubilo gc --apply` compact tombstones only after every *non-revoked sync* device cursor is past the delete seq. Devices with no cursor are treated as 0 (nothing is compacted). DAV-only devices are ignored because they do not ACK the journal.

### Device cursor

```text
device_id
scope           '*' or collection_id
last_ack_seq
last_ack_at
```

The cursor is **advanced only by ACK**, not by GET_CHANGES. If a client fetches 100–150, applies 100–140, crashes, and retries, GET_CHANGES(since=99) returns 100–150 again. Applying must be idempotent: "set object to revision N with hash H" is a no-op if already so.

## Canonical payload

Adapters define canonical bytes for `content_hash`:

| Kind | Canonical bytes |
| --- | --- |
| file / photo original | exact file bytes, no transcoding |
| event | ICS of the VEVENT stored as UTF-8, as written by the adapter (not pretty-printed differently on each write) |
| contact | vCard as stored |
| directory / album | empty payload, metadata only |

The engine does not parse ICS or vCard. Adapters must not change canonical bytes unless the user data changed.

## Blob store

```text
blobs/{hash[0:2]}/{hash[2:4]}/{hash}
```

`hash` is hex(SHA-256(plaintext)).

On disk the file is `nonce || ciphertext || tag`. Write path:

1. Create `$data_dir/tmp/<random>`
2. Write ciphertext, `fsync` file
3. `rename` into `blobs/...`
4. `fsync` parent directory
5. SQLite transaction: object + journal + `blob_refs.refcount++`

If crash before step 5: orphan blob. `verify` reports it; `nubilo gc --apply` deletes unreferenced blobs (including photo derivatives only after the photo object is tombstoned).

If crash during step 5: SQLite rolls back; blob may be orphan.

Never decrement refcount and delete a blob in the same window as inserting a new object without a transaction. Replacement: insert new blob, commit object pointing at new hash (refcount++ on new, -- on old), then GC.

## Journal invariants

1. `seq` is strictly increasing, never reused.
2. Every committed object mutation produces exactly one journal row.
3. For a given `object_id`, journal revisions are strictly increasing.
4. `op=create` appears at most once per `object_id` unless a future "reincarnation" protocol version is defined (it is not; do not recycle object IDs).
5. Reads of changes are `WHERE seq > :cursor ORDER BY seq LIMIT :n`.

## Conflict detection and resolution

Detection is mandatory. Resolution is policy.

A push item conflicts when:

- `op=create` and the ID already exists (and is not a tombstone we explicitly allow to resurrect — we do not)
- `op=update|delete` and server `revision != base_revision`
- `op=update` and server object is already tombstoned

Default policy (`conflict.policy=detect`):

- The conflicting item is rejected
- Other items in the same batch may commit (per-item results)
- The client is told `{object_id, server_revision, server_content_hash}`

Optional later policies (not default):

- `lww-timestamp` — last writer wins (dangerous for notes; acceptable for some calendar adapters if documented)
- `keep-both` — fork a new object ID for files

The engine never silently merges bytes. Adapters that want CalDAV "last PUT wins" must send `force=true` **and** be authorized; `force` still records a journal row and the previous hash in the object history table.

`object_history` keeps `(object_id, revision, content_hash, blob_id, device_id, ts)` for every committed revision so a force-write is not silent data loss.

## Protocol `/sync/v1`

The protocol does not expose SQL. Bodies are JSON. All endpoints require a signed device (or, later, will not be used by DAV — DAV talks to adapters, adapters talk to the engine in-process).

Versioning: the path is `/sync/v1`. Additive fields may appear. Removing/renaming fields requires `/sync/v2`. Clients send `protocol_min` / `protocol_max` in HELLO.

### HELLO

Request:

```json
{
  "protocol_min": 1,
  "protocol_max": 1,
  "device_name": "Studio Mac",
  "cursor": 99,
  "collections": ["*"]
}
```

Response:

```json
{
  "protocol": 1,
  "server_time_ms": 0,
  "head_seq": 102,
  "need_reconcile": false,
  "reason": ""
}
```

`need_reconcile` is true when:

- `cursor` is greater than `head_seq` (client from the future / wrong server)
- `cursor` refers to a gap that cannot be served (after future compaction)
- client device was restored and sends `restore_hint: true`

### GET_COLLECTIONS

Returns all collections the device may see, including tombstoned ones the client has not ACKed past.

### GET_CHANGES

Request:

```json
{
  "since_seq": 99,
  "limit": 200,
  "collection_id": ""
}
```

Response:

```json
{
  "changes": [
    {
      "seq": 100,
      "object_id": "...",
      "collection_id": "...",
      "op": "update",
      "revision": 12,
      "content_hash": "...",
      "metadata_hash": "...",
      "metadata": {},
      "blob_id": "...",
      "size": 1234,
      "deleted": false
    }
  ],
  "next_seq": 102,
  "has_more": false,
  "need_reconcile": false
}
```

Payload bytes are **not** inline in GET_CHANGES. The client fetches blobs with `GET /sync/v1/blob/{blob_id}` (authenticated, authorized if the device can see at least one object referencing that hash). Small metadata-only objects have `blob_id=""`.

This keeps calendar-query-sized batches from embedding 50MB photos.

### PUSH_CHANGES

Request:

```json
{
  "idempotency_key": "ulid-or-uuid",
  "changes": [
    {
      "object_id": "...",
      "collection_id": "...",
      "op": "update",
      "base_revision": 11,
      "content_hash": "...",
      "metadata_hash": "...",
      "metadata": {},
      "blob_id": "...",
      "size": 1234
    }
  ]
}
```

Blobs must already have been uploaded (`PUT /sync/v1/blob/{expected_sha256}`) or be omitted for metadata-only. The server verifies `SHA-256(plaintext) == blob_id` after decrypt-or-hash of the stored blob.

Response:

```json
{
  "results": [
    {"object_id": "...", "status": "ok", "revision": 12, "seq": 100},
    {"object_id": "...", "status": "conflict", "server_revision": 13, "server_content_hash": "..."}
  ]
}
```

Replaying the same `idempotency_key` returns the original results without applying twice.

Partial batch: committed items stay committed. The client retries only the failures with a new idempotency key.

### ACK

```json
{ "seq": 102 }
```

Server stores `cursors.last_ack_seq = MAX(old, 102)` for that device. ACK must not move backwards. ACK past `head_seq` is an error.

The client must ACK only after *applying* the changes (including blob writes). If it ACKs early, it can lose the ability to re-fetch after a local crash; reconciliation is the recovery path.

### RECONCILE

Client sends a compact inventory:

```json
{
  "collection_id": "...",
  "objects": [{"id": "...", "revision": 12, "content_hash": "..."}]
}
```

Server returns:

```json
{
  "missing_on_client": ["id"...],
  "missing_on_server": ["id"...],
  "mismatch": [{"id": "...", "server_revision": 14, "server_content_hash": "..."}]
}
```

`missing_on_server` is **not** treated as an implicit delete. The client must PUSH those objects. The server never deletes because a PhotoKit query temporarily returned empty (that rule lives in the agent adapter; the engine simply will not invent deletes).

## Interrupted sync

| Failure | Result |
| --- | --- |
| Disconnect mid GET_CHANGES | Client retries same cursor; identical seqs |
| Disconnect after apply, before ACK | Repeat GET; idempotent apply; then ACK |
| Disconnect mid PUSH | Retry same idempotency_key |
| Server crash after commit, before response | Retry same idempotency_key, get stored result |
| Server crash during blob PUT | Incomplete tmp file discarded; client retries PUT |
| Client offline for months | GET_CHANGES from cursor; if journal still has it, incremental; else RECONCILE |
| Two clients update same object | Second push conflicts; both copies remain until the user/adapter resolves |
| Device restored from backup | Likely stale cursor or `cursor > head`; HELLO sets `need_reconcile` |

## Idempotency rules for clients

Applying change `seq` for `object_id`:

- If local object revision > change.revision: ignore (local is newer; will PUSH or RECONCILE)
- If local object revision == change.revision and hashes match: no-op
- If local object revision == change.revision and hashes differ: treat as divergence → RECONCILE
- If local object revision < change.revision: apply
- If change.op=delete: tombstone locally even if the object was never present

## Concurrency

The engine serializes writers with a mutex **and** SQLite transactions. SQLite is the durability boundary; the mutex prevents in-process races around blob refcounts and idempotency inserts.

Readers (GET_CHANGES) use `Read` transactions and may run concurrently.

## Mapping future adapters onto this model

### WebDAV

- Collection kind `files`
- Collection ↔ DAV collection
- Object ↔ file or directory
- ETag = `content_hash` (strong)
- DAV `sync-token` = journal seq of that collection
- href is a function of object ID, not the user filename
- Display name is metadata

### CalDAV

- Collection kind `calendar`
- Object kind `event`, payload = ICS
- UID stored in metadata for calendar-query
- ETag = `content_hash`
- `sync-token` = journal seq
- calendar-multiget is adapter-side; data comes from engine Get

### CardDAV

Same as CalDAV with `addressbook` / `contact` / vCard.

### macOS agent

- Local EventKit identifiers stored in a *client-side* map `ek_identifier → object_id`
- That map is not the server identity
- Notifications trigger GET/PUSH; a timer and startup path always RECONCILE
- A failed PhotoKit enumeration does not PUSH deletes

### Photos

- Original is a blob
- Preview and thumbnail are derived blobs referenced from metadata, never substitutes for original
- Dedup: same `content_hash` shares `blob_id`
- Perceptual hash is optional metadata, not identity

## Verify

`nubilo verify` must detect:

- blob file missing for a referenced `blob_id` or live `preview_hash`/`thumb_hash`
- blob plaintext hash mismatch after decrypt
- orphan blobs
- object.collection_id not found
- journal row pointing at unknown object
- object.revision not matching last journal row for that object
- refcount ≠ live references (`objects.blob_id` plus metadata derivative hashes)
- cursor pointing past head
- device public key malformed
- incomplete idempotency rows

Any of these is a non-zero exit. Repair is explicit (`nubilo verify --repair` for orphans and refcounts, never for hash mismatches). `nubilo gc --apply` deletes unreferenced blobs and compactable tombstones.
