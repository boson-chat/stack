#!/bin/sh
# Runs once, when the Postgres volume is first created: a database and role
# for the push relay (pushrelay profile). Existing installs: see
# docs/push.md "Running the relay".
set -eu
if [ -n "${PUSHRELAY_DB_PASSWORD:-}" ]; then
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
		-v pw="$PUSHRELAY_DB_PASSWORD" -v usr="${PUSHRELAY_DB_USER:-pushrelay}" -v db="${PUSHRELAY_DB_NAME:-pushrelay}" <<'SQL'
CREATE ROLE :"usr" LOGIN PASSWORD :'pw';
CREATE DATABASE :"db" OWNER :"usr";
SQL
fi
