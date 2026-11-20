"""Linux CI regressions for bounded storage and real Nginx ingress behaviour.

The loopback upstream is a body-reading stub, not Sub2API or a model service.
Existing Go ingress tests separately cover Sub2API's body reader.
"""

import datetime as dt
import gzip
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import http.client
import json
from pathlib import Path
import queue
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest

DEPLOY = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(DEPLOY))
import upload_diagnostics as diagnostics
import upload_probe


def sample():
    return {
        "nginx_time": "1800000000.001", "edge_request_id": "a" * 32,
        "app_request_id": "a" * 32, "incoming_client_request_id": "",
        "app_client_request_id": "", "edge_ray": "", "host": "api.aifxo.com",
        "route": "responses", "status": "408", "client_protocol": "HTTP/1.1",
        "encoding": "identity", "declared_body_bytes": "1048576",
        "request_bytes_including_headers": "20000", "request_complete": "",
        "request_seconds": "180.005", "upstream_status": "408",
        "upstream_seconds": "180.004", "upstream_bytes_sent_including_headers": "20001",
    }


def packet(row):
    return b"<190>Nov 20 12:00:00 sub2api_upload: " + json.dumps(row).encode()


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="u408-")
        self.addCleanup(self.tmp.cleanup)
        self.directory = Path(self.tmp.name)
        self.store = diagnostics.BucketStore(self.directory)

    def test_schema_projects_only_metadata_and_rejects_malformed_values(self):
        row = sample()
        self.assertEqual(diagnostics.parse_datagram(packet(dict(row, authorization="secret", body="private"))), row)
        for data in (b"garbage", b"x" * 4097, packet([]), packet(dict(row, status="200")),
                     packet(dict(row, host="secret.invalid")), packet(dict(row, app_request_id="\nsecret")),
                     packet(dict(row, declared_body_bytes="secret")), packet(dict(row, request_seconds=None))):
            with self.subTest(data=data[:40]):
                self.assertIsNone(diagnostics.parse_datagram(data))

    def test_capacity_is_bounded_and_expiry_does_not_need_new_requests(self):
        start = 1800000000  # An exact UTC minute.
        for minute in range(70):
            for _ in range(80):
                self.store.write(sample(), start + minute * 60 + 1)
        files = list(self.directory.glob("upload-*.jsonl"))
        self.assertEqual(len(files), 60)
        self.assertTrue(all(path.stat().st_size <= diagnostics.BUCKET_BYTES for path in files))
        self.assertLessEqual(sum(path.stat().st_size for path in files), 60 * diagnostics.BUCKET_BYTES)
        events = [json.loads(line) for path in files for line in path.read_text().splitlines()]
        self.assertTrue(any(event.get("dropped_due_to_capacity", 0) > 0 for event in events))
        self.store.maintain(start + 130 * 60)
        self.assertEqual(list(self.directory.glob("upload-*.jsonl")), [])

    def test_private_files_no_symlink_following_and_restart_expiry(self):
        now = 1800000000
        self.store.write(sample(), now)
        path = next(self.directory.glob("upload-*.jsonl"))
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        path.unlink()
        target = self.directory / "unrelated"
        target.write_text("keep")
        path.symlink_to(target)
        with self.assertRaises(OSError):
            self.store.write(sample(), now)
        self.assertEqual(target.read_text(), "keep")
        path.unlink()
        self.store.write(sample(), now)
        diagnostics.BucketStore(self.directory).cleanup(now + 3601)
        self.assertFalse(path.exists())
        self.assertEqual(target.read_text(), "keep")


def wait_until(predicate, timeout=5):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.025)
    raise AssertionError("timed out waiting for local test condition")


def stop_process(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
    if process.stdout:
        process.stdout.close()
    if process.stderr:
        process.stderr.close()


class CollectorProcessTests(unittest.TestCase):
    def test_idle_cleanup_and_second_process_does_not_take_socket(self):
        with tempfile.TemporaryDirectory(prefix="u408-") as directory:
            root = Path(directory)
            store = diagnostics.BucketStore(root / "data")
            store.write(sample(), time.time() - 7200)
            command = [sys.executable, "-B", str(DEPLOY / "upload_diagnostics.py"),
                       "--socket", str(root / "access.sock"), "--directory", str(root / "data")]
            process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                wait_until(lambda: (root / "access.sock").exists())
                wait_until(lambda: not list((root / "data").glob("upload-*.jsonl")))
                inode = (root / "access.sock").stat().st_ino
                second = subprocess.run(command, capture_output=True, timeout=5)
                self.assertNotEqual(second.returncode, 0)
                self.assertEqual((root / "access.sock").stat().st_ino, inode)
                with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as sender:
                    sender.sendto(packet(sample()), str(root / "access.sock"))
                wait_until(lambda: list((root / "data").glob("upload-*.jsonl")))
                # Check expiry while no datagrams arrive, without a one-hour test wait.
                old = dt.datetime.fromtimestamp(time.time() - 7200, dt.timezone.utc)
                stale = root / "data" / old.strftime("upload-%Y%m%dT%H%MZ.jsonl")
                stale.write_text("{}\n")
                # The maintenance loop runs at minute boundaries (at most 61 s).
                wait_until(lambda: not stale.exists(), timeout=62)
            finally:
                stop_process(process)
            self.assertFalse((root / "access.sock").exists())


class BodyReader(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass  # Never log synthetic Authorization headers, queries or bodies.

    def do_POST(self):
        self.connection.settimeout(5)
        size = int(self.headers.get("Content-Length", "0"))
        try:
            body = self.rfile.read(size)
            if len(body) != size:
                return
            decoded = gzip.decompress(body) if self.headers.get("Content-Encoding") == "gzip" else body
            self.server.received.put({
                "wire_bytes": len(body), "decoded_bytes": len(decoded),
                "invalid_json": decoded.startswith(b"!"),
                "request_id": self.headers.get("X-Request-ID", ""),
                "host": self.headers.get("Host", ""),
            })
            if self.headers.get("X-Test-Hold") == "1":
                self.server.request_ready.set()
                self.server.release_response.wait(timeout=5)
            status = 408 if self.headers.get("X-Test-Status") == "408" else 400
            payload = json.dumps({"error": {"type": "invalid_request_error",
                                  "message": "解析请求体失败 / Failed to parse request body"}}).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.send_header("X-Request-ID", self.headers.get("X-Request-ID", ""))
            self.send_header("X-Client-Request-ID", "11111111-2222-3333-4444-555555555555")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(payload)
        except (OSError, EOFError):
            pass  # Expected when Nginx closes an incomplete upload.

    do_GET = do_POST


class NginxTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        nginx = shutil.which("nginx")
        if not nginx:
            raise RuntimeError("Nginx is required; this regression must not silently skip")
        cls.tmp = tempfile.TemporaryDirectory(prefix="u408-")
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.root = Path(cls.tmp.name)
        cls.backend = ThreadingHTTPServer(("127.0.0.1", 0), BodyReader)
        cls.backend.daemon_threads = True
        cls.backend.received = queue.Queue()
        cls.backend.request_ready = threading.Event()
        cls.backend.release_response = threading.Event()
        thread = threading.Thread(target=cls.backend.serve_forever, daemon=True)
        thread.start()
        cls.addClassCleanup(cls.backend.server_close)
        cls.addClassCleanup(cls.backend.shutdown)
        # Reserve an unused loopback port, then let Nginx bind it.
        with socket.socket() as reserve:
            reserve.bind(("127.0.0.1", 0))
            cls.port = reserve.getsockname()[1]
        cls.url = f"http://127.0.0.1:{cls.port}/v1/responses"
        cls.sock_path = cls.root / "access.sock"
        cls.data = cls.root / "data"
        cls.collector = subprocess.Popen(
            [sys.executable, "-B", str(DEPLOY / "upload_diagnostics.py"),
             "--socket", str(cls.sock_path), "--directory", str(cls.data)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        cls.addClassCleanup(stop_process, cls.collector)
        wait_until(cls.sock_path.exists)
        location = (DEPLOY / "nginx/upload-diagnostics-location.conf").read_text().replace(
            "/run/sub2api-upload-diagnostics/access.sock", str(cls.sock_path))
        configuration = f"""
daemon off;
master_process off;
pid {cls.root}/nginx.pid;
error_log {cls.root}/error.log warn;
events {{ worker_connections 64; }}
http {{
    access_log off;
    client_body_temp_path {cls.root}/body;
    proxy_temp_path {cls.root}/proxy;
    include {DEPLOY}/nginx/upload-diagnostics-http.conf;
    server {{
        listen 127.0.0.1:{cls.port};
        client_max_body_size 32m;
        client_body_timeout 2s;
        location / {{
            proxy_pass http://127.0.0.1:{cls.backend.server_port};
            proxy_http_version 1.1;
            proxy_request_buffering off;
            proxy_buffering off;
            proxy_set_header Host $http_host;
            proxy_set_header Connection close;
            {location}
        }}
    }}
}}
"""
        config = cls.root / "nginx.conf"
        config.write_text(configuration)
        subprocess.run([nginx, "-t", "-p", str(cls.root), "-c", str(config)], check=True, capture_output=True)
        cls.nginx = subprocess.Popen([nginx, "-p", str(cls.root), "-c", str(config)],
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        cls.addClassCleanup(stop_process, cls.nginx)

        def listening():
            if cls.nginx.poll() is not None:
                raise AssertionError("test Nginx exited")
            try:
                with socket.create_connection(("127.0.0.1", cls.port), timeout=0.2):
                    return True
            except OSError:
                return False

        wait_until(listening)

    def records(self):
        return [json.loads(line) for path in self.data.glob("upload-*.jsonl")
                for line in path.read_text().splitlines() if line]

    def failed_request(self, path="/v1/responses", headers=None, method="POST", body=b"!synthetic"):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=5)
        try:
            connection.request(method, path, body=body, headers={
                "Host": "api.aifxo.com", "X-Test-Status": "408", **(headers or {}),
            })
            response = connection.getresponse()
            response.read()
            return response.status, response.getheader("X-Request-ID")
        finally:
            connection.close()

    def test_complete_pause_resume_and_gzip_upload(self):
        before = len(self.records())
        for size, encoding, pause in ((1024 * 1024, "identity", 0),
                                      (18_815_061, "identity", 0),
                                      (1024 * 1024, "identity", 0.25),
                                      (1024 * 1024, "gzip", 0)):
            with self.subTest(size=size, encoding=encoding, pause=pause):
                result = upload_probe.probe(self.url, "synthetic-test-key", size, encoding, pause_seconds=pause)
                self.assertTrue(result.get("json_validation_rejected"), result)
                self.assertEqual(result["sent_body_bytes"], result["wire_body_bytes"])
                self.assertEqual(result["pause_applied"], bool(pause))
                self.assertRegex(result["request_id"], r"^[0-9a-f]{32}$")
                self.assertEqual(result["app_client_request_id"], "11111111-2222-3333-4444-555555555555")
                rows = []
                while not self.backend.received.empty():
                    rows.append(self.backend.received.get_nowait())
                row = next(item for item in rows if item["request_id"] == result["request_id"])
                self.assertEqual(row["decoded_bytes"], size)
                self.assertTrue(row["invalid_json"])
                self.assertEqual(row["host"], f"127.0.0.1:{self.port}")
                if encoding == "gzip":
                    self.assertLess(row["wire_bytes"], size)
        time.sleep(0.1)
        self.assertEqual(len(self.records()), before)

    def test_edge_timeout_and_completed_upload_abort_are_distinct_from_upstream_408(self):
        incoming_id = "22222222-3333-4444-5555-666666666666"
        before = len(self.records())
        with socket.create_connection(("127.0.0.1", self.port), timeout=5) as connection:
            connection.sendall(("POST /v1/responses HTTP/1.1\r\nHost: api.aifxo.com\r\n"
                                "Content-Length: 1048576\r\nConnection: close\r\n"
                                f"X-Client-Request-ID: {incoming_id}\r\n\r\n!").encode())
            reply = connection.recv(4096)
            if reply:  # Some timeout paths close without sending a response body.
                self.assertIn(b" 408 ", reply)
        row = wait_until(lambda: self.records()[before:])[0]
        self.assertEqual(row["status"], "408")
        self.assertEqual(row["incoming_client_request_id"], incoming_id)
        self.assertEqual(row["app_request_id"], "")
        self.assertEqual(row["app_client_request_id"], "")
        self.assertLess(int(row["request_bytes_including_headers"]), 1048576)

        # A real 499 AFTER the full body reached the upstream must not be
        # interpreted as evidence of an incomplete upload.
        before = len(self.records())
        try:
            with socket.create_connection(("127.0.0.1", self.port), timeout=5) as connection:
                connection.sendall(b"POST /v1/responses HTTP/1.1\r\nHost: api.aifxo.com\r\n"
                                   b"Content-Length: 10\r\nX-Test-Hold: 1\r\n\r\n!synthetic")
                self.assertTrue(self.backend.request_ready.wait(timeout=5))
                connection.shutdown(socket.SHUT_RDWR)
            row = wait_until(lambda: self.records()[before:])[0]
            self.assertEqual(row["status"], "499")
            self.assertEqual(row["declared_body_bytes"], "10")
            self.assertGreater(int(row["upstream_bytes_sent_including_headers"]), 10)
        finally:
            self.backend.release_response.set()
        before = len(self.records())
        status, request_id = self.failed_request()
        self.assertEqual(status, 408)
        row = wait_until(lambda: self.records()[before:])[0]
        self.assertEqual(row["upstream_status"], "408")
        self.assertEqual(row["edge_request_id"], request_id)
        self.assertEqual(row["app_request_id"], request_id)
        self.assertEqual(row["app_client_request_id"], "11111111-2222-3333-4444-555555555555")

    def test_only_api_failures_logged_without_credentials_queries_or_bodies(self):
        before = len(self.records())
        status, _ = self.failed_request("/v1/responses?token=PRIVATE_QUERY", {
            "Authorization": "Bearer PRIVATE_AUTHORIZATION", "X-Client-Request-ID": "PRIVATE_ID",
        })
        self.assertEqual(status, 408)
        row = wait_until(lambda: self.records()[before:])[0]
        self.assertEqual(row["incoming_client_request_id"], "")
        self.assertEqual(row["route"], "responses")
        self.assertEqual(row["host"], "api.aifxo.com")
        serialized = json.dumps(self.records())
        for marker in ("PRIVATE_", "!synthetic", "Authorization", "token="):
            self.assertNotIn(marker, serialized)
        before = len(self.records())
        self.assertEqual(self.failed_request("/guide")[0], 408)
        # A GET does not enter our POST-only failure collection.
        self.assertEqual(self.failed_request(method="GET")[0], 408)
        time.sleep(0.1)
        self.assertEqual(len(self.records()), before)

    def test_aliases_and_gzip_failure_are_classified(self):
        for path, route in (("/backend-api/codex/responses", "responses"),
                            ("/responses/compact", "compact"),
                            ("/antigravity/v1/messages", "messages"),
                            ("/v1/images/generations/async", "images"),
                            ("/antigravity/v1beta/models/example:generateContent", "gemini")):
            before = len(self.records())
            self.assertEqual(self.failed_request(path, {"Content-Encoding": "gzip"},
                                                body=gzip.compress(b"!synthetic"))[0], 408)
            row = wait_until(lambda: self.records()[before:])[0]
            self.assertEqual(row["route"], route)
            self.assertEqual(row["encoding"], "gzip")

    def test_z_unavailable_collector_does_not_change_http_response(self):
        stop_process(self.collector)
        self.assertFalse(self.sock_path.exists())
        self.assertEqual(self.failed_request()[0], 408)


class ProbeValidationTests(unittest.TestCase):
    def test_unsafe_targets_or_inputs_are_rejected_before_connect(self):
        for url, options in (
            ("https://example.invalid/v1/responses?key=secret", {}),
            ("https://example.invalid/v1/chat/completions", {}),
            ("http://example.invalid/v1/responses", {}),
            ("http://localhost/v1/responses", {"connect_ip": "203.0.113.1"}),
            ("https://example.invalid/v1/responses", {"pause_seconds": float("nan")}),
        ):
            with self.subTest(url=url), self.assertRaises(ValueError):
                upload_probe.probe(url, "test-only", 1024, **options)
        self.assertEqual(upload_probe.response_id("secret-or-newline\n"), "")


if __name__ == "__main__":
    unittest.main()
