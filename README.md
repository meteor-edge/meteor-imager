# Meteor Imager

Meteor Imager writes Raspberry Pi OS to an SD card and prepares the card to join your
[MeteorCloud](https://github.com/meteor-edge/meteor-cloud) fleet on first boot: Wi-Fi, the
MeteorCloud domain and an enrollment key go onto the card's boot partition, and the Pi enrolls itself.

It runs on Windows, macOS and Linux as one self-contained program. Run it with no arguments and
the desktop UI opens in your browser. Subcommands give the same features on the command line,
for scripts and for flashing many cards.

```
┌──────────────┐   1. list + download (no login)   ┌──────────────┐
│ Meteor Imager│ ─────────────────────────────────▶│  MeteorCloud │
│  (your PC)   │   GET /api/v1/os-images           │  (server)    │
└──────┬───────┘                                   └──────▲───────┘
       │ 2. write, verify, configure boot partition       │ 4. enrolls with key
       ▼                                                  │
┌──────────────┐        3. first boot (cloud-init)  ┌─────┴────────┐
│   SD card    │ ──────────────────────────────────▶│ Raspberry Pi │
└──────────────┘                                    └──────────────┘
```

## Download

Get the build for your system from [Releases](https://github.com/meteor-edge/meteor-imager/releases):

| System | File | Notes |
|---|---|---|
| Windows | `MeteorImager-<version>-setup.exe` or `meteor-imager_<version>_windows_amd64.zip` | Asks for administrator rights when it starts (needed to write disks). |
| macOS | `MeteorImager-<version>.dmg` (Apple silicon and Intel) | Not notarized yet: the first time, right-click the app → Open. |
| Ubuntu / Debian | `meteor-imager_<version>_amd64.deb` / `arm64.deb` | `sudo apt install ./meteor-imager_*.deb` |
| Other Linux | `MeteorImager-<version>-x86_64.AppImage` or the `.tar.gz` | `chmod +x` the AppImage and run it. |

## Use the desktop UI

1. **Image.** Enter your MeteorCloud address (for example `fleet.example.com` or `192.168.0.10:8000`)
   and pick one of the OS images the server publishes. No login is needed. You can also pick a local
   `.img`, `.img.xz`, `.gz` or `.zip` file.
2. **Settings.** Two tabs:
   - **General**, like Raspberry Pi Imager: hostname, timezone, **username and password** (the
     image's first user is renamed; only a hash of the password goes on the card), **Wi-Fi** name,
     password and country, and **SSH** (password login, or public key only with your key). SSH with
     password login is on by default, so after first boot you can `ssh <user>@<hostname>.local` with
     the username and password you set; untick **Enable SSH** to leave the image's SSH setting alone.
   - **MeteorCloud**: the **domain** the Pi connects to (filled in from the address), an
     **enrollment key** (`key_…`, created in MeteorCloud under Devices → Enrollment keys) and an
     optional device name. Untick it to write a card that does not register itself.
3. **Card.** Pick the SD card. Only removable disks are listed. Disks that hold the running system are
   never listed.
4. **Write.** Confirm, and Meteor Imager writes the image, reads it back to verify it, writes the
   settings to the boot partition and ejects the card.

Put the card in the Pi and power it on. On first boot, cloud-init joins the Wi-Fi network and runs
`meteor-firstrun.sh` from the boot partition. It installs `meteorcli` if it is missing and sends
MeteorCloud a token request. Once the request is sent, it deletes itself and `.meteorenv` from the
card. The device then shows up in MeteorCloud as a pending enrollment. Approve it there, and
`meteorcli.service` (which keeps waiting across reboots) picks up the token and starts the agent.

To write the next card, press **Write another card**. Your settings stay filled in until you quit.

### Make your own image (Docker, packages, meteorcli preinstalled)

1. Write a plain OS (for example Raspberry Pi OS Lite Bookworm), boot it and install what every
   device needs: Docker, packages, `meteorcli`.
2. On the device run meteor-cloud's prepare script. It installs MeteorCloud first boot, removes the
   device's identity (enrollment, keys, Wi-Fi, SSH host keys, machine-id, logs) and powers off:
   ```sh
   curl -fsSLO https://github.com/meteor-edge/meteor-cloud/releases/latest/download/meteorcli-prepare-image.sh
   sudo bash meteorcli-prepare-image.sh
   ```
3. Put the card in your computer, open **Back up a card**, pick the card and save
   `….img.gz`. With e2fsprogs installed the image is shrunk to the data on the card, so it fits
   smaller cards; each card grows it back on first boot.
4. Write cards from it right away (**Write this backup to a card** when the backup finishes, or
   **Local file** and pick the `.img.gz`), or share it: upload the file in MeteorCloud as an OS
   image and tick **Show in Meteor Imager**.
5. Your General and MeteorCloud settings are applied to each card. Each device boots with its own
   keys and IDs, and asks to be enrolled.

When a card is done, **Write another card** keeps the image and settings; **Start over** clears
them and goes back to choosing an image.

### Publishing images on MeteorCloud

The server only lists OS images that an owner or admin has published. In the MeteorCloud console,
open **Artifacts**, upload an OS image, and tick **Show in Meteor Imager** in the upload dialog or
in the **Meteor Imager** column. See [docs/meteorcloud.md](docs/meteorcloud.md).

## Use the command line

```sh
meteor-imager images --server fleet.example.com          # published OS images
meteor-imager list-devices                                # removable cards

# Write a published image with Wi-Fi and enrollment. Secrets come from the
# environment or a hidden prompt, never from flags.
export METEOR_WIFI_PSK='wifi-password' METEOR_API_KEY='key_…'
meteor-imager flash --server fleet.example.com --image "Raspberry Pi OS Lite" \
  --device /dev/sdb --wifi-ssid MyNetwork --wifi-country DE --meteor-domain fleet.example.com

# Or use a FlashProfile file (see docs/contract.md) and a local image.
meteor-imager profile > card.json      # example to edit
meteor-imager flash --image raspios-lite.img.xz --device /dev/disk4 --profile card.json

# User, password (METEOR_USER_PASSWORD or a prompt) and SSH with keys only.
meteor-imager flash --image raspios-lite.img.xz --device /dev/sdb --user ops \
  --ssh --ssh-password-auth false --ssh-key-file ~/.ssh/id_ed25519.pub

# Configure an image file instead of a card (flash it later with any tool).
meteor-imager configure --image raspios-lite.img.xz --out configured.img --profile card.json

# Back up a card into a (shrunk, compressed) image file.
meteor-imager backup --device /dev/sdb --out pi-golden.img.gz
```

`flash` shows the card and asks you to type its name before it erases anything. `--yes` skips
that prompt, for scripts.

| Command | What it does |
|---|---|
| `gui` (default) | Desktop UI in your browser. `--no-browser` prints the address instead. |
| `images` | Lists a server's published OS images (`--json` for scripts). |
| `download` | Downloads a published image into the cache and prints its path. |
| `list-devices` | Lists writable cards. `--all` also shows disks that cannot be written. |
| `flash` | Writes, verifies, configures and ejects. |
| `configure` | Writes a configured copy of an image to a new `.img` file. |
| `backup` | Reads a card into `.img` or `.img.gz`, shrunk to its data when e2fsprogs is installed (`--no-shrink` to keep the size). |
| `profile` | Prints an example FlashProfile. |

Device paths: `/dev/sdX` or `/dev/mmcblkN` on Linux, `/dev/diskN` on macOS, and
`\\.\PhysicalDriveN` on Windows.

## What goes on the card

Only the FAT boot partition is changed. The root filesystem is written exactly as it is in the image.

**Meteor base images** (MeteorCloud first boot preinstalled; they carry `.meteor-settings`) get a
single `.meteorenv` with every setting: domain, key, device name, hostname, timezone, Wi-Fi, user
(password as a hash) and SSH. The image applies it before the network starts, deletes it from the
card and sends one token request. A FAT partition labelled `METEORCFG` is used instead of the boot
partition when the image has one (Jetson, NUC and other device types).

Stock Raspberry Pi OS images get the files below.

| File | When | Contents |
|---|---|---|
| `.meteorenv` | Enrollment set | `METEOR_DOMAIN` and `METEOR_API_KEY`. Deleted by `meteor-firstrun.sh` once the token request is sent |
| `meteor-firstrun.sh` | Enrollment set, cloud-init images | Sends the token request, then deletes itself and `.meteorenv` |
| `user-data` | cloud-init images | Merged into the existing file: runs `meteor-firstrun.sh`, hostname, timezone, user (password hash), SSH |
| `userconf.txt`, `ssh` | Images without cloud-init | User and password hash; SSH on |
| `network-config` | Wi-Fi set | netplan v2 Wi-Fi (same layout as Raspberry Pi Imager) |
| `meta-data` | cloud-init images | New `instance-id`, so cloud-init runs again even on a reused card |
| `cmdline.txt` | cloud-init images | `ds=nocloud;i=<instance-id>` appended |
| `wpa_supplicant.conf` | Images without cloud-init | Legacy Wi-Fi. The password is stored as a derived key, not in plain text. |

cloud-init images are Raspberry Pi OS from November 2024 onwards (Bookworm and later). With older
images, Meteor Imager sets up Wi-Fi and `.meteorenv` and warns that enrollment needs MeteorCloud
first boot preinstalled in the image. The full format is in [docs/contract.md](docs/contract.md).

## Security

- **Only this computer can use the UI.** It listens on `127.0.0.1` only. A random token in the
  link it opens becomes a session cookie, and other web pages are refused (Host and Origin checks,
  a strict CSP).
- **Secrets are not stored.** The Wi-Fi password and the enrollment key stay in memory and are
  written only to the card, or to a file you name. They are never logged, never sent to the
  server and never saved. "Remember settings" saves only the server address, domain, Wi-Fi name,
  country, hostname and timezone, in a file only you can read.
- **Anyone holding the card can read its Wi-Fi password and enrollment key** on the boot
  partition until the Pi's first boot sends the token request (the key file is deleted then; the
  Wi-Fi settings stay). Give enrollment keys an expiry date, revoke them when the batch is done,
  and keep cards safe. The key alone never joins a device: an administrator approves each request.
- **HTTPS by default.** Without a scheme, `https://` is used. Plain HTTP is accepted only for LAN
  and dev addresses (private IPs, `localhost`, `*.local`), or with `--insecure-http`. Redirects
  from HTTPS to HTTP are refused.
- **Downloads are checked.** Every image is checked against the SHA-256 the server publishes,
  before it is written and again when a cached copy is reused.
- **Disks are protected.** Internal disks and disks holding the running system are never offered.
  You must confirm the exact card before anything is erased.

## How it writes cards

1. The image is streamed (xz, gzip and zip are decompressed on the fly) to the card in 4 MiB
   chunks. The first megabyte, which holds the partition table, is held back.
2. Verify: the card is read back (page cache dropped) and its SHA-256 compared with what was written.
3. The boot partition is edited in place with a userspace FAT driver: no mounting and no root.
4. The partition table goes last, and the changed sectors are read back. Until this step the OS
   sees an empty card, so it never mounts or locks a half-written card.
5. The card is ejected.

Raw disk access works the way the OS expects:

- **Linux:** udisks2 with a polkit prompt, or root.
- **macOS:** `authopen` with an administrator password prompt.
- **Windows:** an elevated process. `diskpart clean` runs first so no volume is locked.

## Build from source

Go 1.24 or newer:

```sh
make test        # unit tests; synthetic disk images, fsck.vfat when installed
make build       # ./dist/meteor-imager for this system
make cross       # Linux, macOS and Windows binaries
```

Release builds use `-tags fat_noexfat`, which leaves out exFAT support. Pi boot partitions are FAT32.

## Docs

- [docs/contract.md](docs/contract.md): FlashProfile JSON, the boot partition files and the MeteorCloud API used
- [docs/meteorcloud.md](docs/meteorcloud.md): publishing images on the server
- [docs/manual-test-checklist.md](docs/manual-test-checklist.md): release checks on real hardware
- [docs/licensing.md](docs/licensing.md): third-party components

## License

Apache License 2.0. See [LICENSE](LICENSE).
