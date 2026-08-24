# Nubilo gaps

Snapshot of what is implemented vs missing. Calendar is the current lock-in; photos and contacts wait until calendar is full-featured.

Priorities stay: security, integrity, correct sync. Tailscale is transport only.

## Works today

- Mac agent over `/sync/v1` for selected calendars and reminder lists, including recurrence (`RRULE` / `EXDATE` / exceptions) and event timezones.
- iPhone CalDAV (app password, Apple-trusted TLS).
- WebDAV files, CardDAV (thin contacts), PhotoKit **push**, photo HTTP API.
- Pairing, backup, `verify`, `gc`.

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
| Status (`CONFIRMED` / `TENTATIVE` / `CANCELLED`) | Done | |
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

## Photos (after calendar)

- Mac PhotoKit can **push**. Nothing is written back into Photos.app.
- No iPhone Photos.app / share-sheet / Files→Photos path.
- Videos, Live Photos, and RAW are skipped. Default 64 MiB cap.
- iCloud originals that are not on disk often fail (`networkAccessAllowed` off).
- In-place PhotoKit edits of an already-mapped asset are not re-read.
- No gallery UI. HTTP API only (`/api/v1/photos`); local browser via `nubilo ui`.
- `nubilo ui` covers browse + create/rename/delete collections, object delete, pair/verify/gc, device password/rename/revoke, and most config fields. Still CLI-only: backup/restore, device enroll/rotate (pubkey files), TLS cert regen, agent selection.
- Identical plaintext bytes dedup to one blob.

## Contacts

- Only given name, family name, and the first email.
- No phones, addresses, org, notes, photos, or extra emails.
- CardDAV exists; it is not a full Contacts.app replacement.

## Files

- iOS Files / Finder can mount `/dav/`.
- Mac agent does not sync folders. `nubilo client` is a stub.
- Collection COPY is unimplemented. LOCK/UNLOCK are compatibility no-ops.

## Integrity / sync

- Failed `apply_change` no longer advances the journal cursor.
- Synthetic `EXDATE` for “EventKit did not list this instance in the window” can hide occurrences if listing is incomplete.
- One operator, one object graph. No multi-user isolation.

## Security / ops (accepted)

- SQLite metadata is not encrypted (LUKS is the control).
- Device keys are `0600` files, not Keychain.
- DAV app passwords are bearer tokens. iPhone CalDAV needs a cert Apple trusts (Tailscale Serve or install `tls.crt`).
- No automatic backup, no HA, no metrics UI.
- Single-owner personal cloud.

## Intentionally out of scope for calendar lock-in

Corporate credentials never leave the Mac. Linux `nubilo agent` stays refused. GPS never lands in SQLite. Originals are never mutated.
