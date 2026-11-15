#!/bin/sh
# Runs once, when the Postgres volume is first created: a database and role
# for the boson files service (files profile). Existing installs: see
# docs/files.md.
set -eu
if [ -n "${FILES_DB_PASSWORD:-}" ]; then
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
		-v pw="$FILES_DB_PASSWORD" -v usr="${FILES_DB_USER:-files}" -v db="${FILES_DB_NAME:-files}" <<'SQL'
CREATE ROLE :"usr" LOGIN PASSWORD :'pw';
CREATE DATABASE :"db" OWNER :"usr";
SQL
fi
