#!/usr/bin/env python3
"""BIN-125 UAT artifact upload core / 制品上传核心.

One fixed CLI action, driven by one bounded JSON request line, so no
caller-controlled path, URL or command ever reaches this process:

  upload    forced SSH command for the dedicated Jenkins upload user; reads
            the request line then a raw tar.gz until EOF into
            /var/lib/binflow/staging under a computed name, hashing while
            streaming (32MiB per artifact, 256MiB/16 artifacts staging).

Activation and recovery arrive in the stacked follow-up change; this file
contains only upload functionality and its dependencies, with a CLI that
accepts upload only / 仅含上传功能与依赖,CLI 只接受 upload.

Internal functions accept paths so tests can use temporary directories.
"""
import fcntl
import hashlib
import json
import os
import re
import stat
import sys
import time

STAGING_DIR = "/var/lib/binflow/staging"

MAX_REQUEST_BYTES = 1024
MAX_ARCHIVE_BYTES = 32 * 1024 * 1024      # compressed artifact at upload
MAX_STAGING_BYTES = 256 * 1024 * 1024     # total staging directory
MAX_STAGING_ARTIFACTS = 16
# Finite entry cap: each interrupted upload strands at most one .incoming
# orphan (older orphans are never auto-deleted), so bound the backlog.
MAX_STAGING_ENTRIES = 64

# All patterns are matched with .fullmatch: a $ anchor would silently accept
# a trailing newline coming from JSON string values.
COMMIT_RE = re.compile(r"[0-9a-f]{40}")
SHA_RE = re.compile(r"[0-9a-f]{64}")
ARTIFACT_FMT = "binflow-%s.tar.gz"
ARTIFACT_RE = re.compile(r"binflow-[0-9a-f]{40}\.tar\.gz")
USAGE = "usage: uat-release.py {upload}"


class ReleaseError(Exception):
    """Concise, safe-to-echo protocol failure."""


def read_request(stream):
    """One JSON line (<=1024 bytes): exactly commit(40 hex) + sha256(64 hex)."""
    line = stream.readline(MAX_REQUEST_BYTES + 1)
    if not line:
        raise ReleaseError("empty request")
    if len(line) > MAX_REQUEST_BYTES:
        raise ReleaseError("request line exceeds 1024 bytes")
    if not line.endswith(b"\n"):
        raise ReleaseError("request line must end with a newline")
    try:
        req = json.loads(line.decode("utf-8"), object_pairs_hook=_no_dup_keys)
    except (ValueError, UnicodeDecodeError):
        raise ReleaseError("request is not valid json")
    if not isinstance(req, dict) or set(req) != {"commit", "sha256"}:
        raise ReleaseError("request must contain exactly commit and sha256")
    commit, sha = req["commit"], req["sha256"]
    if not isinstance(commit, str) or not COMMIT_RE.fullmatch(commit):
        raise ReleaseError("commit must be 40 lowercase hex characters")
    if not isinstance(sha, str) or not SHA_RE.fullmatch(sha):
        raise ReleaseError("sha256 must be 64 lowercase hex characters")
    return commit, sha


def _no_dup_keys(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError("duplicate key")
        out[key] = value
    return out


class DirLock:
    """Nonblocking exclusive flock on a directory fd; no lock files to clean.

    Also rejects symlinked directories via O_NOFOLLOW at open time.
    """

    def __init__(self, path, label):
        try:
            fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        except OSError:
            raise ReleaseError("%s unavailable" % label)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            os.close(fd)
            raise ReleaseError("%s is busy" % label)
        self.fd = fd

    def close(self):
        if self.fd is not None:
            try:
                fcntl.flock(self.fd, fcntl.LOCK_UN)
            finally:
                os.close(self.fd)
            self.fd = None


def upload_action(staging, stdin, stdout):
    """Stream one artifact into staging under quota, idempotently."""
    commit, sha = read_request(stdin)
    lock = DirLock(staging, "staging")
    final = os.path.join(staging, ARTIFACT_FMT % commit)
    tmp = os.path.join(staging, ".incoming.%d.%d" % (os.getpid(), time.time_ns()))
    stored = False
    try:
        try:
            st = os.lstat(final)
        except FileNotFoundError:
            st = None
        if st is not None:
            if not stat.S_ISREG(st.st_mode):
                raise ReleaseError("existing artifact is not a regular file")
            # Retry of a completed artifact: verify by bounded streaming
            # hash, storing nothing, so retries still work at a full quota.
            digest = _stream_digest(stdin)
            if digest != sha:
                raise ReleaseError("sha256 mismatch")
            if _file_digest(final) != sha:
                raise ReleaseError("commit already staged with a different artifact")
        else:
            # Refuse before allocating anything when staging is already full;
            # every regular file counts, interrupted .incoming orphans
            # included. Earlier invocations' orphans are never auto-deleted.
            used, entries = _scan_staging(staging)
            if entries + 1 > MAX_STAGING_ENTRIES:
                raise ReleaseError("staging entry count quota exceeded")
            if used >= MAX_STAGING_BYTES:
                raise ReleaseError("staging size quota exceeded")
            _check_quota(staging)
            digest = _stream_to(stdin, tmp, used)
            if digest != sha:
                raise ReleaseError("sha256 mismatch")
            os.rename(tmp, final)
            _fsync_fd(lock.fd)
            stored = True
        _emit(stdout, {"status": "ok", "commit": commit, "stored": stored})
        return 0
    finally:
        _unlink(tmp)  # only the temporary object this invocation created
        lock.close()


def _stream_to(stdin, tmp, baseline):
    """Bounded write to tmp while hashing; returns the streamed digest.

    The staging-wide byte budget (existing regular bytes plus this temp) is
    enforced chunk by chunk under the caller's directory lock.
    """
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o640)
    hasher = hashlib.sha256()
    total = 0
    try:
        os.fchmod(fd, 0o640)
        while True:
            chunk = stdin.read(1 << 20)
            if not chunk:
                break
            total += len(chunk)
            if total > MAX_ARCHIVE_BYTES:
                raise ReleaseError("artifact exceeds 32MiB compressed size")
            if baseline + total > MAX_STAGING_BYTES:
                raise ReleaseError("staging size quota exceeded")
            hasher.update(chunk)
            view = memoryview(chunk)
            while view:
                view = view[os.write(fd, view):]
        os.fsync(fd)
    finally:
        os.close(fd)
    return hasher.hexdigest()


def _stream_digest(stdin):
    """Hash a bounded retry payload without storing anything."""
    hasher = hashlib.sha256()
    total = 0
    while True:
        chunk = stdin.read(1 << 20)
        if not chunk:
            break
        total += len(chunk)
        if total > MAX_ARCHIVE_BYTES:
            raise ReleaseError("artifact exceeds 32MiB compressed size")
        hasher.update(chunk)
    return hasher.hexdigest()


def _scan_staging(staging):
    """Sum bytes of every regular staging file and count entries.

    Interrupted .incoming orphans count toward the byte budget; unexpected
    non-regular entries are rejected instead of followed or skipped.
    """
    used = 0
    entries = 0
    with os.scandir(staging) as items:
        for entry in items:
            try:
                st = entry.stat(follow_symlinks=False)
            except FileNotFoundError:
                continue
            if not stat.S_ISREG(st.st_mode):
                raise ReleaseError("staging contains an unexpected non-regular entry")
            used += st.st_size
            entries += 1
    return used, entries


def _check_quota(staging):
    """Completed-artifact count cap; bytes are enforced while streaming."""
    completed = 0
    with os.scandir(staging) as items:
        for entry in items:
            if not ARTIFACT_RE.fullmatch(entry.name):
                continue
            try:
                st = entry.stat(follow_symlinks=False)
            except FileNotFoundError:
                continue
            if stat.S_ISREG(st.st_mode):
                completed += 1
    if completed + 1 > MAX_STAGING_ARTIFACTS:
        raise ReleaseError("staging artifact count quota exceeded")


def _file_digest(path):
    return hashlib.sha256(_read_bounded(path, MAX_ARCHIVE_BYTES, "staged artifact")).hexdigest()


def _read_bounded(path, limit, label, bound_desc="size bound"):
    # O_NONBLOCK so an attacker-planted FIFO cannot block the open; the
    # regular-file requirement below is enforced before any read happens.
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        raise ReleaseError("%s unavailable" % label)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ReleaseError("%s is not a regular file" % label)
        buf = bytearray()
        while len(buf) <= limit:
            chunk = os.read(fd, 1 << 20)
            if not chunk:
                break
            buf += chunk
        if len(buf) > limit:
            raise ReleaseError("%s exceeds the %s" % (label, bound_desc))
        return bytes(buf)
    finally:
        os.close(fd)


def _emit(out, obj):
    out.write((json.dumps(obj, sort_keys=True) + "\n").encode("utf-8"))
    out.flush()


def _unlink(path):
    try:
        os.unlink(path)
    except FileNotFoundError:
        pass


def _fsync_fd(fd):
    try:
        os.fsync(fd)
    except OSError:
        pass  # directory fsync is best-effort across platforms


def main(argv, stdin, stdout):
    if len(argv) != 2 or argv[1] != "upload":
        _emit(stdout, {"status": "error", "error": USAGE})
        return 2
    try:
        return upload_action(STAGING_DIR, stdin, stdout)
    except ReleaseError as exc:
        _emit(stdout, {"status": "error", "error": str(exc)})
        return 1
    except Exception:
        _emit(stdout, {"status": "error", "error": "internal error"})
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv, sys.stdin.buffer, sys.stdout.buffer))
