#!/bin/sh
# Prune merged branches and stale worktrees / 清理已合并分支与过期 worktree.
#
# Safety rules / 安全规则:
#   - only branches whose tip is an ancestor of origin/HEAD are touched;
#   - the current branch, the default branch, the primary checkout and any
#     branch checked out in a surviving worktree are never deleted;
#   - worktrees are removed without --force: a dirty worktree is skipped
#     with a warning, never discarded;
#   - remote deletion is opt-in via --remote (git push origin --delete)
#     and never targets the default branch.
#   - 仅处理 tip 是 origin/HEAD 祖先的分支;当前分支、默认分支、主检出与
#     任何被 worktree 占用的分支绝不删除;worktree 不用 --force 移除,脏
#     worktree 仅警告跳过;远端删除需显式 --remote 且绝不删除默认分支。
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
TAB=$(printf '\t')
while IFS="$TAB" read -r wbranch wpath; do
	[ -n "$wpath" ] || continue
	if ! git -C "$REPO" merge-base --is-ancestor "refs/heads/$wbranch" "$DEFAULT_REF"; then
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
		git -C "$REPO" branch -d "$branch"
		deleted_local=$((deleted_local + 1))
	fi
done
echo "prune: deleted $deleted_local merged local branch(es)"

if [ "$REMOTE" -eq 1 ]; then
	deleted_remote=0
	for ref in $(git -C "$REPO" for-each-ref --format='%(refname:short)' refs/remotes/origin); do
		[ "$ref" = "origin/HEAD" ] && continue
		branch=${ref#origin/}
		[ "$branch" = "$DEFAULT" ] && continue
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
