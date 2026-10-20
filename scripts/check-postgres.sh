#!/bin/sh
# BIN-129 PostgreSQL registry gate / PostgreSQL 登记检查入口.
# Runs the real-PostgreSQL integration suite from the nested module with the
# same command locally and in CI. Requires BINFLOW_TEST_POSTGRES_DSN (never
# logged) and BINFLOW_TEST_POSTGRES_DISPOSABLE=1 declaring a disposable
# test database; the suite itself refuses to run otherwise.
set -eu

REPO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

if [ -z "${BINFLOW_TEST_POSTGRES_DSN:-}" ]; then
	echo 'check-postgres: BINFLOW_TEST_POSTGRES_DSN is required (value is never logged)' >&2
	exit 1
fi
if [ "${BINFLOW_TEST_POSTGRES_DISPOSABLE:-}" != "1" ]; then
	echo 'check-postgres: BINFLOW_TEST_POSTGRES_DISPOSABLE must be 1 (disposable test database only)' >&2
	exit 1
fi

cd "$REPO_ROOT/integration/postgres"

echo '== go mod verify =='
go mod verify

echo '== go vet =='
go vet ./...

echo '== go test -race =='
go test -race -timeout 5m ./...

echo 'check-postgres: all gates passed / 全部检查通过'
