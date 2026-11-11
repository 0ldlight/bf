# Permanent tests for merged-branch and worktree pruning.
# 针对已合并分支与 worktree 清理脚本的永久测试。
#
# Every case builds a disposable repository with a local bare origin,
# merged and unmerged branches, and clean and dirty worktrees; the real
# helper then runs against that fixture. No skips: missing tools fail
# actionably.
import datetime
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

HELPER = pathlib.Path(__file__).resolve().parent / "prune-merged-branches.sh"

GIT_ENV = dict(os.environ)
for _key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY"):
    GIT_ENV.pop(_key, None)
GIT_ENV["GIT_CONFIG_GLOBAL"] = os.devnull
GIT_ENV["GIT_CONFIG_NOSYSTEM"] = "1"
GIT_ENV["GIT_AUTHOR_NAME"] = "fixture"
GIT_ENV["GIT_AUTHOR_EMAIL"] = "fixture@example.test"
GIT_ENV["GIT_COMMITTER_NAME"] = "fixture"
GIT_ENV["GIT_COMMITTER_EMAIL"] = "fixture@example.test"


def _need_tools():
    missing = [
        tool
        for tool in ("git", "sh", "awk")
        if subprocess.run(
            ["sh", "-c", f"command -v {tool} >/dev/null 2>&1"],
            env=GIT_ENV,
        ).returncode
        != 0
    ]
    if missing:
        raise AssertionError(
            f"required tools missing from PATH: {', '.join(missing)} "
            "(scripts/prune-merged-branches.sh needs git, a POSIX shell and awk)"
        )


class PruneFixture:
    """A clone with a bare local origin, plus branch/worktree builders."""

    def __init__(self, root):
        self.root = pathlib.Path(root)
        self.origin = self.root / "origin.git"
        self.repo = self.root / "clone"
        self.run("init", "--bare", "--initial-branch=main", str(self.origin))
        self.run("clone", "--quiet", str(self.origin), str(self.repo))
        # clones of an empty repository have no origin/HEAD: seed main first.
        self.repo_git("commit", "--allow-empty", "-m", "seed")
        self.repo_git("push", "-q", "origin", "main")
        self.run(
            "-C", str(self.repo), "remote", "set-head", "origin", "--auto"
        )

    def run(self, *args):
        proc = subprocess.run(
            ["git", *args], env=GIT_ENV, capture_output=True, text=True, timeout=60
        )
        if proc.returncode != 0:
            raise AssertionError(f"fixture git {args!r} failed: {proc.stderr}")
        return proc.stdout

    def repo_git(self, *args):
        return self.run("-C", str(self.repo), *args)

    def commit_file(self, rel, text, message):
        path = self.repo / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        self.repo_git("add", "--", rel)
        self.repo_git("commit", "-m", message)

    def merged_branch(self, name):
        self.repo_git("switch", "-c", name)
        self.commit_file(f"{name}.txt", name, f"feat: {name}")
        self.repo_git("switch", "main")
        self.repo_git("merge", "--no-ff", "-m", f"merge {name}", name)
        self.repo_git("push", "-q", "origin", "--all")

    def unmerged_branch(self, name):
        self.repo_git("switch", "-c", name)
        self.commit_file(f"{name}.txt", name, f"feat: {name}")
        self.repo_git("switch", "main")
        self.repo_git("push", "-q", "origin", "--all")

    def worktree(self, name, branch):
        path = self.root / f"wt-{name}"
        self.repo_git("worktree", "add", str(path), branch)
        return path

    def branches(self):
        return set(
            self.repo_git("for-each-ref", "--format=%(refname:short)", "refs/heads")
                .split()
        )

    def worktree_paths(self):
        out = self.repo_git("worktree", "list", "--porcelain")
        return {
            os.path.realpath(line[len("worktree "):])
            for line in out.splitlines()
            if line.startswith("worktree ")
        }

    def origin_branches(self):
        out = self.run(
            "-C", str(self.origin), "for-each-ref",
            "--format=%(refname:short)", "refs/heads",
        )
        return set(out.split())

    def prune(self, *args):
        return subprocess.run(
            ["sh", str(HELPER), str(self.repo), *args],
            env=GIT_ENV, capture_output=True, text=True, timeout=120,
        )


class PruneMergedBranches(unittest.TestCase):
    """Behavior of scripts/prune-merged-branches.sh."""

    def setUp(self):
        _need_tools()
        if not HELPER.exists():
            self.fail(f"prune helper missing: {HELPER}")
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.fx = PruneFixture(tmp.name)

    def test_merged_branches_and_worktrees_are_pruned(self):
        self.fx.merged_branch("codex/merged-a")
        self.fx.merged_branch("codex/merged-b")
        self.fx.unmerged_branch("codex/open-work")
        wt_merged = self.fx.worktree("merged", "codex/merged-a")
        wt_open = self.fx.worktree("open", "codex/open-work")

        proc = self.fx.prune()
        self.assertEqual(
            proc.returncode, 0, f"prune failed:\n{proc.stdout}{proc.stderr}"
        )

        branches = self.fx.branches()
        self.assertNotIn("codex/merged-a", branches)
        self.assertNotIn("codex/merged-b", branches)
        self.assertIn("codex/open-work", branches)
        self.assertIn("main", branches)

        paths = self.fx.worktree_paths()
        self.assertNotIn(os.path.realpath(wt_merged), paths, "merged worktree survived")
        self.assertFalse(wt_merged.exists())
        self.assertIn(os.path.realpath(wt_open), paths, "open worktree was removed")
        self.assertTrue(wt_open.exists())

    def test_dirty_merged_worktree_is_skipped_not_discarded(self):
        self.fx.merged_branch("codex/dirty")
        wt = self.fx.worktree("dirty", "codex/dirty")
        (wt / "uncommitted.txt").write_text("precious", encoding="utf-8")

        proc = self.fx.prune()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("SKIP dirty worktree", proc.stderr)
        self.assertTrue(wt.exists(), "dirty worktree was discarded")
        self.assertTrue((wt / "uncommitted.txt").exists())
        # its branch stays occupied and is therefore never deleted
        self.assertIn("codex/dirty", self.fx.branches())

    def test_primary_checkout_and_current_branch_are_never_touched(self):
        self.fx.merged_branch("codex/merged-a")
        proc = self.fx.prune()
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("main", self.fx.branches())
        self.assertIn(os.path.realpath(self.fx.repo), self.fx.worktree_paths())
        self.assertIn("prune: removed", proc.stdout)

    def test_remote_mode_deletes_only_merged_remote_branches(self):
        self.fx.merged_branch("codex/merged-a")
        self.fx.unmerged_branch("codex/open-work")
        proc = self.fx.prune("--remote")
        self.assertEqual(
            proc.returncode, 0, f"remote prune failed:\n{proc.stdout}{proc.stderr}"
        )
        origin = self.fx.origin_branches()
        self.assertNotIn("codex/merged-a", origin)
        self.assertIn("codex/open-work", origin)
        self.assertIn("main", origin)

    def test_not_a_repository_fails_closed(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        proc = subprocess.run(
            ["sh", str(HELPER), tmp.name],
            env=GIT_ENV, capture_output=True, text=True, timeout=30,
        )
        self.assertNotEqual(proc.returncode, 0, "prune passed on a non-repo")
        self.assertIn("not a Git repository", proc.stderr)


if __name__ == "__main__":
    print(
        "run-at", datetime.datetime.now(datetime.timezone.utc).isoformat(),
        file=sys.stderr,
    )
    unittest.main(verbosity=2)
