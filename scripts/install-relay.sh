#!/usr/bin/env bash
# One-shot relay setup on a fresh Linux VPS (run as the user that will own it):
#
#   curl -fsSL <url>/install-relay.sh | bash -s -- --public-host relay.example.com --binary ./nubilo
#
# Installs the nubilo binary (built with scripts/build-wired.sh), lets
# unprivileged processes bind 80/443, opens the firewall if ufw is active,
# initializes the relay, and installs it as a systemd user service.
set -euo pipefail
PUBLIC_HOST=""
BINARY="./nubilo"
HOME_NODE="home"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --public-host) PUBLIC_HOST="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --home) HOME_NODE="$2"; shift 2 ;;
    *) echo "unknown flag $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$PUBLIC_HOST" ]]; then
  PUBLIC_HOST="$(curl -fsS https://api.ipify.org || true)"
  [[ -n "$PUBLIC_HOST" ]] || { echo "pass --public-host IP_OR_DNS" >&2; exit 2; }
fi
mkdir -p "$HOME/.local/bin"
install -m 0755 "$BINARY" "$HOME/.local/bin/nubilo"
NUBILO="$HOME/.local/bin/nubilo"
if ! "$NUBILO" version | grep -q 'wired: yes'; then
  echo "this nubilo binary has no wired support; build it with scripts/build-wired.sh" >&2
  exit 1
fi
# Low ports for a user service (systemd user units cannot hold capabilities).
echo 'net.ipv4.ip_unprivileged_port_start=80' | sudo tee /etc/sysctl.d/60-nubilo-relay.conf >/dev/null
sudo sysctl -q --system
if command -v ufw >/dev/null && sudo ufw status | grep -q active; then
  sudo ufw allow 80/tcp && sudo ufw allow 443/tcp
  sudo ufw allow 7443/tcp && sudo ufw allow 7443/udp && sudo ufw allow 7842/udp
fi
sudo loginctl enable-linger "$USER"
"$NUBILO" relay init --public-host "$PUBLIC_HOST" --home "$HOME_NODE"
"$NUBILO" relay install
echo
echo "Relay is running. On the home server run: nubilo setup  → relay, and paste the invite above."
echo "Point your domain's A record at $PUBLIC_HOST."
