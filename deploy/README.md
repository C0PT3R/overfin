# Running Overfin, all in one

This folder runs Overfin with everything it needs on one machine: Caddy for
HTTPS and Pocket ID for logins. People sign in with a passkey (their phone,
their computer, a security key): no passwords. Overfin sets Pocket ID up
by itself, so there's nothing to click through there.

## Before you start

- A machine with Docker and Docker Compose, ports **80 and 443** open to the
  internet.
- Two names pointing to it: your site (`example.org`) and its sign-in
  (`auth.example.org`).
- A **Jellyfin 12** server this machine can reach, and **not** reachable
  from the internet. In Jellyfin's dashboard, make an API key for Overfin.
- Optional: an SMTP server for emails. With one, people who lost their
  passkey can sign in with a code sent by email.

## Setting up

Get this folder from the release you want, then run the setup:

```sh
git clone --depth 1 --branch v0.2.0 https://github.com/overfin/server overfin
cd overfin/deploy
./setup.sh
docker compose up -d
```

`setup.sh` asks for the two names, your site's name, your email and, if
you want emails, the SMTP server. Before writing anything, it shows a
summary, with where each name points, to confirm. It generates every
secret into `.env` (readable by you only: keep a copy somewhere safe) and
writes `overfin.toml`. Made a mistake? Run `./setup.sh` again: it asks the
same questions with your answers as defaults and keeps every secret; then
`docker compose up -d`. The site people use is overfin-web, Overfin's own
web client, served by Overfin itself; to serve your own client instead,
see `[site]` in Overfin's README.

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
2. **Sign in for the first time.** Overfin made your account in Pocket ID,
   with the email you gave. Get a sign-in link (it works once, for an hour):

   ```sh
   docker compose exec pocket-id /app/pocket-id one-time-access-token you@example.org
   ```

   Open it, and add a passkey when Pocket ID asks. You're Pocket ID's admin,
   and Overfin's.
3. **Invite people**, from the control panel's Invites tab. An invitation
   link leads to Pocket ID, where the person makes their account and their
   passkey; then they open the site (it's also on Pocket ID's page, under
   "My apps"). Sign-ups are only by invitation.
4. **Open `https://example.org`** and log in.

## The control panel

Overfin's control panel only answers on the machine itself. From your
computer:

```sh
ssh -L 8081:localhost:8081 your-server
```

then open `http://localhost:8081` and sign in. To let someone else in, add
them to the `overfin-admins` group in Pocket ID (its admin pages, at
`https://auth.example.org`).

## What's where

| | |
| --- | --- |
| `compose.yaml` | The three containers and their network |
| `Caddyfile` | HTTPS for the site (served by Overfin) and the sign-in |
| `.env`, `overfin.toml` | Written by `setup.sh`; private to this install |

At each start, Overfin makes sure Pocket ID has its login client (with its
secret and return addresses) and the `overfin-admins` group; it makes your
account only while Pocket ID has nobody. Pocket ID's settings all come from
`compose.yaml` and `.env`, not from its admin pages: sign-ups only by
invitation, emails when an SMTP server is set, and no usage reports sent to
Pocket ID's makers.

Each container receives only the secrets it needs. Nothing gets the Docker
socket.

## Upgrading and backups

Versions are set in `.env` (`OVERFIN_IMAGE`, `POCKET_ID_TAG`, `CADDY_TAG`).
Change one, then `docker compose up -d`. Read Pocket ID's release notes
before a major upgrade.

Back up `.env`, `overfin.toml` and the Docker volumes (`overfin-data`,
`overfin-secrets`, `pocket-id-data`). `overfin-secrets` holds the secret
key Overfin generated on its first start: without it, what Overfin stores
can't be read. Keep its copy apart from the others. Pocket ID's data is
encrypted with `POCKET_ID_ENCRYPTION_KEY`, in `.env`: keep it too.

## From Authentik

Earlier versions of this folder ran Authentik. `setup.sh` won't touch such
a folder: moving people over means matching each Authentik account to a
Pocket ID one, by email, before Overfin's accounts can follow. Keep the
Authentik version running until that's done.

## If something's off

- `docker compose exec overfin /overfin -check` tests the setup (the
  secret key, Jellyfin and its key, the sign-in) and says in plain words
  what's wrong, changing nothing. If Overfin isn't running,
  `docker compose run --rm --no-deps overfin -check` does the same.
- `docker compose logs -f overfin` (or `pocket-id`, `caddy`).
- The sign-in link expired, or was used? Run the command again.
- Caddy needs both names to point to this machine, with ports 80 and 443
  open, to get its certificates.
