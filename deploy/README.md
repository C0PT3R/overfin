# Overfin and its Jellyfin, on one machine

This folder runs Overfin with a Jellyfin of its own: Overfin sets that
Jellyfin up on its first start, keeps the settings it needs there, and is
the only one that talks to it. Nobody signs in to Jellyfin itself.

## Before you start

- A machine with Docker and Docker Compose, with your films and series on
  it (or mounted on it).
- For the internet: a domain pointing to the machine, and ports **80 and
  443** open to it. Without one, Overfin serves plain http, for your LAN.

## Starting it

```sh
mkdir overfin && cd overfin
curl -fsSLO https://raw.githubusercontent.com/overfin/server/v0.4.0/deploy/compose.yaml
curl -fsSL -o .env https://raw.githubusercontent.com/overfin/server/v0.4.0/deploy/.env.example
```

In `.env`, set `MEDIA_DIR` to your media folder, and `OVERFIN_DOMAIN` to
your domain if you have one. Then:

```sh
docker compose up -d
```

On Fedora, RHEL and other SELinux systems, Jellyfin can't read the media
folder until it's labeled for containers: add `,z` after `:ro` on the media
line of `compose.yaml`.

## Setting it up

Open the machine's address in a browser. The setup wizard asks first for a
one-time code from Overfin's log, so nobody else can set your server up
before you do. This prints it:

```sh
docker logs overfin | grep -o '"code":"[^"]*' | tail -1 | cut -d'"' -f4
```

The code changes each time Overfin restarts. Then the wizard asks, one step
at a time: the default language, a TMDB token (it says how to get one;
skippable), Intro Skipper (skippable), Seerr (skippable), and your email
and a password, as the first admin.

Then, in the control panel (`/admin/`):

- **Libraries**: add yours, with folders under `/media`, where the compose
  file puts `MEDIA_DIR`.
- **Server, Email**: an SMTP server, so people can register with an
  invitation and choose a new password by email. Without one, you add
  people with a password, and set forgotten ones.
- **Server, Transcoding**: your GPU, once Jellyfin has it (below).
- **People**: add them, or send invitations.

## A GPU for converting videos

Uncomment the lines for yours under `jellyfin` in `compose.yaml` (Intel or
AMD: `/dev/dri`; NVIDIA: the `deploy` block, with the NVIDIA Container
Toolkit installed), run `docker compose up -d`, then choose it in the
control panel and test it on your own videos.

## Looking inside Jellyfin

Jellyfin publishes no port: only Overfin reaches it, on this project's own
network. To look inside, open access in the control panel (Server,
Jellyfin), which gives Overfin's administrator account there a new
password, shown once. Then uncomment Jellyfin's `ports:` line and run
`docker compose up -d`: it publishes Jellyfin on this machine only (reach
it with `ssh -L 8096:localhost:8096 <the machine>`), or on your LAN if you
put the machine's LAN address instead. Changes made there may break
Overfin, or be undone by it. Close access again from the panel when you're
done.

## Updating

Set the new release's images in `compose.yaml` (or take the release's
`compose.yaml`), then `docker compose pull && docker compose up -d`.
Overfin migrates its database by itself. Jellyfin's version comes with
Overfin's releases: keep the one the compose file names.

## Backups

`overfin-secrets` holds Overfin's secret key, `overfin-data` everything
else; `jellyfin-config` is Jellyfin's. Back the secret key up apart from
the rest. Stop the stack first (`docker compose stop`), so the databases
are copied whole, then start it again.
