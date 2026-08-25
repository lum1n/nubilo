# Nubilo

A single-binary personal cloud: sync engine first, then CalDAV, CardDAV, WebDAV, and a macOS agent as adapters.

This repository is in **Phase 8**. Foundation through photos are implemented, plus hardening: threat-model review, fuzz seeds, storage corruption tests, backup drills, blob/tombstone GC, and a SQLite-at-rest encryption evaluation.

See [GAPS.md](GAPS.md) for what is still missing. Calendar is the current lock-in.

Read, in order:

1. [ARCHITECTURE.md](ARCHITECTURE.md)
2. [SECURITY.md](SECURITY.md)
3. [SYNC.md](SYNC.md)
4. [IMPLEMENTATION.md](IMPLEMENTATION.md)

## Build

```bash
go build -o nubilo ./cmd/nubilo
```

## Quick start (local)

```bash
./nubilo init --data-dir ~/.nubilo --listen 0.0.0.0:8443
./nubilo server --data-dir ~/.nubilo
# another terminal
./nubilo pair --data-dir ~/.nubilo --role agent
# Mac
./nubilo pair --data-dir ~/.nubilo-agent --server https://<lan-or-tailscale-ip>:8443 --code XXXXX-XXXXX --name "Eika Mac"
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Files" --scope webdav
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Calendar" --scope caldav
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Contacts" --scope carddav
./nubilo verify --data-dir ~/.nubilo
```

On a Mac, pair a **signing** agent (not a DAV password) into a separate data directory:

```bash
# server (Linux) — already running
./nubilo pair --data-dir ~/.nubilo --role agent
# Mac
./nubilo pair --data-dir ~/.nubilo-agent --server https://<host>:8443 --code XXXXX-XXXXX --name "Studio Mac"
./nubilo agent --data-dir ~/.nubilo-agent calendars
./nubilo agent --data-dir ~/.nubilo-agent select <eventkit-id>
./nubilo agent --data-dir ~/.nubilo-agent reminder-lists
./nubilo agent --data-dir ~/.nubilo-agent select-reminder <eventkit-id>
./nubilo agent --data-dir ~/.nubilo-agent contacts on
./nubilo agent --data-dir ~/.nubilo-agent
```

### Mac Photos permission

PhotoKit dialogs are attributed to the **process that calls the API**. When you run `nubilo` from Terminal, macOS often asks for Terminal — or never prompts.

To get a **Nubilo** system dialog (and Full Access for whole albums):

```bash
go install ./cmd/nubilo
./scripts/mac-sign.sh "$(go env GOPATH)/bin/nubilo"
nubilo agent authorize
```

Choose **Allow Full Access** (not Limited / Selected Photos). Limited access is why an album of 154 can sync only ~10 photos.

Then:

```bash
nubilo agent --data-dir ~/.nubilo-agent albums   # shows per-album counts visible to PhotoKit
nubilo agent --data-dir ~/.nubilo-agent
```

If access stays Limited: System Settings → Privacy & Security → Photos → **Nubilo** → Full Access. Reset with `tccutil reset Photos` if stuck.
`init` writes a self-signed certificate covering localhost and local IPs. Pairing **pins** that cert (TOFU). You do not run `nubilo tls` and you do not pass `--insecure` on a normal setup. `--insecure` remains a debug escape hatch. `nubilo tls` only regenerates the cert (new IPs, expiry).

Mount WebDAV at `https://<host>:8443/dav/` using the printed username (device id) and one-time password. CalDAV is at `/caldav/`. CardDAV is at `/carddav/`. iPhone Calendar/Contacts need a certificate Apple trusts: put [Tailscale Serve](https://tailscale.com/kb/1242/tailscale-serve) (or another TLS terminator) in front and keep Nubilo on loopback, or install `tls.crt` on the phone. The Mac agent does not need that; it uses the pairing pin.

Browse and configure your cloud locally with `nubilo ui` (loopback web UI on port 8787). It covers browsing photos/calendar/contacts/files, creating collections, pairing, verify/gc, backup create/download, restore to a non-live dest, device enroll/rotate/password, TLS regen, auto-backup settings, and config. Agent calendar/album/folder selection stays on the CLI. Restoring onto the live data dir still requires stopping the server and `nubilo restore`.

### iPhone Calendar

Settings → Calendar → Accounts → Add Account → Other → Add CalDAV Account:

- Server: `https://<tailscale-ip>:8443` (not `127.0.0.1`; the phone cannot reach loopback on the server)
- Username: the device id printed by `nubilo devices password --scope caldav`
- Password: the one-time app password
- Path / discovery: leave blank or `/.well-known/caldav`. SSL **on**. Apple will not sync to a self-signed cert: use Tailscale Serve, or install `~/.nubilo/tls.crt` on the phone first.

A WebDAV-only password cannot access calendars, and a CalDAV-only password cannot access files.

### iPhone Contacts

Settings → Contacts → Accounts → Add Account → Other → Add CardDAV Account:

- Server: `https://<tailscale-ip>:8443` (not `127.0.0.1`)
- Username: the device id printed by `nubilo devices password --scope carddav`
- Password: the one-time app password
- Path / discovery: `/.well-known/carddav` redirects to `/carddav/user/`

Protocol scopes are independent: a CalDAV password cannot read contacts, and a CardDAV password cannot read calendars or files.

### macOS agent

The agent is a signing device over `/sync/v1` (Ed25519), not an app password. Pair with `--role agent` on the server, then complete pairing on the Mac into the **Mac** data dir (`device.json` / `device.key`). Linux `nubilo agent` refuses to start.

macOS will prompt for Calendar, Contacts, and Photos access (TCC). Grant it to Terminal or the `nubilo` binary. The agent only reads EventKit/Contacts/PhotoKit data the OS already granted; corporate credentials never leave the Mac.

Sync uses a time window (default ±730 days). Recurring events are stored as one object with `RRULE` (plus `EXDATE` / exception instances). Events whose series never intersects that window are not pushed. A failed EventKit, Contacts, or PhotoKit listing never pushes deletes. Change detection is periodic (default 120s).

```bash
./nubilo agent --data-dir ~/.nubilo-agent photos on
./nubilo agent --data-dir ~/.nubilo-agent albums
./nubilo agent --data-dir ~/.nubilo-agent photos source all   # or albums|dates
./nubilo agent --data-dir ~/.nubilo-agent photos select <album-id>
./nubilo agent --data-dir ~/.nubilo-agent files add ~/Documents/Nubilo
./nubilo agent --data-dir ~/.nubilo-agent files on
```

`files add` selects a local folder to push/pull under WebDAV `/dav/files/<name>/`. Nested directories become nested collections.

### Photos HTTP API

Signing devices and app passwords with `--scope photos` can use:

- `GET /api/v1/photos`
- `POST /api/v1/photos?name=shot.jpg` (raw image body)
- `GET /api/v1/photos/{id}`
- `GET /api/v1/photos/{id}/original|preview|thumb|live`

Originals are stored byte-for-byte. Preview and thumbnail are derived JPEGs. GPS stays in the encrypted original; it is not written to SQLite metadata. `photos.strip_gps_from_derivatives` (default true) keeps GPS out of derivatives.

## Commands

```text
nubilo init
nubilo server
nubilo ui
nubilo agent
nubilo agent calendars
nubilo agent select ID
nubilo agent unselect ID
nubilo agent reminder-lists
nubilo agent select-reminder ID
nubilo agent unselect-reminder ID
nubilo agent contacts on|off
nubilo agent photos on|off
nubilo agent photos source all|albums|dates
nubilo agent albums
nubilo status
nubilo pair
nubilo devices
nubilo devices revoke <id>
nubilo devices rename <id>
nubilo devices password --name NAME [--scope webdav|caldav|carddav|photos|all]
nubilo files
nubilo calendars
nubilo contacts
nubilo photos
nubilo verify
nubilo gc
nubilo backup
nubilo restore
```

`--json` is accepted on list/status commands for machine-readable output.

## Security

Tailscale is a network transport, not the security model. Every client has its own device identity. See [SECURITY.md](SECURITY.md).
