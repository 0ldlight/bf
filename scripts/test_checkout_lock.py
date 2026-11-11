# Permanent tests for the single-writer checkout lock.
# 针对检出单写者锁的永久测试。
#
# Every case drives the real shell helper against an owned temporary lock
# directory through CHECKOUT_LOCK_DIR, so nothing touches the working
# checkout's .binflow state. No skips: a missing sh fails actionably.
import datetime
import os
import pathlib
import subprocess
import sys
import tempfile
import time
import unittest

HELPER = pathlib.Path(__file__).resolve().parent / "checkout-lock.sh"


def _need_tools():
    # No skips: missing tools must fail the gate tests actionably.
    # 不跳过：缺少工具必须让测试可操作地失败。
    missing = [
        tool
        for tool in ("sh", "sleep", "mkdir")
        if subprocess.run(
            ["sh", "-c", f"command -v {tool} >/dev/null 2>&1"],
        ).returncode
        != 0
    ]
    if missing:
        raise AssertionError(
            f"required tools missing from PATH: {', '.join(missing)} "
            "(scripts/checkout-lock.sh and its tests need a POSIX shell)"
        )


class LockFixture:
    """A disposable lock directory plus a runner for the real helper."""

    def __init__(self, root):
        self.root = pathlib.Path(root)
        self.lock_dir = self.root / "locks" / "checkout"
        self.lock_dir.parent.mkdir(parents=True, exist_ok=True)

    def run(self, *args, timeout=30):
        env = dict(os.environ)
        env["CHECKOUT_LOCK_DIR"] = str(self.lock_dir)
        return subprocess.run(
            ["sh", str(HELPER), *args],
            env=env,
            capture_output=True,
            text=True,
            timeout=timeout,
        )

    def spawn(self, *args):
        env = dict(os.environ)
        env["CHECKOUT_LOCK_DIR"] = str(self.lock_dir)
        return subprocess.Popen(
            ["sh", str(HELPER), *args],
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )

    def write_owner(self, label, pid):
        self.lock_dir.mkdir(parents=True, exist_ok=True)
        (self.lock_dir / ".checkout-lock").write_text("", encoding="utf-8")
        (self.lock_dir / "owner").write_text(
            f"label={label}\npid={pid}\n", encoding="utf-8"
        )


def _surely_dead_pid():
    # A syntactically valid but unreachable PID on every supported host
    # (macOS PIDs stay below 1e6; Linux below 2^22).
    # 在所有受支持主机上语法有效但必然不存在的 PID。
    return 999999999


class CheckoutLock(unittest.TestCase):
    """Behavior of scripts/checkout-lock.sh."""

    def setUp(self):
        _need_tools()
        if not HELPER.exists():
            self.fail(f"lock helper missing: {HELPER}")
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.fx = LockFixture(tmp.name)

    def _hold_in_background(self, label="holder", seconds="30"):
        proc = self.fx.spawn("with", label, "--", "sleep", seconds)
        self.addCleanup(proc.kill)
        self.addCleanup(proc.wait)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if self.lock_dir_has_owner():
                return proc
            if proc.poll() is not None:
                self.fail(f"background holder exited early: {proc.returncode}")
            time.sleep(0.02)
        self.fail("background holder never acquired the lock")

    def lock_dir_has_owner(self):
        return (self.fx.lock_dir / "owner").exists()

    def test_with_runs_command_and_releases(self):
        proc = self.fx.run("with", "builder", "--", "printf", "ok")
        self.assertEqual(
            proc.returncode, 0, f"holder run failed:\n{proc.stdout}{proc.stderr}"
        )
        self.assertEqual(proc.stdout, "ok")
        self.assertFalse(
            self.lock_dir_has_owner(), "lock was not released after the command"
        )

    def test_command_failure_propagates_and_releases(self):
        proc = self.fx.run("with", "builder", "--", "sh", "-c", "exit 7")
        self.assertEqual(proc.returncode, 7, proc.stderr)
        self.assertFalse(
            self.lock_dir_has_owner(), "lock leaked after a failing command"
        )

    def test_second_writer_is_rejected_while_held(self):
        holder = self._hold_in_background("primary-writer")
        proc = self.fx.run("with", "second-writer", "--", "true")
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("busy", proc.stderr)
        self.assertIn("primary-writer", proc.stderr)
        holder.kill()
        holder.wait()

    def test_stale_lock_is_reclaimed(self):
        self.fx.write_owner("crashed", _surely_dead_pid())
        proc = self.fx.run("with", "reclaimer", "--", "true")
        self.assertEqual(
            proc.returncode, 0, f"stale lock blocked a new writer:\n{proc.stderr}"
        )
        self.assertFalse(self.lock_dir_has_owner())

    def test_status_reports_free_busy_and_stale(self):
        free = self.fx.run("status")
        self.assertEqual(free.returncode, 0, free.stderr)
        self.assertIn("free", free.stdout)

        holder = self._hold_in_background("status-holder")
        busy = self.fx.run("status")
        self.assertEqual(busy.returncode, 1, busy.stdout)
        self.assertIn("busy", busy.stdout)
        self.assertIn("status-holder", busy.stdout)
        holder.kill()
        holder.wait()

        self.fx.write_owner("ancient", _surely_dead_pid())
        stale = self.fx.run("status")
        self.assertEqual(stale.returncode, 1, stale.stdout)
        self.assertIn("stale", stale.stdout)

    def test_release_removes_only_our_own_lock(self):
        self.fx.write_owner("explicit", _surely_dead_pid())
        proc = self.fx.run("release")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertFalse(self.fx.lock_dir.exists())

        # A directory without the marker file must never be removed.
        # 未打标的目录绝不能被删除。
        foreign = self.fx.lock_dir
        foreign.mkdir(parents=True, exist_ok=True)
        (foreign / "precious.txt").write_text("keep me", encoding="utf-8")
        proc = self.fx.run("release")
        self.assertEqual(proc.returncode, 1, proc.stdout)
        self.assertIn("refusing", proc.stderr)
        self.assertTrue((foreign / "precious.txt").exists())

    def test_usage_errors_fail_closed(self):
        for bad_args in ((), ("with", "label"), ("with", "label", "cmd"),
                         ("bogus",)):
            proc = self.fx.run(*bad_args)
            self.assertEqual(
                proc.returncode, 2, f"args {bad_args!r} must fail usage: {proc.stdout}"
            )
        # A refused invocation must not leave a lock behind.
        self.assertFalse(self.fx.lock_dir.exists())


if __name__ == "__main__":
    print(
        "run-at", datetime.datetime.now(datetime.timezone.utc).isoformat(),
        file=sys.stderr,
    )
    unittest.main(verbosity=2)
