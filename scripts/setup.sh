#!/usr/bin/env bash
# Interactive first-time setup: writes .env with your hostnames and freshly
# generated secrets, and hashes the IRC operator password.
#
#   scripts/setup.sh            # asks questions
#   scripts/setup.sh --local    # *.localhost dev install, local CA, no questions
set -euo pipefail
cd "$(dirname "$0")/.."

ERGO_IMAGE=ghcr.io/ergochat/ergo:v2.19.1
HARBOR=https://harbor.floret.dev/api/v2.0/projects/boson/repositories

if [ -e .env ]; then
	echo ".env already exists; move it away first if you want to start over." >&2
	exit 1
fi
command -v openssl >/dev/null || { echo "setup.sh needs openssl" >&2; exit 1; }

# (tr gets SIGPIPE when head stops reading; don't let pipefail treat that as an error)
rand() { (set +o pipefail; LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "${1:-32}"); }
ask() { # ask VAR "question" default
	local reply
	read -r -p "$2 [$3]: " reply
	printf -v "$1" '%s' "${reply:-$3}"
}
# latest_release REPO: newest x.y.z tag on Harbor, or nothing.
latest_release() {
	(set +o pipefail; curl -fsS --max-time 10 "$HARBOR/$1/artifacts?with_tag=true&page_size=100" 2>/dev/null |
		grep -oE '"name":"[0-9]+\.[0-9]+\.[0-9]+"' | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)
}

# local_ca: a local CA plus one certificate for every *.localhost name the
# stack uses, in ./local-certs. Traefik serves it as its default certificate
# and certsync hands it to Ergo and quicvoiced. Trust local-certs/ca.pem in
# your browser/OS to get rid of certificate warnings.
local_ca() {
	local d=local-certs
	mkdir -p "$d"
	(
		umask 077
		cd "$d"
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 3650 \
			-keyout ca.key -out ca.pem -subj "/CN=boson stack local CA" \
			-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
		openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
			-keyout key.pem -out cert.csr -subj "/CN=localhost" 2>/dev/null
		cat > cert.ext <<-EXT
			basicConstraints=critical,CA:FALSE
			keyUsage=critical,digitalSignature
			extendedKeyUsage=serverAuth
			subjectKeyIdentifier=hash
			authorityKeyIdentifier=keyid,issuer
			subjectAltName=DNS:localhost,DNS:*.localhost,DNS:irc.localhost,DNS:voice.localhost,DNS:auth.localhost,DNS:admin.localhost,DNS:api.localhost,IP:127.0.0.1,IP:::1
		EXT
		openssl x509 -req -in cert.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 825 \
			-extfile cert.ext -out cert.pem 2>/dev/null
		rm -f cert.csr cert.ext ca.srl
		chmod 644 ca.pem cert.pem
	)
	echo "Local CA: $d/ca.pem (trust it to avoid TLS warnings); certificate: $d/cert.pem"
}

LOCAL=false
if [ "${1:-}" = "--local" ]; then
	LOCAL=true
	NETWORK_NAME=Boson
	DOMAIN=localhost
	IRC_HOST=irc.localhost
	VOICE_HOST=voice.localhost
	AUTH_HOST=auth.localhost
	ADMIN_HOST=admin.localhost
	API_HOST=api.localhost
	ADMIN_EMAIL=admin@localhost
	TLS_ISSUER=local
	ACME_CA_SERVER=
	PROFILES=irc,voice,auth,api,wellknown
	VOICE_ROOMS=general:General
	REQUIRE_EMAIL_VERIFICATION=false
	OPER_NAME=admin
	OPER_PASSWORD=$(rand 20)
	echo "Local install: oper login is  /OPER admin $OPER_PASSWORD"
else
	ask NETWORK_NAME "Network name" "Boson"
	ask DOMAIN "Your domain" "example.com"
	ask IRC_HOST "IRC hostname" "irc.$DOMAIN"
	ask VOICE_HOST "Voice hostname" "voice.$DOMAIN"
	ask AUTH_HOST "Accounts (auth) hostname" "auth.$DOMAIN"
	ADMIN_HOST="admin.$DOMAIN"
	API_HOST="api.$DOMAIN"
	ask ADMIN_EMAIL "Admin email (also used for Let's Encrypt)" "admin@$DOMAIN"
	ask TLS_ISSUER "TLS: Let's Encrypt email, or 'local' for a self-made local CA" "$ADMIN_EMAIL"
	ACME_CA_SERVER=
	if [ "$TLS_ISSUER" != local ]; then
		ask STAGING "Use the Let's Encrypt staging CA (test certs, no rate limits)? y/N" "n"
		case "$STAGING" in [yY]*) ACME_CA_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory ;; esac
	fi
	ask PROFILES "Parts to run (irc, voice, auth, api, metrics, admin)" "irc,voice,auth,api"
	case ",$PROFILES," in *,voice,*)
		ask VOICE_ROOMS "Voice channels (id:Display Name, comma-separated)" "general:General" ;;
	*) VOICE_ROOMS=general:General ;;
	esac
	ask WELLKNOWN "Serve https://$DOMAIN/.well-known/boson.json from this server, so the app finds everything from \"$DOMAIN\"? Only if $DOMAIN's DNS points here. y/N" "n"
	case "$WELLKNOWN" in [yY]*) PROFILES="$PROFILES,wellknown" ;; esac
	REQUIRE_EMAIL_VERIFICATION=true
	ask OPER_NAME "IRC operator name" "admin"
	read -r -s -p "IRC operator password (empty = generate): " OPER_PASSWORD; echo
	if [ -z "$OPER_PASSWORD" ]; then
		OPER_PASSWORD=$(rand 20)
		echo "Generated oper password: $OPER_PASSWORD  (save it now; only the hash is stored)"
	fi
fi

echo "Hashing the operator password with ergo genpasswd ($ERGO_IMAGE)..."
OPER_PASSWORD_HASH=$(printf '%s\n' "$OPER_PASSWORD" | docker run --rm -i --entrypoint /ircd-bin/ergo "$ERGO_IMAGE" genpasswd |
	grep -oE '\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}' | tail -1)
[ -n "$OPER_PASSWORD_HASH" ] || { echo "could not hash the oper password" >&2; exit 1; }

[ "$TLS_ISSUER" = local ] && local_ca

# The auth service image: the newest release on Harbor, else build from a
# checkout next to this repo (compose.auth-src.yaml).
AUTH_VERSION=$(latest_release auth || true)
AUTH_LINES=()
if [ -n "$AUTH_VERSION" ]; then
	AUTH_LINES+=("AUTH_IMAGE=harbor.floret.dev/boson/auth:$AUTH_VERSION")
elif [ -f ../auth/Dockerfile ]; then
	echo "No auth release on Harbor yet: building it from ../auth (compose.auth-src.yaml)."
	AUTH_LINES+=("COMPOSE_FILE=compose.yaml:compose.auth-src.yaml" "AUTH_SRC=../auth")
else
	echo "WARNING: no auth release on Harbor and no ../auth checkout; set AUTH_IMAGE or AUTH_SRC in .env." >&2
fi

umask 077
{
	echo "# Generated by scripts/setup.sh on $(date -u +%Y-%m-%d). See .env.example for all options."
	echo "COMPOSE_PROFILES=$PROFILES"
	for l in ${AUTH_LINES[@]+"${AUTH_LINES[@]}"}; do echo "$l"; done
	echo "NETWORK_NAME=$NETWORK_NAME"
	echo "DOMAIN=$DOMAIN"
	echo "IRC_HOST=$IRC_HOST"
	echo "VOICE_HOST=$VOICE_HOST"
	echo "AUTH_HOST=$AUTH_HOST"
	echo "ADMIN_HOST=$ADMIN_HOST"
	echo "API_HOST=$API_HOST"
	case ",$PROFILES," in
	*,wellknown,*) echo "CERT_HOSTS=$IRC_HOST,$VOICE_HOST,$AUTH_HOST,$API_HOST,$DOMAIN" ;;
	*) echo "CERT_HOSTS=$IRC_HOST,$VOICE_HOST,$AUTH_HOST,$API_HOST" ;;
	esac
	echo "ADMIN_EMAIL=$ADMIN_EMAIL"
	echo "TLS_ISSUER=$TLS_ISSUER"
	[ -z "$ACME_CA_SERVER" ] || echo "ACME_CA_SERVER=$ACME_CA_SERVER"
	echo "OPER_NAME=$OPER_NAME"
	# Single quotes: compose would otherwise expand the hash's $ signs.
	echo "OPER_PASSWORD_HASH='${OPER_PASSWORD_HASH}'"
	echo "IRC_PLAINTEXT=false"
	echo "REGISTRATION=open"
	echo "REQUIRE_EMAIL_VERIFICATION=$REQUIRE_EMAIL_VERIFICATION"
	echo "POSTGRES_PASSWORD=$(rand 40)"
	echo "AUTH_KEY_ENCRYPTION_KEY=$(openssl rand -base64 32)"
	echo "INTERNAL_API_TOKEN=$(openssl rand -hex 32)"
	echo "ERGO_API_TOKEN=$(openssl rand -hex 32)"
	echo "ADMIN_SESSION_KEY=$(openssl rand -base64 32)"
	echo "API_DB_PASSWORD=$(rand 40)"
	echo "VOICE_JWT_SECRET=$(rand 64)"
	echo "VOICE_ROOMS=$VOICE_ROOMS"
	echo "SMTP_HOST="
	echo "SMTP_PORT=587"
	echo "SMTP_USER="
	echo "SMTP_PASSWORD="
	echo "SMTP_FROM="
	echo "SMTP_TLS=starttls"
} > .env

echo "Wrote .env. Next:"
if [ "$LOCAL" = false ] && [ "$TLS_ISSUER" != local ]; then
	case ",$PROFILES," in
	*,wellknown,*) echo "  1. Point DNS A records for $DOMAIN, $IRC_HOST, $VOICE_HOST, $AUTH_HOST and $API_HOST at this server." ;;
	*) echo "  1. Point DNS A records for $IRC_HOST, $VOICE_HOST, $AUTH_HOST and $API_HOST at this server." ;;
	esac
	echo "  2. Open ports 80/tcp, 443/tcp, 6697/tcp and 7700/udp."
	echo "  3. Optional: SMTP_* in .env so the auth service can send verification email."
fi
echo "  docker compose up -d --build"
