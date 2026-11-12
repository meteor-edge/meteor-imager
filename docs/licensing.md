# Third-party components

Meteor Imager is licensed under Apache-2.0. Everything below is linked into the binary. Each one
is under a permissive licence compatible with Apache-2.0, and binary releases must ship the
licence texts (see `THIRD_PARTY_LICENSES` in release archives).

| Module | Version | License | Use |
|---|---|---|---|
| [github.com/soypat/fat](https://github.com/soypat/fat) | v0.2.1 + local patches | BSD-3-Clause (a port of ChaN's FatFs, 1-clause BSD-style licence) | FAT filesystem edits on the boot partition. Vendored in `third_party/soypat-fat`; see its `PATCHES.md`. |
| [github.com/ulikunitz/xz](https://github.com/ulikunitz/xz) | v0.5.15 | BSD-3-Clause | `.xz` decompression |
| [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml) | v3.0.1 | MIT and Apache-2.0 | cloud-init YAML |
| [github.com/godbus/dbus/v5](https://github.com/godbus/dbus) | v5.1.0 | BSD-2-Clause | udisks2 on Linux |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | v0.35.0 | BSD-3-Clause | raw disk and OS APIs |
| [golang.org/x/term](https://pkg.go.dev/golang.org/x/term) | v0.34.0 | BSD-3-Clause | hidden password prompts |
| Go standard library | | BSD-3-Clause | |

Release builds use `-tags fat_noexfat`, which leaves soypat/fat's exFAT code out of the binary.

SHA-512 crypt (user password hashes) is implemented in `internal/shacrypt` from the published
algorithm; no third-party code.

## Tools run but not shipped

| Tool | License | Use |
|---|---|---|
| [e2fsprogs](https://e2fsprogs.sourceforge.net/) (`e2fsck`, `resize2fs`, `dumpe2fs`) | GPL-2.0 | Optional. `backup` runs them as separate programs, when the user has installed them, to shrink a copy of the root filesystem. Not linked, bundled or redistributed. |

Before adding a dependency, check its licence (no GPL/LGPL/AGPL or unclear terms) and add it to
this table. To regenerate the list of linked modules:

```sh
go list -deps -f '{{if not .Standard}}{{.Module.Path}} {{.Module.Version}}{{end}}' ./cmd/meteor-imager | sort -u
```
