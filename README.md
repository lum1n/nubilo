# Nubilo

Single-binary personal cloud for one operator: calendars, reminders, contacts, files, and photos.

A Linux or macOS **server** holds encrypted blobs and metadata. A **macOS agent** syncs EventKit, Contacts, PhotoKit, and selected folders. The **iPhone** talks CalDAV / CardDAV / WebDAV with app passwords — no Nubilo iOS app.

Priorities: security, integrity, correct sync. Tailscale is transport only.

## Requirements

| Role | Where | Notes |
| --- | --- | --- |
| Server | Linux or macOS | Encrypted volume recommended (LUKS / FileVault); `nubilo doctor` checks this |
| Agent | macOS only | Separate `--data-dir` from the server |
| Phone | iOS | Apple Calendar / Contacts / Files; TLS Apple trusts (Tailscale Serve or install `tls.crt`) |

Reach the server over Tailscale (or LAN). Do not expose the listen port to the public internet without your own TLS terminator and threat model.

## Install

```bash
go build -o nubilo ./cmd/nubilo
```

On the Mac agent host, prefer a signed binary so Photos/Calendar prompts attribute to **Nubilo**:

```bash
go install ./cmd/nubilo
./scripts/mac-sign.sh "$(go env GOPATH)/bin/nubilo"
```

## Quick start

**Server**

```bash
./nubilo setup --data-dir ~/.nubilo --yes
./nubilo doctor --data-dir ~/.nubilo
./nubilo server install --data-dir ~/.nubilo   # optional always-on
./nubilo pair --data-dir ~/.nubilo --role agent
```

Save the pairing code. Enable auto-backup passphrase when setup prints it.

**Mac agent** (separate data dir)

```bash
./nubilo agent setup --data-dir ~/.nubilo-agent \
  --server https://<host>:8443 --code XXXXX-XXXXX --name "Studio Mac"
./nubilo doctor --agent --data-dir ~/.nubilo-agent
./nubilo agent install --data-dir ~/.nubilo-agent   # LaunchAgent + Nubilo.app for TCC
```

Or configure sync in the loopback UI: `nubilo agent ui` (port 8788).

**iPhone app passwords** (each scope is independent)

```bash
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Calendar" --scope caldav
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Contacts" --scope carddav
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Files" --scope webdav
./nubilo devices password --data-dir ~/.nubilo --name "iPhone Photos" --scope photos   # optional Shortcuts upload
./nubilo verify --data-dir ~/.nubilo
```

## What syncs

| Surface | Path | Notes |
| --- | --- | --- |
| Calendars / reminders | Mac EventKit ↔ CalDAV | Recurrence, timezones, alarms (DISPLAY + EMAIL), URI attachments |
| Contacts | Mac Contacts ↔ CardDAV | Names, org, note, emails, phones, addresses, URLs, birthday, photo; full vCard extras preserved across Mac round-trips |
| Files | Mac folders ↔ WebDAV `/dav/` | Nested folders; iOS Files / Finder |
| Photos | Mac PhotoKit ↔ gallery API | Originals byte-for-byte; preview/thumb derived; Live/RAW/video |

Server UI (loopback): `nubilo ui` on port 8787 — browse, upload photos, pairing, verify/gc, backup, devices, config.

## Always-on

User-level only (no root): LaunchAgent on macOS, `systemd --user` on Linux.

```bash
nubilo server install --data-dir ~/.nubilo && nubilo server service
nubilo agent install --data-dir ~/.nubilo-agent && nubilo agent service
# uninstall: nubilo server uninstall | nubilo agent uninstall
```

Linux user units stop on logout unless `loginctl enable-linger $USER`. Logs: `$data_dir/logs/server.log` and `$data_dir/logs/agent.log`.

Same Mac may run server and agent with **two** data dirs — never share one `--data-dir`.

## TLS

`init` / `setup` writes a self-signed cert (localhost + local IPs). The Mac agent **pins** it at pair (TOFU). Do not use `--insecure` in normal operation. `nubilo tls` regenerates the cert (new IPs / expiry).

iPhone CalDAV/CardDAV need a cert Apple trusts: [Tailscale Serve](https://tailscale.com/kb/1242/tailscale-serve) in front of Nubilo, or install `~/.nubilo/tls.crt` on the phone.

## iPhone

Server host must be reachable (Tailscale IP or hostname), not `127.0.0.1`.

**Calendar** — Settings → Calendar → Accounts → Add Account → Other → CalDAV:

- Server: `https://<host>:8443`
- Username / password: from `devices password --scope caldav`
- SSL on; discovery `/.well-known/caldav` or blank

**Contacts** — Settings → Contacts → Accounts → Other → CardDAV:

- Same host; `--scope carddav`
- Discovery: `/.well-known/carddav`

**Files** — Files app → connect to server `https://<host>:8443/dav/` with `--scope webdav`.

**Photos (no app)** — pick one:

1. WebDAV folder **Camera Upload** under `/dav/files/` — saves are also ingested as photos (agent can pull into Photos.app).
2. Shortcuts: Basic auth with `--scope photos`, `POST https://<host>:8443/api/v1/photos?name=IMG.JPG` (raw body).
3. `nubilo ui` upload on the server (or via SSH tunnel to loopback).

## macOS agent

Signing device over `/sync/v1` (Ed25519). Device key lives in Keychain when possible. Linux `nubilo agent` refuses to start.

Grant Calendar, Contacts, and Photos (Full Access) to **Nubilo** (or Terminal if you run the unsigned CLI). Corporate credentials never leave the Mac.

```bash
nubilo agent authorize                          # Photos Full Access dialog
nubilo agent --data-dir ~/.nubilo-agent calendars
nubilo agent --data-dir ~/.nubilo-agent select <id>
nubilo agent --data-dir ~/.nubilo-agent reminder-lists
nubilo agent --data-dir ~/.nubilo-agent select-reminder <id>
nubilo agent --data-dir ~/.nubilo-agent contacts on
nubilo agent --data-dir ~/.nubilo-agent photos on
nubilo agent --data-dir ~/.nubilo-agent albums
nubilo agent --data-dir ~/.nubilo-agent photos source albums
nubilo agent --data-dir ~/.nubilo-agent photos select '<album-or-person-id>'
nubilo agent --data-dir ~/.nubilo-agent files add ~/Documents/Nubilo
nubilo agent --data-dir ~/.nubilo-agent files on
nubilo agent --data-dir ~/.nubilo-agent          # run once; prefer agent install
```

People & Pets are `person:…` rows from `albums`, not same-named user albums. Sync window defaults to ±730 days; EventKit/Contacts changes wake the agent (poll is a backstop). Failed local listings never push mass deletes.

If Photos stays Limited: System Settings → Privacy & Security → Photos → Nubilo → Full Access. Reset with `tccutil reset Photos` if stuck.

## Operations

```bash
nubilo doctor --data-dir ~/.nubilo
nubilo doctor --agent --data-dir ~/.nubilo-agent
nubilo verify --data-dir ~/.nubilo
nubilo gc --data-dir ~/.nubilo            # add --apply to collect
nubilo backup --data-dir ~/.nubilo        # encrypted archive
nubilo restore …                          # stop server first for live data_dir
nubilo status --data-dir ~/.nubilo
```

UI can create/download backups and restore to an empty **non-live** destination. Restoring onto the running data dir is CLI-only after stop.

## Photos API

With a signing device or `--scope photos` app password:

| Method | Path |
| --- | --- |
| `GET` | `/api/v1/photos` |
| `POST` | `/api/v1/photos?name=shot.jpg` |
| `GET` | `/api/v1/photos/{id}` |
| `GET` | `/api/v1/photos/{id}/original\|preview\|thumb\|live` |

GPS stays in the encrypted original; it is not stored in SQLite. Derivatives strip GPS by default (`photos.strip_gps_from_derivatives`).

## Commands

```text
nubilo setup | doctor | init | server | ui | status
nubilo server install|uninstall|service
nubilo pair | devices | devices revoke|rename|password|enroll|rotate
nubilo agent | agent setup|ui|install|uninstall|service|authorize
nubilo agent calendars|select|unselect|reminder-lists|select-reminder|unselect-reminder
nubilo agent contacts on|off | photos on|off | photos source|select | albums | files add|on|off
nubilo verify | gc | backup | restore | tls
```

`--json` works on list/status commands.

## Security

Every client has its own device identity and authorization. Blobs are encrypted at rest; SQLite metadata is not — encrypt the volume. DAV app passwords are bearer secrets (TLS + revoke). See [SECURITY.md](SECURITY.md).

## Docs

| Doc | Contents |
| --- | --- |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Topology and design |
| [SECURITY.md](SECURITY.md) | Threat model and controls |
| [SYNC.md](SYNC.md) | `/sync/v1` protocol |
| [GAPS.md](GAPS.md) | Known limits and deferred work |
| [IMPLEMENTATION.md](IMPLEMENTATION.md) | Phase history |
