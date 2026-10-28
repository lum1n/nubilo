# Nubilo

Single-binary private cloud for a family or small team: calendars, reminders, contacts, files, and photos, with per-user data and sharing.

A Linux or macOS **server** holds encrypted blobs and metadata for several **users** (admins and members). Each user's devices see only that user's collections and what others shared with them (read-only or read-write). A **macOS agent** syncs selected folders; the Nubilo app backs up Photos.app on the Mac and the iPhone. iPhones and Macs use native CalDAV / CardDAV accounts for calendars, reminders and contacts (installed by a configuration profile) and WebDAV, plus the Nubilo app (`apps/Nubilo`) for onboarding by QR code, photo backup, shared albums and managing people and devices.

Priorities: security, integrity, correct sync. Server-side encryption, not end-to-end (see SECURITY.md).

## Requirements

| Role | Where | Notes |
| --- | --- | --- |
| Server | Linux or macOS | Encrypted volume recommended (LUKS / FileVault); `nubilo doctor` checks this |
| Agent | macOS (always-on install); runs anywhere in the foreground | Separate `--data-dir` from the server |
| Phone | iOS | Apple Calendar / Contacts / Files + the Nubilo app; TLS Apple trusts (public domain with Let's Encrypt via relay/direct, the tailnet name in tailscale mode, or install `tls.crt` on LAN) |

Reach the server from anywhere through a small **VPS relay** (the home stays closed; TLS still terminates at home with Let's Encrypt), by hosting it **directly** on public ports, over your **tailnet** with a certificate from `tailscale cert`, or on the LAN only. See [docs/RELAY.md](docs/RELAY.md).

## Install

```bash
go build -o nubilo ./cmd/nubilo
```

## Quick start

**Server**

```bash
./nubilo setup --data-dir ~/.nubilo        # asks relay / direct / lan, domain, installs the service
./nubilo doctor --data-dir ~/.nubilo
```

Setup ends with an admin invite QR code: scan it with the Nubilo app to become the first admin. Save the auto-backup passphrase it prints.

**Adding people**

```bash
./nubilo users invite --role member        # QR code + https://<domain>/join#<code>
./nubilo users list
./nubilo users disable USERNAME            # revokes all their devices and app passwords
./nubilo users quota USERNAME 50G          # storage limit; 0 removes it
```

The same is available in the admin console (`nubilo ui`, Users tab) and in the app. Data commands take `--user USERNAME` (default: the owner admin), e.g. `nubilo --user anna calendars list`.

**Pair the Mac agent**

```bash
./nubilo pair --data-dir ~/.nubilo --role agent [--user USERNAME]
```

Save the pairing code.

**Mac agent** (separate data dir)

```bash
./nubilo agent setup --data-dir ~/.nubilo-agent \
  --server https://<host>:8443 --code XXXXX-XXXXX --name "Studio Mac"
./nubilo doctor --agent --data-dir ~/.nubilo-agent
./nubilo agent install --data-dir ~/.nubilo-agent   # LaunchAgent running this binary
```

Or configure sync in the loopback UI: `nubilo agent ui` (port 8788).

**iPhone without the app** — app passwords (each scope is independent; log in with your username)

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
| Calendars / reminders | CalDAV account on each device | Installed by the configuration profile; the server stores what Calendar and Reminders send |
| Contacts | CardDAV account on each device | Installed by the configuration profile |
| Files | Mac folders ↔ WebDAV `/dav/` | Nested folders; iOS Files / Finder |
| Photos | Mac and iPhone apps → gallery API | The apps back up Photos.app; the agent syncs folders only. Originals byte-for-byte; preview/thumb derived; Live/RAW/video |

Server UI (loopback): `nubilo ui` on port 8787, or the next free port if that one is taken — browse, upload photos, pairing, verify/gc, backup, devices, config.

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

iPhone CalDAV/CardDAV need a cert Apple trusts: a public domain (relay, proxy or direct), `nubilo setup --mode tailscale` (the server serves its MagicDNS name with a certificate from `tailscale cert`), or install `~/.nubilo/tls.crt` on the phone.

## iPhone

Server host must be reachable (Tailscale IP or hostname), not `127.0.0.1`.

**Calendar** — Settings → Calendar → Accounts → Add Account → Other → CalDAV:

- Server: `https://<host>:8443`
- Username / password: from `devices password --scope caldav`
- SSL on; discovery `/.well-known/caldav` or blank

**Contacts** — Settings → Contacts → Accounts → Other → CardDAV:

- Same host; `--scope carddav`
- Discovery: `/.well-known/carddav`

**Web** — open `https://<server>/web/` in any browser and approve the code from the Nubilo app (People → Sign In on the Web, or scan it). Photos, albums and files, no password.

**Files** — with the Nubilo app installed, open the Files app → Browse → Nubilo. Other WebDAV clients (Finder: Go → Connect to Server) use `https://<host>:8443/dav/` with a `--scope webdav` app password.

**Photos (no app)** — pick one:

1. WebDAV folder **Camera Upload** under `/dav/files/` — saves are also ingested as photos.
2. Shortcuts: Basic auth with `--scope photos`, `POST https://<host>:8443/api/v1/photos?name=IMG.JPG` (raw body).
3. `nubilo ui` upload on the server (or via SSH tunnel to loopback).

## macOS agent

Folder sync only. Signing device over `/sync/v1` (Ed25519). Device key lives in Keychain when possible. `nubilo agent` runs on any OS in the foreground; `agent install` (LaunchAgent) is macOS-only. Corporate credentials never leave the Mac.

```bash
nubilo agent --data-dir ~/.nubilo-agent files add ~/Documents/Nubilo
nubilo agent --data-dir ~/.nubilo-agent files on
nubilo agent --data-dir ~/.nubilo-agent          # run once; prefer agent install
```

Failed local listings never push mass deletes.

The agent does not sync Photos, Calendar, Reminders or Contacts. The Nubilo app backs up Photos.app on the Mac and the iPhone; for calendars and contacts install the Calendar & Contacts profile (Nubilo app → Calendar & Contacts). Older agents' `photos`, `calendars`, `reminders` and `sync_contacts` settings in `agent.json` are ignored.

**Calendar mirror.** For calendars only a particular Mac can reach, such as a work Exchange account, the Nubilo Mac app mirrors chosen Mac calendars one way into read-only server calendars (Nubilo app → Calendar & Contacts → Mirror Mac Calendars). They show up read-only in the CalDAV account on your other devices: the server marks such a calendar with `"mirror": true`, only the device that owns the mirror may write to it, and edits from anything else are refused (`read-only mirror`, HTTP 403 over CalDAV). Deleting the whole mirrored calendar stays possible. Nothing is written back to the Mac. Events from 30 days ago to a year ahead are copied, a repeating meeting as one repeating event with its moved and cancelled occurrences (or, for a repeat rule that cannot be copied faithfully, each occurrence as its own event); the copy updates while the app runs (Settings → Open at Login), shortly after Calendar changes and every 15 minutes. If one of your own calendars already has the same name, the copy is named "… (Mac)". Turning a calendar off leaves its copy until you delete it. Check your employer's policy before copying work calendars to a personal server.

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
nubilo agent | agent setup|ui|install|uninstall|service
nubilo agent files add|remove|list|on|off
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
