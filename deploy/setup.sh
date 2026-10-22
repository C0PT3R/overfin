#!/bin/sh
# Prepares this folder to run Overfin with Caddy and Pocket ID: asks a few
# questions, generates every secret, and writes .env (secrets, readable by
# you only) and overfin.toml. Then: docker compose up -d, and Overfin's
# setup page connects Jellyfin.
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
	sed -n "s/^$1=//p" .env | head -n 1 | sed -e 's/^"\(.*\)"$/\1/' -e "s/^'\\(.*\\)'\$/\\1/"
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
if [ -n "$(current AUTHENTIK_SECRET_KEY)" ]; then
	echo "This folder runs Authentik, which this version replaces with Pocket ID: see \"From Authentik\" in README.md." >&2
	exit 1
fi
if $again; then
	echo "This folder is already set up: Enter keeps each current answer, and every secret is kept." >&2
fi

domain='[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+'
# The answers start as the current ones, if any; after a "no" at the
# summary, as the ones just given.
site=$(current OVERFIN_DOMAIN)
auth=$(current AUTH_DOMAIN)
name=$(current OVERFIN_SITE_NAME)
email=""
[ -e overfin.toml ] && email=$(sed -n 's/^first_admin = "\(.*\)"$/\1/p' overfin.toml | head -n 1)
old_email=$email
smtp_host=$(current SMTP_HOST)
smtp_port=$(current SMTP_PORT)
smtp_from=$(current SMTP_FROM)
smtp_user=$(current SMTP_USER)
# Jellyfin is connected in Overfin's setup page. Installs made before that
# have it here, and keep it.
jellyfin=""
[ -e overfin.toml ] && jellyfin=$(sed -n '/^\[jellyfin\]/,/^\[/s/^url = "\(http[^"]*\)"$/\1/p' overfin.toml | head -n 1)

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
	# Pocket ID also takes it as the admin's username: no "+".
	email=$(ask "Your email, for your admin account" "[A-Za-z0-9]([A-Za-z0-9_.-]*[A-Za-z0-9])?@$domain" "$email")
	echo "Emails let people who lost their passkey sign in with a code. Without an SMTP server, there are none." >&2
	smtp_host=$(ask "SMTP server (e.g. smtp.example.org; - for none)" "-|$domain" "${smtp_host:--}")
	if [ "$smtp_host" != "-" ]; then
		smtp_port=$(ask "SMTP port (587: STARTTLS, 465: TLS)" "[0-9]{1,5}" "${smtp_port:-587}")
		smtp_from=$(ask "Emails come from" "[A-Za-z0-9._%+-]+@$domain" "${smtp_from:-noreply@$site}")
		smtp_user=$(ask "SMTP username" "[^ ]+" "${smtp_user:-$smtp_from}")
	fi

	# Where the names point, so a typo or a missing DNS record shows now,
	# before Caddy asks for certificates.
	site_ip=$(address "$site")
	auth_ip=$(address "$auth")
	cat >&2 <<EOF

  Site:           https://$site (points to ${site_ip:-nothing})
  Sign-in:        https://$auth (points to ${auth_ip:-nothing})
  Site's name:    $name
  Admin's email:  $email
  Emails:         $( [ "$smtp_host" = "-" ] && echo none || echo "from $smtp_from, by $smtp_host:$smtp_port")
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

# The SMTP password, typed unseen; Enter keeps the current one.
smtp_password=$(current SMTP_PASSWORD)
if [ "$smtp_host" != "-" ]; then
	if [ -n "$smtp_password" ]; then printf 'SMTP password [Enter keeps it]: ' >&2; else printf 'SMTP password: ' >&2; fi
	while :; do
		stty -echo 2>/dev/null || true
		typed=""
		read -r typed || true
		stty echo 2>/dev/null || true
		echo >&2
		case "$typed" in
		*"'"*) printf "  It can't contain a ' (single quote). SMTP password: " >&2 ;;
		*) break ;;
		esac
	done
	[ -n "$typed" ] && smtp_password=$typed
fi
smtp_tls=starttls
[ "$smtp_port" = "465" ] && smtp_tls=tls
email_login=true
if [ "$smtp_host" = "-" ]; then
	smtp_host="" smtp_port="" smtp_from="" smtp_user="" smtp_password="" smtp_tls=none email_login=false
fi

# Edge network: Caddy's fixed address on the network it shares with
# Overfin and Pocket ID; Overfin trusts it to say who's connecting. Other
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

umask 077
cat > .env.new <<EOF
# Written by setup.sh. Every secret of this install: keep it private, and
# keep a copy somewhere safe. Overfin's own secret key is generated on its
# first start, in the overfin-secrets volume: back that up too.
OVERFIN_DOMAIN=$site
AUTH_DOMAIN=$auth
OVERFIN_SITE_NAME="$name"

OVERFIN_IMAGE=$(keep OVERFIN_IMAGE ghcr.io/overfin/overfin:0.2.0)
POCKET_ID_TAG=$(keep POCKET_ID_TAG v2.19.1)
CADDY_TAG=$(keep CADDY_TAG 2)

# Caddy's fixed address on the internal network, which Overfin trusts.
EDGE_SUBNET=${edge_subnet:-172.29.250.0/28}
EDGE_RANGE=${edge_range:-172.29.250.8/29}
CADDY_IP=${caddy_ip:-172.29.250.2}

OVERFIN_OIDC_CLIENT_SECRET=$(keep OVERFIN_OIDC_CLIENT_SECRET "$(secret 32)")
OVERFIN_CONNECTOR_TOKEN=$(keep OVERFIN_CONNECTOR_TOKEN "$(secret 32)")

POCKET_ID_ENCRYPTION_KEY=$(keep POCKET_ID_ENCRYPTION_KEY "$(secret 32)")

SMTP_HOST=$smtp_host
SMTP_PORT=$smtp_port
SMTP_TLS=$smtp_tls
SMTP_FROM=$smtp_from
SMTP_USER=$smtp_user
SMTP_PASSWORD='$smtp_password'
EMAIL_LOGIN=$email_login
EOF
# An older install's own secret key and Jellyfin key, set in .env, stay.
for v in OVERFIN_SECRET_KEY OVERFIN_JELLYFIN_ADMIN_KEY; do
	if [ -n "$(current $v)" ]; then
		printf '%s=%s\n' "$v" "$(current $v)" >> .env.new
	fi
done
caddy_ip=$(sed -n 's/^CADDY_IP=//p' .env.new)
mv .env.new .env

umask 022
[ -e overfin.toml ] && cp overfin.toml overfin.toml.bak
{
cat <<EOF
# Written by setup.sh. What Overfin needs to start; Jellyfin is connected in
# its setup page, and features are switched in its control panel. Every
# setting: overfin.example.toml in the repository.
EOF
if [ -n "$jellyfin" ]; then
	printf '\n[jellyfin]\nurl = "%s"\n' "$jellyfin"
fi
cat <<EOF

[identity]
mode = "oidc"
trusted_proxies = ["$caddy_ip"]

[oidc]
issuer = "https://$auth"
client_id = "overfin"
redirect_url = "https://$site/api/v1/auth/callback"

[admin]
listen = ":8081"
group = "overfin-admins"
redirect_url = "http://localhost:8081/admin/auth/callback"

[connector]
kind = "pocketid"
url = "https://$auth"
first_admin = "$email"

# On the first start only; then, the control panel's Features.
[plugins]
enable = ["invites"]
EOF
} > overfin.toml

if $again; then
	cat >&2 <<EOF

Updated (the previous overfin.toml is in overfin.toml.bak). To apply:
  docker compose up -d
EOF
	if [ "$email" != "$old_email" ]; then
		echo "The admin's email only counts while Pocket ID has nobody: change it in Pocket ID itself (your account's page)." >&2
	fi
	exit 0
fi

cat >&2 <<EOF

Done. Next:
  docker compose up -d
Then, after a minute or two:
  - connect Jellyfin, in Overfin's setup page: ssh -L 8081:localhost:8081
    <this machine>, then http://localhost:8081, with the setup code from
    docker compose logs overfin;
  - your sign-in link (it works once, for an hour):
      docker compose exec -u 1000:1000 pocket-id /app/pocket-id one-time-access-token $email
    open it and add a passkey;
  - https://$site : the site;
  - the control panel, later: same address as the setup page. Invite people
    from its Invites tab.
If something's off: docker compose exec overfin /overfin -check
EOF
