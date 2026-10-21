#!/bin/sh
# Prepares this folder to run Overfin with Caddy and Authentik: asks a few
# questions, generates every secret, and writes .env (secrets, readable by
# you only) and overfin.toml. Then: docker compose up -d
#
# Run it again to fix an answer: it asks the same questions with the
# current answers as defaults, and keeps every secret.
set -eu
cd "$(dirname "$0")"

command -v openssl >/dev/null || { echo "setup.sh needs openssl." >&2; exit 1; }
secret() { openssl rand -hex "$1"; }

# current NAME: a value from the existing .env, without quotes; empty
# without one. .env is only read, never run.
current() {
	[ -e .env ] || return 0
	sed -n "s/^$1=//p" .env | head -n 1 | sed 's/^"\(.*\)"$/\1/'
}

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

# address NAME: the address a name points to, or nothing; "?" when this
# system can't tell.
address() {
	if command -v getent >/dev/null; then
		getent ahostsv4 "$1" 2>/dev/null | awk 'NR == 1 { print $1 }'
	else
		echo "?"
	fi
}

again=false
[ -e .env ] && again=true
if $again; then
	echo "This folder is already set up: Enter keeps each current answer, and every secret is kept." >&2
fi

domain='[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+'
# The answers start as the current ones, if any; after a "no" at the
# summary, as the ones just given.
site=$(current OVERFIN_DOMAIN)
auth=$(current AUTH_DOMAIN)
name=$(current OVERFIN_SITE_NAME)
jellyfin=""
[ -e overfin.toml ] && jellyfin=$(sed -n 's/^url = "\(http[^"]*\)"$/\1/p' overfin.toml | head -n 1)
old_email=$(current AUTHENTIK_BOOTSTRAP_EMAIL)
email=$old_email
jfkey=$(current OVERFIN_JELLYFIN_ADMIN_KEY)

while :; do
	echo "Both names below must point to this machine (DNS), with ports 80 and 443 open." >&2
	previous_site=$site
	site=$(ask "Your site's domain (e.g. example.org)" "$domain" "$site")
	# The suggested sign-in name follows the site's, unless it was chosen.
	if [ -z "$auth" ] || [ "$auth" = "auth.$previous_site" ]; then
		auth="auth.$site"
	fi
	auth=$(ask "The sign-in's domain" "$domain" "$auth")
	name=$(ask "Your site's name, as people will see it" "[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}" "${name:-Movies}")
	# Overfin runs in a container: "localhost" there is Overfin itself, never
	# the machine, so Jellyfin is reached by an address on the network.
	while :; do
		jellyfin=$(ask "Jellyfin's address on your network (e.g. http://192.168.1.10:8096), not localhost" \
			"https?://[A-Za-z0-9.:-]+(/[A-Za-z0-9._/-]*)?" "$jellyfin")
		case "$jellyfin" in
		*://localhost* | *://127.* | *://0.0.0.0*)
			echo "  Inside Overfin's container, that's Overfin itself. Use this machine's address on your network instead." >&2 ;;
		*) break ;;
		esac
	done
	kept_key=$jfkey
	while :; do
		if [ -n "$kept_key" ]; then
			printf 'A Jellyfin API key (Dashboard > API Keys), not shown as you type [Enter keeps the current one]: ' >&2
		else
			printf 'A Jellyfin API key (Dashboard > API Keys), not shown as you type: ' >&2
		fi
		stty -echo 2>/dev/null || true
		read -r jfkey || exit 1
		stty echo 2>/dev/null || true
		echo >&2
		[ -z "$jfkey" ] && jfkey=$kept_key
		printf '%s' "$jfkey" | grep -Eq '^[A-Za-z0-9]{16,128}$' && break
		echo "  That doesn't look like a Jellyfin API key; try again." >&2
	done
	email=$(ask "Your email, for the sign-in's admin account" "[A-Za-z0-9._%+-]+@$domain" "$email")

	# Where the names point, so a typo or a missing DNS record shows now,
	# before Caddy asks for certificates.
	site_ip=$(address "$site")
	auth_ip=$(address "$auth")
	cat >&2 <<EOF

  Site:           https://$site (points to ${site_ip:-nothing})
  Sign-in:        https://$auth (points to ${auth_ip:-nothing})
  Site's name:    $name
  Jellyfin:       $jellyfin
  Admin's email:  $email
EOF
	if [ -z "$site_ip" ] || [ -z "$auth_ip" ]; then
		echo "  A name doesn't point anywhere yet: check its spelling and its DNS record." >&2
	elif [ "$site_ip" != "?" ] && [ "$site_ip" != "$auth_ip" ]; then
		echo "  The two names point to different addresses: both should point to this machine." >&2
	fi
	printf 'Is this right? [Y/n]: ' >&2
	read -r ok || exit 1
	case "$ok" in
	"" | [Yy]*) break ;;
	esac
	echo >&2
done

# Edge network: Caddy's fixed address on the network it shares with
# Overfin and Authentik; Overfin trusts it to say who's connecting. Other
# containers get addresses from the range, which leaves Caddy's alone.
edge_subnet=$(current EDGE_SUBNET)
edge_range=$(current EDGE_RANGE)
caddy_ip=$(current CADDY_IP)

# keep NAME DEFAULT: the current value of a setting or a secret, or DEFAULT
# for a new install.
keep() {
	v=$(current "$1")
	printf '%s' "${v:-$2}"
}

admin_password=$(keep AUTHENTIK_BOOTSTRAP_PASSWORD "$(secret 12)")
umask 077
cat > .env.new <<EOF
# Written by setup.sh. Every secret of this install: keep it private, and
# keep a copy somewhere safe. Overfin's own secret key is generated on its
# first start, in the overfin-secrets volume: back that up too.
OVERFIN_DOMAIN=$site
AUTH_DOMAIN=$auth
OVERFIN_SITE_NAME="$name"

OVERFIN_IMAGE=$(keep OVERFIN_IMAGE ghcr.io/c0pt3r/overfin:0.1.0)
AUTHENTIK_TAG=$(keep AUTHENTIK_TAG 2026.8.4)
CADDY_TAG=$(keep CADDY_TAG 2)

# Caddy's fixed address on the internal network, which Overfin trusts.
EDGE_SUBNET=${edge_subnet:-172.29.250.0/28}
EDGE_RANGE=${edge_range:-172.29.250.8/29}
CADDY_IP=${caddy_ip:-172.29.250.2}

OVERFIN_JELLYFIN_ADMIN_KEY=$jfkey
OVERFIN_OIDC_CLIENT_SECRET=$(keep OVERFIN_OIDC_CLIENT_SECRET "$(secret 32)")
OVERFIN_CONNECTOR_TOKEN=$(keep OVERFIN_CONNECTOR_TOKEN "$(secret 32)")

AUTHENTIK_SECRET_KEY=$(keep AUTHENTIK_SECRET_KEY "$(secret 40)")
PG_PASS=$(keep PG_PASS "$(secret 24)")
# Only used on the first start, to create the sign-in's admin (akadmin).
AUTHENTIK_BOOTSTRAP_PASSWORD=$admin_password
AUTHENTIK_BOOTSTRAP_EMAIL=$email
EOF
# An older install's own secret key, set in .env, stays.
if [ -n "$(current OVERFIN_SECRET_KEY)" ]; then
	printf 'OVERFIN_SECRET_KEY=%s\n' "$(current OVERFIN_SECRET_KEY)" >> .env.new
fi
caddy_ip=$(sed -n 's/^CADDY_IP=//p' .env.new)
mv .env.new .env

umask 022
[ -e overfin.toml ] && cp overfin.toml overfin.toml.bak
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

if $again; then
	cat >&2 <<EOF

Updated (the previous overfin.toml is in overfin.toml.bak). To apply:
  docker compose up -d
EOF
	if [ "$email" != "$old_email" ]; then
		echo "The admin's email only counts on the sign-in's first start: change it in Authentik itself (akadmin's profile)." >&2
	fi
	exit 0
fi

cat >&2 <<EOF

Done. Next:
  docker compose up -d
Then, after a minute or two:
  - https://$auth : sign in as akadmin, password $admin_password
    (change it), and create the people who'll use the site;
  - https://$site : the site;
  - the control panel: ssh -L 8081:localhost:8081 <this machine>, then
    http://localhost:8081, signed in as akadmin.
If something's off: docker compose run --rm --no-deps overfin -check
EOF
