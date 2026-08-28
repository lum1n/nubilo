# Nubilo gaps

Snapshot of what is implemented vs missing. Calendar remains the lock-in surface; contacts and photos now have production-minded round-trips without a mobile app.

Priorities stay: security, integrity, correct sync. Tailscale is transport only.

## Works today

- Mac agent over `/sync/v1` for selected calendars and reminder lists, including recurrence (`RRULE` / `EXDATE` / exceptions) and event timezones.
- iPhone CalDAV (app password, Apple-trusted TLS).
- WebDAV files, CardDAV contacts, PhotoKit **push + pull write-back**, photo HTTP API and `nubilo ui` gallery + upload.
- Pairing, backup/restore (CLI + UI), enroll/rotate, TLS regen, verify, gc, optional server auto-backup.
- Always-on user services: `nubilo server install` (Linux systemd `--user` or macOS LaunchAgent), `nubilo agent install` (macOS LaunchAgent via Nubilo.app).
- Guided setup + doctor: `nubilo setup`, `nubilo agent setup`, `nubilo doctor`, UI health panel; macOS agent keys in Keychain.
- EventKit / Contacts change notifications wake the agent (still polls as a backstop).

## Calendar (lock-in)

Target: EventKit → ICS → CalDAV → iPhone looks like the same event, then edits round-trip.

| Area | Status | Notes |
| --- | --- | --- |
| Summary, notes, location, start/end, all-day | Done | |
| Recurrence (`RRULE`, `EXDATE`, detached `RECURRENCE-ID`) | Done | Window ±730 days; series with no in-window occurrence is not pushed. Sparse EventKit listings do not invent EXDATEs |
| Event timezone (`TZID`) | Done | |
| `VTIMEZONE` component | Done | Emitted next to `TZID` |
| Alarms (`VALARM`) | Done | DISPLAY + EMAIL (ATTENDEE); relative and absolute |
| URL | Done | |
| URI `ATTACH` | Done | URI attachments round-trip in ICS; inline binary attachments deferred |
| Status (`CONFIRMED` / `TENTATIVE` / `CANCELLED`) | Done | Read into ICS from EventKit. `EKEvent.status` is readonly — pull cannot set STATUS; cancelled events are deletes |
| Busy/free (`TRANSP`) | Done | |
| Organizer / attendees | Done | Read from EventKit into ICS so iPhone sees invitees. macOS EventKit does not allow creating attendees without EventKitUI; pull will not attach people on the Mac |
| Failed `apply_change` still ACKed | Done | Journal cursor does not advance if any apply in the batch failed |
| Calendar color (`calendar-color` PROPPATCH) | Done | Stored in collection metadata; returned on PROPFIND. Mac agent pushes EventKit calendar color |
| Alarms that are procedure | Open | Not useful on Apple clients |
| Inline binary attachments | Open | Would bloat blobs; URI ATTACH only |
| Travel time / conference / structured location extras | Open | Not in standard VEVENT |
| Reminders (`VTODO` / EventKit reminders) | Done | Mac agent syncs selected reminder lists as VTODO-only CalDAV collections. Incomplete always; completed within `window_days` |
| EventKit change notifications | Done | `EKEventStoreChangedNotification` + 2s debounce; poll remains as backstop |
| Calendar window | Open | Default ±730 days; operator can raise `window_days` |

## Photos

- Mac PhotoKit **push** and **pull write-back** (import into Photos library / optional album). Remote deletes do not remove Photos.app assets.
- iCloud originals: export allows network access with a timed wait (`icloud_fetch` / `export_failed` in errors).
- In-place edits: PhotoKit `modificationDate` stored in agent idmap; mod change re-exports and pushes.
- Video, Live Photo (still + paired movie blob), and RAW originals are synced; gallery shows kind/size/taken-at, video playback when possible, Live movie download. No RAW develop pipeline.
- `nubilo ui` gallery: **upload** (file / camera on supporting browsers), download original, delete, captions, video/Live/RAW affordances.
- **No-app iPhone upload:** create WebDAV folder `Camera Upload` and save from Files.app — PUT also ingests as a photo object. Or `nubilo devices password --scope photos` + Shortcuts `POST /api/v1/photos`.
- **People & Pets** in Photos.app are `PHPerson` entities, not albums. `nubilo agent albums` / agent UI lists them as `kind=person|pet` with ids `person:…`; select those for the full set (a same-named user album often only has ~key photos).
- Mac agent configuration UI: `nubilo agent ui` (loopback, same look as `nubilo ui`) for sync selection and setup; CLI selection remains.
- Still out: proprietary edit recipes beyond original resource bytes; native Photos.app share extension (no iOS app by design).
- Identical plaintext bytes dedup to one blob. Default **256 MiB** cap (`sync.max_blob_bytes`; legacy 32/64 MiB configs are bumped on load so phone videos sync).
- Blob PUT/GET use a 15m client timeout (JSON sync stays at 60s). Oversized bodies return 413; truncated uploads no longer trip the auth fail → 429 cascade.

## Contacts

- Mac agent syncs FN/N, org, nickname, note, emails, phones, postal addresses, URLs, birthday (`BDAY`, including yearless `--MM-DD`), and photo when present.
- CardDAV stores full vCard blobs; agent keeps a per-contact cache and **merges** Mac-managed fields into the last full card so iPhone extras (and `X-*` props) are not wiped on the next push.
- FN-only / org-only cards apply a displayable name into Contacts.app so they do not round-trip blank.
- Contacts store change notifications wake the agent (with poll backstop).

## Files

- iOS Files / Finder can mount `/dav/` (nested folders, file + collection COPY, MOVE across folders).
- Mac agent can sync selected local folders: `nubilo agent files add PATH`, `files on`. Nested dirs become nested collections; pull writes back to disk.
- `nubilo ui` can upload into a files collection; files browser drills into nested subfolders (agent syncs nested dirs as child collections).
- LOCK/UNLOCK remain compatibility no-ops. `nubilo client` is still a stub (agent folder sync covers the common case).
- Symlinks, dotfiles, `.git`, and files over `sync.max_blob_bytes` are skipped by the agent walker.

## Integrity / sync

- Failed `apply_change` no longer advances the journal cursor.
- Synthetic `EXDATE` skipped when EventKit listing looks incomplete (missing ≫ listed).
- One operator, one object graph. No multi-user isolation.

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
- Corporate credentials never leave the Mac. Linux `nubilo agent` stays refused. GPS never lands in SQLite. Originals are never mutated.
- No iOS/Android app (Calendar/Contacts/Files/photos via Apple accounts + WebDAV/Shortcuts + optional UI).
