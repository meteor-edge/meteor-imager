#!/usr/bin/env bash
# Builds .tar.gz, .deb and (amd64) AppImage from dist/linux_<arch>/meteor-imager.
# Usage: scripts/package-linux.sh <version> <arch: amd64|arm64>
set -euo pipefail
version="${1#v}"; arch="$2"
root="$(cd "$(dirname "$0")/.." && pwd)"
src="$root/dist/linux_$arch"
out="$root/dist/release"
mkdir -p "$out"

# tar.gz
tar -C "$src" -czf "$out/meteor-imager_${version}_linux_${arch}.tar.gz" meteor-imager THIRD_PARTY_LICENSES.txt -C "$root" LICENSE README.md

# .deb
pkg="$(mktemp -d)"; chmod 755 "$pkg"
install -Dm755 "$src/meteor-imager" "$pkg/usr/bin/meteor-imager"
install -Dm644 "$root/packaging/linux/meteor-imager.desktop" "$pkg/usr/share/applications/meteor-imager.desktop"
install -Dm644 "$root/packaging/icon/icon.png" "$pkg/usr/share/icons/hicolor/512x512/apps/meteor-imager.png"
install -Dm644 "$root/LICENSE" "$pkg/usr/share/doc/meteor-imager/copyright"
install -Dm644 "$src/THIRD_PARTY_LICENSES.txt" "$pkg/usr/share/doc/meteor-imager/THIRD_PARTY_LICENSES.txt"
mkdir -p "$pkg/DEBIAN"
cat > "$pkg/DEBIAN/control" <<CONTROL
Package: meteor-imager
Version: ${version//-/\~}
Architecture: $arch
Maintainer: Meteor Edge <noreply@github.com>
Homepage: https://github.com/meteor-edge/meteor-imager
Recommends: udisks2, policykit-1 | polkitd, xdg-utils, zenity
Suggests: dosfstools
Section: utils
Priority: optional
Description: Write Raspberry Pi OS cards that enroll in MeteorCloud
 Meteor Imager writes Raspberry Pi OS images to SD cards and sets up Wi-Fi
 and MeteorCloud enrollment on the card's boot partition.
CONTROL
dpkg-deb --root-owner-group --build "$pkg" "$out/meteor-imager_${version}_${arch}.deb"
rm -rf "$pkg"

# AppImage (x86_64 only; appimagetool is fetched by the release workflow)
if [[ "$arch" == amd64 && -n "${APPIMAGETOOL:-}" ]]; then
  app="$(mktemp -d)/MeteorImager.AppDir"
  install -Dm755 "$src/meteor-imager" "$app/usr/bin/meteor-imager"
  install -Dm644 "$root/packaging/linux/meteor-imager.desktop" "$app/meteor-imager.desktop"
  install -Dm644 "$root/packaging/icon/icon.png" "$app/meteor-imager.png"
  install -Dm644 "$src/THIRD_PARTY_LICENSES.txt" "$app/usr/share/doc/meteor-imager/THIRD_PARTY_LICENSES.txt"
  cat > "$app/AppRun" <<'APPRUN'
#!/bin/sh
exec "$(dirname "$(readlink -f "$0")")/usr/bin/meteor-imager" "$@"
APPRUN
  chmod +x "$app/AppRun"
  ARCH=x86_64 "$APPIMAGETOOL" --no-appstream "$app" "$out/MeteorImager-${version}-x86_64.AppImage"
fi
ls -la "$out"
