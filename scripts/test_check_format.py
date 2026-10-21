# BIN-133: permanent tests for the Git-scoped gofmt gate.
# BIN-133：针对“基于 Git 发现范围”的 gofmt 门禁的永久测试。
#
# Every fixture lives in an owned TemporaryDirectory with its own Git
# repository and real gofmt subprocesses; nothing writes into the real
# source tree and no global Git configuration is consulted.
import datetime
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

HELPER = pathlib.Path(__file__).resolve().parent / "check-format.py"

FORMATTED = "package main\n\nfunc main() {}\n"
UNFORMATTED = "package main\n\nfunc  main() {}\n"

GIT_ENV = dict(os.environ)
for _key in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY"):
    GIT_ENV.pop(_key, None)
GIT_ENV["GIT_CONFIG_GLOBAL"] = os.devnull
GIT_ENV["GIT_CONFIG_NOSYSTEM"] = "1"


def _need_tools():
    for tool in ("git", "gofmt"):
        if subprocess.run(
            ["sh", "-c", f"command -v {tool} >/dev/null 2>&1"],
            env=GIT_ENV,
        ).returncode != 0:
            raise unittest.SkipTest(f"{tool} not available")


class Fixture:
    """A disposable Git repository with a working tree and ignore rules."""

    def __init__(self, root):
        self.root = pathlib.Path(root)
        self.run("init", "--quiet")

    def run(self, *args):
        proc = subprocess.run(
            ["git", "-C", str(self.root), *args],
            env=GIT_ENV,
            capture_output=True,
            text=True,
        )
        if proc.returncode != 0:
            raise AssertionError(
                f"fixture git {args!r} failed ({proc.returncode}): {proc.stderr}"
            )
        return proc.stdout

    def write(self, rel, text):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def add(self, rel):
        self.run("add", "--", rel)

    def ignore(self, pattern):
        with open(self.root / ".gitignore", "a", encoding="utf-8") as fh:
            fh.write(pattern + "\n")
        self.run("add", "--", ".gitignore")

    def gate(self, cwd=None):
        return subprocess.run(
            [sys.executable, str(HELPER), str(self.root)],
            cwd=str(cwd or self.root),
            env=GIT_ENV,
            capture_output=True,
            text=True,
        )


class OldRecursiveGofmtBehavior(unittest.TestCase):
    """Permanent reproduction of the pre-fix shared-script format step.

    `gofmt -l .` walks every directory, including Git-ignored local
    archive/research worktrees, so ignored unformatted Go used to fail (or
    get scanned by) the shared gate. These assertions document that defect.
    """

    def setUp(self):
        _need_tools()
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.fx = Fixture(self._tmp.name)
        self.fx.write("cmd/app/main.go", FORMATTED)
        self.fx.add("cmd/app/main.go")
        self.fx.ignore("/archive-private/")
        self.fx.write("archive-private/old.go", UNFORMATTED)

    def test_recursive_gofmt_scans_ignored_archive(self):
        proc = subprocess.run(
            ["gofmt", "-l", "."],
            cwd=str(self.fx.root),
            env=GIT_ENV,
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn(
            "archive-private/old.go",
            proc.stdout,
            "pre-fix gate no longer scans ignored trees; update this "
            "reproduction alongside scripts/check-format.py",
        )


class GitScopedGofmtGate(unittest.TestCase):
    """Behavior of scripts/check-format.py over Git-discovered sources."""

    def setUp(self):
        _need_tools()
        if not HELPER.exists():
            self.fail(f"gate helper missing: {HELPER}")

    def _fixture(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        return Fixture(tmp.name)

    def test_clean_source_passes(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", FORMATTED)
        fx.add("cmd/app/main.go")
        proc = fx.gate()
        self.assertEqual(
            proc.returncode, 0, f"clean gate failed:\n{proc.stdout}{proc.stderr}"
        )

    def test_tracked_unformatted_source_fails(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", UNFORMATTED)
        fx.add("cmd/app/main.go")
        proc = fx.gate()
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("need formatting", proc.stderr)
        self.assertIn("cmd/app/main.go", proc.stderr)

    def test_untracked_nonignored_unformatted_go_fails(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", FORMATTED)
        fx.add("cmd/app/main.go")
        fx.write("internal/new/slice.go", UNFORMATTED)  # never git-added
        proc = fx.gate()
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("internal/new/slice.go", proc.stderr)

    def test_private_ignored_unformatted_archive_is_excluded(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", FORMATTED)
        fx.add("cmd/app/main.go")
        fx.ignore("/archive-private/")
        fx.write("archive-private/old.go", UNFORMATTED)
        proc = fx.gate()
        self.assertEqual(
            proc.returncode, 0, f"ignored archive leaked into gate:\n{proc.stderr}"
        )
        self.assertNotIn("archive-private", proc.stderr)

    def test_tracked_file_matched_by_new_ignore_rule_still_fails(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", UNFORMATTED)
        fx.add("cmd/app/main.go")
        fx.ignore("cmd/app/main.go")  # ignore rule added after tracking
        proc = fx.gate()
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("cmd/app/main.go", proc.stderr)

    def test_nested_integration_module_is_included(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", FORMATTED)
        fx.add("cmd/app/main.go")
        fx.write("integration/postgres/go.mod", "module example.test/integration\n")
        fx.add("integration/postgres/go.mod")
        fx.write("integration/postgres/store_test.go", UNFORMATTED)
        fx.add("integration/postgres/store_test.go")
        proc = fx.gate()
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("integration/postgres/store_test.go", proc.stderr)

    def test_filename_with_space_is_safe(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", FORMATTED)
        fx.add("cmd/app/main.go")
        fx.write("cmd/app/bad name.go", UNFORMATTED)
        fx.add("cmd/app/bad name.go")
        proc = fx.gate()
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("cmd/app/bad name.go", proc.stderr)

    def test_staged_deletion_missing_from_worktree_is_skipped(self):
        fx = self._fixture()
        gone = fx.write("cmd/app/gone.go", FORMATTED)
        fx.add("cmd/app/gone.go")
        gone.unlink()  # staged, then removed from the working tree
        proc = fx.gate()
        self.assertEqual(
            proc.returncode, 0, f"staged deletion broke gate:\n{proc.stdout}{proc.stderr}"
        )
        self.assertNotIn("gone.go", proc.stderr)

    def test_git_failure_fails_closed(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        not_a_repo = pathlib.Path(tmp.name)
        (not_a_repo / "main.go").write_text(UNFORMATTED, encoding="utf-8")
        proc = subprocess.run(
            [sys.executable, str(HELPER), str(not_a_repo)],
            cwd=str(not_a_repo),
            env=GIT_ENV,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(proc.returncode, 0, "gate passed without a Git repo")
        self.assertIn("git", proc.stderr.lower())

    def test_caller_cwd_does_not_change_the_gate(self):
        fx = self._fixture()
        fx.write("cmd/app/main.go", UNFORMATTED)
        fx.add("cmd/app/main.go")
        elsewhere = tempfile.TemporaryDirectory()
        self.addCleanup(elsewhere.cleanup)
        proc = fx.gate(cwd=pathlib.Path(elsewhere.name))
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("cmd/app/main.go", proc.stderr)


if __name__ == "__main__":
    print(
        "run-at", datetime.datetime.now(datetime.timezone.utc).isoformat(),
        file=sys.stderr,
    )
    unittest.main(verbosity=2)
