#!/bin/bash
# Builds services.conf from Anope's stock example.conf plus a handful of
# substitutions from the environment, and msmtp settings for email.
set -euo pipefail

BASE=/home/anope/anope
EX="$BASE/conf/example.conf"
OUT="$BASE/conf/services.conf"

die() { echo "entrypoint: $*" >&2; exit 1; }
for v in SERVICES_HOST NETWORK_NAME UPLINK_HOST SERVICES_LINK_PASSWORD OPER_NAME; do
	[ -n "${!v:-}" ] || die "$v is not set (see .env.example)"
done

# A stable random seed, generated once and kept with the database.
SEED_FILE="$BASE/data/.seed"
[ -s "$SEED_FILE" ] || shuf -i 100000000-4000000000 -n 1 > "$SEED_FILE"
SEED=$(cat "$SEED_FILE")

# Escape values for use as sed replacements.
esc() { printf '%s' "$1" | sed -e 's/[\/&|]/\\&/g'; }

USEMAIL=no
[ -n "${SMTP_HOST:-}" ] && USEMAIL=yes

umask 077
sed \
	-e "s|^\(\s*\)value = \"services.example.com\"|\1value = \"$(esc "$SERVICES_HOST")\"|" \
	-e "s|^\(\s*\)host = \"127.0.0.1\"|\1host = \"$(esc "$UPLINK_HOST")\"|" \
	-e "s|^\(\s*\)port = 7000|\1port = ${UPLINK_PORT:-7000}|" \
	-e "0,/^\s*ssl = no/s|^\(\s*\)ssl = no|\1ssl = yes|" \
	-e "s|^\(\s*\)password = \"mypassword\"|\1password = \"$(esc "$SERVICES_LINK_PASSWORD")\"|" \
	-e "s|^\(\s*\)description = \"Services for IRC Networks\"|\1description = \"$(esc "$NETWORK_NAME") Services\"\n\1id = \"${SERVICES_SID:-00A}\"|" \
	-e "s|^\(\s*\)name = \"inspircd3\"|\1name = \"unreal4\"|" \
	-e "s|^\(\s*\)networkname = \"LocalNet\"|\1networkname = \"$(esc "$NETWORK_NAME")\"|" \
	-e "s|^\(\s*\)#seed = 9866235|\1seed = $SEED|" \
	-e "s|^\(\s*\)usemail = yes|\1usemail = $USEMAIL|" \
	-e "s|^\(\s*\)sendfrom = \"services@example.com\"|\1sendfrom = \"$(esc "${SMTP_FROM:-services@$SERVICES_HOST}")\"|" \
	"$EX" > "$OUT"

# Every substitution must have hit, or Anope would silently run with defaults.
for want in "value = \"$SERVICES_HOST\"" 'ssl = yes' 'name = "unreal4"' "seed = $SEED" "id = \"${SERVICES_SID:-00A}\""; do
	grep -qF "$want" "$OUT" || die "config substitution failed for: $want (example.conf changed?)"
done

cat >> "$OUT" <<CONF

/* Added by entrypoint.sh */
module { name = "m_ssl_openssl" }

oper
{
	name = "$OPER_NAME"
	type = "Services Root"
	require_oper = yes
}
CONF

if [ "$USEMAIL" = yes ]; then
	cat > "$HOME/.msmtprc" <<MSMTP
defaults
auth           ${SMTP_AUTH:-on}
tls            ${SMTP_TLS:-on}
tls_trust_file /etc/ssl/certs/ca-certificates.crt
logfile        $BASE/logs/msmtp.log

account default
host     $SMTP_HOST
port     ${SMTP_PORT:-587}
from     ${SMTP_FROM:-services@$SERVICES_HOST}
user     ${SMTP_USER:-}
password ${SMTP_PASSWORD:-}
MSMTP
	chmod 600 "$HOME/.msmtprc"
fi

exec "$@"
