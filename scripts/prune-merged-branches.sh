#!/bin/sh
# Prune merged branches and stale worktrees / 清理已合并分支与过期 worktree.
#
# Safety rules / 安全规则:
#   - only branches whose tip is an ancestor of origin/HEAD are touched;
#   - the current branch, the default branch, the primary checkout and any
#     branch checked out in a surviving worktree are never deleted;
#   - worktrees younger than PRUNE_MIN_AGE_HOURS (default 24) are never
#     removed: a just-created worktree sits on a merged tip with a clean
#     tree and is indistinguishable from a stale one by content alone;
#   - worktrees are removed without --force: a dirty worktree is skipped
#     with a warning, never discarded;
#   - remote deletion is opt-in via --remote (git push origin --delete),
#     never targets the default branch, and skips branches occupied by a
#     local worktree (their first push may still be pending).
#   - 仅处理 tip 是 origin/HEAD 祖先的分支;当前分支、默认分支、主检出与
#     任何被 worktree 占用的分支绝不删除;新建不满 PRUNE_MIN_AGE_HOURS
#     （默认 24 小时）的 worktree 绝不 removal——刚建的 worktree 停在已合并
#     tip 且树是干净的,仅凭内容无法与陈旧者区分;worktree 不用 --force 移除;
#     远端删除需显式 --remote、绝不删除默认分支,且本地被 worktree 占用的
#     分支跳过（其首次推送可能尚未发生）。
#
# Usage / 用法:
#   scripts/prune-merged-branches.sh [repo-dir] [--remote]
set -eu

fail() {
	echo "prune-merged-branches: $*" >&2
	exit 1
}

usage() {
	echo "usage: scripts/prune-merged-branches.sh [repo-dir] [--remote]" >&2
	exit 2
}

REPO=""
REMOTE=0
for arg in "$@"; do
	case "$arg" in
	--remote) REMOTE=1 ;;
	-*) usage ;;
	*) [ -z "$REPO" ] || usage; REPO=$arg ;;
	esac
done
if [ -z "$REPO" ]; then
	REPO=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
fi

git -C "$REPO" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
	fail "not a Git repository: $REPO"

DEFAULT_REF=$(git -C "$REPO" symbolic-ref --quiet --short refs/remotes/origin/HEAD) ||
	fail "cannot resolve origin/HEAD (run: git remote set-head origin --auto)"
DEFAULT=${DEFAULT_REF#origin/}
PRIMARY=$(git -C "$REPO" worktree list --porcelain | sed -n 's/^worktree //p' | sed -n '1p')

# "branch<TAB>path" pairs, primary checkout excluded from the start.
# branch<TAB>path 对,主检出从一开始就排除。
PAIRS=$(git -C "$REPO" worktree list --porcelain | awk -v primary="$PRIMARY" '
	/^worktree / { path = substr($0, 10) }
	/^branch refs\/heads\// {
		branch = substr($0, 19)
		if (path != primary && path != "") printf "%s\t%s\n", branch, path
	}
')

removed_worktrees=0
skipped_worktrees=0
min_age_seconds=$(( ${PRUNE_MIN_AGE_HOURS:-24} * 3600 ))
now_epoch=$(date +%s)

# Creation-time proxy: the admin HEAD file is written once when the
# worktree is added. If it cannot be located, fail safe and skip removal.
# 创建时间代理:admin HEAD 文件在 worktree 添加时写入一次;无法定位时
# 安全起见跳过移除。
worktree_age_ok() {
	# min age <= 0 disables the gate entirely (explicit opt-out).
	# 最小年龄 <= 0 时完全关闭护栏(显式退出)。
	[ "$min_age_seconds" -gt 0 ] || return 0
	_wt=$1
	_admin=$(git -C "$REPO" rev-parse --git-path "worktrees/$(basename -- "$_wt")/HEAD" 2>/dev/null) || return 1
	[ -f "$_admin" ] || return 1
	if [ "$(uname -s)" = Darwin ]; then
		_created=$(stat -f %m "$_admin" 2>/dev/null) || return 1
	else
		_created=$(stat -c %Y "$_admin" 2>/dev/null) || return 1
	fi
	case "$_created" in '' | *[!0-9]*) return 1 ;; esac
	[ $(( now_epoch - _created )) -ge "$min_age_seconds" ]
}

TAB=$(printf '\t')
while IFS="$TAB" read -r wbranch wpath; do
	[ -n "$wpath" ] || continue
	if ! git -C "$REPO" merge-base --is-ancestor "refs/heads/$wbranch" "$DEFAULT_REF"; then
		continue
	fi
	if ! worktree_age_ok "$wpath"; then
		skipped_worktrees=$((skipped_worktrees + 1))
		echo "prune: SKIP young worktree $wpath ($wbranch, under ${PRUNE_MIN_AGE_HOURS:-24}h)" >&2
		continue
	fi
	if git -C "$REPO" worktree remove "$wpath"; then
		removed_worktrees=$((removed_worktrees + 1))
		echo "prune: removed worktree ${wpath##*/} ($wbranch)"
	else
		skipped_worktrees=$((skipped_worktrees + 1))
		echo "prune: SKIP dirty worktree $wpath ($wbranch)" >&2
	fi
done <<EOF
$PAIRS
EOF
echo "prune: removed $removed_worktrees merged worktree(s), skipped $skipped_worktrees"

# Refresh the occupied-branch list after worktree removal so freed branches
# become deletable; the current branch stays protected.
# 移除 worktree 后刷新占用列表,被释放的分支才可删除;当前分支仍受保护。
skip_list=$(git -C "$REPO" branch --show-current)
skip_list="$skip_list
$(git -C "$REPO" worktree list --porcelain | sed -n 's/^branch refs\/heads\///p')"
skip_list=$(printf '%s\n' "$skip_list" | sed '/^$/d' | sort -u)

is_skipped() {
	printf '%s\n' "$skip_list" | grep -qx -- "$1"
}

deleted_local=0
for branch in $(git -C "$REPO" for-each-ref --format='%(refname:short)' refs/heads/); do
	[ "$branch" = "$DEFAULT" ] && continue
	if is_skipped "$branch"; then
		continue
	fi
	if git -C "$REPO" merge-base --is-ancestor "refs/heads/$branch" "$DEFAULT_REF"; then
		# -d can still refuse (for example a diverged same-name upstream);
		# that refusal is respected and the sweep continues.
		# -d 仍可能拒绝(如同名远端分支已分叉);尊重拒绝并继续清扫。
		if git -C "$REPO" branch -d "$branch"; then
			deleted_local=$((deleted_local + 1))
		else
			echo "prune: SKIP local branch $branch (git branch -d refused)" >&2
		fi
	fi
done
echo "prune: deleted $deleted_local merged local branch(es)"

	if [ "$REMOTE" -eq 1 ]; then
	deleted_remote=0
	for ref in $(git -C "$REPO" for-each-ref --format='%(refname:short)' refs/remotes/origin); do
		[ "$ref" = "origin/HEAD" ] && continue
		branch=${ref#origin/}
		[ "$branch" = "$DEFAULT" ] && continue
		if is_skipped "$branch"; then
			echo "prune: SKIP remote branch $branch (occupied by a local worktree)" >&2
			continue
		fi
		if git -C "$REPO" merge-base --is-ancestor "$ref" "$DEFAULT_REF"; then
			if git -C "$REPO" push origin --delete "$branch"; then
				deleted_remote=$((deleted_remote + 1))
			else
				echo "prune: SKIP remote branch $branch (push failed)" >&2
			fi
		fi
	done
	echo "prune: deleted $deleted_remote merged remote branch(es)"
fi
