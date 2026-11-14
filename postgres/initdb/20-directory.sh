#!/bin/sh
# Runs once, when the Postgres volume is first created: a database and role
# for the boson directory (directory profile). Existing installs: see
# docs/directory.md.
set -eu
if [ -n "${DIRECTORY_DB_PASSWORD:-}" ]; then
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
		-v pw="$DIRECTORY_DB_PASSWORD" -v usr="${DIRECTORY_DB_USER:-directory}" -v db="${DIRECTORY_DB_NAME:-directory}" <<'SQL'
CREATE ROLE :"usr" LOGIN PASSWORD :'pw';
CREATE DATABASE :"db" OWNER :"usr";
SQL
fi
