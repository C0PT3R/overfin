#!/bin/sh
# Prepares this folder to run Overfin with Caddy and Authentik: asks a few
# questions, generates every secret, and writes .env (secrets, readable by
# you only) and overfin.toml. Run it once; then: docker compose up -d
set -eu
cd "$(dirname "$0")"

if [ -e .env ]; then
	echo ".env already exists: it holds this install's secrets, so it's never overwritten." >&2
	echo "To start over, remove .env, overfin.toml and the Docker volumes (docker compose down -v)." >&2
	exit 1
fi

# ask PROMPT PATTERN [DEFAULT]: reads an answer until it matches PATTERN.
ask() {
	while :; do
		if [ -n "${3:-}" ]; then printf '%s [%s]: ' "$1" "$3" >&2; else printf '%s: ' "$1" >&2; fi
		read -r answer || exit 1
		[ -z "$answer" ] && answer="${3:-}"
		if printf '%s' "$answer" | grep -Eq "^($2)\$"; then
			printf '%s' "$answer"
			return
		fi
		echo "  That doesn't look right; try again." >&2
	done
}

command -v openssl >/dev/null || { echo "setup.sh needs openssl." >&2; exit 1; }
secret() { openssl rand -hex "$1"; }

# Caddy's fixed address on the network it shares with Overfin and
# Authentik; Overfin trusts it to say who's connecting. Other containers get
# addresses from edge_range, which leaves Caddy's alone.
edge_subnet=172.29.250.0/28
edge_range=172.29.250.8/29
caddy_ip=172.29.250.2

domain='[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+'
echo "Overfin setup. Both names below must point to this machine (DNS), with ports 80 and 443 open." >&2
site=$(ask "Your site's domain (e.g. example.org)" "$domain")
auth=$(ask "The sign-in's domain" "$domain" "auth.$site")
name=$(ask "Your site's name, as people will see it" "[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}" "Movies")
# Overfin runs in a container: "localhost" there is Overfin itself, never
# the machine, so Jellyfin is reached by an address on the network.
while :; do
	jellyfin=$(ask "Jellyfin's address on your network (e.g. http://192.168.1.10:8096), not localhost" "https?://[A-Za-z0-9.:-]+(/[A-Za-z0-9._/-]*)?")
	case "$jellyfin" in
	*://localhost* | *://127.* | *://0.0.0.0*)
		echo "  Inside Overfin's container, that's Overfin itself. Use this machine's address on your network instead." >&2 ;;
	*) break ;;
	esac
done
printf 'A Jellyfin API key (Dashboard > API Keys), not shown as you type: ' >&2
stty -echo 2>/dev/null || true
read -r jfkey
stty echo 2>/dev/null || true
echo >&2
printf '%s' "$jfkey" | grep -Eq '^[A-Za-z0-9]{16,128}$' || { echo "That doesn't look like a Jellyfin API key." >&2; exit 1; }
email=$(ask "Your email, for the sign-in's admin account" "[A-Za-z0-9._%+-]+@$domain")

admin_password=$(secret 12)
umask 077
cat > .env <<EOF
# Written by setup.sh. Every secret of this install: keep it private, and
# keep a copy somewhere safe (losing OVERFIN_SECRET_KEY resets people's PINs).
OVERFIN_DOMAIN=$site
AUTH_DOMAIN=$auth
OVERFIN_SITE_NAME="$name"

OVERFIN_IMAGE=ghcr.io/c0pt3r/overfin:0.1.0
AUTHENTIK_TAG=2026.8.4
CADDY_TAG=2

# Caddy's fixed address on the internal network, which Overfin trusts.
EDGE_SUBNET=$edge_subnet
EDGE_RANGE=$edge_range
CADDY_IP=$caddy_ip

OVERFIN_SECRET_KEY=$(openssl rand -base64 32)
OVERFIN_JELLYFIN_ADMIN_KEY=$jfkey
OVERFIN_OIDC_CLIENT_SECRET=$(secret 32)
OVERFIN_CONNECTOR_TOKEN=$(secret 32)

AUTHENTIK_SECRET_KEY=$(secret 40)
PG_PASS=$(secret 24)
# Only used on the first start, to create the sign-in's admin (akadmin).
AUTHENTIK_BOOTSTRAP_PASSWORD=$admin_password
AUTHENTIK_BOOTSTRAP_EMAIL=$email
EOF

umask 022
cat > overfin.toml <<EOF
# Written by setup.sh. What Overfin needs to start; features are switched in
# its control panel. Every setting: overfin.example.toml in the repository.

[jellyfin]
url = "$jellyfin"

[identity]
mode = "oidc"
trusted_proxies = ["$caddy_ip"]

[oidc]
issuer = "https://$auth/application/o/overfin/"
client_id = "overfin"
redirect_url = "https://$site/api/v1/auth/callback"

[admin]
listen = ":8081"
group = "overfin-admins"
redirect_url = "http://localhost:8081/admin/auth/callback"

[connector]
kind = "authentik"
url = "https://$auth"

[plugins]
enable = ["library", "playback"]
EOF

cat >&2 <<EOF

Done. Next:
  docker compose up -d
Then, after a minute or two:
  - https://$auth : sign in as akadmin, password $admin_password
    (change it), and create the people who'll use the site;
  - https://$site : the site;
  - the control panel: ssh -L 8081:localhost:8081 <this machine>, then
    http://localhost:8081, signed in as akadmin.
EOF
