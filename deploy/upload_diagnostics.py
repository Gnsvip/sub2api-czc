#!/usr/bin/env python3
"""Receive bounded Nginx upload-failure metadata; never receive request bodies."""

import argparse
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import re
import signal
import socket
import stat
import time

BUCKET_BYTES = 32 * 1024
RETENTION_SECONDS = 3600
RECORD_BYTES = 2048
FILE_RE = re.compile(r"upload-[0-9]{8}T[0-9]{4}Z\.jsonl")
HEX_ID = r"[0-9a-fA-F]{32}"
UUID = r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}"
NUMBER = r"[0-9]{1,12}(?:\.[0-9]{1,3})?"
INTEGER = r"[0-9]{1,15}"
PATTERNS = {
    "nginx_time": NUMBER,
    "edge_request_id": HEX_ID,
    "app_request_id": rf"(?:{HEX_ID}|{UUID})?",
    "incoming_client_request_id": rf"(?:{UUID})?",
    "app_client_request_id": rf"(?:{UUID})?",
    "edge_ray": r"(?:[0-9a-fA-F]{16}-[A-Z]{3})?",
    "host": r"(?:api\.aifxo\.com|www\.aifxo\.com|aifxo\.com|other)",
    "route": r"(?:responses|compact|chat|messages|images|gemini)",
    "status": r"(?:408|499)",
    "client_protocol": r"HTTP/(?:1\.0|1\.1|2\.0|3\.0)",
    "encoding": r"(?:identity|gzip|zstd|deflate|other)",
    "declared_body_bytes": rf"(?:{INTEGER}|-)?",
    "request_bytes_including_headers": INTEGER,
    "request_complete": r"(?:OK)?",
    "request_seconds": NUMBER,
    "upstream_status": r"(?:-|[0-9]{3})(?:[ ,:]+(?:-|[0-9]{3})){0,5}",
    "upstream_seconds": rf"(?:-|{NUMBER})(?:[ ,:]+(?:-|{NUMBER})){{0,5}}",
    "upstream_bytes_sent_including_headers": rf"(?:-|{INTEGER})(?:[ ,:]+(?:-|{INTEGER})){{0,5}}",
}


def parse_datagram(data):
    if len(data) > 4096:
        return None
    _, separator, payload = data.partition(b"sub2api_upload: ")
    if not separator or len(payload) > RECORD_BYTES:
        return None
    try:
        source = json.loads(payload)
    except (ValueError, UnicodeDecodeError):
        return None
    if not isinstance(source, dict):
        return None
    row = {}
    for key, pattern in PATTERNS.items():
        value = source.get(key)
        if not isinstance(value, str) or re.fullmatch(pattern, value) is None:
            return None
        row[key] = value
    # Project only this closed schema, even if a future log format adds fields.
    return row


class BucketStore:
    def __init__(self, directory, limit=BUCKET_BYTES):
        self.directory = Path(directory)
        self.limit = limit
        self.dropped = 0
        self.last_cleanup = 0
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        info = self.directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
            raise ValueError("diagnostic directory must be private")

    def cleanup(self, now):
        for path in self.directory.iterdir():
            if not FILE_RE.fullmatch(path.name) or not stat.S_ISREG(path.lstat().st_mode):
                continue
            try:
                stamp = dt.datetime.strptime(path.name, "upload-%Y%m%dT%H%MZ.jsonl").replace(tzinfo=dt.timezone.utc).timestamp()
            except ValueError:
                continue
            if stamp <= now - RETENTION_SECONDS or stamp > now:
                path.unlink()
        self.last_cleanup = now

    def maintain(self, now):
        if int(now // 60) != int(self.last_cleanup // 60):
            self.cleanup(now)

    def write(self, row, now):
        self.maintain(now)
        event = dict(row, collected_at=dt.datetime.fromtimestamp(now, dt.timezone.utc).isoformat())
        if self.dropped:
            event["dropped_due_to_capacity"] = self.dropped
        line = (json.dumps(event, separators=(",", ":"), ensure_ascii=True) + "\n").encode()
        if len(line) > RECORD_BYTES:
            return False
        name = dt.datetime.fromtimestamp(now, dt.timezone.utc).strftime("upload-%Y%m%dT%H%MZ.jsonl")
        fd = os.open(self.directory / name, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_nlink != 1:
                raise ValueError("unsafe diagnostic file")
            if info.st_size + len(line) > self.limit:
                self.dropped = min(self.dropped + 1, 2**31 - 1)
                return False
            with os.fdopen(fd, "ab", closefd=False) as stream:
                stream.write(line)
            self.dropped = 0
            return True
        finally:
            os.close(fd)


def serve(socket_path, store):
    socket_path = Path(socket_path)
    socket_path.parent.mkdir(mode=0o750, parents=True, exist_ok=True)
    # A second collector must not unlink the live receiver's socket.
    lock_fd = os.open(socket_path.with_suffix(".lock"), os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lock_fd, "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            info = socket_path.lstat()
        except FileNotFoundError:
            info = None
        if info is not None:
            if not stat.S_ISSOCK(info.st_mode):
                raise ValueError("refusing to replace a non-socket")
            socket_path.unlink()
        stopped = False

        def stop(*_):
            nonlocal stopped
            stopped = True

        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        last_warning = 0
        with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as receiver:
            receiver.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 64 * 1024)
            receiver.bind(str(socket_path))
            os.chmod(socket_path, 0o660)
            receiver.settimeout(1)
            try:
                store.cleanup(time.time())
                while not stopped:
                    try:
                        # One bounded datagram at a time; no application queue.
                        data = receiver.recv(4097)
                    except socket.timeout:
                        data = b""
                    now = time.time()
                    try:
                        store.maintain(now)  # Also expires records during idle periods.
                        row = parse_datagram(data)
                        if row is not None:
                            store.write(row, now)
                    except (OSError, ValueError):
                        if now - last_warning >= 60:
                            print("upload diagnostic storage unavailable", flush=True)
                            last_warning = now
            finally:
                socket_path.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--socket", default="/run/sub2api-upload-diagnostics/access.sock")
    parser.add_argument("--directory", default="/var/lib/sub2api-upload-diagnostics")
    args = parser.parse_args()
    os.umask(0o007)
    serve(args.socket, BucketStore(args.directory))


if __name__ == "__main__":
    main()
