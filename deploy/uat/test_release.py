#!/usr/bin/env python3
"""BIN-125 behavior tests for release.py upload staging.

Every test uses real temporary directories: no network, no systemctl, no
host paths outside the per-test temporary directory. CLI subprocess cases
only exercise failures that exit before any fixed production path is
opened. Activation coverage arrives with the stacked follow-up change.
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


def build_archive(members):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tf:
        for info, data in members:
            tf.addfile(info, io.BytesIO(data))
    return buf.getvalue()


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


class UatCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="bf-uat-")
        self.base = self._tmp.name
        self.staging = os.path.join(self.base, "staging")
        os.makedirs(self.staging)

    def tearDown(self):
        self._tmp.cleanup()


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

    def test_full_artifact_count_refuses_before_reading_payload(self):
        blob_a, sha_a = make_artifact(COMMIT_A)
        self.assertEqual(call_upload(self.staging, request(COMMIT_A, sha_a) + blob_a)[0], 0)
        blob_b, sha_b = make_artifact(COMMIT_B)
        line = request(COMMIT_B, sha_b)
        stream = CountingStream(line + blob_b)
        with mock.patch.object(release, "MAX_STAGING_ARTIFACTS", 1):
            code, result = call_upload_stream(self.staging, stream)
        self.assertEqual(code, 1)
        self.assertIn("quota", result["error"])
        self.assertEqual(stream.consumed, len(line))
        self.assertEqual(os.listdir(self.staging), ["binflow-%s.tar.gz" % COMMIT_A])

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


class ContractTests(unittest.TestCase):
    def test_fixed_protocol_constants(self):
        self.assertEqual(release.MAX_REQUEST_BYTES, 1024)
        self.assertEqual(release.MAX_ARCHIVE_BYTES, 32 * 1024 * 1024)
        self.assertEqual(release.MAX_STAGING_BYTES, 256 * 1024 * 1024)
        self.assertEqual(release.MAX_STAGING_ARTIFACTS, 16)
        self.assertEqual(release.MAX_STAGING_ENTRIES, 64)
        self.assertEqual(release.STAGING_DIR, "/var/lib/binflow/staging")


class CliTests(unittest.TestCase):
    def run_cli(self, args, data):
        return subprocess.run([sys.executable, "-I", RELEASE_PY] + args,
                              input=data, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=60)

    def test_rejects_unknown_actions_and_option_arguments(self):
        for args in ([], ["deploy"], ["activate"], ["--root", "/tmp/x", "activate"], ["upload", "extra"]):
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


if __name__ == "__main__":
    unittest.main()
