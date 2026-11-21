"""Optional Cloudflare interstitial solving for the Codex protocol login.

The solver runs a real browser from the account's own egress address and returns
``cf_clearance`` together with the user agent it used. Both values must be
replayed unchanged, so the caller pins ``solution['useragent']`` on the session.

Every path in this module is best-effort. Solving is disabled unless
``SOLVERIFY_ENABLED`` is ``1`` and a proxy plus API key are configured; any
failure returns ``None`` and the caller keeps its existing ``security_check``
stop. No cookie, token, page text or response body is logged or returned.
"""
from __future__ import annotations

import json
import os
import time
import urllib.parse
import urllib.request

ENDPOINT = 'https://solver.solverify.net'
# Solve on the login entry, never on the authorize URL holding state and a
# PKCE challenge: the solver's browser could otherwise consume the OAuth code.
CHALLENGE_URL = 'https://auth.openai.com/log-in'
TASK_TYPE = 'interstitial'
# cf_clearance is host scoped; seed every host the login client talks to.
DOMAINS = ('.auth.openai.com', 'auth.openai.com', '.openai.com', 'openai.com')
POLL_SECONDS = 3.0
MAX_BODY = 65536


def enabled():
    return os.environ.get('SOLVERIFY_ENABLED', '').strip() == '1'


def _bounded(name, default, low, high):
    try:
        value = int(os.environ.get(name, '').strip())
    except (TypeError, ValueError):
        return default
    return value if low <= value <= high else default


def proxy_from_url(raw):
    """Only plain HTTP proxies can be forwarded; the solver rejects anything else."""
    value = str(raw or '').strip()
    if not value:
        return None
    try:
        target = urllib.parse.urlsplit(value if '://' in value else 'http://' + value)
        port = target.port
        if target.scheme not in ('http', 'https') or not target.hostname or not port:
            return None
        if target.fragment or target.query or target.path not in ('', '/'):
            return None
        if not 1 <= port <= 65535:
            return None
        return {'host': target.hostname, 'port': str(port),
                'login': urllib.parse.unquote(target.username or ''),
                'password': urllib.parse.unquote(target.password or '')}
    except ValueError:
        return None


def _env_proxy():
    host = os.environ.get('SOLVERIFY_PROXY_HOST', '').strip()
    port = os.environ.get('SOLVERIFY_PROXY_PORT', '').strip()
    if not host or len(host) > 256 or not port.isdigit() or not 1 <= int(port) <= 65535:
        return None
    return {'host': host, 'port': port,
            'login': os.environ.get('SOLVERIFY_PROXY_LOGIN', '').strip(),
            'password': os.environ.get('SOLVERIFY_PROXY_PASSWORD', '')}


def configuration(claim):
    """Resolve solver settings, or None when solving is unavailable here."""
    if not enabled():
        return None
    key = os.environ.get('SOLVERIFY_API_KEY', '').strip()
    if not key or len(key) > 256:
        return None
    # The clearance is bound to the egress the browser used, so the solver must
    # leave through the same address this task talks from.
    proxy = proxy_from_url((claim or {}).get('proxy_url', '')) or _env_proxy()
    if proxy is None:
        return None
    endpoint = os.environ.get('SOLVERIFY_ENDPOINT', ENDPOINT).strip().rstrip('/')
    if urllib.parse.urlsplit(endpoint).scheme != 'https' or len(endpoint) > 256:
        return None
    return {'key': key, 'endpoint': endpoint, 'proxy': proxy,
            'timeout': _bounded('SOLVERIFY_TIMEOUT', 90, 10, 300)}


def _post(endpoint, path, payload):
    request = urllib.request.Request(
        endpoint + path, data=json.dumps(payload).encode('utf-8'), method='POST',
        headers={'Content-Type': 'application/json', 'Accept': 'application/json'})
    # Never inherit environment proxies: the solve itself must not be proxied.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=20) as response:
        if response.status != 200:
            return None
        raw = response.read(MAX_BODY)
    return json.loads(raw)


def normalize(solution):
    """Accept a mapping or a list of cookies; require cf_clearance and a UA."""
    if not isinstance(solution, dict):
        return None
    raw = solution.get('cookies')
    pairs = []
    if isinstance(raw, dict):
        pairs = [(str(name), str(value)) for name, value in raw.items()]
    elif isinstance(raw, list):
        pairs = [(str(item['name']), str(item.get('value', '')))
                 for item in raw if isinstance(item, dict) and item.get('name')]
    pairs = [(name, value) for name, value in pairs if name and value]
    if not any(name == 'cf_clearance' for name, _ in pairs):
        return None
    useragent = solution.get('useragent')
    if not isinstance(useragent, str) or not useragent.strip() or len(useragent) > 512:
        return None
    return {'cookies': pairs, 'useragent': useragent}


def solve(claim, useragent):
    """Return {'cookies': [...], 'useragent': str} or None. Never raises."""
    settings = configuration(claim)
    if settings is None or not useragent:
        return None
    proxy = settings['proxy']
    task = {'type': TASK_TYPE, 'websiteURL': CHALLENGE_URL, 'useragent': useragent,
            'proxyType': 'http', 'proxyAddress': proxy['host'], 'proxyPort': proxy['port']}
    if proxy['login']:
        task['proxyLogin'] = proxy['login']
        task['proxyPassword'] = proxy['password']
    deadline = time.monotonic() + settings['timeout']
    try:
        created = _post(settings['endpoint'], '/createTask', {'clientKey': settings['key'], 'task': task})
        task_id = (created or {}).get('taskId') if isinstance(created, dict) else None
        if not isinstance(task_id, (str, int)) or task_id == '':
            return None
        while time.monotonic() < deadline:
            time.sleep(POLL_SECONDS)
            result = _post(settings['endpoint'], '/getTaskResult',
                           {'clientKey': settings['key'], 'taskId': task_id})
            if not isinstance(result, dict):
                return None
            status = result.get('status')
            if status == 'completed':
                return normalize(result.get('solution'))
            if status in ('failed', 'cancelled'):
                return None
    except Exception:
        return None
    return None


def apply(session, solution):
    """Seed the solved cookies and user agent onto the shared login session."""
    if not isinstance(solution, dict) or not isinstance(solution.get('cookies'), list):
        return False
    try:
        for name, value in solution['cookies']:
            for domain in DOMAINS:
                session.cookies.set(name, value, domain=domain)
        session.headers['User-Agent'] = solution['useragent']
    except Exception:
        return False
    return True
