#!/usr/bin/env python3
"""BIN-125 behavior tests for release.py (upload + activate).

Every test uses real temporary directories and a fake service adapter: no
network, no systemctl, no host paths outside the per-test temporary
directory. CLI subprocess cases only exercise failures that exit before
any fixed production path is opened.
"""
import fcntl
import gzip
import hashlib
import io
import json
import os
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import release  # noqa: E402

RELEASE_PY = os.path.join(HERE, "release.py")
ELF_MAGIC = bytes((0x7F, 0x45, 0x4C, 0x46))

COMMIT_A = "1f" * 20
COMMIT_B = "2e" * 20
COMMIT_C = "3d" * 20
COMMIT_D = "4c" * 20


def elf_binary(padding=b"binflow-uat-payload"):
    """Minimal ELF64 little-endian amd64 image: header only plus padding."""
    hdr = bytearray(64)
    hdr[0:4] = ELF_MAGIC
    hdr[4] = 2  # ELFCLASS64
    hdr[5] = 1  # ELFDATA2LSB
    hdr[6] = 1  # EV_CURRENT
    hdr[16:18] = (2).to_bytes(2, "little")  # ET_EXEC
    hdr[18:20] = (0x3E).to_bytes(2, "little")  # EM_X86_64
    return bytes(hdr) + padding


def manifest_text(commit, sha, **over):
    fields = {
        "module": "github.com/0ldlight/bf",
        "commit": commit,
        "go": "go1.24.5",
        "platform": "linux/amd64",
        "cgo": "disabled",
        "sha256": sha,
    }
    fields.update(over)
    return "".join("%s: %s\n" % kv for kv in fields.items()).encode()


def file_member(name, data):
    info = tarfile.TarInfo(name)
    info.size = len(data)
    info.mtime = 0
    return info, data


def link_member(name, ltype, target):
    info = tarfile.TarInfo(name)
    info.type = ltype
    info.linkname = target
    info.mtime = 0
    return info, b""


def build_archive(members):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tf:
        for info, data in members:
            tf.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def append_file(path, data):
    with open(path, "ab") as fh:
        fh.write(data)


def make_artifact(commit, binary=None, manifest=None):
    binary = elf_binary() if binary is None else binary
    if manifest is None:
        manifest = manifest_text(commit, hashlib.sha256(binary).hexdigest())
    blob = gzip.compress(
        build_archive([file_member("binflow", binary),
                       file_member("manifest.txt", manifest)]),
        mtime=0,
    )
    return blob, hashlib.sha256(blob).hexdigest()


def request(commit, sha):
    return (json.dumps({"commit": commit, "sha256": sha}) + "\n").encode()


def call_upload(staging, raw):
    """Mirror of the CLI contract: JSON result line plus exit code."""
    out = io.BytesIO()
    code = 1
    try:
        code = release.upload_action(staging, io.BytesIO(raw), out)
    except release.ReleaseError as exc:
        out.write((json.dumps({"status": "error", "error": str(exc)}) + "\n").encode())
    return code, json.loads(out.getvalue())


def call_activate(prod, staging, service, raw, deadline=0.3):
    out = io.BytesIO()
    code = 1
    try:
        code = release.activate_action(prod, staging, service, io.BytesIO(raw),
                                       out, health_deadline=deadline)
    except release.ReleaseError as exc:
        out.write((json.dumps({"status": "error", "error": str(exc)}) + "\n").encode())
    return code, json.loads(out.getvalue())


class CountingStream:
    """Wraps upload input; records how many bytes the action consumed."""

    def __init__(self, data):
        self._buf = io.BytesIO(data)
        self.consumed = 0

    def readline(self, size=-1):
        data = self._buf.readline(size)
        self.consumed += len(data)
        return data

    def read(self, size=-1):
        data = self._buf.read(size)
        self.consumed += len(data)
        return data


def call_upload_stream(staging, stream):
    out = io.BytesIO()
    code = 1
    try:
        code = release.upload_action(staging, stream, out)
    except release.ReleaseError as exc:
        out.write((json.dumps({"status": "error", "error": str(exc)}) + "\n").encode())
    return code, json.loads(out.getvalue())


class FakeService:
    """Test adapter: counts calls and scripts failures; never touches a host."""

    def __init__(self, health=(200, b"ok\n"), ok_after_restarts=None):
        self.health_response = health
        self.ok_after_restarts = ok_after_restarts
        self.restarts = 0
        self.stops = 0
        self.health_calls = 0

    def restart(self):
        self.restarts += 1

    def stop(self):
        self.stops += 1

    def health(self):
        self.health_calls += 1
        if self.ok_after_restarts is not None and self.restarts < self.ok_after_restarts:
            return 503, b""
        return self.health_response


class UatCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="bf-uat-")
        self.base = self._tmp.name
        self.staging = os.path.join(self.base, "staging")
        self.prod = os.path.join(self.base, "prod")
        os.makedirs(self.staging)
        os.makedirs(os.path.join(self.prod, "releases"))

    def tearDown(self):
        self._tmp.cleanup()

    def stage_raw(self, commit, blob):
        """Place bytes in staging directly (upload has its own tests)."""
        with open(os.path.join(self.staging, release.ARTIFACT_FMT % commit), "wb") as fh:
            fh.write(blob)
        return hashlib.sha256(blob).hexdigest()

    def current_target(self):
        return os.readlink(os.path.join(self.prod, "current"))

    def release_dir(self, commit):
        return os.path.join(self.prod, "releases", commit)

    def state(self):
        with open(os.path.join(self.prod, "state.json")) as fh:
            return json.load(fh)

    def audit_lines(self):
        with open(os.path.join(self.prod, "audit.log")) as fh:
            return [json.loads(line) for line in fh if line.strip()]


class UploadTests(UatCase):
    def test_stores_artifact_under_computed_name(self):
        blob, sha = make_artifact(COMMIT_A)
        code, res = call_upload(self.staging, request(COMMIT_A, sha) + blob)
        self.assertEqual(code, 0)
        self.assertEqual(res["status"], "ok")
        self.assertTrue(res["stored"])
        path = os.path.join(self.staging, "binflow-%s.tar.gz" % COMMIT_A)
        with open(path, "rb") as fh:
            self.assertEqual(fh.read(), blob)
        self.assertEqual(os.listdir(self.staging), [os.path.basename(path)])
        self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o640)

    def test_rejects_sha256_mismatch_and_cleans_up(self):
        blob, _ = make_artifact(COMMIT_A)
        code, res = call_upload(self.staging, request(COMMIT_A, "0" * 64) + blob)
        self.assertEqual(code, 1)
        self.assertEqual(res["status"], "error")
        self.assertIn("sha256", res["error"])
        self.assertEqual(os.listdir(self.staging), [])

    def test_rejects_oversize_payload_and_cleans_up(self):
        payload = b"\x00" * (release.MAX_ARCHIVE_BYTES + 1)
        sha = hashlib.sha256(payload).hexdigest()
        code, res = call_upload(self.staging, request(COMMIT_A, sha) + payload)
        self.assertEqual(code, 1)
        self.assertIn("32MiB", res["error"])
        self.assertEqual(os.listdir(self.staging), [])

    def test_rejects_malformed_request_lines(self):
        good = request(COMMIT_A, "0" * 64)
        bad_lines = [
            b"",
            b"not json\n",
            b"{}\n",
            b'{"commit": "%s"}\n' % COMMIT_A.encode(),
            good + b'{"path": "/etc"}\n',
            ('{"commit": "%s", "sha256": "%s", "url": "http://x"}\n' % (COMMIT_A, "0" * 64)).encode(),
            ('{"commit": "%s", "commit": "%s", "sha256": "%s"}\n' % (COMMIT_A, COMMIT_A, "0" * 64)).encode(),
            ('{"commit": "%s", "sha256": "%s"}' % (COMMIT_A, "0" * 64)).encode(),  # no newline
            ('{"commit": "%s", "sha256": "%s"}\n' % (COMMIT_A.upper(), "0" * 64)).encode(),
            ('{"commit": "%s", "sha256": "%s"}\n' % (COMMIT_A[:39], "0" * 64)).encode(),
            ('{"commit": "%s", "sha256": "%s"}\n' % (COMMIT_A, "0" * 63)).encode(),
            b'{"commit": 1, "sha256": "%s"}\n' % ("0" * 64).encode(),
            b"\xff\xfe binary garbage\n",
            ("x" * 1025).encode() + b"\n",
        ]
        for raw in bad_lines:
            with self.subTest(raw=raw[:40]):
                code, res = call_upload(self.staging, raw + b"payload\n")
                self.assertEqual(code, 1)
                self.assertEqual(res["status"], "error")
                self.assertEqual(os.listdir(self.staging), [])

    def test_identical_repeat_is_idempotent(self):
        blob, sha = make_artifact(COMMIT_A)
        first = call_upload(self.staging, request(COMMIT_A, sha) + blob)
        second = call_upload(self.staging, request(COMMIT_A, sha) + blob)
        self.assertEqual(first[0], 0)
        self.assertEqual(second[0], 0)
        self.assertFalse(second[1]["stored"])
        name = "binflow-%s.tar.gz" % COMMIT_A
        self.assertEqual(os.listdir(self.staging), [name])
        with open(os.path.join(self.staging, name), "rb") as fh:
            self.assertEqual(fh.read(), blob)

    def test_conflicting_repeat_preserves_existing_artifact(self):
        blob1, sha1 = make_artifact(COMMIT_A)
        blob2, sha2 = make_artifact(COMMIT_A, binary=elf_binary(b"different payload"))
        self.assertNotEqual(sha1, sha2)
        self.assertEqual(call_upload(self.staging, request(COMMIT_A, sha1) + blob1)[0], 0)
        code, res = call_upload(self.staging, request(COMMIT_A, sha2) + blob2)
        self.assertEqual(code, 1)
        self.assertIn("different artifact", res["error"])
        with open(os.path.join(self.staging, "binflow-%s.tar.gz" % COMMIT_A), "rb") as fh:
            self.assertEqual(fh.read(), blob1)

    def test_enforces_staging_quotas(self):
        with mock.patch.object(release, "MAX_STAGING_ARTIFACTS", 2):
            for commit in (COMMIT_A, COMMIT_B):
                blob, sha = make_artifact(commit)
                self.assertEqual(call_upload(self.staging, request(commit, sha) + blob)[0], 0)
            blob, sha = make_artifact(COMMIT_C)
            code, res = call_upload(self.staging, request(COMMIT_C, sha) + blob)
            self.assertEqual(code, 1)
            self.assertIn("quota", res["error"])
            self.assertEqual(len(os.listdir(self.staging)), 2)
        with mock.patch.object(release, "MAX_STAGING_BYTES", 4096):
            blob, sha = make_artifact(COMMIT_D, binary=elf_binary(os.urandom(8192)))
            code, res = call_upload(self.staging, request(COMMIT_D, sha) + blob)
            self.assertEqual(code, 1)
            self.assertIn("quota", res["error"])
            self.assertEqual(len(os.listdir(self.staging)), 2)

    def test_denied_while_staging_lock_held(self):
        fd = os.open(self.staging, os.O_RDONLY | os.O_DIRECTORY)
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            blob, sha = make_artifact(COMMIT_A)
            code, res = call_upload(self.staging, request(COMMIT_A, sha) + blob)
            self.assertEqual(code, 1)
            self.assertIn("busy", res["error"])
            self.assertEqual(os.listdir(self.staging), [])
        finally:
            os.close(fd)
        self.assertEqual(call_upload(self.staging, request(COMMIT_A, sha) + blob)[0], 0)

    def test_rejects_symlinked_staging_directory(self):
        real = os.path.join(self.base, "staging-real")
        os.makedirs(real)
        link = os.path.join(self.base, "staging-link")
        os.symlink(real, link)
        blob, sha = make_artifact(COMMIT_A)
        code, res = call_upload(link, request(COMMIT_A, sha) + blob)
        self.assertEqual(code, 1)
        self.assertEqual(res["status"], "error")
        self.assertEqual(os.listdir(real), [])

    def test_rejects_escaped_newline_in_request_values(self):
        blob, sha = make_artifact(COMMIT_A)
        for key in ("commit", "sha256"):
            with self.subTest(key=key):
                values = {"commit": COMMIT_A, "sha256": sha}
                values[key] += "\n"
                raw = (json.dumps(values) + "\n").encode()
                code, res = call_upload(self.staging, raw + blob)
                self.assertEqual(code, 1)
                self.assertIn("must be", res["error"])
                self.assertEqual(os.listdir(self.staging), [])

    def test_identical_retry_requires_matching_payload_digest(self):
        blob, sha = make_artifact(COMMIT_A)
        self.assertEqual(call_upload(self.staging, request(COMMIT_A, sha) + blob)[0], 0)
        corrupted = blob[:-1] + bytes([blob[-1] ^ 0xFF])
        code, res = call_upload(self.staging, request(COMMIT_A, sha) + corrupted)
        self.assertEqual(code, 1)
        self.assertIn("sha256", res["error"])
        with open(os.path.join(self.staging, "binflow-%s.tar.gz" % COMMIT_A), "rb") as fh:
            self.assertEqual(fh.read(), blob)
        self.assertEqual(os.listdir(self.staging), ["binflow-%s.tar.gz" % COMMIT_A])

    def test_orphan_incoming_bytes_consume_quota(self):
        orphan = os.path.join(self.staging, ".incoming.999.1")
        with open(orphan, "wb") as fh:
            fh.write(b"\x00" * 12288)
        blob, sha = make_artifact(COMMIT_A, binary=elf_binary(os.urandom(6144)))
        with mock.patch.object(release, "MAX_STAGING_BYTES", 16384):
            code, res = call_upload(self.staging, request(COMMIT_A, sha) + blob)
            self.assertEqual(code, 1)
            self.assertIn("quota", res["error"])
            self.assertEqual(os.listdir(self.staging), [".incoming.999.1"])
            self.assertEqual(os.path.getsize(orphan), 12288)
            # an artifact small enough to fit beside the orphan still lands
            small, small_sha = make_artifact(COMMIT_B)
            code, res = call_upload(self.staging, request(COMMIT_B, small_sha) + small)
            self.assertEqual(code, 0)
        self.assertEqual(sorted(os.listdir(self.staging)),
                         [".incoming.999.1", "binflow-%s.tar.gz" % COMMIT_B])

    def test_full_staging_refuses_before_allocating_payload(self):
        blob_a, sha_a = make_artifact(COMMIT_A)
        self.assertEqual(call_upload(self.staging, request(COMMIT_A, sha_a) + blob_a)[0], 0)
        with open(os.path.join(self.staging, ".incoming.999.2"), "wb") as fh:
            fh.write(b"\x00" * 8192)
        used = sum(os.stat(os.path.join(self.staging, name)).st_size
                   for name in os.listdir(self.staging))
        blob_b, sha_b = make_artifact(COMMIT_B)
        line = request(COMMIT_B, sha_b)
        with mock.patch.object(release, "MAX_STAGING_BYTES", used):
            stream = CountingStream(line + blob_b)
            code, res = call_upload_stream(self.staging, stream)
        self.assertEqual(code, 1)
        self.assertIn("quota", res["error"])
        self.assertEqual(stream.consumed, len(line))  # nothing beyond the request pulled
        self.assertEqual(sorted(os.listdir(self.staging)),
                         [".incoming.999.2", "binflow-%s.tar.gz" % COMMIT_A])

    def test_identical_retry_succeeds_when_quota_is_full(self):
        blob, sha = make_artifact(COMMIT_A)
        self.assertEqual(call_upload(self.staging, request(COMMIT_A, sha) + blob)[0], 0)
        with open(os.path.join(self.staging, ".incoming.999.3"), "wb") as fh:
            fh.write(b"\x00" * 8192)
        used = sum(os.stat(os.path.join(self.staging, name)).st_size
                   for name in os.listdir(self.staging))
        with mock.patch.object(release, "MAX_STAGING_BYTES", used):
            code, res = call_upload(self.staging, request(COMMIT_A, sha) + blob)
        self.assertEqual(code, 0)
        self.assertFalse(res["stored"])
        self.assertEqual(sorted(os.listdir(self.staging)),
                         [".incoming.999.3", "binflow-%s.tar.gz" % COMMIT_A])
        with open(os.path.join(self.staging, "binflow-%s.tar.gz" % COMMIT_A), "rb") as fh:
            self.assertEqual(fh.read(), blob)

    def test_rejects_unexpected_nonregular_staging_entries(self):
        blob, sha = make_artifact(COMMIT_A)
        for kind in ("symlink", "fifo", "dir"):
            with self.subTest(kind=kind):
                staging = os.path.join(self.base, "staging-" + kind)
                os.makedirs(staging)
                weird = os.path.join(staging, "weird")
                if kind == "symlink":
                    os.symlink("/etc/hosts", weird)
                elif kind == "fifo":
                    os.mkfifo(weird)
                else:
                    os.makedirs(weird)
                code, res = call_upload(staging, request(COMMIT_A, sha) + blob)
                self.assertEqual(code, 1)
                self.assertIn("non-regular", res["error"])
                self.assertTrue(os.path.lexists(weird))
                self.assertEqual(os.listdir(staging), ["weird"])

    def test_staging_entry_cap_bounds_orphan_accumulation(self):
        with mock.patch.object(release, "MAX_STAGING_ENTRIES", 3):
            for commit in (COMMIT_A, COMMIT_B):
                blob, sha = make_artifact(commit)
                self.assertEqual(call_upload(self.staging, request(commit, sha) + blob)[0], 0)
            with open(os.path.join(self.staging, ".incoming.999.4"), "wb") as fh:
                fh.write(b"\x00" * 16)
            blob, sha = make_artifact(COMMIT_C)
            code, res = call_upload(self.staging, request(COMMIT_C, sha) + blob)
            self.assertEqual(code, 1)
            self.assertIn("quota", res["error"])
        self.assertEqual(len(os.listdir(self.staging)), 3)


class ActivateTests(UatCase):
    def test_first_activation_creates_release_and_switches_current(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        binary = elf_binary()
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 0)
        self.assertEqual(res["status"], "ok")
        rel = self.release_dir(COMMIT_A)
        self.assertTrue(stat.S_ISDIR(os.lstat(rel).st_mode))
        with open(os.path.join(rel, "binflow"), "rb") as fh:
            self.assertEqual(fh.read(), binary)
        with open(os.path.join(rel, "manifest.txt"), "rb") as fh:
            self.assertEqual(fh.read(),
                             manifest_text(COMMIT_A, hashlib.sha256(binary).hexdigest()))
        with open(os.path.join(rel, "RELEASE.json")) as fh:
            rec = json.load(fh)
        self.assertEqual(rec["commit"], COMMIT_A)
        self.assertEqual(rec["archive_sha256"], sha)
        self.assertEqual(self.current_target(), "releases/%s" % COMMIT_A)
        self.assertEqual(service.restarts, 1)
        self.assertGreaterEqual(service.health_calls, 1)
        self.assertEqual(self.state()["current"], COMMIT_A)
        self.assertEqual(self.state()["status"], "ok")
        self.assertEqual(len(self.audit_lines()), 1)
        self.assertEqual(set(os.listdir(self.prod)),
                         {"releases", "current", "state.json", "audit.log"})

    def test_requires_eof_after_request_line(self):
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, "0" * 64) + b"X")
        self.assertEqual(code, 1)
        self.assertIn("trailing", res["error"])
        self.assertFalse(os.path.lexists(os.path.join(self.prod, "current")))
        self.assertEqual(service.restarts, 0)
        self.assertEqual(os.listdir(os.path.join(self.prod, "releases")), [])

    def test_missing_staged_artifact(self):
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, "0" * 64))
        self.assertEqual(code, 1)
        self.assertEqual(res["status"], "error")
        self.assertEqual(service.restarts, 0)
        self.assertFalse(os.path.lexists(os.path.join(self.prod, "current")))

    def test_request_digest_must_match_staged_artifact(self):
        blob, _ = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, "5" * 64))
        self.assertEqual(code, 1)
        self.assertIn("digest", res["error"])
        self.assertEqual(service.restarts, 0)

    def test_rejects_invalid_archives(self):
        good_blob = make_artifact(COMMIT_A)[0]
        cases = {
            "truncated": good_blob[:-8],
            "not gzip": b"this is not gzip data " * 4,
            "not tar": gzip.compress(b"definitely not a tar file", mtime=0),
            "extra member": gzip.compress(build_archive([
                file_member("binflow", elf_binary()),
                file_member("manifest.txt", b"module: x\n"),
                file_member("extra.txt", b"surprise\n"),
            ]), mtime=0),
            "symlinked member": gzip.compress(build_archive([
                link_member("binflow", tarfile.SYMTYPE, "/etc/passwd"),
                file_member("manifest.txt", b""),
            ]), mtime=0),
            "hardlinked member": gzip.compress(build_archive([
                link_member("binflow", tarfile.LNKTYPE, "manifest.txt"),
                file_member("manifest.txt", b""),
            ]), mtime=0),
            "traversal member": gzip.compress(build_archive([
                file_member("../binflow", elf_binary()),
                file_member("manifest.txt", b""),
            ]), mtime=0),
            "prefixed member": gzip.compress(build_archive([
                file_member("./binflow", elf_binary()),
                file_member("manifest.txt", b""),
            ]), mtime=0),
            "duplicate member": gzip.compress(build_archive([
                file_member("binflow", elf_binary()),
                file_member("binflow", elf_binary()),
                file_member("manifest.txt", b""),
            ]), mtime=0),
            "decompression bomb": gzip.compress(build_archive([
                file_member("binflow", b"\x00" * (release.MAX_UNPACKED_BYTES + 1)),
                file_member("manifest.txt", b""),
            ]), mtime=0),
            "oversize compressed": b"\x00" * (release.MAX_UNPACKED_BYTES + 1),
        }
        for name, blob in cases.items():
            with self.subTest(case=name):
                sha = self.stage_raw(COMMIT_A, blob)
                service = FakeService()
                code, res = call_activate(self.prod, self.staging, service,
                                          request(COMMIT_A, sha))
                self.assertEqual(code, 1)
                self.assertEqual(res["status"], "error")
                self.assertFalse(os.path.exists(self.release_dir(COMMIT_A)))
                self.assertFalse(os.path.lexists(os.path.join(self.prod, "current")))
                self.assertEqual(service.restarts, 0)

    def test_rejects_manifest_and_binary_violations(self):
        binary = elf_binary()
        bsha = hashlib.sha256(binary).hexdigest()
        base_lines = manifest_text(COMMIT_A, bsha).decode().splitlines()
        cases = {
            "commit mismatch": manifest_text(COMMIT_B, bsha),
            "binary hash mismatch": manifest_text(COMMIT_A, hashlib.sha256(b"other").hexdigest()),
            "wrong module": manifest_text(COMMIT_A, bsha, module="github.com/evil/bf"),
            "wrong platform": manifest_text(COMMIT_A, bsha, platform="linux/arm64"),
            "cgo enabled": manifest_text(COMMIT_A, bsha, cgo="enabled"),
            "uppercase sha": manifest_text(COMMIT_A, bsha.upper()),
            "unknown field": ("\n".join(base_lines) + "\nunknown: 1\n").encode(),
            "missing field": ("\n".join(l for l in base_lines if not l.startswith("platform:")) + "\n").encode(),
            "duplicate field": ("\n".join(base_lines) + "\ncommit: %s" % COMMIT_A).encode(),
            "non-ascii value": manifest_text(COMMIT_A, bsha).replace(b"linux/amd64", b"linux/amd\xff64"),
            "blank line": manifest_text(COMMIT_A, bsha).replace(b"cgo", b"\ncgo"),
            "not elf": None,
        }
        for name, manifest in cases.items():
            with self.subTest(case=name):
                if manifest is None:
                    blob, sha = make_artifact(COMMIT_A, binary=b"MZ pad pad pad")
                else:
                    blob, sha = make_artifact(COMMIT_A, binary=binary, manifest=manifest)
                self.stage_raw(COMMIT_A, blob)
                service = FakeService()
                code, res = call_activate(self.prod, self.staging, service,
                                          request(COMMIT_A, sha))
                self.assertEqual(code, 1)
                self.assertEqual(res["status"], "error")
                self.assertFalse(os.path.exists(self.release_dir(COMMIT_A)))
                self.assertFalse(os.path.lexists(os.path.join(self.prod, "current")))
                self.assertEqual(service.restarts, 0)

    def test_immutable_conflict_same_commit_different_artifact(self):
        blob1, sha1 = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob1)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha1))[0], 0)
        blob2, sha2 = make_artifact(COMMIT_A, binary=elf_binary(b"second build"))
        self.stage_raw(COMMIT_A, blob2)
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, sha2))
        self.assertEqual(code, 1)
        self.assertIn("different artifact", res["error"])
        self.assertEqual(self.current_target(), "releases/%s" % COMMIT_A)
        with open(os.path.join(self.release_dir(COMMIT_A), "binflow"), "rb") as fh:
            self.assertEqual(fh.read(), elf_binary())

    def test_identical_reactivation_still_verifies_health(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        service = FakeService()
        self.assertEqual(call_activate(self.prod, self.staging, service,
                                       request(COMMIT_A, sha))[0], 0)
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 0)
        self.assertEqual(res["status"], "ok")
        self.assertEqual(service.restarts, 2)
        self.assertGreaterEqual(service.health_calls, 2)
        self.assertEqual(self.current_target(), "releases/%s" % COMMIT_A)
        self.assertEqual(os.listdir(os.path.join(self.prod, "releases")), [COMMIT_A])

    def test_failed_activation_recovers_previous_version(self):
        blob_a, sha_a = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob_a)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha_a))[0], 0)
        blob_b, sha_b = make_artifact(COMMIT_B, binary=elf_binary(b"version b"))
        self.stage_raw(COMMIT_B, blob_b)
        service = FakeService(ok_after_restarts=2)  # healthy again once rolled back
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_B, sha_b))
        self.assertEqual(code, 1)
        self.assertIn("health", res["error"])
        self.assertTrue(res["recovered"])
        self.assertEqual(self.current_target(), "releases/%s" % COMMIT_A)
        self.assertTrue(os.path.exists(os.path.join(self.release_dir(COMMIT_B), "binflow")))
        self.assertEqual(service.restarts, 2)
        self.assertEqual(self.state()["current"], COMMIT_A)
        self.assertEqual(self.state()["status"], "failed")
        self.assertTrue(self.state()["recovered"])
        self.assertEqual(self.audit_lines()[-1]["status"], "failed")

    def test_first_activation_failure_stops_service_and_keeps_evidence(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        service = FakeService(health=(500, b""))
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("health", res["error"])
        self.assertFalse(res["recovered"])
        self.assertEqual(service.restarts, 1)
        self.assertEqual(service.stops, 1)
        self.assertFalse(os.path.lexists(os.path.join(self.prod, "current")))
        self.assertTrue(os.path.exists(os.path.join(self.release_dir(COMMIT_A), "binflow")))
        self.assertIsNone(self.state()["current"])
        self.assertFalse(self.state()["recovered"])

    def test_failed_recovery_reports_unrecovered(self):
        blob_a, sha_a = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob_a)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha_a))[0], 0)
        blob_b, sha_b = make_artifact(COMMIT_B)
        self.stage_raw(COMMIT_B, blob_b)
        service = FakeService(health=(500, b""))
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_B, sha_b))
        self.assertEqual(code, 1)
        self.assertFalse(res["recovered"])
        self.assertEqual(service.restarts, 2)
        self.assertEqual(service.stops, 0)
        self.assertEqual(self.current_target(), "releases/%s" % COMMIT_A)
        self.assertTrue(os.path.exists(os.path.join(self.release_dir(COMMIT_B), "binflow")))
        self.assertFalse(self.state()["recovered"])

    def test_rejects_foreign_current_entries(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        current = os.path.join(self.prod, "current")
        targets = [
            "/elsewhere/releases/1f",
            "../outside/releases/1f",
            "releases/not-hex",
            "releases/%s" % COMMIT_B,
        ]
        for target in targets:
            with self.subTest(target=target):
                os.symlink(target, current)
                service = FakeService()
                code, res = call_activate(self.prod, self.staging, service,
                                          request(COMMIT_A, sha))
                self.assertEqual(code, 1)
                self.assertIn("current", res["error"])
                self.assertEqual(os.readlink(current), target)
                self.assertEqual(service.restarts, 0)
                os.unlink(current)
        with open(current, "wb") as fh:
            fh.write(b"not a symlink")
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("current", res["error"])
        self.assertEqual(service.restarts, 0)

    def test_rejects_symlinked_release_paths(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        releases = os.path.join(self.prod, "releases")
        elsewhere = os.path.join(self.base, "elsewhere")
        os.makedirs(elsewhere)
        real = os.path.join(self.base, "releases-real")
        os.rename(releases, real)
        os.symlink(real, releases)
        code, res = call_activate(self.prod, self.staging, FakeService(),
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("releases", res["error"])
        prod_link = os.path.join(self.base, "prod-link")
        os.symlink(self.prod, prod_link)
        code, res = call_activate(prod_link, self.staging, FakeService(),
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("production root", res["error"])
        os.unlink(releases)
        os.rename(real, releases)
        os.symlink(elsewhere, self.release_dir(COMMIT_A))
        code, res = call_activate(self.prod, self.staging, FakeService(),
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("release path", res["error"])
        os.unlink(self.release_dir(COMMIT_A))
        staged = os.path.join(self.staging, "binflow-%s.tar.gz" % COMMIT_A)
        os.unlink(staged)
        os.symlink("/etc/hosts", staged)
        code, res = call_activate(self.prod, self.staging, FakeService(),
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("staged artifact", res["error"])

    def test_denied_while_production_root_lock_held(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        fd = os.open(self.prod, os.O_RDONLY | os.O_DIRECTORY)
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            code, res = call_activate(self.prod, self.staging, FakeService(),
                                      request(COMMIT_A, sha))
            self.assertEqual(code, 1)
            self.assertIn("busy", res["error"])
        finally:
            os.close(fd)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha))[0], 0)

    def test_snapshot_rejects_fifo_without_blocking(self):
        os.mkfifo(os.path.join(self.staging, release.ARTIFACT_FMT % COMMIT_A))
        driver = (
            "import sys\n"
            "sys.path.insert(0, %r)\n"
            "import release\n"
            "try:\n"
            "    release.snapshot_staged(%r, %r)\n"
            "    print('accepted')\n"
            "except release.ReleaseError as exc:\n"
            "    print('rejected: ' + str(exc))\n"
        ) % (HERE, self.staging, COMMIT_A)
        proc = subprocess.run([sys.executable, "-c", driver],
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              timeout=15)
        out = proc.stdout.decode()
        self.assertIn("rejected", out)
        self.assertIn("regular file", out)

    def test_staged_directory_rejected(self):
        os.makedirs(os.path.join(self.staging, release.ARTIFACT_FMT % COMMIT_A))
        code, res = call_activate(self.prod, self.staging, FakeService(),
                                  request(COMMIT_A, "0" * 64))
        self.assertEqual(code, 1)
        self.assertIn("staged artifact", res["error"])

    def test_compressed_artifact_bound_is_32mib(self):
        blob, sha = make_artifact(COMMIT_A,
                                  binary=elf_binary(os.urandom(33 * 1024 * 1024)))
        self.assertGreater(len(blob), release.MAX_ARCHIVE_BYTES)
        self.stage_raw(COMMIT_A, blob)
        code, res = call_activate(self.prod, self.staging, FakeService(),
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("32MiB", res["error"])
        self.assertFalse(os.path.lexists(os.path.join(self.prod, "current")))
        self.assertEqual(os.listdir(os.path.join(self.prod, "releases")), [])

    def test_current_symlink_with_trailing_newline_rejected(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha))[0], 0)
        current = os.path.join(self.prod, "current")
        os.unlink(current)
        os.symlink("releases/%s\n" % COMMIT_A, current)
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_A, sha))
        self.assertEqual(code, 1)
        self.assertIn("current", res["error"])
        self.assertEqual(service.restarts, 0)

    def test_rejects_tampered_existing_release_without_service_mutation(self):
        def fresh(tag):
            prod = os.path.join(self.base, "prod-" + tag)
            staging = os.path.join(self.base, "staging-" + tag)
            os.makedirs(os.path.join(prod, "releases"))
            os.makedirs(staging)
            return prod, staging

        def rewrite_digest(rel):
            path = os.path.join(rel, "RELEASE.json")
            with open(path) as fh:
                rec = json.load(fh)
            rec["archive_sha256"] = "0" * 64
            with open(path, "w") as fh:
                json.dump(rec, fh)

        corruptions = {
            "binary-mode-widened": lambda rel: os.chmod(os.path.join(rel, "binflow"), 0o777),
            "manifest-appended": lambda rel: append_file(os.path.join(rel, "manifest.txt"), b"x"),
            "extra-entry": lambda rel: append_file(os.path.join(rel, "rogue"), b"x"),
            "record-digest-rewritten": rewrite_digest,
            "binary-appended": lambda rel: append_file(os.path.join(rel, "binflow"), b"x"),
            "record-replaced-by-symlink": lambda rel: (
                os.unlink(os.path.join(rel, "RELEASE.json")),
                os.symlink("/etc/hosts", os.path.join(rel, "RELEASE.json")),
            ),
            "release-dir-mode-widened": lambda rel: os.chmod(rel, 0o777),
        }
        for name, corrupt in corruptions.items():
            with self.subTest(corruption=name):
                prod, staging = fresh(name)
                blob, sha = make_artifact(COMMIT_A)
                with open(os.path.join(staging, release.ARTIFACT_FMT % COMMIT_A), "wb") as fh:
                    fh.write(blob)
                self.assertEqual(call_activate(prod, staging, FakeService(),
                                               request(COMMIT_A, sha))[0], 0)
                corrupt(os.path.join(prod, "releases", COMMIT_A))
                service = FakeService()
                code, res = call_activate(prod, staging, service,
                                          request(COMMIT_A, sha))
                self.assertEqual(code, 1)
                self.assertIn("release", res["error"])
                self.assertEqual(service.restarts, 0)
                self.assertEqual(os.readlink(os.path.join(prod, "current")),
                                 "releases/%s" % COMMIT_A)

    def test_rollback_requires_valid_previous_release(self):
        blob_a, sha_a = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob_a)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha_a))[0], 0)
        append_file(os.path.join(self.release_dir(COMMIT_A), "rogue"), b"x")
        blob_b, sha_b = make_artifact(COMMIT_B)
        self.stage_raw(COMMIT_B, blob_b)
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_B, sha_b))
        self.assertEqual(code, 1)
        self.assertIn("release", res["error"])
        self.assertEqual(service.restarts, 0)
        self.assertFalse(os.path.exists(self.release_dir(COMMIT_B)))

    def test_mere_directory_is_not_rollback_evidence(self):
        os.symlink("releases/%s" % COMMIT_A, os.path.join(self.prod, "current"))
        os.makedirs(self.release_dir(COMMIT_A))
        blob_b, sha_b = make_artifact(COMMIT_B)
        self.stage_raw(COMMIT_B, blob_b)
        service = FakeService()
        code, res = call_activate(self.prod, self.staging, service,
                                  request(COMMIT_B, sha_b))
        self.assertEqual(code, 1)
        self.assertIn("release", res["error"])
        self.assertEqual(service.restarts, 0)
        self.assertFalse(os.path.exists(self.release_dir(COMMIT_B)))

    def test_release_is_public_executable_and_owned_by_activator(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        self.assertEqual(call_activate(self.prod, self.staging, FakeService(),
                                       request(COMMIT_A, sha))[0], 0)
        rel = self.release_dir(COMMIT_A)
        self.assertEqual(stat.S_IMODE(os.lstat(rel).st_mode), 0o755)
        self.assertEqual(os.lstat(rel).st_uid, os.geteuid())
        binary = os.path.join(rel, "binflow")
        self.assertEqual(stat.S_IMODE(os.lstat(binary).st_mode), 0o755)
        self.assertEqual(os.lstat(binary).st_uid, os.geteuid())
        # Portable proxy for a distinct unprivileged identity traversing and
        # executing: the "other" bits carry read+execute, nobody can write.
        self.assertEqual(stat.S_IMODE(os.lstat(binary).st_mode) & 0o005, 0o005)
        for name in ("binflow", "manifest.txt", "RELEASE.json"):
            path = os.path.join(rel, name)
            self.assertEqual(os.lstat(path).st_uid, os.geteuid())
            self.assertEqual(stat.S_IMODE(os.lstat(path).st_mode) & 0o022, 0)
        self.assertEqual(stat.S_IMODE(os.lstat(rel).st_mode) & 0o022, 0)

    def test_release_modes_ignore_restrictive_umask(self):
        blob, sha = make_artifact(COMMIT_A)
        self.stage_raw(COMMIT_A, blob)
        previous = os.umask(0o077)
        try:
            code, _ = call_activate(self.prod, self.staging, FakeService(),
                                    request(COMMIT_A, sha))
        finally:
            os.umask(previous)
        self.assertEqual(code, 0)
        rel = self.release_dir(COMMIT_A)
        self.assertEqual(stat.S_IMODE(os.lstat(rel).st_mode), 0o755)
        self.assertEqual(stat.S_IMODE(os.lstat(os.path.join(rel, "binflow")).st_mode), 0o755)
        self.assertEqual(stat.S_IMODE(os.lstat(os.path.join(rel, "manifest.txt")).st_mode), 0o640)


class ContractTests(unittest.TestCase):
    def test_fixed_protocol_constants(self):
        self.assertEqual(release.MAX_REQUEST_BYTES, 1024)
        self.assertEqual(release.MAX_ARCHIVE_BYTES, 32 * 1024 * 1024)
        self.assertEqual(release.MAX_STAGING_BYTES, 256 * 1024 * 1024)
        self.assertEqual(release.MAX_STAGING_ARTIFACTS, 16)
        self.assertEqual(release.MAX_STAGING_ENTRIES, 64)
        self.assertEqual(release.MAX_UNPACKED_BYTES, 64 * 1024 * 1024)
        self.assertEqual(release.STAGING_DIR, "/var/lib/binflow/staging")
        self.assertEqual(release.PROD_ROOT, "/opt/binflow")
        self.assertEqual(release.SERVICE, "binflow-uat.service")
        self.assertEqual(release.HEALTH_URL, "http://127.0.0.1:18080/healthz")

    def test_health_requires_200_and_exact_ok_newline(self):
        with self.assertRaises(release.ReleaseError):
            release.wait_healthy(FakeService(health=(200, b"ok")), deadline=0.1)
        with self.assertRaises(release.ReleaseError):
            release.wait_healthy(FakeService(health=(302, b"ok\n")), deadline=0.1)
        release.wait_healthy(FakeService(), deadline=1.0)


class CliTests(unittest.TestCase):
    def run_cli(self, args, data):
        return subprocess.run([sys.executable, "-I", RELEASE_PY] + args,
                              input=data, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=60)

    def test_rejects_unknown_actions_and_option_arguments(self):
        for args in ([], ["deploy"], ["--root", "/tmp/x", "activate"], ["upload", "extra"]):
            with self.subTest(args=args):
                proc = self.run_cli(args, b"")
                self.assertNotEqual(proc.returncode, 0)
                res = json.loads(proc.stdout.decode())
                self.assertEqual(res["status"], "error")
                self.assertIn("usage", res["error"])

    def test_upload_rejects_invalid_request_before_fixed_paths(self):
        proc = self.run_cli(["upload"], b"not json\n")
        self.assertEqual(proc.returncode, 1)
        res = json.loads(proc.stdout.decode())
        self.assertEqual(res["status"], "error")

    def test_activate_requires_eof_before_fixed_paths(self):
        sha = hashlib.sha256(b"anything").hexdigest()
        proc = self.run_cli(["activate"], request(COMMIT_A, sha) + b"X")
        self.assertEqual(proc.returncode, 1)
        res = json.loads(proc.stdout.decode())
        self.assertIn("trailing", res["error"])


if __name__ == "__main__":
    unittest.main()
