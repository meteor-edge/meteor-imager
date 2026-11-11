#!/usr/bin/env bash
# Builds a universal MeteorImager.app, a .dmg and CLI tarballs from
# dist/darwin_{amd64,arm64}/meteor-imager. Runs on macOS.
# Usage: scripts/package-macos.sh <version>
set -euo pipefail
version="${1#v}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/dist/release"
mkdir -p "$out"
work="$(mktemp -d)"

lipo -create -output "$work/meteor-imager" "$root/dist/darwin_amd64/meteor-imager" "$root/dist/darwin_arm64/meteor-imager"
for arch in amd64 arm64; do
  tar -C "$root/dist/darwin_$arch" -czf "$out/meteor-imager_${version}_darwin_${arch}.tar.gz" meteor-imager THIRD_PARTY_LICENSES.txt -C "$root" LICENSE README.md
done

app="$work/dmg/Meteor Imager.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$work/meteor-imager" "$app/Contents/MacOS/meteor-imager"
# Bundle versions must be numeric: 0.2.0-rc.1 -> 0.2.0.
sed "s/@VERSION@/${version%%-*}/g" "$root/packaging/macos/Info.plist" > "$app/Contents/Info.plist"
cp "$root/dist/darwin_arm64/THIRD_PARTY_LICENSES.txt" "$root/LICENSE" "$app/Contents/Resources/"

iconset="$work/MeteorImager.iconset"
mkdir -p "$iconset"
for s in 16 32 128 256 512; do
  sips -z $s $s "$root/packaging/icon/icon.png" --out "$iconset/icon_${s}x${s}.png" >/dev/null
  d=$((s * 2)); [[ $d -le 512 ]] && sips -z $d $d "$root/packaging/icon/icon.png" --out "$iconset/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$app/Contents/Resources/MeteorImager.icns"

# Ad-hoc signature so Apple silicon runs it; notarization needs a Developer ID (not set up yet).
codesign --force --deep --sign - "$app"
ln -s /Applications "$work/dmg/Applications"
hdiutil create -volname "Meteor Imager" -srcfolder "$work/dmg" -ov -format UDZO "$out/MeteorImager-${version}.dmg"
rm -rf "$work"
ls -la "$out"
