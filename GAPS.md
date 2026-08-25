# Nubilo gaps

Snapshot of what is implemented vs missing. Calendar is the current lock-in; photos and contacts wait until calendar is full-featured.

Priorities stay: security, integrity, correct sync. Tailscale is transport only.

## Works today

- Mac agent over `/sync/v1` for selected calendars and reminder lists, including recurrence (`RRULE` / `EXDATE` / exceptions) and event timezones.
- iPhone CalDAV (app password, Apple-trusted TLS).
- WebDAV files, CardDAV (contacts with name/email/phone/address/birthday), PhotoKit **push + pull write-back**, photo HTTP API and `nubilo ui` gallery.
- Pairing, backup/restore (CLI + UI), enroll/rotate, TLS regen, verify, gc, optional server auto-backup.

## Calendar (lock-in)

Target: EventKit → ICS → CalDAV → iPhone looks like the same event, then edits round-trip.

| Area | Status | Notes |
| --- | --- | --- |
| Summary, notes, location, start/end, all-day | Done | |
| Recurrence (`RRULE`, `EXDATE`, detached `RECURRENCE-ID`) | Done | Window ±730 days; series with no in-window occurrence is not pushed |
| Event timezone (`TZID`) | Done | |
| `VTIMEZONE` component | Done | Emitted next to `TZID` |
| Alarms (`VALARM`) | Done | Relative and absolute display alarms |
| URL | Done | |
| Status (`CONFIRMED` / `TENTATIVE` / `CANCELLED`) | Done | Read into ICS from EventKit. `EKEvent.status` is readonly — pull cannot set STATUS; cancelled events are deletes |
| Busy/free (`TRANSP`) | Done | |
| Organizer / attendees | Done | Read from EventKit into ICS so iPhone sees invitees. macOS EventKit does not allow creating attendees without EventKitUI; pull will not attach people on the Mac |
| Failed `apply_change` still ACKed | Done | Journal cursor does not advance if any apply in the batch failed |
| Calendar color (`calendar-color` PROPPATCH) | Done | Stored in collection metadata; returned on PROPFIND. Mac agent pushes EventKit calendar color |
| Alarms that are email/procedure | Open | Display/audio relative+absolute only |
| Attachments | Open | Would bloat blobs; not in v1 |
| Travel time / conference / structured location extras | Open | Not in standard VEVENT |
| Reminders (`VTODO` / EventKit reminders) | Done | Mac agent syncs selected reminder lists as VTODO-only CalDAV collections. Incomplete always; completed within `window_days` |
| EventKit change notifications | Open | Poll every 120s |
| Calendar window | Open | Default ±730 days; operator can raise `window_days` |

## Photos

- Mac PhotoKit **push** and **pull write-back** (import into Photos library / optional album). Remote deletes do not remove Photos.app assets.
- iCloud originals: export allows network access with a timed wait (`icloud_fetch` / `export_failed` in errors).
- In-place edits: PhotoKit `modificationDate` stored in agent idmap; mod change re-exports and pushes.
- Video, Live Photo (still + paired movie blob), and RAW originals are synced; gallery shows kind/size/taken-at, video playback when possible, Live movie download. No RAW develop pipeline.
- `nubilo ui` gallery: download original, delete, captions, video/Live/RAW affordances.
- **People & Pets** in Photos.app are `PHPerson` entities, not albums. `nubilo agent albums` / agent UI lists them as `kind=person|pet` with ids `person:…`; select those for the full set (a same-named user album often only has ~key photos).
- Mac agent configuration UI: `nubilo agent ui` (loopback, same look as `nubilo ui`) for sync selection and setup; CLI selection remains.
- Still out: iPhone Photos.app / share sheet / Camera Upload; proprietary edit recipes beyond original resource bytes.
- Identical plaintext bytes dedup to one blob. Default 64 MiB cap.
- Blob PUT/GET use a 15m client timeout (JSON sync stays at 60s). Oversized bodies return 413; truncated uploads no longer trip the auth fail → 429 cascade.

## Contacts

- Mac agent syncs name (FN/N), emails, phones, postal addresses, and birthday (`BDAY`, including yearless `--MM-DD`).
- CardDAV stores full vCard blobs; metadata carries FN + preferred email/phone/birthday for listings.
- Still missing: org, notes, photos/avatars, URLs, extra structured name parts.

## Files

- iOS Files / Finder can mount `/dav/` (nested folders, file + collection COPY, MOVE across folders).
- Mac agent can sync selected local folders: `nubilo agent files add PATH`, `files on`. Nested dirs become nested collections; pull writes back to disk.
- `nubilo ui` can upload into a files collection.
- LOCK/UNLOCK remain compatibility no-ops. `nubilo client` is still a stub (agent folder sync covers the common case).
- Symlinks, dotfiles, `.git`, and files over 64 MiB are skipped by the agent walker.

## Integrity / sync

- Failed `apply_change` no longer advances the journal cursor.
- Synthetic `EXDATE` for “EventKit did not list this instance in the window” can hide occurrences if listing is incomplete.
- One operator, one object graph. No multi-user isolation.

## Security / ops

- SQLite metadata is not encrypted (LUKS is the control).
- Device keys are `0600` files, not Keychain.
- DAV app passwords are bearer tokens. iPhone CalDAV needs a cert Apple trusts (Tailscale Serve or install `tls.crt`).
- **Auto-backup:** `config.backup` (`enabled`, `interval_hours`, `passphrase_file`, `keep`) runs in `nubilo server`; archives under `data_dir/backups/`. Passphrase stays in a file on disk, not in JSON.
- **UI ops:** create encrypted backup + one-shot download; restore to an empty **non-live** destination (confirm `RESTORE`); enroll/rotate device pubkeys; TLS regen; richer status (blobs, devices, last backup).
- Restore onto the running live `data_dir` stays CLI-only (`nubilo restore` after stop).
- **HA:** out of scope — single-node personal cloud; no multi-server failover.
- Metrics: ops/status panel only (not Prometheus).

## Intentionally deferred

- Operator verification drills (iPhone CalDAV + Apple-trusted TLS, Finder/Files mount, backup restore drill, LUKS under data dir) — checklist, not product code.
- Corporate credentials never leave the Mac. Linux `nubilo agent` stays refused. GPS never lands in SQLite. Originals are never mutated.
