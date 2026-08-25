#!/usr/bin/env bash
# Ad-hoc codesign nubilo so PhotoKit / EventKit / Contacts TCC prompts can appear.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${1:-$(command -v nubilo || true)}"
if [[ -z "${BIN}" || ! -x "${BIN}" ]]; then
  echo "usage: $0 /path/to/nubilo" >&2
  echo "or: go install ./cmd/nubilo && $0 \"\$(go env GOPATH)/bin/nubilo\"" >&2
  exit 2
fi
ENT="$ROOT/cmd/nubilo/nubilo.entitlements"
codesign --force --sign - --entitlements "$ENT" "$BIN"
echo "signed $BIN (ad-hoc)"
echo "re-run: nubilo agent --data-dir ~/.nubilo-agent albums"
echo "if still denied: System Settings → Privacy & Security → Photos → enable your Terminal (or nubilo)"
