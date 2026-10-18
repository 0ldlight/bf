#!/bin/sh
# BIN-124 shared check / 共享检查入口.
# Runs every quality gate from the repository root regardless of the
# caller's working directory, so local runs and CI run exactly the same
# script. Locally:   ./scripts/check.sh
# In CI:            ./scripts/check.sh (after setup-go)
set -eu

REPO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$REPO_ROOT"

if ! command -v python3 >/dev/null 2>&1; then
	echo "check: python3 is required for the smoke gate" >&2
	exit 1
fi

echo '== gofmt (gate: fails, never auto-formats) =='
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo 'gofmt: these files need formatting (run gofmt -w, then commit):' >&2
	printf '%s\n' "$unformatted" >&2
	exit 1
fi

echo '== go mod verify =='
go mod verify

echo '== go vet =='
go vet ./...

echo '== go test -race =='
go test -race ./...

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/binflow-check.XXXXXX")
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT INT TERM

echo '== go build (temporary output) =='
go build -o "$tmp_dir/binflow" ./cmd/binflow

echo '== smoke (scripts/smoke.py) =='
python3 scripts/smoke.py "$tmp_dir/binflow"

echo 'check: all gates passed / 全部检查通过'
