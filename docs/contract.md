# Meteor Imager contract

This document is the interface between Meteor Imager, MeteorCloud and the Raspberry Pi's first
boot. It covers three things: the FlashProfile settings format, the files written to the card's
boot partition, and the MeteorCloud API the imager calls.

## 1. FlashProfile (JSON, `schema_version: 1`)

```json
{
  "schema_version": 1,
  "wifi": {
    "ssid": "MyNetwork",
    "psk": "wifi-password",
    "hidden": false,
    "country": "DE"
  },
  "enrollment": {
    "meteor_domain": "fleet.example.com",
    "meteor_api_key": "key_..."
  },
  "hostname": "pi-01",
  "timezone": "Europe/Berlin"
}
```

| Field | Required | Rules |
|---|---|---|
| `schema_version` | yes | `1`. A newer version is rejected with "update Meteor Imager". |
| `wifi` | no | Omit for wired-only devices. |
| `wifi.ssid` | with `wifi` | 1–32 bytes. |
| `wifi.psk` | with `wifi` | 8–63 characters (WPA2/WPA3 personal). No line breaks. |
| `wifi.hidden` | no | `true` for networks that do not broadcast their name. |
| `wifi.country` | with `wifi` | ISO 3166-1 alpha-2 in capitals. Wi-Fi stays off on the Pi without it. |
| `enrollment` | no | Omit to write the image without MeteorCloud enrollment. |
| `enrollment.meteor_domain` | with `enrollment` | `host` or `host:port`. No scheme or path, and not `localhost`. MeteorCloud first boot tries HTTP and HTTPS. |
| `enrollment.meteor_api_key` | with `enrollment` | A MeteorCloud enrollment key, starting with `key_`. |
| `hostname` | no | `null` or 1–63 characters: `a-z`, `0-9`, `-`. |
| `timezone` | no | `null` or an IANA name. |
| `ssh` | no | Reserved; must be absent or empty in v1. |
| `extras` | no | Reserved; must be absent or empty in v1. |

Unknown fields are rejected, so a typo never silently does nothing. At least one of `wifi`,
`enrollment`, `hostname` or `timezone` must be set.

**Secrets.** `wifi.psk` and `enrollment.meteor_api_key` are only held in memory, or in a profile
file you create yourself. Meteor Imager never writes them anywhere except the card, and never
logs them. Validation errors name the field, never its value.

**Saved preferences.** The desktop UI can remember non-secret fields only, in
`<user config dir>/meteor-imager/prefs.json` with mode 0600: server address, domain, SSID, hidden
flag, country, hostname and timezone.

## 2. Boot partition files

Meteor Imager finds the first FAT partition (MBR types `01 04 06 0b 0c 0e ef`, or the GPT basic
data / EFI system partition types). It edits files in that partition's root directory with a
userspace FAT driver. Nothing outside that partition is changed.

### `.meteorenv` (enrollment set)

```
METEOR_DOMAIN=fleet.example.com
METEOR_API_KEY=key_...
```

`KEY=value` lines, no quoting, LF line endings. `meteor-firstrun.sh` reads it (the file is
parsed, never sourced) and deletes it once the token request is sent. MeteorCloud's own first-boot
scripts in golden images read `/boot/firmware/.meteorenv` too (see MeteorCloud's
`src/device-plane/agent/packaging/firstboot/`).

### `meteor-firstrun.sh` (enrollment set, cloud-init images)

Embedded in Meteor Imager (`internal/bootfs/firstrun.sh`). cloud-init runs it once, as root:

1. Reads `.meteorenv` next to it. Stops (files kept) without `METEOR_DOMAIN` and a `key_…` key.
2. Installs `meteorcli` if it is missing, from
   `https://github.com/meteor-edge/meteor-cloud/releases/latest/download/install-meteorcli.sh --with-service`.
   Retries for up to 10 minutes while the network comes up.
3. `meteorcli config --domain … --api-key …`, then `meteorcli request-token --name <hostname> --wait 0`,
   retried for up to 10 minutes.
4. **Only once the request is sent** (a claim secret is in `/etc/meteorcli`): deletes
   `meteor-firstrun.sh` and `.meteorenv` from the boot partition, enables and restarts
   `meteorcli.service`.
5. `meteorcli run` waits for the administrator to approve the request, then runs the agent. The wait
   survives reboots, and an expired request is submitted again with the key `meteorcli` keeps in
   `/etc/meteorcli/api-key` (root only). meteorcli releases without this wait get a transient
   `meteorcli-claim.service` instead (not kept across a reboot).

If no request could be sent, both files stay and the log says why. Fix `.meteorenv` and run
`sudo bash /boot/firmware/meteor-firstrun.sh`. A run after the request was sent only removes the
files and starts the service; it never sends a second request. Log: `/var/log/meteor-firstrun.log`
and `/var/log/cloud-init-output.log`.

### `user-data` (cloud-init images)

The existing file is parsed as YAML and merged. Keys already present (for example users from an
organization's golden image) are kept. The first line is always `#cloud-config`.

```yaml
#cloud-config
manage_resolv_conf: false          # added if absent
hostname: "pi-01"                  # if hostname set
manage_etc_hosts: true             # if hostname set
timezone: "Europe/Berlin"          # if timezone set
packages: [curl, ca-certificates]  # if enrollment set; merged without duplicates
runcmd:                            # if enrollment set; added once
  - bash /boot/firmware/meteor-firstrun.sh
```

A runcmd entry from older Meteor Imager versions (downloading `install-meteorcli-firstboot.sh`)
is replaced. Running Meteor
Imager twice on the same card does not duplicate entries.

### `network-config` (Wi-Fi set, cloud-init images)

netplan v2, in the same layout Raspberry Pi Imager writes:

```yaml
network:
  version: 2
  ethernets:
    eth0: {dhcp4: true, dhcp6: true, optional: true}   # only if no ethernets are configured
  wifis:
    wlan0:
      dhcp4: true
      regulatory-domain: "DE"
      access-points:
        "MyNetwork":
          hidden: true          # only for hidden networks
          password: "wifi-password"
      optional: true
```

SSIDs and passwords are always double-quoted YAML strings, so values like `yes`, `0123` or
`a: b` stay strings.

### `meta-data` and `cmdline.txt` (cloud-init images)

`meta-data` is replaced with `instance-id: meteor-imager-<unix ms>`, and ` ds=nocloud;i=<same id>`
is set on the single line of `cmdline.txt`, replacing an earlier `ds=nocloud…`. A new instance id
makes cloud-init run the configuration again, even on a card that has booted before.

### `wpa_supplicant.conf` (images without `user-data`)

For Raspberry Pi OS before Bookworm 2024-11. The SSID is hex-encoded, and the PSK is the 256-bit
key derived with PBKDF2-SHA1 (4096 rounds, SSID as salt), not the passphrase:

```
ctrl_interface=DIR=/var/run/wpa_supplicant GROUP=netdev
update_config=1
country=DE

network={
	ssid=4d794e6574776f726b
	scan_ssid=1            # hidden networks only
	psk=<64 hex>
}
```

Hostname and timezone need cloud-init and are skipped on these images, with a warning. Enrollment
writes `.meteorenv`, with a warning that MeteorCloud first boot must already be installed in the image.

## 3. MeteorCloud API (no login)

Both endpoints are public, rate limited per IP (default 60 requests a minute;
`PUBLIC_OS_IMAGE_RATE_LIMIT_REQUESTS` / `_WINDOW_SECONDS`), and only serve artifacts of type
`os_image` that an owner or admin published.

### `GET /api/v1/os-images`

Newest first. `200`:

```json
[
  {
    "id": "6f1c…",
    "name": "Raspberry Pi OS Lite",
    "version": "2026.10",
    "description": "64-bit, Trixie",
    "device_type": "Raspberry Pi 4",
    "file_name": "raspios-lite.img.xz",
    "content_type": "application/x-xz",
    "size_bytes": 550466056,
    "checksum_sha256": "9a3e…",
    "created_at": "2026-10-07T09:00:00Z",
    "download_url": "/api/v1/os-images/6f1c…/download"
  }
]
```

### `GET /api/v1/os-images/{id}/download`

Streams the file with `Content-Length`, `Content-Disposition` and `X-Checksum-SHA256`. It
returns `404` for unknown or unpublished images and `429` when rate limited.

**Client rules (Meteor Imager):**

- `download_url` must be a path on the same server; other hosts are refused.
- The download must match `size_bytes` and `checksum_sha256` (and `X-Checksum-SHA256` when sent).
  Partial files are deleted.
- Downloads are cached as `<user cache dir>/meteor-imager/images/<sha256><ext>`, and the hash is
  checked again before a cached copy is used.
- Without a scheme the server address uses HTTPS. LAN and dev hosts (private, loopback and
  link-local IPs, `localhost`, `*.local`) use HTTP. Plain HTTP to other hosts needs
  `--insecure-http`. HTTPS to HTTP redirects are refused.

### Publishing (authenticated, console or API)

- Upload: `POST /api/v1/organizations/{org}/artifacts` with form field `published=true` (OS images only).
- Change later: `PATCH /api/v1/organizations/{org}/artifacts/{id}` with `{"published": true|false}`.
  Needs `artifact.update` and is audited.
