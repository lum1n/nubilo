#!/usr/bin/env bash
# Build a Release Nubilo.app on the Mac and install it in /Applications, so it
# runs without Xcode.
#
#   scripts/build-mac-app.sh                 Developer ID, notarized DMG, install
#   scripts/build-mac-app.sh --dev           Apple Development signing, no
#                                            notarization (this team's Macs only)
#   scripts/build-mac-app.sh --no-install    build only
#
# Needs Xcode, xcodegen and apps/Nubilo/project.local.yml with your
# DEVELOPMENT_TEAM (see apps/Nubilo/README.md). The default build also needs a
# Developer ID Application certificate and a notarytool keychain profile
# (NOTARY_PROFILE, default lapp-notary: the credentials are per team, not per
# app):
#
#   xcrun notarytool store-credentials lapp-notary --apple-id <you> --team-id <team>
#
# The bundled `nubilo` CLI is built with relay support when a wired checkout
# is found (WIRED_DIR, default ../wired), without it otherwise. Set
# NUBILO_CLI_SOURCE to bundle an existing binary instead.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP_DIR="$ROOT/apps/Nubilo"
OUT="$ROOT/build/macos"
PROFILE="${NOTARY_PROFILE:-lapp-notary}"
DEV=0
INSTALL=1
for arg in "$@"; do
  case "$arg" in
    --dev) DEV=1 ;;
    --no-install) INSTALL=0 ;;
    *) echo "unknown option $arg" >&2; exit 2 ;;
  esac
done

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
TEAM="$(sed -n 's/.*DEVELOPMENT_TEAM: *\([A-Z0-9]\{10\}\).*/\1/p' "$APP_DIR/project.local.yml" | head -1)"
if [[ "$DEV" == 0 && -z "$TEAM" ]]; then
  echo "no DEVELOPMENT_TEAM in project.local.yml; Developer ID export needs it (or use --dev)" >&2
  exit 2
fi

ARCH="$(uname -m)"
GOARCH_T=arm64
[[ "$ARCH" == x86_64 ]] && GOARCH_T=amd64
VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
VERSION="${VERSION#v}"
rm -rf "$OUT" && mkdir -p "$OUT"

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

# 3. Build. The CLI is built for this Mac only, so the app is too.
if [[ "$DEV" == 1 ]]; then
  echo "==> xcodebuild Release, Apple Development ($ARCH)"
  xcodebuild -project "$APP_DIR/Nubilo.xcodeproj" -scheme Nubilo \
    -configuration Release -destination "platform=macOS,arch=$ARCH" \
    -derivedDataPath "$OUT/derived" -allowProvisioningUpdates \
    ONLY_ACTIVE_ARCH=YES build -quiet
  APP="$OUT/derived/Build/Products/Release/Nubilo.app"
  codesign --verify --deep --strict "$APP"
else
  echo "==> xcodebuild archive ($ARCH)"
  xcodebuild -project "$APP_DIR/Nubilo.xcodeproj" -scheme Nubilo \
    -configuration Release -destination "generic/platform=macOS" \
    -archivePath "$OUT/Nubilo.xcarchive" -allowProvisioningUpdates \
    ARCHS="$ARCH" archive -quiet

  echo "==> export, Developer ID"
  cat >"$OUT/ExportOptions.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>method</key>
	<string>developer-id</string>
	<key>teamID</key>
	<string>$TEAM</string>
	<key>signingStyle</key>
	<string>automatic</string>
</dict>
</plist>
EOF
  xcodebuild -exportArchive -archivePath "$OUT/Nubilo.xcarchive" \
    -exportPath "$OUT/export" -exportOptionsPlist "$OUT/ExportOptions.plist" \
    -allowProvisioningUpdates
  APP="$OUT/export/Nubilo.app"
  codesign --verify --deep --strict "$APP"
  # The export re-signs nested code; the helper must carry Developer ID too
  # or notarization rejects it.
  for code in "$APP" "$APP/Contents/Helpers/nubilo"; do
    [[ -e "$code" ]] || continue
    if ! codesign -dvv "$code" 2>&1 | grep -q '^Authority=Developer ID Application'; then
      echo "$code is not signed with Developer ID:" >&2
      codesign -dvv "$code" 2>&1 | grep '^Authority' >&2
      exit 1
    fi
  done

  echo "==> DMG"
  DMG="$OUT/Nubilo-$VERSION.dmg"
  mkdir -p "$OUT/dmg"
  cp -R "$APP" "$OUT/dmg/"
  ln -s /Applications "$OUT/dmg/Applications"
  hdiutil create -quiet -volname Nubilo -srcfolder "$OUT/dmg" -ov -format UDZO "$DMG"
  # Xcode may sign with a cloud-managed certificate, leaving no local key for
  # the DMG. A signed DMG is optional; notarization covers the app inside.
  IDENTITY="$(security find-identity -v -p codesigning | grep -o "\"Developer ID Application: .*($TEAM)\"" | head -1 | tr -d '"' || true)"
  if [[ -n "$IDENTITY" ]]; then
    codesign --sign "$IDENTITY" --timestamp "$DMG"
  else
    echo "No local Developer ID identity; notarizing an unsigned DMG."
  fi

  echo "==> notarize (profile $PROFILE)"
  xcrun notarytool submit "$DMG" --keychain-profile "$PROFILE" --wait
  xcrun stapler staple "$DMG"
  xcrun stapler staple "$APP"
  spctl --assess --type execute "$APP"
  echo "notarized $DMG"
fi
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
find "$HOME/Library/Developer/Xcode/DerivedData" "$OUT" -name Nubilo.app -type d -prune 2>/dev/null |
  while read -r stale; do "$LSREGISTER" -u "$stale" 2>/dev/null || true; done
"$LSREGISTER" -f /Applications/Nubilo.app

open /Applications/Nubilo.app
echo "installed and launched /Applications/Nubilo.app ($VERSION)"
