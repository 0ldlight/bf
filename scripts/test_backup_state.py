# Permanent tests for the private-state backup helper.
# 针对私有状态备份脚本的永久测试。
#
# Every case runs the real shell helper against an owned temporary state
# directory through BINFLOW_STATE_DIR; nothing reads or writes the real
# .binflow state.
import datetime
import hashlib
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest

HELPER = pathlib.Path(__file__).resolve().parent / "backup-local-state.sh"


def _need_tools():
    # No skips: missing tools must fail the tests actionably.
    # 不跳过：缺少工具必须让测试可操作地失败。
    missing = [
        tool
        for tool in ("sh", "tar")
        if subprocess.run(
            ["sh", "-c", f"command -v {tool} >/dev/null 2>&1"],
        ).returncode
        != 0
    ]
    if missing:
        raise AssertionError(
            f"required tools missing from PATH: {', '.join(missing)} "
            "(scripts/backup-local-state.sh needs a POSIX shell and tar)"
        )


class BackupFixture:
    """A disposable state directory plus a runner for the real helper."""

    def __init__(self, root):
        self.root = pathlib.Path(root)
        self.state = self.root / "state"
        self.dest = self.root / "dest"

    def write(self, rel, text):
        path = self.state / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def run(self, *args):
        env = dict()
        env.update(__import__("os").environ)
        env["BINFLOW_STATE_DIR"] = str(self.state)
        return subprocess.run(
            ["sh", str(HELPER), *args],
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
        )

    def archives(self):
        return sorted(self.dest.glob("binflow-state-*.tar.gz"))


class BackupLocalState(unittest.TestCase):
    """Behavior of scripts/backup-local-state.sh."""

    def setUp(self):
        _need_tools()
        if not HELPER.exists():
            self.fail(f"backup helper missing: {HELPER}")
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.fx = BackupFixture(tmp.name)

    def test_backup_contains_state_and_sidecar_matches(self):
        self.fx.write("worker-slots.json", '{"schemaVersion": 1}')
        self.fx.write("tasks/BIN-1/contract.md", "contract")
        proc = self.fx.run(str(self.fx.dest))
        self.assertEqual(
            proc.returncode, 0, f"backup failed:\n{proc.stdout}{proc.stderr}"
        )
        archives = self.fx.archives()
        self.assertEqual(len(archives), 1, proc.stdout)
        archive = archives[0]

        sidecar = pathlib.Path(str(archive) + ".sha256")
        self.assertTrue(sidecar.exists(), "sha256 sidecar missing")
        recorded = sidecar.read_text(encoding="utf-8").split()[0]
        actual = hashlib.sha256(archive.read_bytes()).hexdigest()
        self.assertEqual(recorded, actual, "sidecar digest does not match archive")

        names = {m.name for m in tarfile.open(archive)}
        base = self.fx.state.name
        self.assertIn(f"{base}/worker-slots.json", names)
        self.assertIn(f"{base}/tasks/BIN-1/contract.md", names)

    def test_worktrees_caches_and_locks_are_excluded(self):
        self.fx.write("worker-slots.json", "{}")
        self.fx.write("worktrees/bf-old/cmd/main.go", "package main")
        self.fx.write("__pycache__/junk.pyc", "junk")
        self.fx.write("worker-slots.lock", "0")
        # Lock DIRECTORIES (checkout-lock state) are excluded as a whole,
        # not only *.lock files (reviewer-verified 2026-11-13 gap).
        # 锁目录(checkout-lock 状态)整体排除,而不仅是 *.lock 文件。
        self.fx.write("locks/checkout/owner", "label=x\npid=1\n")
        proc = self.fx.run(str(self.fx.dest))
        self.assertEqual(proc.returncode, 0, proc.stderr)
        names = {m.name for m in tarfile.open(self.fx.archives()[0])}
        self.assertFalse(
            any("worktrees" in n or "main.go" in n for n in names),
            f"worktree content leaked into backup: {sorted(names)}",
        )
        self.assertFalse(
            any("__pycache__" in n or "locks" in n or n.endswith(".lock") for n in names),
            f"cache, lock directory or lock file leaked into backup: {sorted(names)}",
        )

    def test_missing_state_directory_fails_closed(self):
        # The fixture never creates the state dir contents' parent as state.
        proc = self.fx.run(str(self.fx.dest))
        self.assertNotEqual(proc.returncode, 0, "backup passed without state dir")
        self.assertIn("not found", proc.stderr)
        self.assertEqual(self.fx.archives(), [])

    def test_repeated_backups_are_distinct_files(self):
        self.fx.write("worker-slots.json", "{}")
        first = self.fx.run(str(self.fx.dest))
        second = self.fx.run(str(self.fx.dest))
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(len(self.fx.archives()), 2, "backups overwrote each other")

    def test_usage_errors_fail_closed(self):
        for bad_args in ((), ("",)):
            proc = self.fx.run(*bad_args)
            self.assertEqual(proc.returncode, 2, f"args {bad_args!r}: {proc.stderr}")


if __name__ == "__main__":
    print(
        "run-at", datetime.datetime.now(datetime.timezone.utc).isoformat(),
        file=sys.stderr,
    )
    unittest.main(verbosity=2)
