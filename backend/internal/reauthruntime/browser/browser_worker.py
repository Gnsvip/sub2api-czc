"""Managed Codex browser adapter; retain the pinned Excel/BPS worker unchanged."""
from __future__ import annotations

import base64
from dataclasses import replace
import hashlib
import importlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import platform
import queue
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from urllib.request import urlopen

PLAYWRIGHT_VERSION = "1.62.1"
PLAYWRIGHT_SHA512 = "wPYSwEBJY9GHraISXqyqtx0na0LpO3XEX7jNDhntbex7tzUS7kLnZsOlFruFJB4Hi/rhDMjXGqHewDZ68nYZVw=="
# This keyless upstream release passed the OVH password/TOTP/token/model trial.
# Its signed upstream manifest was verified before pinning this archive digest.
CLOAK_VERSION = "146.0.7680.177.5"
CLOAK_SHA256 = "4a12bcde95fa1bb1beef2b41ab5e5c27c36be78e3be3d0dac8c64d705216670e"
CLOAK_ARCHIVE_SIZE = 216890134
CLOAK_URL = ("https://github.com/CloakHQ/CloakBrowser/releases/download/"
             f"chromium-v{CLOAK_VERSION}/cloakbrowser-linux-x64.tar.gz")
FAILURES = frozenset({"browser_unavailable", "browser_start_failed", "browser_dependency_failed",
                     "security_check", "additional_verification", "password_rejected", "totp_rejected",
                     "identity_mismatch", "workspace_unavailable", "workspace_selection_required", "proxy_unavailable", "timeout", "transport_error",
                     "account_blocked", "rate_limited", "service_error", "verification_failed", "worker_failed"})
STAGES = frozenset({"starting", "browser_starting", "protocol_connecting", "email_submitted",
                   "password_submitted", "mfa_submitted", "waiting_callback", "exchanging_token",
                   "applying_credentials"})


class BrowserFailure(Exception):
    def __init__(self, code):
        self.code = code if code in FAILURES else "worker_failed"
        super().__init__(self.code)


def unpack_cloak(archive, destination):
    """Authenticate the complete archive before creating executable files."""
    with archive.open("rb") as source:
        if hashlib.file_digest(source, "sha256").hexdigest() != CLOAK_SHA256:
            raise BrowserFailure("browser_dependency_failed")
    total = 0
    with tarfile.open(archive, "r:gz") as package:
        for count, entry in enumerate(package, 1):
            if entry.isdir() and entry.name in (".", "./"):
                continue
            name = PurePosixPath(entry.name)
            if (count > 10000 or not name.parts or name.is_absolute()
                    or ".." in name.parts or "\\" in entry.name or ":" in entry.name):
                raise BrowserFailure("browser_dependency_failed")
            target = destination.joinpath(*name.parts)
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True, mode=0o700)
            elif entry.isfile():
                total += entry.size
                if entry.size < 0 or total > 1024 * 1024 * 1024:
                    raise BrowserFailure("browser_dependency_failed")
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                with target.open("xb") as output, package.extractfile(entry) as source:
                    shutil.copyfileobj(source, output)
                target.chmod(0o700 if entry.mode & 0o111 else 0o600)
            else:
                # Browser distributions must not install symlinks, devices or
                # hard links outside the private, versioned cache.
                raise BrowserFailure("browser_dependency_failed")


def ensure_cloak(root):
    """Install once under the shared runtime root; never fall back to Chrome."""
    if sys.platform != "linux" or platform.machine().lower() not in ("x86_64", "amd64"):
        raise BrowserFailure("browser_unavailable")
    import fcntl

    root = Path(root)
    destination = root / ("cloak-" + CLOAK_VERSION + "-linux-amd64")
    marker = destination / ".integrity"
    executable = destination / "chrome"
    try:
        root.mkdir(parents=True, exist_ok=True, mode=0o700)
        # Concurrent account jobs share one download, not three 217 MB copies.
        with (root / (".cloak-" + CLOAK_VERSION + ".lock")).open("a+b") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            if destination.exists():
                if (not destination.is_symlink() and marker.is_file()
                        and not marker.is_symlink() and marker.read_text() == CLOAK_SHA256
                        and executable.is_file() and not executable.is_symlink()
                        and os.access(executable, os.X_OK)):
                    return executable
                raise BrowserFailure("browser_dependency_failed")
            with tempfile.TemporaryDirectory(prefix=".cloak-download-", dir=root) as temp:
                archive = Path(temp) / "browser.tar.gz"
                digest = hashlib.sha256()
                received = 0
                deadline = time.monotonic() + 180
                with urlopen(CLOAK_URL, timeout=60) as response, archive.open("xb") as output:
                    while chunk := response.read(1024 * 1024):
                        received += len(chunk)
                        if received > CLOAK_ARCHIVE_SIZE or time.monotonic() > deadline:
                            raise BrowserFailure("browser_dependency_failed")
                        digest.update(chunk)
                        output.write(chunk)
                if received != CLOAK_ARCHIVE_SIZE or digest.hexdigest() != CLOAK_SHA256:
                    raise BrowserFailure("browser_dependency_failed")
                package = Path(temp) / "package"
                package.mkdir(mode=0o700)
                unpack_cloak(archive, package)
                if not (package / "chrome").is_file() or not os.access(package / "chrome", os.X_OK):
                    raise BrowserFailure("browser_dependency_failed")
                (package / ".integrity").write_text(CLOAK_SHA256)
                package.rename(destination)
            return executable
    except BrowserFailure:
        raise
    except Exception:
        raise BrowserFailure("browser_dependency_failed") from None


def unpack_playwright(data, destination):
    """Verify the lockfile integrity before extracting any executable source."""
    if base64.b64encode(hashlib.sha512(data).digest()).decode() != PLAYWRIGHT_SHA512:
        raise BrowserFailure("browser_dependency_failed")
    total = 0
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        for entry in archive:
            name = PurePosixPath(entry.name)
            if (not name.parts or name.parts[0] != "package" or name.is_absolute()
                    or ".." in name.parts or "\\" in entry.name or ":" in entry.name):
                raise BrowserFailure("browser_dependency_failed")
            target = destination.joinpath(*name.parts[1:])
            if entry.isdir():
                target.mkdir(parents=True, exist_ok=True, mode=0o700)
            elif entry.isfile():
                total += entry.size
                if entry.size < 0 or total > 64 * 1024 * 1024:
                    raise BrowserFailure("browser_dependency_failed")
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                with target.open("xb") as output, archive.extractfile(entry) as source:
                    shutil.copyfileobj(source, output)
                target.chmod(0o600)
            else:
                raise BrowserFailure("browser_dependency_failed")


def ensure_playwright(root):
    destination = root / ("playwright-core-" + PLAYWRIGHT_VERSION)
    marker = destination / ".integrity"
    if marker.is_file() and marker.read_text() == PLAYWRIGHT_SHA512:
        return destination / "index.mjs"
    try:
        with urlopen(f"https://registry.npmjs.org/playwright-core/-/playwright-core-{PLAYWRIGHT_VERSION}.tgz", timeout=60) as response:
            data = response.read(8 * 1024 * 1024 + 1)
        if len(data) > 8 * 1024 * 1024:
            raise BrowserFailure("browser_dependency_failed")
        with tempfile.TemporaryDirectory(prefix=".playwright-", dir=root) as temp:
            package = Path(temp) / "package"
            package.mkdir(mode=0o700)
            unpack_playwright(data, package)
            (package / ".integrity").write_text(PLAYWRIGHT_SHA512)
            try:
                package.rename(destination)
            except OSError:
                if not marker.is_file() or marker.read_text() != PLAYWRIGHT_SHA512:
                    raise BrowserFailure("browser_dependency_failed") from None
        return destination / "index.mjs"
    except BrowserFailure:
        raise
    except Exception:
        raise BrowserFailure("browser_dependency_failed") from None


def stop_child(child):
    if child.poll() is None:
        child.terminate()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=5)


def completion_api(api, worker):
    # The managed overlay owns the Codex-only change; the pinned Excel worker
    # and normal claim/progress calls retain their original request timeout.
    if not hasattr(api, "config"):
        return api
    config = replace(api.config, request_timeout=max(api.config.request_timeout, 60.0))
    return worker.WorkerAPI(config)


def run_browser(api, claim, worker, root=None):
    task_id = int(claim.get("task_id") or 0)
    if task_id <= 0 or not claim.get("login_email") or not claim.get("password"):
        raise BrowserFailure("worker_failed")
    root = Path(root or __file__).resolve().parent
    executable = os.environ.get("OPENAI_CODEX_BROWSER_EXECUTABLE", "").strip()
    if executable and not Path(executable).is_file():
        raise BrowserFailure("browser_unavailable")
    worker._best_effort_progress(api, task_id, "browser_starting")
    if not executable:
        executable = str(ensure_cloak(root.parent))
    playwright = ensure_playwright(root)
    node = os.environ.get("NODE_EXECUTABLE", "node")
    command = [node, str(root / "codex_browser.mjs"), str(playwright)]
    if sys.platform.startswith("linux"):
        if not shutil.which("xvfb-run") or not shutil.which("xauth"):
            raise BrowserFailure("browser_unavailable")
        # xvfb-run creates private Xauthority, selects a free display and traps
        # exit to clean it up. No shared desktop or public display port.
        command = ["xvfb-run", "--auto-servernum", "--server-args=-screen 0 1280x800x24 -nolisten tcp", *command]
    payload = {key: str(claim.get(key) or "") for key in
               ("login_email", "password", "totp_secret", "proxy_url", "expected_workspace", "preferred_workspace_id")}
    # No credentials in argv, process environment, screenshots, traces or files.
    with tempfile.TemporaryDirectory(prefix="sub2api-codex-browser-") as temp:
        env = {"PATH": os.defpath, "LANG": "C.UTF-8", "HOME": temp, "TMPDIR": temp,
               "TEMP": temp, "TMP": temp, "OPENAI_CODEX_BROWSER_EXECUTABLE": executable}
        if sys.platform == "win32":
            env["SystemRoot"] = os.environ.get("SystemRoot", r"C:\Windows")
        events = queue.Queue(maxsize=32)
        child = None
        credentials = None
        reader = None
        deadline = time.monotonic() + 240
        try:
            child = subprocess.Popen(command,
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                     env=env, cwd=temp)

            def read_events():
                total = 0
                try:
                    while True:
                        line = child.stdout.readline(65537)
                        if not line:
                            events.put(None, timeout=1)
                            return
                        total += len(line)
                        if len(line) > 65536 or total > 1024 * 1024:
                            events.put({"type": "error", "code": "worker_failed"}, timeout=1)
                            return
                        events.put(json.loads(line), timeout=1)
                except Exception:
                    try:
                        events.put({"type": "error", "code": "worker_failed"}, timeout=1)
                    except queue.Full:
                        pass

            reader = threading.Thread(target=read_events, daemon=True)
            reader.start()
            child.stdin.write(json.dumps(payload).encode())
            child.stdin.close()
            payload.clear()
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise BrowserFailure("timeout")
                try:
                    event = events.get(timeout=remaining)
                except queue.Empty:
                    raise BrowserFailure("timeout") from None
                if event is None:
                    break
                if not isinstance(event, dict):
                    raise BrowserFailure("worker_failed")
                if event.get("type") == "stage" and event.get("stage") in STAGES:
                    worker._best_effort_progress(api, task_id, event["stage"])
                elif event.get("type") == "result" and credentials is None:
                    credentials = event.get("credentials")
                elif event.get("type") == "error":
                    raise BrowserFailure(event.get("code"))
                else:
                    raise BrowserFailure("worker_failed")
            if child.wait(timeout=max(1, deadline - time.monotonic())) != 0:
                raise BrowserFailure("worker_failed")
        except (OSError, subprocess.SubprocessError):
            raise BrowserFailure("browser_start_failed") from None
        finally:
            payload.clear()
            if child is not None:
                stop_child(child)
                child.stdout.close()
                if child.stdin and not child.stdin.closed:
                    child.stdin.close()
            if reader is not None:
                reader.join(timeout=2)
    if not isinstance(credentials, dict) or not all(isinstance(credentials.get(k), str) and credentials[k]
            for k in ("access_token", "refresh_token", "id_token", "client_id")):
        raise BrowserFailure("worker_failed")
    worker._best_effort_progress(api, task_id, "applying_credentials")
    try:
        result = completion_api(api, worker).credentials(task_id, credentials, {})
    except Exception:
        raise BrowserFailure("verification_failed") from None
    finally:
        credentials.clear()
    if not result or result.get("status") != "succeeded":
        raise BrowserFailure("verification_failed")


def install(worker):
    original_claim = worker.process_password_claim
    original_failure = worker.safe_worker_failure

    def process(api, claim):
        if (claim.get("oauth_profile", "codex") == "codex"
                and claim.get("engine", "local_worker") == "local_worker"):
            return run_browser(api, claim, worker)
        return original_claim(api, claim)

    worker.process_password_claim = process
    def safe_failure(exc):
        if isinstance(exc, BrowserFailure):
            return exc.code
        # WorkerAPI.fail sanitizes the already-safe code a second time.
        if isinstance(exc, worker.WorkerError) and str(exc) in FAILURES:
            return str(exc)
        return original_failure(exc)
    worker.safe_worker_failure = safe_failure


if __name__ == "__main__":
    base = Path(sys.argv.pop(1)).resolve()
    sys.path.insert(0, str(base))
    legacy = importlib.import_module("worker")
    install(legacy)
    raise SystemExit(legacy.main())
