#!/usr/bin/env bash
# Build Nubilo.app on the Mac as a notarized DMG: Release archive, Developer
# ID export, DMG, notarization and stapling. Open the DMG and drag Nubilo to
# Applications to install it.
#
#   scripts/build-mac-app.sh     writes build/macos/Nubilo-<version>.dmg
#
# Needs Xcode, xcodegen, apps/Nubilo/project.local.yml with your
# DEVELOPMENT_TEAM (see apps/Nubilo/README.md), a Developer ID Application
# certificate and a notarytool keychain profile (NOTARY_PROFILE, default
# lapp-notary: the credentials are per team, not per app):
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
if [[ -z "$TEAM" ]]; then
  echo "no DEVELOPMENT_TEAM in project.local.yml; the Developer ID export needs it" >&2
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

# 3. Archive for this Mac only (the CLI is built for it too) and export.
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
  # Not piped into grep -q: with pipefail, codesign's SIGPIPE fails the test.
  info="$(codesign -dvv "$code" 2>&1)"
  if [[ "$info" != *$'\nAuthority=Developer ID Application'* ]]; then
    echo "$code is not signed with Developer ID:" >&2
    grep '^Authority' <<<"$info" >&2 || true
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
