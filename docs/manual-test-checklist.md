# Manual test checklist

Run before each release on real hardware. The unit tests cover profiles, boot partition edits on
synthetic images, the write/verify pipeline on an in-memory card, the MeteorCloud client and the
UI's request checks. They cannot cover raw disk access, OS permission prompts, browsers or a Pi booting.

Record the version, OS version, card reader and card for each run.

## Setup

- [ ] A MeteorCloud server with at least one published OS image: Raspberry Pi OS Lite 64-bit,
      2024-11 or later.
- [ ] An enrollment key, a Wi-Fi network the Pi can join, and a Raspberry Pi with a spare SD card.
- [ ] One non-cloud-init image (Raspberry Pi OS Bullseye) for the legacy check.

## Every platform (Windows 11, macOS on Apple silicon and Intel, Ubuntu LTS, one other Linux)

### Install and start

- [ ] The installer, dmg, deb or AppImage installs and starts. The UI opens in the default browser.
- [ ] Windows: one UAC prompt at start. macOS: no prompt until a card is written. Linux: no prompt
      until a card is written.
- [ ] Opening the printed address without the token, in another browser, gives "Open Meteor Imager
      from the link".
- [ ] Closing the tab ends the app within about 2 minutes. **Quit** ends it at once.

### Image step

- [ ] An address without a scheme and with a public name uses HTTPS. A LAN IP uses HTTP.
- [ ] `http://<public name>` is refused unless "Allow plain HTTP" is ticked.
- [ ] The list shows only published images, with name, version, device type and size.
- [ ] Unpublishing an image in the console removes it from the list after **Load images**.
- [ ] A server with no published images shows the "no OS images yet" hint.
- [ ] Wrong address or server down gives a readable error.
- [ ] **Local file → Browse…** opens the native file dialog. The chosen path is used.

### Settings step

- [ ] Tabs are **OS settings** (first) and **MeteorCloud (optional)**.
- [ ] On a fresh start, user / Wi-Fi / SSH are on; MeteorCloud enroll is **off**.
- [ ] **Generate** fills the SSH password and Wi-Fi password (shown as text); **Show** still works.
- [ ] **Upload…** a `meteorenv.txt` fills OS + MeteorCloud boxes; a `METEOR_USER_PASSWORD_HASH` is kept for writing until you Generate/type a new password.
- [ ] The domain is pre-filled from the server address when enroll is on, and can be edited.
- [ ] With enroll off, you can continue with only OS settings (SSID + passwords filled).
- [ ] With enroll on, a missing key, a short Wi-Fi password or a missing country is shown inline.
- [ ] **Show** reveals the password and key; the browser does not offer to save them.
- [ ] A problem on the other tab opens that tab.
- [ ] Username with capitals or `root`, an empty user password, or "public key only" without a key
      is shown inline.
- [ ] On a fresh start (no saved settings) **Enable SSH** is ticked with password authentication.
- [ ] With **Remember** ticked, the next start pre-fills domain, SSID, country, hostname, timezone,
      username and SSH choices (including SSH unticked), but **not** the passwords or key.
      **Forget saved settings** clears them.
- [ ] Check the prefs file (`~/.config/meteor-imager/prefs.json`, `~/Library/Application Support/…`,
      `%AppData%\meteor-imager\…`): no secrets, mode 0600 on Unix.

### Card step

- [ ] Only the SD card or USB reader is listed. The system disk is never listed, even with
      **Show non-removable disks**.
- [ ] Inserting or removing a card and pressing **Refresh** updates the list.
- [ ] A card with the lock switch on shows as read-only and cannot be chosen.

### Write

- [ ] The summary shows the image, domain and SSID, with password and key masked.
- [ ] **Write** stays disabled until "I understand" is ticked.
- [ ] Linux desktop: polkit password prompt. macOS: administrator password prompt. Cancelling the
      prompt gives a clear error, and the card is untouched.
- [ ] Progress moves through Download (published image) → Prepare → Write → Verify → Settings →
      Finish → Eject.
- [ ] A second run with the same image skips the download (cache).
- [ ] **Cancel** during Write stops quickly, and the result says the card must be written again.
- [ ] Unplugging the card during Write gives an error and does not crash.
- [ ] A card smaller than the image is refused ("larger than the card").
- [ ] After **Done**, the card is ejected (Windows: safe to remove).
- [ ] With a user and SSH password login, **Done** shows the login command (`ssh <user>@<hostname>.local`).
- [ ] **Write another card** goes back to the card step with settings kept.

### Card contents (mount the boot partition on a PC)

- [ ] `.meteorenv`, `user-data`, `network-config`, `meta-data` and `cmdline.txt` are as in
      [contract.md](contract.md), and the dates are the current date.
- [ ] Linux: `sudo fsck.vfat -n /dev/<card>1` reports no errors.
- [ ] The OS mounts the boot partition without warnings (Windows: no "scan and fix" prompt).

### First boot on a Pi

- [ ] The Pi joins Wi-Fi and shows up in MeteorCloud as a pending enrollment within a few minutes.
- [ ] Before approving: `/boot/firmware/meteor-firstrun.sh` and `/boot/firmware/.meteorenv` are gone,
      and `journalctl -u meteorcli` says the request is waiting for approval.
- [ ] Reboot before approving: `meteorcli.service` waits again (same request ID, no new request).
- [ ] Approve: within ~10 s the device is online and `meteorcli status` shows the device ID.
- [ ] The hostname and timezone are as set.
- [ ] Rewriting the same card with different Wi-Fi works (new instance-id).
- [ ] Hidden SSID: the Pi joins.
- [ ] Wired only (Wi-Fi unticked): the Pi enrolls over Ethernet.
- [ ] User and password set: you can log in on the console and over SSH (password auth) as that
      user; the image's default user no longer exists. With "public key only", password SSH is
      refused and the key works.
- [ ] The card's boot partition never contains the user password in plain text.

### Golden image (prepare, back up, write)

- [ ] Plain Raspberry Pi OS Lite → install Docker and `meteorcli` → `sudo meteorcli-prepare-image.sh`
      → the Pi powers off.
- [ ] **Back up a card** with e2fsprogs: the file is much smaller than the card and the report says
      "shrunk"; without e2fsprogs the checkbox is disabled with a hint.
- [ ] Upload the backup as an OS image, publish it, and write two cards from it with different
      hostnames, users and Wi-Fi.
- [ ] Both boot, grow the root filesystem to the card (`df -h /`), have different SSH host keys and
      `/etc/machine-id`, Docker works, and both appear in MeteorCloud as pending enrollments.
- [ ] **Cancel** during a backup leaves no file.
- [ ] **Write this backup to a card** opens Settings with the backup as the local file; writing it
      with a user, Wi-Fi and enrollment key gives a card that boots and enrolls.
- [ ] **Start over** after a write or a backup goes back to the image step with no image, card,
      passwords or key; saved preferences are filled in again.

### Legacy image (no cloud-init)

- [ ] The result shows the "no cloud-init" warnings. `wpa_supplicant.conf` has a hex SSID and a
      64-hex `psk`, not the passphrase.
- [ ] The Pi joins Wi-Fi.
- [ ] With a user and SSH on: `userconf.txt` and `ssh` are on the card, and you can SSH in as the user.

## CLI (one Linux and one Windows or macOS machine)

- [ ] `meteor-imager images --server …` and `--json`.
- [ ] `meteor-imager list-devices`, plus `--all` showing the system disk marked not writable.
- [ ] `flash` asks you to type the device name; a wrong name cancels. `--yes` skips the prompt.
- [ ] `flash --device <system disk> --yes` is refused.
- [ ] Without `METEOR_WIFI_PSK` / `METEOR_API_KEY`, `flash` prompts for them with no echo. In a
      pipe without them it fails with a hint.
- [ ] `configure --out x.img` writes an image that boots like a flashed card. An existing `--out` is refused.
- [ ] `ps` / Task Manager during a run shows no password or key in the command line.
- [ ] Linux without a desktop: `sudo meteor-imager flash …` works.
