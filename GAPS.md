# Nubilo gaps

Snapshot of what is implemented vs missing. Calendars, reminders and contacts live on the server; every device (iPhone, iPad, Mac) reaches them through its built-in CalDAV / CardDAV account, installed by the configuration profile.

Priorities stay: security, integrity, correct sync. Tailscale is transport only.

## Works today

- CalDAV (calendars and reminders) and CardDAV (contacts) for every device, with app passwords and Apple-trusted TLS.
- WebDAV files, Mac agent folder sync, photo backup from the Mac and iPhone apps, photo HTTP API and `nubilo ui` gallery + upload.
- Pairing, backup/restore (CLI + UI), enroll/rotate, TLS regen, verify, gc, optional server auto-backup.
- Always-on user services: `nubilo server install` (Linux systemd `--user` or macOS LaunchAgent), `nubilo agent install` (macOS LaunchAgent running the current binary).
- Guided setup + doctor: `nubilo setup`, `nubilo agent setup`, `nubilo doctor`, UI health panel; macOS agent keys in Keychain.

## Calendars, reminders and contacts

The Mac, like the iPhone, adds the server as a CalDAV / CardDAV account (the profile from the app or the web UI), so Apple's own clients talk to the server directly and the server stores their ICS / vCard bodies unchanged. In addition, the Nubilo Mac app can mirror chosen Mac calendars that only that Mac reaches (e.g. Exchange) one way into read-only server calendars.

| Area | Status | Notes |
| --- | --- | --- |
| Events, recurrence, timezones, alarms, attendees | Done | Stored as the client sent them |
| Reminders (`VTODO`) | Done | VTODO-only calendar collections |
| Calendar color (`calendar-color` PROPPATCH) | Done | Stored in collection metadata; returned on PROPFIND |
| Sharing a calendar or address book with another user | Done | Read-only or read-write shares (SECURITY.md §5.1) |
| Calendar mirror (Mac → server, read-only elsewhere) | Done | Nubilo Mac app reads EventKit and writes the chosen calendars to server calendars marked `"mirror": true`; only the owning device may write them. Events from −30 to +365 days; repeating meetings as series (RRULE with EXDATE and RECURRENCE-ID overrides), falling back to one event per occurrence for rules that cannot be written faithfully (week-of-year or day-of-year rules, yearly weekday rules without a month, or a rule that does not reproduce EventKit's occurrences). Outside the window the phone expands the bare rule, so occurrences moved or cancelled there are not known. Runs only while the app runs. No reminders or contacts |
| Inline binary attachments | Open | Would bloat blobs; URI ATTACH only |
| Moving data out of iCloud | Manual | Export from iCloud (or drag the calendars into the Nubilo account on the Mac) |

## Photos

- The Mac and iPhone apps back up Photos.app. The Go agent no longer reads or writes Photos; there is no pull write-back into Photos.app.
- Video, Live Photo (still + paired movie blob), and RAW originals are synced; gallery shows kind/size/taken-at, video playback when possible, Live movie download. No RAW develop pipeline.
- `nubilo ui` gallery: **upload** (file / camera on supporting browsers), download original, delete, captions, video/Live/RAW affordances.
- **No-app iPhone upload:** create WebDAV folder `Camera Upload` and save from Files.app — PUT also ingests as a photo object. Or `nubilo devices password --scope photos` + Shortcuts `POST /api/v1/photos`.
- Still out: proprietary edit recipes beyond original resource bytes; native Photos.app share extension (no iOS app by design).
- Identical plaintext bytes dedup to one blob. Default **256 MiB** cap (`sync.max_blob_bytes`; legacy 32/64 MiB configs are bumped on load so phone videos sync).
- Blob PUT/GET use a 15m client timeout (JSON sync stays at 60s). Oversized bodies return 413; truncated uploads no longer trip the auth fail → 429 cascade.

## Files

- iPhone and iPad: Nubilo is a location in the Files app (File Provider extension in the app, signed sync API, no app password). Browse, open, add, edit, rename, move and delete files and folders, including folders shared with you (read-only ones are locked). Changes made elsewhere show up when Files refreshes (the app also asks it to when it comes to the foreground). Not yet: thumbnails, Camera Upload ingestion through Files (WebDAV only), offline pinning beyond what iOS keeps.
- Finder (and other WebDAV clients) can mount `/dav/` (nested folders, file + collection COPY, MOVE across folders).
- Mac agent can sync selected local folders: `nubilo agent files add PATH`, `files on`. Nested dirs become nested collections; pull writes back to disk.
- Agent configuration UI: `nubilo agent ui` (loopback, same look as `nubilo ui`) for folder selection and setup; CLI selection remains.
- `nubilo ui` can upload into a files collection; files browser drills into nested subfolders (agent syncs nested dirs as child collections).
- LOCK/UNLOCK remain compatibility no-ops. `nubilo client` is still a stub (agent folder sync covers the common case).
- Symlinks, dotfiles, `.git`, and files over `sync.max_blob_bytes` are skipped by the agent walker.

## Integrity / sync

- Failed `apply_change` no longer advances the journal cursor.
- Multi-user since schema v2: users, per-collection ownership, read-only/read-write shares of top-level collections, isolated journal/blobs/photos/DAV (SECURITY.md §5.1). Per-user storage quotas (schema v3; `nubilo users quota`, app Members → Storage Limit) count live objects in collections the user owns. Groups (schema v4; `nubilo groups`, app People → Groups) can be shared with like users; a member gets the best of their direct and group shares. Per-user web logins are not implemented yet; the admin console acts as a chosen user.

## Security / ops

- SQLite metadata is not encrypted (LUKS / FileVault is the control; `nubilo doctor` checks this).
- Agent device keys use macOS Keychain; server `master.key` remains a `0600` file (protect the volume).
- DAV app passwords are bearer tokens. iPhone CalDAV needs a cert Apple trusts (Tailscale Serve or install `tls.crt`).
- **Setup / doctor:** `nubilo setup` initializes, enables auto-backup (passphrase shown once), and can install the always-on service. `nubilo doctor` / UI health panel checks perms, TLS, backup, disk encryption, pairing, and service. `nubilo agent setup` pairs, Keychain-stores the key, and installs the LaunchAgent.
- **UI ops:** create encrypted backup + one-shot download; restore to an empty **non-live** destination (confirm `RESTORE`); enroll/rotate device pubkeys; TLS regen; richer status (blobs, devices, last backup).
- Restore onto the running live `data_dir` stays CLI-only (`nubilo restore` after stop).
- **Always-on:** `nubilo server install` / `nubilo agent install` (user-level: LaunchAgent on macOS; systemd `--user` on Linux for the server). Agent install is macOS-only. Linux user units stop on logout unless `loginctl enable-linger` is set.
- **HA:** out of scope — single-node personal cloud; no multi-server failover.
- Metrics: ops/status panel only (not Prometheus).

## Intentionally deferred

- Hands-on phone/Finder restore drills remain operator actions; `nubilo doctor` surfaces the checklist in product.
- Corporate credentials never leave the Mac. `nubilo agent` runs in the foreground on any OS; only its always-on install is macOS-only. GPS never lands in SQLite. Originals are never mutated.
- iOS/macOS app (`apps/Nubilo`): onboarding by QR, CalDAV/CardDAV profile install, best-effort photo backup, gallery and shared albums, managing people and devices, Files app integration on iPhone/iPad. No Android app. Profiles are signed when the server holds a trusted certificate (relay, direct, Tailscale); proxy and LAN-only servers hand them out unsigned.
