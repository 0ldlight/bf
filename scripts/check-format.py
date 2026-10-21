#!/usr/bin/env python3
# BIN-124/BIN-133 shared gofmt gate / 共享 gofmt 门禁.
#
# Discovers Go sources through Git (tracked plus untracked-but-not-ignored)
# instead of `gofmt -l .`, so Git-ignored local archive/research worktrees
# (for example .binflow/worktrees) are never scanned, while the whole active
# working tree — including the nested integration module, tracked files that
# newly match an ignore rule, untracked new Go, and unusual filenames — stays
# covered. Fails closed: any Git or gofmt error fails the gate. Never writes
# (-w is never used).
#
# 通过 Git（已跟踪 + 未跟踪且未被忽略）发现 Go 源码，替代 `gofmt -l .`：
# 被忽略的本地归档/研究工作树（如 .binflow/worktrees）不再被扫描，
# 而活动工作树（含嵌套 integration 模块、被新忽略规则命中的已跟踪文件、
# 未跟踪新 Go、特殊文件名）保持全覆盖。Git 或 gofmt 出错即失败，
# 永不自动改写文件。
import os
import subprocess
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _git_ls(repo, *args):
    proc = subprocess.run(
        ["git", "-C", repo, "ls-files", "-z", *args, "--", "*.go"],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if proc.returncode != 0:
        sys.stderr.write(
            "check-format: git ls-files failed "
            f"(exit {proc.returncode}): {proc.stderr.decode(errors='replace').strip()}\n"
        )
        raise SystemExit(1)
    return {p for p in proc.stdout.split(b"\0") if p}


def main(argv):
    repo = argv[1] if len(argv) > 1 else REPO_ROOT
    # Tracked files stay checked even if a new ignore rule matches them;
    # untracked files are checked only when not ignored.
    paths = _git_ls(repo) | _git_ls(repo, "-o", "--exclude-standard")
    files = sorted(
        os.fsdecode(p)
        for p in paths
        if os.path.exists(os.path.join(repo, os.fsdecode(p)))  # staged deletions
    )
    if not files:
        return 0
    proc = subprocess.run(["gofmt", "-l", *files], stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE)
    if proc.returncode != 0:
        sys.stderr.write(
            "check-format: gofmt failed "
            f"(exit {proc.returncode}): {proc.stderr.decode(errors='replace').strip()}\n"
        )
        raise SystemExit(1)
    unformatted = proc.stdout.decode(errors="replace").splitlines()
    if unformatted:
        sys.stderr.write(
            "gofmt: these files need formatting (run gofmt -w, then commit) "
            "/ 以下文件需要格式化（运行 gofmt -w 后提交）：\n"
        )
        for name in unformatted:
            sys.stderr.write(f"{name}\n")
        raise SystemExit(1)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
