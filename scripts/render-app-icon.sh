#!/usr/bin/env bash
# Renders apps/Nubilo/Design/AppIcon-*.svg into the app's AppIcon asset set.
# Needs rsvg-convert (librsvg) and ImageMagick.
set -euo pipefail
cd "$(dirname "$0")/../apps/Nubilo"
SET=App/Assets.xcassets/AppIcon.appiconset
mkdir -p "$SET"

# iOS: one full-bleed 1024 image, no alpha (the system rounds the corners).
rsvg-convert -w 1024 -h 1024 Design/AppIcon-iOS.svg | magick - -alpha remove -alpha off "$SET/ios-1024.png"

# macOS: the rounded body with its shadow is part of the image.
for px in 16 32 64 128 256 512 1024; do
  rsvg-convert -w "$px" -h "$px" Design/AppIcon-macOS.svg -o "$SET/mac-$px.png"
done
