#!/usr/bin/env bash
# Runs the NubiloKit Swift tests, including the live end-to-end test against a
# throwaway loopback server (works on Linux with a Swift toolchain).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
PORT="${PORT:-18666}"
trap 'kill "${PID:-0}" 2>/dev/null || true; rm -rf "$TMP"' EXIT
go build -o "$TMP/nubilo" "$ROOT/cmd/nubilo"
"$TMP/nubilo" --data-dir "$TMP/data" init --listen "127.0.0.1:$PORT" >/dev/null
# Loopback without TLS keeps URLSession from needing a trust override.
python3 - "$TMP/data/config.json" <<'PY'
import json, sys
p = sys.argv[1]; c = json.load(open(p))
c["tls"].update(cert="", key="", auto=False)
json.dump(c, open(p, "w"), indent=2)
PY
INVITE=$("$TMP/nubilo" --data-dir "$TMP/data" --json users invite --role admin | python3 -c 'import json,sys; print(json.load(sys.stdin)["code"])')
INVITE_2=$("$TMP/nubilo" --data-dir "$TMP/data" --json users invite --role member | python3 -c 'import json,sys; print(json.load(sys.stdin)["code"])')
"$TMP/nubilo" --data-dir "$TMP/data" server >"$TMP/server.log" 2>&1 &
PID=$!
sleep 1
cd "$ROOT/apps/Nubilo/Packages/NubiloKit"
NUBILO_TEST_URL="http://127.0.0.1:$PORT" NUBILO_TEST_INVITE="$INVITE" NUBILO_TEST_INVITE_2="$INVITE_2" swift test
