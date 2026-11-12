#!/bin/sh
# Single-writer checkout lock / 检出单写者锁.
#
# One checkout (the primary checkout or one task worktree) must have at
# most one active writer. This helper provides that mutual exclusion with
# an atomic `mkdir` lock, records who holds it, reclaims locks left behind
# by exited processes, and refuses to delete paths it did not create.
# 一个检出（主检出或某个任务 worktree）最多允许一个活跃写者：mkdir 原子加锁、
# 记录持有者、按 PID 回收陈旧锁、绝不删除非本脚本创建的路径。
#
# Usage / 用法:
#   scripts/checkout-lock.sh with <label> -- <command...>   run under the lock
#   scripts/checkout-lock.sh status                          0=free, 1=busy/stale
#   scripts/checkout-lock.sh release                         remove our own lock
#
# The lock lives at <checkout>/.binflow/locks/checkout, where <checkout> is
# the tree containing this script (a worktree therefore gets its own lock).
# CHECKOUT_LOCK_DIR overrides the location for tests and special layouts.
set -eu

CHECKOUT_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
LOCK_PATH=${CHECKOUT_LOCK_DIR:-"$CHECKOUT_ROOT/.binflow/locks/checkout"}

fail() {
	echo "checkout-lock: $*" >&2
	exit 1
}

usage() {
	cat >&2 <<'EOF'
usage: scripts/checkout-lock.sh with <label> -- <command...>
       scripts/checkout-lock.sh status
       scripts/checkout-lock.sh release
EOF
	exit 2
}

owner_alive() {
	_pid=$1
	case "$_pid" in '' | *[!0-9]*) return 1 ;; esac
	[ "$_pid" -gt 0 ] 2>/dev/null || return 1
	kill -0 "$_pid" 2>/dev/null
}

holder_pid() {
	sed -n 's/^pid=//p' "$LOCK_PATH/owner" 2>/dev/null | head -n 1
}

describe_holder() {
	if [ -f "$LOCK_PATH/owner" ]; then
		tr '\n' ' ' < "$LOCK_PATH/owner"
	else
		printf 'unknown'
	fi
}

# Same-user PID liveness only: a recycled PID can hide a stale lock, and a
# live PID held by another user reports dead. Both are documented limits.
# 仅支持同用户 PID 存活判断：PID 复用可能掩盖陈旧锁，跨用户进程会被判为
# 已退出；两者都是已记录的限制。
acquire() {
	_label=$1
	_attempts=0
	while :; do
		_attempts=$((_attempts + 1))
		if mkdir "$LOCK_PATH" 2>/dev/null; then
			if ! printf 'label=%s\npid=%s\nhost=%s\nacquired=%s\n' \
				"$_label" "$$" "$(uname -n 2>/dev/null || printf unknown)" \
				"$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || printf unknown)" \
				> "$LOCK_PATH/owner"; then
				rm -rf "$LOCK_PATH"
				fail "cannot write the owner file under $LOCK_PATH"
			fi
			if ! touch "$LOCK_PATH/.checkout-lock"; then
				rm -rf "$LOCK_PATH"
				fail "cannot mark the lock directory $LOCK_PATH"
			fi
			return 0
		fi
		[ -d "$LOCK_PATH" ] || fail "lock path exists but is not a directory: $LOCK_PATH"
		if owner_alive "$(holder_pid)"; then
			fail "checkout is busy (holder: $(describe_holder))"
		fi
		# Stale holder: remove exactly one generation, then retry once.
		# 陈旧持有者：只清除这一代锁，随后仅重试一次。
		[ "$_attempts" -ge 2 ] && fail "cannot reclaim the stale lock at $LOCK_PATH"
		rm -rf "$LOCK_PATH"
	done
}

cmd_with() {
	_label=$1
	shift
	[ "${1-}" = "--" ] || usage
	shift
	[ "$#" -gt 0 ] || usage
	acquire "$_label"
	trap 'rm -rf "$LOCK_PATH"' EXIT INT TERM
	"$@"
}

cmd_status() {
	if [ ! -d "$LOCK_PATH" ]; then
		echo "free"
		return 0
	fi
	if owner_alive "$(holder_pid)"; then
		echo "busy: $(describe_holder)"
		return 1
	fi
	echo "stale: $(describe_holder)"
	return 1
}

cmd_release() {
	[ -f "$LOCK_PATH/.checkout-lock" ] ||
		fail "refusing to remove a path this script did not create: $LOCK_PATH"
	# Only the live holder or a stale lock may be released: a third party
	# must not break another writer's mutual exclusion.
	# 仅存活持有者或陈旧锁可释放:第三方不得破坏其他写者的互斥。
	_holder=$(holder_pid)
	if owner_alive "$_holder" && [ "$_holder" != "$$" ]; then
		fail "lock is held by live pid ${_holder}; only its holder or a stale reclaim may release"
	fi
	rm -rf "$LOCK_PATH"
}

case "${1-}" in
with)
	shift
	[ "$#" -ge 2 ] || usage
	cmd_with "$@"
	;;
status)
	cmd_status
	;;
release)
	cmd_release
	;;
*)
	usage
	;;
esac
