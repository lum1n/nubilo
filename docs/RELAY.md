# Reaching Nubilo from anywhere

Phones and laptops away from home need a public HTTPS address with a certificate Apple trusts (iOS Calendar and Contacts use plain CalDAV/CardDAV and cannot use an in-app tunnel). `nubilo setup` offers four ways:

| Mode | What runs where | Router changes |
| --- | --- | --- |
| **relay** (recommended) | Home server joins a [wired](https://github.com/lum1n/wired) overlay; a small VPS runs `nubilo relay`, which forwards public TCP 443/80 into it | none |
| **proxy** | Like relay, but the relay runs behind a TLS-terminating proxy such as [exe.dev](https://exe.dev) (one HTTPS name, no public ports). The proxy holds the certificate and **can read the traffic** | none |
| **direct** | The server itself listens on :443/:80 (a VPS, or a port forward at home) | forward 443 and 80 |
| **lan** | LAN or Tailscale only, self-signed certificate | none |

In relay and direct mode the home server obtains a Let's Encrypt certificate for your domain by itself (TLS-ALPN-01 on 443, HTTP-01 on 80 as fallback) and terminates TLS. The VPS only moves encrypted bytes: it holds no keys and no data, and can at worst stop traffic. See SECURITY.md §15a.

## Relay setup

You need a domain name you control and a Linux VPS with a public IP (the smallest size is plenty).

1. **Build** a binary with wired support (needs Go and Rust):

   ```sh
   WIRED_DIR=~/src/wired scripts/build-wired.sh bin/nubilo linux/amd64
   ```

   `nubilo version` prints `wired: yes`. Default `go build` produces binaries without wired (`wired: no`) that can run in lan or direct mode.

2. **VPS:** copy the binary and run

   ```sh
   scripts/install-relay.sh --public-host relay.example.com --binary ./nubilo
   ```

   or by hand:

   ```sh
   nubilo relay init --public-host 203.0.113.7   # prints the relay invite (a secret) and a QR code
   nubilo relay install                         # systemd user service; or `nubilo relay run`
   ```

   Open TCP 80, 443, 7443 and UDP 7443, 7842. On Linux, low ports for a user service need `sysctl net.ipv4.ip_unprivileged_port_start=80` and `loginctl enable-linger $USER` (the script does both).

3. **DNS:** point an A (and AAAA) record for your domain, e.g. `cloud.example.com`, at the VPS.

4. **Home server:**

   ```sh
   nubilo setup                      # choose relay, enter the domain and paste the invite
   # or non-interactively:
   nubilo setup --yes --mode relay --domain cloud.example.com --email you@example.com --relay-invite 'wired1…'
   ```

   Setup joins the overlay, installs the service, waits until `https://cloud.example.com/api/v1/status` answers, and prints an admin invite QR code for the Nubilo app.

5. **Check:** `nubilo doctor` shows `DNS`, `relay — connected (direct path)` (or `relay path` when the peers cannot reach each other directly), `https://cloud.example.com` and `public certificate`.

Relay housekeeping on the VPS:

```sh
nubilo relay members             # nodes and how traffic flows to them
nubilo relay invite --rotate     # new invite; existing members stay joined
nubilo relay revoke NAME         # remove a node (also rotates the invite)
```

## Behind a TLS-terminating proxy (exe.dev)

Hosts like exe.dev give a VM one HTTPS name (`<vm>.exe.xyz`) whose proxy terminates TLS and forwards plain HTTP to one port; there are no public ports for the VPS relay. `nubilo relay --behind-proxy` runs there instead: on that one port it serves wired's relay (`/relay`, `/ping`) and forwards every other request over the overlay to the home server's plain-HTTP `web` service.

**Trade-off.** The proxy holds the certificate and sees every request in plaintext: calendars, contacts, files, photos and app passwords. wired encrypts only the hop from the VM to your home. Use the VPS relay above when TLS must end at home. See SECURITY.md §15c.

1. **VM** (exe.dev forwards to port 8000 by default):

   ```sh
   ssh exe.dev new --name=nubilo
   scp bin/nubilo nubilo.exe.xyz:~/.local/bin/nubilo      # built with scripts/build-wired.sh
   ssh nubilo.exe.xyz
   nubilo relay init --behind-proxy https://nubilo.exe.xyz   # prints the relay invite (a secret)
   XDG_RUNTIME_DIR=/run/user/$(id -u) nubilo relay install
   ```

   Then make it public: `ssh exe.dev share set-public nubilo` (and `ssh exe.dev share port nubilo 8000` if the VM forwards elsewhere). A custom domain works too: a CNAME to `nubilo.exe.xyz`, `ssh exe.dev domain add nubilo cloud.example.com`, and `--behind-proxy https://cloud.example.com`.

2. **Home server:**

   ```sh
   nubilo setup --mode proxy --domain nubilo.exe.xyz --relay-invite 'wired1…'
   ```

   No ACME runs at home in this mode. The relay passes the client address (the last `X-Forwarded-For` entry the proxy added) in its own header, which the home server trusts on the overlay listener only.

3. **Check:** `curl -s -o /dev/null -w '%{http_code}' https://nubilo.exe.xyz/api/v1/status` prints `401`, and `nubilo doctor` shows `relay — connected` plus a `TLS termination` warning that names the proxy. Paired apps switch to the public address on their own (they read `public_url` from `/api/v1/me`). Install the Calendar & Contacts profile again from the app so the accounts use it.

Without inbound UDP on the VM, traffic flows on wired's relay path (through the proxy's WebSocket) rather than a direct path; that is expected.

## Direct setup

```sh
nubilo setup --yes --mode direct --domain cloud.example.com --email you@example.com
```

The server listens on `public.direct_listen` (`:443`) and `:80` in addition to the LAN listener. Forward both ports on your router if the host is behind NAT.

## Smoke test (do this on Let's Encrypt staging first)

Staging avoids the production rate limits while you test DNS and the relay path.

1. VPS: `nubilo relay init --public-host <ip> && nubilo relay run`.
2. Home: `nubilo setup --mode relay --domain <domain> --relay-invite '<invite>' --acme-staging`.
3. From a phone on mobile data (not Wi-Fi): `curl -vk https://<domain>/api/v1/status` returns **401**; the certificate is issued by "(STAGING) …".
4. `nubilo doctor` is green apart from the expected staging warning on `public certificate`.
5. Stop the relay (`systemctl --user stop nubilo-relay`): `nubilo doctor` reports `relay: unreachable`, while the LAN URL (`https://<lan-ip>:8443`) still answers.
6. Switch to production: delete `public.acme.directory` from `config.json` (or rerun setup without `--acme-staging`), remove `<data-dir>/acme`, restart the server, and repeat step 3 without `-k`.

Verified locally (loopback ports instead of 443/80, no ACME): HTTPS through the relay reaches the home server, `:80` redirects to HTTPS, public responses carry HSTS, `relay members` shows the home node on a direct path, and stopping the relay makes doctor report `relay: unreachable` while the LAN listener keeps working. TLS-ALPN-01 through the relay has **not** yet been verified against Let's Encrypt; HTTP-01 on the forwarded :80 is the fallback.

## Limits

- The relay passes the client address to the home server in a PROXY v1 header, so per-IP rate limits and logs see real clients (trusted only on the overlay listener; see SECURITY.md §15).
- Builds with wired are only produced for targets where `scripts/build-wired.sh` can compile the Rust library (linux amd64/arm64 and darwin arm64 in practice).
