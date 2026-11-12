#!/bin/sh
# Backup the private orchestration state / 备份私有编排状态.
#
# The .binflow directory (task records, worker slots, private control
# scripts, research material) is machine-local and ignored by Git, so it
# has no other redundancy. This helper writes one timestamped tar.gz plus
# a sha256 sidecar into a caller-selected destination and verifies the
# archive is readable before reporting success. Worktrees (full Git
# checkouts whose content lives in Git), caches and transient lock files
# are excluded.
# .binflow（任务记录、worker 槽位、私有控制脚本、研究资料）是单机本地且被
# Git 忽略的目录,除此之外没有任何冗余。本脚本向调用方指定的目录写入一份带
# 时间戳的 tar.gz 与 sha256 边车,并在报告成功前验证归档可读。worktrees
#（内容已在 Git 中的完整检出）、缓存与瞬态锁文件被排除。
#
# Usage / 用法:
#   scripts/backup-local-state.sh <destination-directory>
#
# Destination stays private: point it at private storage per host, never
# at a public directory or the repository itself.
# 目标目录保持私有:按主机指向私有存储,绝不指向公开目录或仓库本身。
set -eu

CHECKOUT_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
STATE_DIR=${BINFLOW_STATE_DIR:-"$CHECKOUT_ROOT/.binflow"}

fail() {
	echo "backup-local-state: $*" >&2
	exit 1
}

usage() {
	echo "usage: scripts/backup-local-state.sh <destination-directory>" >&2
	exit 2
}

[ "$#" -eq 1 ] || usage
dest=$1
[ -n "$dest" ] || usage
[ -d "$STATE_DIR" ] || fail "state directory not found: $STATE_DIR"

digest_tool() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

mkdir -p "$dest"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
archive="$dest/binflow-state-$stamp-$$.tar.gz"

# -C plus the basename keeps archive paths rooted under .binflow/ (or the
# override's basename); excludes match at any depth on both BSD and GNU tar.
# -C 与基名让归档路径锚定在 .binflow/ 下;排除规则在 BSD 与 GNU tar 上均对
# 任意深度生效。
tar -czf "$archive" -C "$(dirname -- "$STATE_DIR")" \
	--exclude 'worktrees' \
	--exclude 'locks' \
	--exclude '__pycache__' \
	--exclude '*.lock' \
	"$(basename -- "$STATE_DIR")" || {
	rm -f "$archive"
	fail "tar failed for $STATE_DIR"
}

# Verify the archive is readable before reporting success.
# 报告成功前先验证归档可读。
if ! tar -tzf "$archive" >/dev/null 2>&1; then
	rm -f "$archive"
	fail "archive verification failed: $archive"
fi

digest=$(digest_tool "$archive")
printf '%s  %s\n' "$digest" "$(basename -- "$archive")" > "$archive.sha256"

echo "backup-local-state: wrote $archive"
echo "backup-local-state: sha256 $digest"
