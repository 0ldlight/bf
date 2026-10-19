#!/usr/bin/env python3
"""BIN-125 UAT transfer and activation core / 制品上传与切换核心.

Two fixed CLI actions, both driven by one bounded JSON request line, so no
caller-controlled path, URL or command ever reaches this process:

  upload    forced SSH command for the dedicated Jenkins upload user; reads
            the request line then a raw tar.gz until EOF into
            /var/lib/binflow/staging under a computed name, hashing while
            streaming (32MiB per artifact, 256MiB/16 artifacts staging).
  activate  fixed root-owned sudo command for CircleCI; reads only the
            request line and requires EOF, then validates the staged
            artifact, installs an immutable release under /opt/binflow and
            atomically switches the relative `current` symlink with a fixed
            systemctl restart plus bounded loopback health verification and
            rollback. The released binary is never executed here; it is
            installed root-owned and public-executable (0755) so the non-root
            systemd runtime user can execute but never write it / 二进制绝不以
            root 运行，运行用户只读可执行.

Internal functions accept paths and a service adapter so tests can use
temporary directories and a fake service.
"""
import datetime
import fcntl
import gzip
import hashlib
import io
import json
import os
import re
import stat
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.request
import zlib

STAGING_DIR = "/var/lib/binflow/staging"
PROD_ROOT = "/opt/binflow"
SERVICE = "binflow-uat.service"
HEALTH_URL = "http://127.0.0.1:18080/healthz"

MAX_REQUEST_BYTES = 1024
MAX_ARCHIVE_BYTES = 32 * 1024 * 1024      # compressed artifact at upload
MAX_STAGING_BYTES = 256 * 1024 * 1024     # total staging directory
MAX_STAGING_ARTIFACTS = 16
# Finite entry cap: each interrupted upload strands at most one .incoming
# orphan (older orphans are never auto-deleted), so bound the backlog.
MAX_STAGING_ENTRIES = 64
MAX_UNPACKED_BYTES = 64 * 1024 * 1024     # compressed and uncompressed bound

RESTART_TIMEOUT = 30.0
HEALTH_TIMEOUT = 5.0
HEALTH_DEADLINE = 30.0
HEALTH_BODY = b"ok\n"

# All patterns are matched with .fullmatch: a $ anchor would silently accept
# a trailing newline coming from JSON string values or symlink targets.
COMMIT_RE = re.compile(r"[0-9a-f]{40}")
SHA_RE = re.compile(r"[0-9a-f]{64}")
ARTIFACT_FMT = "binflow-%s.tar.gz"
ARTIFACT_RE = re.compile(r"binflow-[0-9a-f]{40}\.tar\.gz")
CURRENT_RE = re.compile(r"releases/([0-9a-f]{40})")
# Immutable release layout: activating-identity-owned, binary public-executable
# (0755) so the non-root runtime user can execute but never write anything.
RELEASE_FILE_MODES = {"binflow": 0o755, "manifest.txt": 0o640, "RELEASE.json": 0o640}
MANIFEST_KEYS = ("module", "commit", "go", "platform", "cgo", "sha256")
MODULE = "github.com/0ldlight/bf"
ELF_MAGIC = bytes((0x7F, 0x45, 0x4C, 0x46))
USAGE = "usage: uat-release.py {upload|activate}"


class ReleaseError(Exception):
    """Concise, safe-to-echo protocol failure."""


class ServiceError(Exception):
    """Service adapter failure (restart/stop/health transport)."""


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
            digest = _stream_to(stdin, tmp, used)
            if digest != sha:
                raise ReleaseError("sha256 mismatch")
            _check_quota(staging)
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
    for entry in os.scandir(staging):
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
    for entry in os.scandir(staging):
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


def snapshot_staged(staging, commit):
    """Copy the staged artifact into memory under a nonblocking staging lock.

    Lock plus O_NOFOLLOW close the mutable-upload TOCTOU window: the bytes
    hashed and parsed are exactly the bytes snapshotted.
    """
    lock = DirLock(staging, "staging")
    try:
        return _read_bounded(os.path.join(staging, ARTIFACT_FMT % commit),
                             MAX_ARCHIVE_BYTES, "staged artifact",
                             "32MiB compressed size bound")
    finally:
        lock.close()


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


def validate_archive(data, commit):
    """Full artifact validation; returns (binary, manifest_bytes, fields)."""
    if len(data) > MAX_ARCHIVE_BYTES:
        raise ReleaseError("archive exceeds the 32MiB compressed size bound")
    tar_bytes = _gunzip_bounded(data)
    binary, man_bytes = _tar_members(tar_bytes)
    man = _parse_manifest(man_bytes)
    _check_manifest_fields(man, commit)
    if hashlib.sha256(binary).hexdigest() != man["sha256"]:
        raise ReleaseError("manifest sha256 does not match the binary")
    _check_elf(binary)
    return binary, man_bytes, man


def _check_manifest_fields(man, commit):
    """Field-level manifest contract shared by staging and release checks."""
    if man["module"] != MODULE:
        raise ReleaseError("manifest module mismatch")
    if man["commit"] != commit:
        raise ReleaseError("manifest commit mismatch")
    if man["platform"] != "linux/amd64":
        raise ReleaseError("manifest platform mismatch")
    if man["cgo"] != "disabled":
        raise ReleaseError("manifest cgo mismatch")
    if not man["go"] or len(man["go"]) > 64 or any(c.isspace() for c in man["go"]):
        raise ReleaseError("manifest go toolchain value invalid")
    if not SHA_RE.fullmatch(man["sha256"]):
        raise ReleaseError("manifest sha256 field invalid")


def _gunzip_bounded(data):
    """Decompress with a hard bound so metadata bombs cannot exhaust memory."""
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(data)) as gz:
            out = gz.read(MAX_UNPACKED_BYTES + 1)
    except (OSError, EOFError, zlib.error):
        raise ReleaseError("artifact is not valid gzip")
    if len(out) > MAX_UNPACKED_BYTES:
        raise ReleaseError("archive exceeds the uncompressed size bound")
    return out


def _tar_members(tar_bytes):
    try:
        with tarfile.open(fileobj=io.BytesIO(tar_bytes), mode="r:") as tf:
            members = tf.getmembers()
            if len(members) != 2 or sorted(m.name for m in members) != ["binflow", "manifest.txt"]:
                raise ReleaseError("archive must contain exactly binflow and manifest.txt")
            out = {}
            for member in members:
                if not member.isfile():
                    raise ReleaseError("archive member %r is not a regular file" % member.name)
                out[member.name] = tf.extractfile(member).read()
            return out["binflow"], out["manifest.txt"]
    except tarfile.TarError:
        raise ReleaseError("artifact is not a valid tar archive")


def _parse_manifest(raw):
    try:
        text = raw.decode("ascii")
    except UnicodeDecodeError:
        raise ReleaseError("manifest is not strict ascii text")
    if "\r" in text or not text.endswith("\n"):
        raise ReleaseError("manifest line format invalid")
    fields = {}
    for line in text.split("\n")[:-1]:
        match = re.fullmatch(r"([a-z][a-z0-9]*): (.+)", line)
        if not match:
            raise ReleaseError("manifest line format invalid")
        key, value = match.group(1), match.group(2)
        if key not in MANIFEST_KEYS:
            raise ReleaseError("unknown manifest field %r" % key)
        if key in fields:
            raise ReleaseError("duplicate manifest field %r" % key)
        if any(ord(c) < 32 or ord(c) > 126 for c in value):
            raise ReleaseError("manifest value is not strict text")
        fields[key] = value
    if set(fields) != set(MANIFEST_KEYS):
        raise ReleaseError("manifest fields missing")
    return fields


def _check_elf(binary):
    if (len(binary) < 20 or binary[:4] != ELF_MAGIC
            or binary[4] != 2 or binary[5] != 1 or binary[6] != 1
            or int.from_bytes(binary[18:20], "little") != 0x3E):
        raise ReleaseError("binary is not ELF64 little-endian amd64")


class SystemdService:
    """Production adapter: fixed systemctl + loopback health GET.

    No shell, no caller input; proxying is disabled and redirects rejected
    so the health check cannot be steered elsewhere.
    """

    def restart(self):
        self._systemctl("restart")

    def stop(self):
        self._systemctl("stop")

    def _systemctl(self, op):
        try:
            subprocess.run(["/usr/bin/systemctl", op, SERVICE],
                           check=True, timeout=RESTART_TIMEOUT)
        except (subprocess.SubprocessError, OSError):
            raise ServiceError("service %s failed" % op)

    def health(self):
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect)
        try:
            with opener.open(HEALTH_URL, timeout=HEALTH_TIMEOUT) as resp:
                return resp.status, resp.read(64)
        except urllib.error.HTTPError as exc:
            return exc.code, b""
        except OSError:
            raise ServiceError("health request failed")


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None  # any redirect is a failure


def wait_healthy(service, deadline=HEALTH_DEADLINE):
    """Bounded poll for 200 plus exact b'ok\\n'; raises ReleaseError else."""
    end = time.monotonic() + deadline
    while True:
        try:
            code, body = service.health()
            if code == 200 and body == HEALTH_BODY:
                return
        except ServiceError:
            pass
        if time.monotonic() >= end:
            raise ReleaseError("health check failed")
        time.sleep(0.5)


def activate_action(prod_root, staging, service, stdin, stdout,
                    health_deadline=HEALTH_DEADLINE):
    """Validate staged artifact, install immutable release, switch current."""
    commit, sha = read_request(stdin)
    if stdin.read(1):
        raise ReleaseError("trailing input after request")
    lock = DirLock(prod_root, "production root")
    try:
        releases = _require_real_dir(os.path.join(prod_root, "releases"), "releases")
        data = snapshot_staged(staging, commit)
        digest = hashlib.sha256(data).hexdigest()
        if digest != sha:
            raise ReleaseError("staged artifact digest does not match request")
        binary, man_bytes, _man = validate_archive(data, commit)
        prev = _current_commit(prod_root, releases)
        _install_release(releases, commit, digest, binary, man_bytes)
        switched = prev != commit
        if switched:
            _set_current(prod_root, commit)
        try:
            service.restart()
            wait_healthy(service, health_deadline)
        except (ServiceError, ReleaseError) as exc:
            reason = str(exc)
            recovered, current = _restore(prod_root, service, prev, switched,
                                          health_deadline)
            _record(prod_root, {"status": "failed", "commit": commit,
                                "archive_sha256": digest, "current": current,
                                "recovered": recovered, "error": reason})
            _emit(stdout, {"status": "error", "error": reason,
                           "commit": commit, "recovered": recovered})
            return 1
        _record(prod_root, {"status": "ok", "commit": commit,
                            "archive_sha256": digest, "current": commit})
        _emit(stdout, {"status": "ok", "commit": commit, "current": commit})
        return 0
    finally:
        lock.close()


def _restore(prod_root, service, prev, switched, deadline):
    """On start/health failure restore the previous valid current.

    A first deploy has nothing to roll back to: stop the service and remove
    only the symlink this invocation created, never claiming recovery. The
    failed release directory is kept as evidence / 首次部署失败不谎报回滚成功.
    """
    if prev is None:
        try:
            service.stop()
        except ServiceError:
            pass
        if switched:
            _unlink(os.path.join(prod_root, "current"))
        return False, None
    if switched:
        _set_current(prod_root, prev)
    try:
        service.restart()
        wait_healthy(service, deadline)
        return True, prev
    except (ServiceError, ReleaseError):
        return False, prev


def _require_real_dir(path, label):
    try:
        st = os.lstat(path)
    except OSError:
        raise ReleaseError("%s unavailable" % label)
    if not stat.S_ISDIR(st.st_mode):
        raise ReleaseError("%s is not a real directory" % label)
    return path


def _current_commit(prod_root, releases):
    """Return the commit `current` points at, or None; reject foreign state."""
    cur = os.path.join(prod_root, "current")
    try:
        st = os.lstat(cur)
    except FileNotFoundError:
        return None
    if not stat.S_ISLNK(st.st_mode):
        raise ReleaseError("current exists but is not a symlink")
    match = CURRENT_RE.fullmatch(os.readlink(cur))
    if not match:
        raise ReleaseError("current points outside the managed releases tree")
    target = os.path.join(releases, match.group(1))
    # A previous release is rollback evidence only after full validation;
    # a mere directory is not evidence. This runs before any service action.
    try:
        _verify_release(target, match.group(1))
    except ReleaseError:
        raise ReleaseError("current target release is not a valid release")
    return match.group(1)


def _verify_release(rel, commit, expect_digest=None):
    """Fully validate an existing release before it is trusted.

    Exactly the three known entries, regular non-link files with immutable
    modes, owned by the activating identity (tests run as the creating user;
    production activation is root), a consistent RELEASE.json (commit plus
    archive digest), and a manifest whose sha256 matches the ELF64/amd64
    binary. Foreign or corrupt state is rejected, never repaired.
    """
    try:
        st = os.lstat(rel)
    except OSError:
        raise ReleaseError("release directory unreadable")
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        raise ReleaseError("existing release path is not a directory")
    if stat.S_IMODE(st.st_mode) != 0o755 or st.st_uid != os.geteuid():
        raise ReleaseError("existing release state is invalid")
    try:
        names = sorted(os.listdir(rel))
    except OSError:
        raise ReleaseError("existing release state is invalid")
    if names != sorted(RELEASE_FILE_MODES):
        raise ReleaseError("existing release state is invalid")
    for name, mode in RELEASE_FILE_MODES.items():
        st = os.lstat(os.path.join(rel, name))
        if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode):
            raise ReleaseError("existing release state is invalid")
        if stat.S_IMODE(st.st_mode) != mode or st.st_uid != os.geteuid():
            raise ReleaseError("existing release state is invalid")
    try:
        rec = json.loads(_read_bounded(os.path.join(rel, "RELEASE.json"),
                                       4096, "release record"))
        binary = _read_bounded(os.path.join(rel, "binflow"),
                               MAX_UNPACKED_BYTES, "release binary")
        man_bytes = _read_bounded(os.path.join(rel, "manifest.txt"),
                                  65536, "release manifest")
    except (ReleaseError, ValueError):
        raise ReleaseError("existing release state is invalid")
    if (not isinstance(rec, dict)
            or set(rec) != {"commit", "archive_sha256", "created"}
            or rec["commit"] != commit
            or not isinstance(rec["archive_sha256"], str)
            or not SHA_RE.fullmatch(rec["archive_sha256"])
            or not isinstance(rec["created"], str) or not rec["created"]):
        raise ReleaseError("existing release state is invalid")
    try:
        man = _parse_manifest(man_bytes)
        _check_manifest_fields(man, commit)
        if hashlib.sha256(binary).hexdigest() != man["sha256"]:
            raise ReleaseError("binary digest mismatch")
        _check_elf(binary)
    except ReleaseError:
        raise ReleaseError("existing release state is invalid")
    if expect_digest is not None and rec["archive_sha256"] != expect_digest:
        raise ReleaseError("commit already released with a different artifact")


def _install_release(releases, commit, digest, binary, man_bytes):
    """Create immutable releases/<commit> or verify an identical repeat."""
    rel = os.path.join(releases, commit)
    try:
        st = os.lstat(rel)
    except FileNotFoundError:
        st = None
    if st is not None:
        if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
            raise ReleaseError("existing release path is not a directory")
        _verify_release(rel, commit, expect_digest=digest)
        return
    try:
        os.mkdir(rel, 0o755)
        os.chmod(rel, 0o755)  # explicit mode: survive a restrictive umask
        _write_file(os.path.join(rel, "binflow"), binary,
                    RELEASE_FILE_MODES["binflow"])
        _write_file(os.path.join(rel, "manifest.txt"), man_bytes,
                    RELEASE_FILE_MODES["manifest.txt"])
        record = {"commit": commit, "archive_sha256": digest,
                  "created": _now()}
        _write_file(os.path.join(rel, "RELEASE.json"),
                    (json.dumps(record, sort_keys=True) + "\n").encode(),
                    RELEASE_FILE_MODES["RELEASE.json"])
        _fsync_dir(rel)
    except OSError:
        for name in ("RELEASE.json", "manifest.txt", "binflow"):
            _unlink(os.path.join(rel, name))
        try:
            os.rmdir(rel)
        except OSError:
            pass
        raise ReleaseError("failed to write release directory")


def _set_current(prod_root, commit):
    """Atomically point `current` (relative) at releases/<commit>."""
    tmp = os.path.join(prod_root, ".current.tmp.%d" % os.getpid())
    _unlink(tmp)
    try:
        os.symlink("releases/%s" % commit, tmp)
        os.replace(tmp, os.path.join(prod_root, "current"))
    finally:
        _unlink(tmp)


def _record(prod_root, event):
    """Atomic root-owned state.json plus append-only audit.log (JSONL)."""
    event = dict(event, ts=_now(), action="activate")
    state = os.path.join(prod_root, "state.json")
    tmp = state + ".tmp"
    _unlink(tmp)
    _write_file(tmp, (json.dumps(event, sort_keys=True) + "\n").encode(), 0o600)
    os.replace(tmp, state)
    fd = os.open(os.path.join(prod_root, "audit.log"),
                 os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    try:
        os.write(fd, (json.dumps(event, sort_keys=True) + "\n").encode())
        os.fsync(fd)
    finally:
        os.close(fd)


def _now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")


def _write_file(path, data, mode):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        os.fchmod(fd, mode)
        view = memoryview(data)
        while view:
            view = view[os.write(fd, view):]
        os.fsync(fd)
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


def _fsync_dir(path):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    except OSError:
        pass


def main(argv, stdin, stdout):
    if len(argv) != 2 or argv[1] not in ("upload", "activate"):
        _emit(stdout, {"status": "error", "error": USAGE})
        return 2
    try:
        if argv[1] == "upload":
            return upload_action(STAGING_DIR, stdin, stdout)
        return activate_action(PROD_ROOT, STAGING_DIR, SystemdService(),
                               stdin, stdout)
    except ReleaseError as exc:
        _emit(stdout, {"status": "error", "error": str(exc)})
        return 1
    except Exception:
        _emit(stdout, {"status": "error", "error": "internal error"})
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv, sys.stdin.buffer, sys.stdout.buffer))
