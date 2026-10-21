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

echo '== gofmt over Git-discovered sources (gate: fails, never auto-formats) =='
# Git-scoped discovery keeps ignored local archive/research worktrees out of
# the gate while covering tracked, staged and untracked non-ignored Go,
# including the nested integration module and unusual filenames.
# 基于 Git 的发现范围排除被忽略的本地归档/研究工作树，同时覆盖已跟踪、
# 暂存与未跟踪且未被忽略的 Go 源码（含嵌套 integration 模块与特殊文件名）。
python3 scripts/check-format.py

echo '== format gate tests =='
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_*.py'

echo '== go mod verify =='
go mod verify

echo '== go vet =='
go vet ./...

echo '== go test -race =='
go test -race ./...

echo '== UAT artifact and recovery tests =='
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/uat -p 'test_*.py'

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/binflow-check.XXXXXX")
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT INT TERM

echo '== go build (temporary output) =='
go build -o "$tmp_dir/binflow" ./cmd/binflow

echo '== smoke (scripts/smoke.py) =='
python3 scripts/smoke.py "$tmp_dir/binflow"

echo 'check: all gates passed / 全部检查通过'
