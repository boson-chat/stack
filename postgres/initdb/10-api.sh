#!/bin/sh
# Runs once, when the Postgres volume is first created: a database and role
# for the boson API (api profile). Existing installs: see docs/api.md.
set -eu
if [ -n "${API_DB_PASSWORD:-}" ]; then
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
		-v pw="$API_DB_PASSWORD" -v usr="${API_DB_USER:-api}" -v db="${API_DB_NAME:-api}" <<'SQL'
CREATE ROLE :"usr" LOGIN PASSWORD :'pw';
CREATE DATABASE :"db" OWNER :"usr";
SQL
fi
