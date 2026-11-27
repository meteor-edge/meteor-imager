# Contributing

- `make test` and `make vet` must pass. Tests build synthetic Pi disk images; install
  `dosfstools` so they also run `fsck.vfat` on the result.
- Never log, persist or echo the Wi-Fi password or the enrollment key: not in errors, debug
  output, prefs or crash reports. `profile.Redacted()` gives a copy that is safe to log.
- Changes to what is written to the card must update [docs/contract.md](docs/contract.md) and keep
  older images working. FlashProfile changes need a new `schema_version`.
- Third-party code: only permissive licences (MIT, BSD, Apache-2.0, ISC). Add new modules to
  [docs/licensing.md](docs/licensing.md); `scripts/third-party-licenses.sh` must still succeed.
- Anything that touches raw disks needs a pass through the relevant part of
  [docs/manual-test-checklist.md](docs/manual-test-checklist.md) on real hardware.

## Releasing

`.github/workflows/release.yml` builds Windows, macOS and Linux packages and publishes a GitHub
release. Start it either way:

- **Actions tab:** Release → Run workflow → pick the branch (usually `main`) → version `v0.2.0`.
  The tag is created on the built commit when the release is published.
- **Tag:** `git tag v0.2.0 && git push origin v0.2.0`.

A version with a `-` (`v0.3.0-rc.1`) is a pre-release. Any other version becomes **Latest**, so
`https://github.com/meteor-edge/meteor-imager/releases/latest/download/MeteorImager-setup.exe`
(and `MeteorImager.dmg`, `MeteorImager-x86_64.AppImage`, `meteor-imager_amd64.deb`,
`meteor-imager_arm64.deb`) always serve the newest stable build.

