"""Codex protocol adapter; retain the pinned Excel/BPS worker unchanged."""
from __future__ import annotations
from dataclasses import replace
import importlib
import json
import os
from pathlib import Path
import queue
import re
import signal
import subprocess
import sys
import tempfile
import threading
import time

FAILURES = frozenset({"security_check", "additional_verification", "password_rejected", "totp_rejected",
                     "identity_mismatch", "workspace_unavailable", "workspace_selection_required", "proxy_unavailable", "timeout", "transport_error",
                     "account_blocked", "rate_limited", "service_error", "verification_failed", "worker_failed"})
STAGES = frozenset({"starting", "protocol_connecting", "email_submitted",
                   "password_submitted", "mfa_submitted", "waiting_callback", "exchanging_token",
                   "applying_credentials"})
ACTIONS = frozenset({"initializing", "open_login", "read_page",
                     "choose_password", "fill_email", "submit_email", "fill_password", "submit_password",
                     "fill_mfa", "submit_mfa", "select_workspace", "submit_workspace", "wait_next_page",
                     "security_check", "exchange_token", "validate_tokens", "protocol_process", "apply_credentials"})
DIAGNOSTIC = re.compile(r"codex_protocol_failed stage=([a-z_]+) action=([a-z_]+) reason=([a-z_]+) "
                        r"elapsed_ms=([0-9]{1,6}) action_ms=([0-9]{1,6}) http_status=([0-9]{1,3}) response=(none|document|auth_request|token)")
EVIDENCE = re.compile(r"cf_mitigated=(?:none|challenge|other) challenge_signal=(?:none|header|body|both) "
                      r"body_marker=(?:none|just_a_moment|cf_chl|challenge_platform) server=(?:none|cloudflare|other) "
                      r"cf_ray=(?:none|[0-9a-fA-F]{16}(?:-[A-Z]{3})?) retry_after=(none|[0-9]{1,5}) content_type=(?:none|html|json|other)")


def valid_evidence(raw):
    match = EVIDENCE.fullmatch(raw) if isinstance(raw, str) and len(raw) <= 256 else None
    return match is not None and (match[1] == 'none' or int(match[1]) <= 86400)


def safe_diagnostic(code, data):
    if not isinstance(code, str) or code not in FAILURES or not isinstance(data, dict):
        return None
    if (not isinstance(data.get("stage"), str) or data["stage"] not in STAGES
            or not isinstance(data.get("action"), str) or data["action"] not in ACTIONS):
        return None
    elapsed, action, status = (data.get(k) for k in ("elapsed_ms", "action_ms", "http_status"))
    if (any(type(value) is not int for value in (elapsed, action, status))
            or not 0 <= action <= elapsed <= 360000
            or status != 0 and not 100 <= status <= 599
            or data.get("response") not in ("none", "document", "auth_request", "token")
            or (data["response"] == "none") != (status == 0)):
        return None
    base = (f"codex_protocol_failed stage={data['stage']} action={data['action']} reason={code} "
            f"elapsed_ms={elapsed} action_ms={action} http_status={status} response={data['response']}")
    if 'evidence' not in data:
        return base
    if valid_evidence(data['evidence']) and status:
        return base + ' ' + data['evidence']
    return None


def parse_diagnostic(raw):
    if not isinstance(raw, str) or len(raw) > 512:
        return None
    base, separator, fields = raw.partition(' cf_mitigated=')
    match = DIAGNOSTIC.fullmatch(base)
    if not match:
        return None
    stage, action, reason, elapsed, duration, status, response = match.groups()
    data = dict(stage=stage, action=action, elapsed_ms=int(elapsed),
                action_ms=int(duration), http_status=int(status), response=response)
    if separator:
        data['evidence'] = 'cf_mitigated=' + fields
    return safe_diagnostic(reason, data)


class ProtocolFailure(Exception):
    def __init__(self, code, diagnostic=None):
        self.code = code if isinstance(code, str) and code in FAILURES else "worker_failed"
        self.diagnostic = safe_diagnostic(self.code, diagnostic)
        super().__init__(self.code)


def stop_child(child):
    # Allow the interpreter to close its sessions and reap the task child.
    if child.poll() is None:
        try:
            child.wait(timeout=3)
            return
        except subprocess.TimeoutExpired:
            pass
        child.terminate()
        try:
            child.wait(timeout=10)
        except subprocess.TimeoutExpired:
            if sys.platform.startswith("linux"):
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            else:
                child.kill()
            child.wait(timeout=5)


def supervise_protocol(parent, command):
    """Own only this task's descendants, including any detached children.

    Run in a separate interpreter, not preexec_fn in the threaded worker. Linux
    subreaping keeps descendants ours when the protocol interpreter exits; parent-death
    SIGTERM covers a worker slot being killed without running its finally.
    """
    import ctypes

    def terminate(_signum, _frame):
        raise SystemExit(143)

    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(36, 1, 0, 0, 0) != 0 or libc.prctl(1, signal.SIGTERM, 0, 0, 0) != 0:
        return 1  # PR_SET_CHILD_SUBREAPER / PR_SET_PDEATHSIG
    if os.getppid() != parent:
        return 143

    def descendants():
        found, pending = [], [os.getpid()]
        while pending:
            pid = pending.pop()
            try:
                children = Path(f"/proc/{pid}/task/{pid}/children").read_text().split()
            except FileNotFoundError:
                continue
            for value in children:
                child = int(value)
                if child not in found:
                    found.append(child)
                    pending.append(child)
        return found

    try:
        return subprocess.Popen(command).wait()
    finally:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        for sig, seconds in ((signal.SIGTERM, 4), (signal.SIGKILL, 2)):
            end = time.monotonic() + seconds
            while True:
                # Discover only live descendants each pass, never a saved PID
                # list that could now refer to another account's process.
                for pid in reversed(descendants()):
                    try:
                        os.kill(pid, sig)
                    except ProcessLookupError:
                        pass
                try:
                    while os.waitpid(-1, os.WNOHANG)[0]:
                        pass
                except ChildProcessError:
                    pass
                if not descendants() or time.monotonic() >= end:
                    break
                time.sleep(0.05)
        if descendants():
            print(json.dumps({"type": "error", "code": "worker_failed"}), flush=True)


def completion_api(api, worker):
    # The managed overlay owns the Codex-only change; the pinned Excel worker
    # and normal claim/progress calls retain their original request timeout.
    if not hasattr(api, "config"):
        return api
    config = replace(api.config, request_timeout=max(api.config.request_timeout, 60.0))
    return worker.WorkerAPI(config)


def run_protocol(api, claim, worker, root=None):
    task_id = int(claim.get("task_id") or 0)
    if task_id <= 0 or not claim.get("login_email") or not claim.get("password"):
        raise ProtocolFailure("worker_failed")
    root = Path(root or __file__).resolve().parent
    worker._best_effort_progress(api, task_id, "protocol_connecting")
    python = os.environ.get("TOSUB2_PYTHON") or sys.executable
    command = [python, str(root / "codex_protocol.py")]
    if sys.platform.startswith("linux"):
        command = [python, str(Path(__file__).resolve()), "--protocol-supervisor", str(os.getpid()), *command]
    payload = {key: str(claim.get(key) or "") for key in
               ("login_email", "password", "totp_secret", "proxy_url", "expected_workspace", "preferred_workspace_id")}
    # No credentials in argv, process environment, screenshots, traces or files.
    with tempfile.TemporaryDirectory(prefix="sub2api-codex-protocol-") as temp:
        env = {"PATH": os.defpath, "LANG": "C.UTF-8", "HOME": temp, "TMPDIR": temp,
               "TEMP": temp, "TMP": temp, "PYTHONDONTWRITEBYTECODE": "1"}
        if sys.platform == "win32":
            env["SystemRoot"] = os.environ.get("SystemRoot", r"C:\Windows")
        events = queue.Queue(maxsize=32)
        child = None
        credentials = None
        failure = None
        last_stage = "protocol_connecting"
        reader = None
        started = time.monotonic()
        deadline = started + 240

        def process_failure(code):
            elapsed = min(360000, int((time.monotonic() - started) * 1000))
            return ProtocolFailure(code, dict(stage=last_stage, action="protocol_process", elapsed_ms=elapsed,
                                             action_ms=elapsed, http_status=0, response="none"))

        try:
            child = subprocess.Popen(command,
                                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                     env=env, cwd=temp, start_new_session=sys.platform.startswith("linux"))

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
                    raise failure or process_failure("timeout")
                try:
                    event = events.get(timeout=remaining)
                except queue.Empty:
                    raise failure or process_failure("timeout") from None
                if event is None:
                    break
                if not isinstance(event, dict):
                    raise ProtocolFailure("worker_failed")
                if event.get("type") == "stage" and event.get("stage") in STAGES:
                    last_stage = event["stage"]
                    worker._best_effort_progress(api, task_id, event["stage"])
                elif event.get("type") == "forbidden":
                    evidence = event.get("evidence")
                    if valid_evidence(evidence):
                        # An intermediate observation must never fail a login.
                        try:
                            client = worker.WorkerAPI(replace(api.config, request_timeout=1.0))
                            client._post(f"/api/v1/internal/openai-reauth/{task_id}/observe",
                                         {"worker_id": api.config.worker_id, "evidence": evidence})
                        except (worker.WorkerError, OSError):
                            pass
                elif event.get("type") == "result" and credentials is None:
                    if failure is not None:
                        raise failure
                    credentials = event.get("credentials")
                elif event.get("type") == "error":
                    candidate = ProtocolFailure(event.get("code"), event.get("diagnostic"))
                    failure = failure or (candidate if candidate.diagnostic else process_failure(candidate.code))
                    deadline = min(deadline, time.monotonic() + 10)
                else:
                    raise ProtocolFailure("worker_failed")
            exit_code = child.wait(timeout=max(1, deadline - time.monotonic()))
            if failure is not None:
                raise failure
            if exit_code != 0:
                raise process_failure("worker_failed")
        except (OSError, subprocess.SubprocessError):
            raise process_failure("worker_failed") from None
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
        raise ProtocolFailure("worker_failed")
    worker._best_effort_progress(api, task_id, "applying_credentials")
    try:
        result = completion_api(api, worker).credentials(task_id, credentials, {})
    except Exception:
        raise ProtocolFailure("verification_failed") from None
    finally:
        credentials.clear()
    if not result or result.get("status") != "succeeded":
        raise ProtocolFailure("verification_failed")


def install(worker):
    original_claim = worker.process_password_claim
    original_failure = worker.safe_worker_failure

    def process(api, claim):
        if (claim.get("oauth_profile", "codex") == "codex"
                and claim.get("engine", "local_worker") == "local_worker"):
            return run_protocol(api, claim, worker)
        return original_claim(api, claim)

    worker.process_password_claim = process
    def safe_failure(exc):
        if isinstance(exc, ProtocolFailure):
            return exc.diagnostic or exc.code
        # WorkerAPI.fail sanitizes the already-safe code a second time.
        if isinstance(exc, worker.WorkerError) and str(exc).startswith("codex_protocol_failed"):
            return parse_diagnostic(str(exc)) or "worker_failed"
        if isinstance(exc, worker.WorkerError) and str(exc) in FAILURES:
            return str(exc)
        return original_failure(exc)
    worker.safe_worker_failure = safe_failure


if __name__ == "__main__":
    if len(sys.argv) > 3 and sys.argv[1] == "--protocol-supervisor":
        raise SystemExit(supervise_protocol(int(sys.argv[2]), sys.argv[3:]))
    base = Path(sys.argv.pop(1)).resolve()
    sys.path.insert(0, str(base))
    legacy = importlib.import_module("worker")
    install(legacy)
    raise SystemExit(legacy.main())
