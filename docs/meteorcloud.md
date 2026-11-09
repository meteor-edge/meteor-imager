# Publishing OS images for Meteor Imager

Meteor Imager lists only the OS images your MeteorCloud server has published. Listing and
downloading them needs no login. Enrollment keys, Wi-Fi and the domain are entered in the imager
afterwards and never sent to the server.

Requires MeteorCloud with the public OS image API (`GET /api/v1/os-images`).

## Publish an image

1. In the MeteorCloud console, go to **Artifacts** (or a device type's page) and click **Upload**.
2. Choose type **OS image** and upload the `.img.xz` (or `.img`, `.gz`, `.zip`). Fill in name,
   version and device type; Meteor Imager shows all three.
3. Tick **Show in Meteor Imager**.

For an image that is already uploaded, tick the checkbox in the **Meteor Imager** column of the
artifacts table, and confirm. Untick it to stop offering the image. Cards already written are not affected.

With the API:

```sh
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"published": true}' https://fleet.example.com/api/v1/organizations/$ORG/artifacts/$ID
```

Only owners and admins can publish (`artifact.update`). Every change is in the audit log as
`artifact.update`.

## What anyone can see

Anyone who can reach the server can list published images and download them. The list includes
name, version, description, device type, file name, size and SHA-256, but no organization or user
IDs. Publish only images you are happy to share: a stock or hardened OS image, never one with
secrets baked in. Device-specific settings belong in the imager's profile, not in the image.

On a server with several organizations, published images from all of them appear in one list.

## Rate limiting

Both public endpoints share a per-IP limit (default 60 requests a minute):

```
PUBLIC_OS_IMAGE_RATE_LIMIT_REQUESTS=60
PUBLIC_OS_IMAGE_RATE_LIMIT_WINDOW_SECONDS=60
```

Behind a reverse proxy, make sure the control plane sees client IPs, or all clients share one limit.

## Which image to publish

Raspberry Pi OS Lite (64-bit) from November 2024 or later is the simplest: it has cloud-init, so
Meteor Imager sets up Wi-Fi and MeteorCloud first boot without any changes to the image. Older
images need MeteorCloud first boot preinstalled.
