# Running Overfin, all in one

This folder runs Overfin with everything it needs on one machine: Caddy for
HTTPS, Authentik for logins, and Authentik's database. Authentik comes set
up for Overfin by a blueprint, so there's nothing to click through there.

## Before you start

- A machine with Docker and Docker Compose, ports **80 and 443** open to the
  internet.
- Two names pointing to it: your site (`example.org`) and its sign-in
  (`auth.example.org`).
- A **Jellyfin 12** server this machine can reach, and **not** reachable
  from the internet. In Jellyfin's dashboard, make an API key for Overfin.

## Setting up

```sh
./setup.sh
docker compose up -d
```

`setup.sh` asks for the two names, your site's name, Jellyfin's address and
API key, and your email. It generates every secret into `.env` (readable by
you only: keep a copy somewhere safe) and writes `overfin.toml`. It also
copies the demo site into `./site`, unless you've put your own there.

Until Overfin's first release, build its image from this repository first:
`docker compose build overfin`.

After a minute or two:

1. **Sign in at `https://auth.example.org`** as `akadmin`, with the password
   `setup.sh` printed (it's also `AUTHENTIK_BOOTSTRAP_PASSWORD` in `.env`).
   Change it, and set up a second factor if you like.
2. **Create the people** who'll use the site, in Authentik's *Directory →
   Users*, or switch on Overfin's invitations.
3. **Open `https://example.org`** and log in.

## The control panel

Overfin's control panel only answers on the machine itself. From your
computer:

```sh
ssh -L 8081:localhost:8081 your-server
```

then open `http://localhost:8081` and sign in as `akadmin`. To let someone
else in, add them to the `overfin-admins` group in Authentik.

## What's where

| | |
| --- | --- |
| `compose.yaml` | The five containers and their networks |
| `Caddyfile` | The site (with Overfin's API and gate) and the sign-in |
| `authentik/overfin.yaml` | The blueprint: Overfin's login client, a login page with username and password together, the `overfin-admins` group, and the connector's user, allowed only to view users |
| `.env`, `overfin.toml` | Written by `setup.sh`; private to this install |
| `site/` | The website people use |

Each container receives only the secrets it needs. Authentik's database is
on a network only Authentik reaches. Nothing gets the Docker socket.

## Upgrading and backups

Versions are set in `.env` (`OVERFIN_IMAGE`, `AUTHENTIK_TAG`, `CADDY_TAG`).
Change one, then `docker compose up -d`. Read Authentik's release notes
before a major upgrade.

Back up `.env`, `overfin.toml`, `site/` and the Docker volumes
(`overfin-data`, `database`, `authentik-data`).

## If something's off

- `docker compose logs -f overfin` (or `authentik-server`, `caddy`).
- On the very first start, Authentik may show the Overfin blueprint as
  failed for a moment: it's applied before Authentik's own defaults exist,
  then again by itself a minute later.
- Caddy needs both names to point to this machine, with ports 80 and 443
  open, to get its certificates.
