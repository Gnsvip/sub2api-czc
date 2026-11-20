#!/usr/bin/env python3
"""Send one synthetic invalid JSON upload, which must stop before model forwarding.

The API key is read from stdin, never an argument, file, or printed field. The
test body is deliberately invalid JSON from its first byte; no customer history
or model is sent. A 400 JSON-validation response is the expected control result.
"""

import argparse
import base64
import gzip
import http.client
import ipaddress
import json
import random
import re
import socket
import ssl
import sys
import time
from urllib.parse import urlsplit
import uuid


class OriginHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, host, port, connect_ip, timeout):
        super().__init__(host, port, timeout=timeout, context=ssl.create_default_context())
        self.connect_ip = connect_ip

    def connect(self):
        raw = socket.create_connection((self.connect_ip or self.host, self.port), self.timeout)
        try:
            self.sock = self._context.wrap_socket(raw, server_hostname=self.host)
        except Exception:
            raw.close()
            raise


def response_id(value):
    if re.fullmatch(r"[0-9a-fA-F]{32}|[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}", value):
        return value
    return ""


def probe(url, key, size, encoding="identity", connect_ip=None, pause_seconds=0, pause_after=16384):
    target = urlsplit(url)
    if not target.hostname or target.username or target.password or target.query or target.fragment:
        raise ValueError("URL must not contain credentials, query or fragment")
    if target.path not in ("/v1/responses", "/responses"):
        raise ValueError("only a Responses endpoint is allowed")
    if target.scheme != "https" and not (target.scheme == "http" and target.hostname in ("127.0.0.1", "localhost", "::1")):
        raise ValueError("HTTPS is required outside loopback")
    if not 1024 <= size <= 32 * 1024 * 1024 or not 0 <= pause_seconds <= 210 or pause_after < 1:
        raise ValueError("probe size or pause outside allowed bounds")
    if not re.fullmatch(r"[!-~]+", key):
        raise ValueError("one API key must be supplied via stdin")
    if encoding not in ("identity", "gzip"):
        raise ValueError("unsupported probe encoding")
    if connect_ip:
        connect_ip = str(ipaddress.ip_address(connect_ip))
        if target.scheme == "http" and not ipaddress.ip_address(connect_ip).is_loopback:
            raise ValueError("plaintext origin override must also be loopback")
    # Base64-like entropy avoids claiming unrealistic compression gains from a
    # body consisting solely of repeated spaces. This is still synthetic data.
    payload = b"!" + base64.b64encode(random.Random(408).randbytes((size * 3 + 3) // 4))[:size - 1]
    wire = gzip.compress(payload, mtime=0) if encoding == "gzip" else payload
    client_id = str(uuid.uuid4())
    if target.scheme == "https":
        connection = OriginHTTPSConnection(target.hostname, target.port or 443, connect_ip, 30)
    else:
        connection = http.client.HTTPConnection(connect_ip or target.hostname, target.port or 80, timeout=30)
    result = {"incoming_client_request_id": client_id, "host": target.hostname, "route": target.path,
              "connect_ip_override": connect_ip is not None, "encoding": encoding,
              "body_bytes": len(payload), "wire_body_bytes": len(wire), "sent_body_bytes": 0,
              "pause_seconds": pause_seconds, "pause_applied": False}
    started = time.monotonic()
    try:
        connection.connect()
        result["connected_seconds"] = round(time.monotonic() - started, 3)
        result["peer_ip"] = connection.sock.getpeername()[0]
        connection.putrequest("POST", target.path, skip_host=True)
        connection.putheader("Host", target.netloc)
        connection.putheader("Connection", "close")
        connection.putheader("Authorization", "Bearer " + key)
        connection.putheader("Content-Type", "application/json")
        connection.putheader("Content-Length", str(len(wire)))
        connection.putheader("X-Client-Request-ID", client_id)
        if encoding != "identity":
            connection.putheader("Content-Encoding", encoding)
        connection.endheaders()
        result["send_started_seconds"] = round(time.monotonic() - started, 3)
        while result["sent_body_bytes"] < len(wire):
            sent = result["sent_body_bytes"]
            end = min(len(wire), sent + 16384)
            if pause_seconds and not result["pause_applied"] and sent < pause_after:
                end = min(end, pause_after)
            try:
                connection.send(wire[sent:end])
            except (OSError, http.client.HTTPException) as exc:
                result["send_error"] = type(exc).__name__
                break  # The server may already have sent a useful 408 response.
            result["sent_body_bytes"] = end
            if pause_seconds and not result["pause_applied"] and pause_after <= end < len(wire):
                time.sleep(pause_seconds)
                result["pause_applied"] = True
        if result["sent_body_bytes"] == len(wire):
            result["send_finished_seconds"] = round(time.monotonic() - started, 3)
        response = connection.getresponse()
        result["headers_seconds"] = round(time.monotonic() - started, 3)
        result["status"] = response.status
        result["request_id"] = response_id(response.getheader("X-Request-ID", ""))
        result["app_client_request_id"] = response_id(response.getheader("X-Client-Request-ID", ""))
        body = response.read(32768)
        # Inspect only the expected validation response; never print it.
        try:
            error = json.loads(body).get("error", {})
            message = error.get("message", "") if isinstance(error, dict) else ""
            invalid_request = isinstance(error, dict) and error.get("type") == "invalid_request_error"
        except (ValueError, AttributeError):
            message = ""
            invalid_request = False
        result["json_validation_rejected"] = (
            response.status == 400 and invalid_request and isinstance(message, str)
            and "Failed to parse request body" in message
        )
    except (OSError, http.client.HTTPException) as exc:
        result["transport_error"] = type(exc).__name__
    finally:
        connection.close()
    result["elapsed_seconds"] = round(time.monotonic() - started, 3)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("--size-bytes", type=int, default=1024 * 1024)
    parser.add_argument("--encoding", choices=("identity", "gzip"), default="identity")
    parser.add_argument("--connect-ip")
    parser.add_argument("--pause-seconds", type=float, default=0)
    args = parser.parse_args()
    result = probe(args.url, sys.stdin.readline().strip(), args.size_bytes, args.encoding,
                   args.connect_ip, args.pause_seconds)
    print(json.dumps(result, ensure_ascii=True))
    return 0 if result.get("json_validation_rejected") else 1


if __name__ == "__main__":
    raise SystemExit(main())
