#!/usr/bin/env bash
# Build a Release Nubilo.app for this Mac and install it in /Applications,
# so it runs without Xcode.
#
#   scripts/build-mac-app.sh            build, install, launch
#   scripts/build-mac-app.sh --no-install   build only (prints the .app path)
#
# Needs Xcode, xcodegen and apps/Nubilo/project.local.yml with your
# DEVELOPMENT_TEAM (see apps/Nubilo/README.md). Signing is the same automatic
# "Apple Development" signing as a run from Xcode, so the app runs on Macs
# registered to your team (this one is, once Xcode has run the app here).
#
# The bundled `nubilo` CLI is built with relay support when a wired checkout
# is found (WIRED_DIR, default ../wired), without it otherwise. Set
# NUBILO_CLI_SOURCE to bundle an existing binary instead.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP_DIR="$ROOT/apps/Nubilo"
DERIVED="$ROOT/build/mac-release"
INSTALL=1
[[ "${1:-}" == "--no-install" ]] && INSTALL=0

if [[ "$(uname)" != Darwin ]]; then
  echo "run this on the Mac" >&2
  exit 2
fi
for tool in xcodebuild xcodegen go; do
  command -v "$tool" >/dev/null || { echo "$tool not found" >&2; exit 2; }
done
if [[ ! -f "$APP_DIR/project.local.yml" ]]; then
  echo "missing $APP_DIR/project.local.yml (DEVELOPMENT_TEAM, see apps/Nubilo/README.md)" >&2
  exit 2
fi

ARCH="$(uname -m)"
GOARCH_T=arm64
[[ "$ARCH" == x86_64 ]] && GOARCH_T=amd64
VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
VERSION="${VERSION#v}"

# 1. The nubilo CLI the app runs from Contents/Helpers.
if [[ -z "${NUBILO_CLI_SOURCE:-}" ]]; then
  WIRED_DIR="${WIRED_DIR:-$ROOT/../wired}"
  export NUBILO_CLI_SOURCE="$ROOT/bin/nubilo"
  if [[ -e "$WIRED_DIR/.git" ]]; then
    echo "==> nubilo CLI with relay support ($VERSION)"
    WIRED_DIR="$WIRED_DIR" NUBILO_VERSION="$VERSION" \
      "$ROOT/scripts/build-wired.sh" "$NUBILO_CLI_SOURCE" "darwin/$GOARCH_T"
  else
    echo "==> nubilo CLI without relay support (no wired checkout at $WIRED_DIR)"
    (cd "$ROOT" && go build -trimpath \
      -ldflags "-X nubilo/internal/version.String=$VERSION" \
      -o "$NUBILO_CLI_SOURCE" ./cmd/nubilo)
  fi
fi

# 2. The Xcode project.
echo "==> xcodegen"
(cd "$APP_DIR" && xcodegen generate --quiet)

# 3. Release build for this Mac's architecture only.
echo "==> xcodebuild Release ($ARCH)"
xcodebuild \
  -project "$APP_DIR/Nubilo.xcodeproj" \
  -scheme Nubilo \
  -configuration Release \
  -destination "platform=macOS,arch=$ARCH" \
  -derivedDataPath "$DERIVED" \
  -allowProvisioningUpdates \
  ONLY_ACTIVE_ARCH=YES \
  build -quiet
APP="$DERIVED/Build/Products/Release/Nubilo.app"
codesign --verify --deep --strict "$APP"
echo "built $APP"
[[ "$INSTALL" == 1 ]] || exit 0

# 4. Install. Quit the running copy first (Xcode's or an older install).
echo "==> installing /Applications/Nubilo.app"
osascript -e 'tell application id "dev.nubilo.app" to quit' 2>/dev/null || true
for _ in {1..20}; do
  pgrep -x Nubilo >/dev/null || break
  sleep 0.5
done
rm -rf /Applications/Nubilo.app
ditto "$APP" /Applications/Nubilo.app

# Debug builds in DerivedData carry the same bundle ids, so Launch Services
# and the Finder extension may pick one of them over the installed app.
# Unregistering them is harmless: Xcode registers them again on the next run.
LSREGISTER=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
find "$HOME/Library/Developer/Xcode/DerivedData" "$DERIVED" -name Nubilo.app -type d -prune 2>/dev/null |
  while read -r stale; do "$LSREGISTER" -u "$stale" 2>/dev/null || true; done
"$LSREGISTER" -f /Applications/Nubilo.app

open /Applications/Nubilo.app
echo "installed and launched /Applications/Nubilo.app ($VERSION)"
