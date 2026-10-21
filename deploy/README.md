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

Get this folder from the release you want, then run the setup:

```sh
git clone --depth 1 --branch v0.1.0 https://github.com/C0PT3R/overfin-gateway
cd overfin-gateway/deploy
./setup.sh
docker compose up -d
```

`setup.sh` asks for the two names, your site's name and your email. Before
writing anything, it shows a summary, with where each name points, to
confirm. It generates every secret into `.env` (readable by you only: keep
a copy somewhere safe) and writes `overfin.toml`. Made a mistake? Run
`./setup.sh` again: it asks the same questions with your answers as
defaults and keeps every secret; then `docker compose up -d`. The site
people use is overfin-web, Overfin's own web client, served by Overfin
itself; to serve your own client instead, see `[site]` in Overfin's
README.

The image is Overfin's release set in `.env` (`OVERFIN_IMAGE`); from a
checkout of the repository, `docker compose build overfin` builds it
instead.

After a minute or two:

1. **Connect Jellyfin**, in Overfin's setup page, on the control panel's
   address (see below: `ssh -L 8081:localhost:8081 your-server`, then
   `http://localhost:8081`). It asks for the setup code, printed in
   `docker compose logs overfin`, Jellyfin's address and an API key made in
   its dashboard. Jellyfin's address is the one Overfin's container reaches
   it by: its address on your network, never `localhost` (inside the
   container, that's Overfin itself). Everything is tested before it's
   saved, and Overfin starts right after. Until then, the site says it's
   being set up. Later, the control panel's Settings change Jellyfin's
   address or key.
2. **Sign in at `https://auth.example.org`** as `akadmin`, with the password
   `setup.sh` printed (it's also `AUTHENTIK_BOOTSTRAP_PASSWORD` in `.env`).
   Change it, and set up a second factor if you like.
3. **Create the people** who'll use the site, in Authentik's *Directory →
   Users*, or switch on Overfin's invitations.
4. **Open `https://example.org`** and log in.

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
| `Caddyfile` | HTTPS for the site (served by Overfin) and the sign-in |
| `authentik/overfin.yaml` | The blueprint: Overfin's login client, a login page with username and password together, the `overfin-admins` group, and the connector's user, allowed only to view users |
| `.env`, `overfin.toml` | Written by `setup.sh`; private to this install |

Each container receives only the secrets it needs. Authentik's database is
on a network only Authentik reaches. Nothing gets the Docker socket.

## Upgrading and backups

Versions are set in `.env` (`OVERFIN_IMAGE`, `AUTHENTIK_TAG`, `CADDY_TAG`).
Change one, then `docker compose up -d`. Read Authentik's release notes
before a major upgrade.

Back up `.env`, `overfin.toml` and the Docker volumes (`overfin-data`,
`overfin-secrets`, `database`, `authentik-data`). `overfin-secrets` holds
the secret key Overfin generated on its first start: without it, what
Overfin stores can't be read. Keep its copy apart from the others.

## If something's off

- `docker compose exec overfin /overfin -check` tests the setup (the
  secret key, Jellyfin and its key, the sign-in) and says in plain words
  what's wrong, changing nothing. If Overfin isn't running,
  `docker compose run --rm --no-deps overfin -check` does the same.
- `docker compose logs -f overfin` (or `authentik-server`, `caddy`).
- On the very first start, Authentik may show the Overfin blueprint as
  failed for a moment: it's applied before Authentik's own defaults exist,
  then again by itself a minute later.
- Caddy needs both names to point to this machine, with ports 80 and 443
  open, to get its certificates.
