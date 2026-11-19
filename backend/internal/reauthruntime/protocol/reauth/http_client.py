"""reauth-web HTTP adapter for the pinned Sub2API curl-cffi runtime."""
from urllib.parse import quote, urljoin, urlsplit

from curl_cffi.requests import Session

USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
HOSTS = frozenset({'auth.openai.com', 'sentinel.openai.com', 'chatgpt.com'})


class AuthStop(BaseException):
    # Do not let the vendored client's broad Exception handlers retry a policy
    # stop (wrong workspace, additional verification or an unsafe destination).
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def normalize_proxy_url(proxy):
    value = str(proxy or '').strip()
    if not value:
        return ''
    if '://' not in value:
        parts = value.split(':', 3)
        if len(parts) == 4 and parts[1].isdigit():
            value = f'http://{quote(parts[2], safe="")}:{quote(parts[3], safe="")}@{parts[0]}:{parts[1]}'
    try:
        target = urlsplit(value)
        if (target.scheme not in {'http', 'https', 'socks5', 'socks5h'} or not target.hostname
                or target.fragment or target.query or target.path not in ('', '/')
                or (target.port is not None and not 1 <= target.port <= 65535)):
            raise ValueError()
    except ValueError:
        raise AuthStop('proxy_unavailable') from None
    return value.replace('socks5://', 'socks5h://', 1) if target.scheme == 'socks5' else value


def checked_url(url):
    try:
        target = urlsplit(str(url))
        if (target.scheme != 'https' or target.hostname not in HOSTS or
                target.username or target.password or target.port not in (None, 443)):
            raise ValueError()
    except ValueError:
        raise AuthStop('transport_error') from None
    if any(part in target.path for part in ('create_account', 'phone', 'send-otp', 'email-otp', 'passwordless')):
        raise AuthStop('additional_verification')


class AuthSession(Session):
    """Check every redirect before sending anything; never use environment proxies."""
    def request(self, method, url, *args, **kwargs):
        follow = kwargs.pop('allow_redirects', True)
        observer = getattr(self, 'observer', None)
        # Redirects in this protocol are same-origin GETs. Credentials are POSTed
        # to fixed endpoints with redirects disabled by the login client.
        for _ in range(12):
            checked_url(url)
            if observer:
                observer(method, url)
            response = super().request(method, url, *args, allow_redirects=False, **kwargs)
            if observer:
                observer(method, url, response)
            location = response.headers.get('location', '')
            if not follow or response.status_code not in (301, 302, 303, 307, 308) or not location:
                return response
            next_url = urljoin(str(url), location)
            target = urlsplit(next_url)
            if target.scheme == 'http' and target.hostname in {'localhost', '127.0.0.1'} and target.port == 1455 and target.path == '/auth/callback':
                return response  # The OAuth client validates state/code; no local request.
            checked_url(next_url)
            if str(method).upper() not in ('GET', 'HEAD') or urlsplit(str(url)).hostname != target.hostname:
                raise AuthStop('transport_error')
            url = next_url
            kwargs.pop('params', None)
        raise AuthStop('transport_error')


def create_http_session(proxy=None, impersonate='chrome133a'):
    normalized = normalize_proxy_url(proxy)
    return AuthSession(impersonate=impersonate, trust_env=False,
        proxies={'http': normalized, 'https': normalized} if normalized else {'http': '', 'https': ''})
