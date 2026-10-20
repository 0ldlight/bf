#!/usr/bin/env python3
"""Black-box smoke check for the binflow binary (BIN-124).

binflow 二进制黑盒冒烟检查（BIN-124）。

The script starts a real child binary on an ephemeral loopback port via
BINFLOW_LISTEN_ADDR, exercises the /healthz contract over plain HTTP,
requires a clean SIGTERM shutdown, and requires that a second child cannot
bind a port already held by a listening socket. It uses only the Python
standard library, never routes loopback requests through an HTTP proxy,
always stops only the children it started, and reports failure to the
caller with a nonzero exit status.
"""

import http.client
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

STARTUP_TIMEOUT = 5.0   # bounded wait for the child to serve / 等待就绪上限
SHUTDOWN_TIMEOUT = 5.0  # bounded wait for the child to exit after SIGTERM
REQUEST_TIMEOUT = 5.0
POLL_INTERVAL = 0.05

HEALTH_PATH = "/healthz"
HEALTH_BODY = b"ok\n"


class SmokeError(Exception):
    """A smoke expectation was not met / 冒烟期望未满足。"""


# Loopback requests must bypass any configured HTTP proxy (http_proxy etc.).
# 回环请求必须绕过任何已配置的 HTTP 代理。
_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def fetch(method, url, data=None):
    """Perform one HTTP request and return (status, headers, body).

    HTTP status errors (405, 404, ...) are returned, not raised, so callers
    can check them explicitly.
    """
    req = urllib.request.Request(url, data=data, method=method)
    try:
        resp = _OPENER.open(req, timeout=REQUEST_TIMEOUT)
    except urllib.error.HTTPError as err:
        try:
            body = err.read()
        finally:
            err.close()
        return err.code, err.headers, body
    try:
        return resp.status, resp.headers, resp.read()
    finally:
        resp.close()


def free_loopback_port():
    """Reserve and release an ephemeral loopback port / 取一个临时回环端口。"""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def spawn(binary, listen_addr, children):
    """Start one owned child with BINFLOW_LISTEN_ADDR set."""
    env = dict(os.environ)
    env["BINFLOW_LISTEN_ADDR"] = listen_addr
    proc = subprocess.Popen(
        [binary],
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    children.append(proc)
    return proc


def describe_exit(proc):
    """Render a child's exit status and captured stderr for diagnostics."""
    err = b""
    if proc.stderr is not None:
        try:
            err = proc.stderr.read() or b""
        except OSError:
            pass
    return "exit=%r stderr=%r" % (
        proc.returncode,
        err.decode("utf-8", "replace").strip(),
    )


def ensure_stopped(proc):
    """Stop one owned child and release its pipes; never touch others."""
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=SHUTDOWN_TIMEOUT)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    if proc.stderr is not None:
        proc.stderr.close()


def wait_until_serving(base_url, proc):
    """Poll until the child answers on /healthz, within a bounded time."""
    deadline = time.monotonic() + STARTUP_TIMEOUT
    last_error = "no attempt recorded"
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise SmokeError(
                "child exited before serving (%s)" % describe_exit(proc)
            )
        try:
            status, _, _ = fetch("GET", base_url + HEALTH_PATH)
            if status == 200:
                return
            last_error = "GET /healthz status=%d" % status
        except (urllib.error.URLError, http.client.HTTPException, OSError) as exc:
            last_error = repr(exc)
        time.sleep(POLL_INTERVAL)
    raise SmokeError(
        "child did not serve /healthz within %.1fs: %s"
        % (STARTUP_TIMEOUT, last_error)
    )


def check_http_contract(base_url):
    """Verify the /healthz HTTP contract / 校验 /healthz 的 HTTP 契约。"""
    # GET: 200, exact body, plain-text content type, explicit length.
    status, headers, body = fetch("GET", base_url + HEALTH_PATH)
    if status != 200:
        raise SmokeError("GET /healthz status=%d, want 200" % status)
    if body != HEALTH_BODY:
        raise SmokeError("GET /healthz body=%r, want %r" % (body, HEALTH_BODY))
    content_type = headers.get("Content-Type", "")
    if not content_type.startswith("text/plain"):
        raise SmokeError(
            "GET /healthz Content-Type=%r, want text/plain" % content_type
        )
    content_length = headers.get("Content-Length", "")
    if content_length != str(len(HEALTH_BODY)):
        raise SmokeError(
            "GET /healthz Content-Length=%r, want %r"
            % (content_length, str(len(HEALTH_BODY)))
        )

    # HEAD: same representation headers, empty body.
    status, headers, body = fetch("HEAD", base_url + HEALTH_PATH)
    if status != 200:
        raise SmokeError("HEAD /healthz status=%d, want 200" % status)
    if body:
        raise SmokeError("HEAD /healthz body=%r, want empty" % body)
    if headers.get("Content-Type", "") != content_type:
        raise SmokeError(
            "HEAD /healthz Content-Type=%r differs from GET %r"
            % (headers.get("Content-Type", ""), content_type)
        )
    if headers.get("Content-Length", "") != content_length:
        raise SmokeError(
            "HEAD /healthz Content-Length=%r differs from GET %r"
            % (headers.get("Content-Length", ""), content_length)
        )

    # POST: 405 with Allow listing GET and HEAD.
    status, headers, _ = fetch(
        "POST", base_url + HEALTH_PATH, data=b"smoke"
    )
    if status != 405:
        raise SmokeError("POST /healthz status=%d, want 405" % status)
    allow = [m.strip().upper() for m in headers.get("Allow", "").split(",")]
    if "GET" not in allow or "HEAD" not in allow:
        raise SmokeError(
            "POST /healthz Allow=%r, want GET and HEAD" % headers.get("Allow", "")
        )

    # Unknown path: 404.
    status, _, _ = fetch("GET", base_url + "/no-such-path")
    if status != 404:
        raise SmokeError("GET /no-such-path status=%d, want 404" % status)


def shutdown_cleanly(proc):
    """SIGTERM the child and require a clean zero exit."""
    proc.send_signal(signal.SIGTERM)
    try:
        returncode = proc.wait(timeout=SHUTDOWN_TIMEOUT)
    except subprocess.TimeoutExpired:
        raise SmokeError(
            "child did not exit within %.1fs after SIGTERM" % SHUTDOWN_TIMEOUT
        )
    if returncode != 0:
        raise SmokeError(
            "child did not exit cleanly after SIGTERM (%s), want exit=0"
            % describe_exit(proc)
        )


def expect_conflicting_startup_fails(binary, children):
    """A second child must fail nonzero while the port is held."""
    blocker = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    blocker.bind(("127.0.0.1", 0))
    blocker.listen(1)
    try:
        port = blocker.getsockname()[1]
        proc = spawn(binary, "127.0.0.1:%d" % port, children)
        try:
            returncode = proc.wait(timeout=STARTUP_TIMEOUT)
        except subprocess.TimeoutExpired:
            raise SmokeError(
                "second child kept running for %.1fs on an occupied port"
                % STARTUP_TIMEOUT
            )
        if returncode == 0:
            raise SmokeError(
                "second child exited 0 on an occupied port, want nonzero"
            )
    finally:
        blocker.close()


def run_smoke(binary):
    children = []
    port = free_loopback_port()
    base_url = "http://127.0.0.1:%d" % port
    proc = spawn(binary, "127.0.0.1:%d" % port, children)
    try:
        wait_until_serving(base_url, proc)
        check_http_contract(base_url)
        shutdown_cleanly(proc)
        expect_conflicting_startup_fails(binary, children)
    finally:
        for child in children:
            ensure_stopped(child)


def main(argv):
    if len(argv) != 2:
        print("usage: smoke.py <binflow-binary>", file=sys.stderr)
        return 2
    binary = argv[1]
    if not os.path.isfile(binary):
        print("smoke: no such file: %s" % binary, file=sys.stderr)
        return 1
    if not os.access(binary, os.X_OK):
        print("smoke: not executable: %s" % binary, file=sys.stderr)
        return 1
    try:
        run_smoke(binary)
    except SmokeError as exc:
        print("smoke: FAILED: %s" % exc, file=sys.stderr)
        return 1
    except OSError as exc:
        print("smoke: FAILED: %r" % exc, file=sys.stderr)
        return 1
    print("smoke: PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
