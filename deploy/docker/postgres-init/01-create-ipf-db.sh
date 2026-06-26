#!/usr/bin/env bash
# Create the interface-engine's database + user on first boot.
#
# The official postgres image runs every executable in
# /docker-entrypoint-initdb.d/ ONCE on first container start (when PGDATA
# is empty). On subsequent starts the directory is ignored, so this
# script doesn't run on every restart — only when Postgres first
# initializes the data directory. Idempotency is therefore not strictly
# required, but we still guard against re-create errors so the script
# tolerates manual re-runs.
#
# Env vars (set by docker-compose.yml):
#   IPF_DB_USER     — interface-engine's Postgres role (default: ipf)
#   IPF_DB_PASSWORD — that role's password
#   IPF_DB_NAME     — database name (default: ipf)

set -euo pipefail

: "${IPF_DB_USER:?IPF_DB_USER must be set}"
: "${IPF_DB_PASSWORD:?IPF_DB_PASSWORD must be set}"
: "${IPF_DB_NAME:?IPF_DB_NAME must be set}"

echo "[postgres-init] creating role ${IPF_DB_USER} and database ${IPF_DB_NAME}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "postgres" <<-SQL
    DO \$\$
    BEGIN
        IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '${IPF_DB_USER}') THEN
            CREATE ROLE ${IPF_DB_USER} LOGIN PASSWORD '${IPF_DB_PASSWORD}';
        END IF;
    END
    \$\$;

    SELECT 'CREATE DATABASE ${IPF_DB_NAME} OWNER ${IPF_DB_USER}'
    WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '${IPF_DB_NAME}')\gexec

    GRANT ALL PRIVILEGES ON DATABASE ${IPF_DB_NAME} TO ${IPF_DB_USER};
SQL

echo "[postgres-init] done"
