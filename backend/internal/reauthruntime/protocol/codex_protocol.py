"""Codex-only protocol login. Tokens leave this process only over its private pipe."""
from __future__ import annotations

import base64
import json
import logging
import os
import re
import secrets
import signal
import sys
import time
from email.utils import parsedate_to_datetime
from urllib.parse import urlencode, urlsplit

from reauth.http_client import AuthStop
from reauth.oauth_client import OAuthClient, OAUTH_CLIENT_ID, OAUTH_ISSUER, OAUTH_REDIRECT_URI
from reauth.chatgpt_client import ChatGPTClient
from reauth.totp import TotpMfaClient
from reauth.utils import generate_pkce

import interstitial_solver

AUTH = 'https://api.openai.com/auth'
PERSONAL = frozenset({'free', 'go', 'plus', 'pro'})
ORGANIZATION = frozenset({'team', 'business', 'self_serve_business_prolite',
    'self_serve_business_usage_based', 'enterprise', 'ent26', 'enterprise_cbp_usage_based',
    'enterprise_cbp_automation', 'edu', 'edu_plus', 'edu_pro'})
ERRORS = {'oauth_cloudflare_challenge': 'security_check',
    'invalid_username_or_password': 'password_rejected', 'invalid_password': 'password_rejected',
    'rate_limit_exceeded': 'rate_limited', 'mfa_totp_verification_failed': 'totp_rejected',
    'mfa_totp_code_unavailable': 'totp_rejected', 'account_deactivated': 'account_blocked',
    'account_disabled': 'account_blocked', 'email_otp_provider_required': 'additional_verification',
    'mfa_totp_secret_required': 'additional_verification',
    'mfa_totp_factor_not_available': 'additional_verification',
    'mfa_primary_factor_required': 'additional_verification',
    'add_phone_requires_sms_client': 'additional_verification', 'phone_required': 'additional_verification'}


def choose_workspace(data, expected='', preferred=''):
    """Never infer personal fallback from position or an arbitrary team name."""
    choices = data.get('workspaces') if isinstance(data, dict) else None
    if not isinstance(choices, list) or not choices:
        raise AuthStop('workspace_unavailable')
    choices = [item for item in choices if isinstance(item, dict) and isinstance(item.get('id'), str) and item['id']]
    if not choices or len({item['id'] for item in choices}) != len(choices):
        raise AuthStop('workspace_unavailable')
    personal = [item for item in choices if item.get('kind') == 'personal']
    organizations = [item for item in choices if item.get('kind') in ('organization', 'team', 'business')]
    selected = next((item for item in choices if item['id'] == (expected or preferred)), None)
    fallback = False
    if expected and selected is None:
        if len(personal) != 1:
            raise AuthStop('workspace_unavailable')
        selected, fallback = personal[0], True
    elif preferred and not expected and selected is None:
        raise AuthStop('workspace_unavailable')
    elif not expected and not preferred:
        if len(personal) + len(organizations) != len(choices):
            raise AuthStop('workspace_selection_required')
        if len(organizations) > 1 or (not organizations and len(personal) != 1):
            raise AuthStop('workspace_selection_required')
        selected = organizations[0] if organizations else personal[0]
    if selected is None:
        raise AuthStop('workspace_unavailable')
    return {'workspace': selected['id'], 'personal': selected in personal,
            'organization': selected in organizations, 'personalOnly': selected in personal and len(choices) == 1,
            'fallbackFrom': expected if fallback else ''}


class WorkspaceOAuthClient(OAuthClient):
    def __init__(self, config, *, claim, **kwargs):
        super().__init__(config, **kwargs)
        self.claim, self.selection = claim, None

    def _extract_workspace_id_from_session(self):
        self.selection = choose_workspace(self._decode_oauth_session_cookie(),
            self.claim.get('expected_workspace', ''), self.claim.get('preferred_workspace_id', ''))
        return self.selection['workspace']

    def _extract_workspace_id_from_html(self, _html):
        # A bare ID extracted from HTML does not prove a workspace's type.
        raise AuthStop('workspace_selection_required')

    def _select_organization_for_callback(self, data, *args):
        root = data.get('data') if isinstance(data, dict) and isinstance(data.get('data'), dict) else data
        orgs = (root.get('orgs') or root.get('organizations') or []) if isinstance(root, dict) else []
        if orgs and (not isinstance(orgs, list) or len(orgs) != 1 or not isinstance(orgs[0], dict)):
            raise AuthStop('workspace_selection_required')
        if orgs and len(orgs[0].get('projects') or []) > 1:
            raise AuthStop('workspace_selection_required')
        return super()._select_organization_for_callback(data, *args)


def decode_token(raw):
    try:
        if not isinstance(raw, str) or len(raw) > 32768 or len(raw.split('.')) != 3:
            raise ValueError()
        body = raw.split('.')[1]
        claims = json.loads(base64.urlsafe_b64decode(body + '=' * (-len(body) % 4)))
        if not isinstance(claims, dict) or not isinstance(claims.get('exp'), (int, float)) or claims['exp'] <= time.time():
            raise ValueError()
        return claims
    except Exception:
        raise AuthStop('identity_mismatch') from None


def credentials_from_tokens(tokens, claim, selection=None):
    if not isinstance(tokens, dict) or not isinstance(tokens.get('refresh_token'), str) or not tokens['refresh_token']:
        raise AuthStop('identity_mismatch')
    access, identity = decode_token(tokens.get('access_token')), decode_token(tokens.get('id_token'))
    a, i = access.get(AUTH, {}), identity.get(AUTH, {})
    if not isinstance(a, dict) or not isinstance(i, dict):
        raise AuthStop('identity_mismatch')
    selection = selection or {}
    workspace, user = a.get('chatgpt_account_id'), a.get('chatgpt_user_id') or a.get('user_id')
    email = str(identity.get('email') or '').lower()
    audience = identity.get('aud')
    audience = [audience] if isinstance(audience, str) else audience if isinstance(audience, list) else []
    expected, preferred = claim.get('expected_workspace'), claim.get('preferred_workspace_id')
    fallback = (bool(expected) and selection.get('fallbackFrom') == expected
        and selection.get('workspace') == workspace and workspace != expected
        and a.get('chatgpt_plan_type') in PERSONAL and i.get('chatgpt_plan_type') in PERSONAL)
    if ((access.get('client_id') or access.get('azp')) != OAUTH_CLIENT_ID or OAUTH_CLIENT_ID not in audience
            or not workspace or not user or email != str(claim.get('login_email', '')).lower()
            or (access.get('email') and str(access['email']).lower() != email)
            or (i.get('chatgpt_account_id') and i['chatgpt_account_id'] != workspace)
            or ((i.get('chatgpt_user_id') or i.get('user_id')) and (i.get('chatgpt_user_id') or i.get('user_id')) != user)
            or (expected and expected != workspace and not fallback)
            or (selection.get('fallbackFrom') and not fallback)
            or (selection.get('workspace') and selection['workspace'] != workspace)
            or (not expected and preferred and preferred != workspace)
            or (selection.get('organization') and (a.get('chatgpt_plan_type') not in ORGANIZATION or i.get('chatgpt_plan_type') not in ORGANIZATION))
            or (selection.get('personal') and (a.get('chatgpt_plan_type') not in PERSONAL or i.get('chatgpt_plan_type') not in PERSONAL))):
        raise AuthStop('identity_mismatch')
    credentials = {key: tokens[key] for key in ('access_token', 'refresh_token', 'id_token')}
    credentials.update(client_id=OAUTH_CLIENT_ID, email=identity['email'], chatgpt_account_id=workspace,
        chatgpt_user_id=user, expires_at=access['exp'], plan_type=a.get('chatgpt_plan_type', ''))
    if fallback:
        credentials['codex_workspace_fallback_from'] = expected
        credentials['codex_personal_workspace_only'] = selection.get('personalOnly') is True
    elif not expected and selection.get('personalOnly'):
        credentials['codex_personal_workspace_only'] = True
    return credentials


def retry_after_seconds(raw):
    if not raw or len(raw) > 64:
        return 'none'
    if re.fullmatch(r'[0-9]+', raw):
        return str(min(86400, int(raw)))
    try:
        date = parsedate_to_datetime(raw)
        if date.tzinfo is not None:
            return str(max(0, min(86400, int(date.timestamp() - time.time()))))
    except (TypeError, ValueError, OverflowError, OSError):
        pass
    return 'none'


def response_evidence(headers, marker):
    mitigated = headers.get('cf-mitigated', '')
    header = mitigated == 'challenge'
    signal = ('both' if marker else 'header') if header else ('body' if marker else 'none')
    server = headers.get('server', '').lower()
    ray = headers.get('cf-ray', '')
    if not re.fullmatch(r'[0-9a-fA-F]{16}(?:-[A-Z]{3})?', ray):
        ray = 'none'
    mime = headers.get('content-type', '').partition(';')[0].strip().lower()
    content = {'text/html': 'html', 'application/json': 'json'}.get(mime, 'other' if mime else 'none')
    retry = retry_after_seconds(headers.get('retry-after', '').strip())
    return (f"cf_mitigated={'challenge' if header else 'other' if mitigated else 'none'} "
            f"challenge_signal={signal} body_marker={marker or 'none'} "
            f"server={'cloudflare' if server == 'cloudflare' else 'other' if server else 'none'} "
            f"cf_ray={ray} retry_after={retry} content_type={content}")


class Progress:
    def __init__(self, emit):
        self.emit, self.started = emit, time.monotonic()
        self.stage, self.action = 'protocol_connecting', 'open_login'
        self.action_started, self.status, self.response = self.started, 0, 'none'
        self.evidence = None

    def observe(self, method, url, response=None):
        path = urlsplit(str(url)).path
        operations = {'/api/accounts/authorize/continue': ('email_submitted', 'submit_email'),
            '/api/accounts/password/verify': ('password_submitted', 'submit_password'),
            '/api/accounts/mfa/verify': ('mfa_submitted', 'submit_mfa'),
            '/api/accounts/workspace/select': ('waiting_callback', 'select_workspace'),
            '/api/accounts/organization/select': ('waiting_callback', 'select_workspace'),
            '/oauth/token': ('exchanging_token', 'exchange_token')}
        stage, action = operations.get(path, (self.stage, 'read_page'))
        if response is None:
            self.evidence = None
            self.action, self.action_started = action, time.monotonic()
            self.status, self.response = 0, 'none'
            return
        self.status = response.status_code
        self.response = 'token' if path == '/oauth/token' else 'auth_request' if path.startswith('/api/') else 'document'
        header_challenge = response.headers.get('cf-mitigated') == 'challenge'
        marker = OAuthClient._cloudflare_challenge_marker(response)
        if self.status == 403 or header_challenge:
            self.evidence = response_evidence(response.headers, marker)
        if header_challenge or marker:
            raise AuthStop('security_check')
        if self.status == 403:
            # This response may be handled by the client before the task completes.
            self.emit('forbidden', evidence=self.evidence)
        if self.status == 429:
            raise AuthStop('rate_limited')
        if self.status >= 500:
            raise AuthStop('service_error')
        try:
            data = response.json()
        except Exception:
            data = {}
        if isinstance(data, dict):
            error = data.get('error')
            error = error.get('code', '') if isinstance(error, dict) else error
            if isinstance(error, str) and error in ERRORS:
                raise AuthStop(ERRORS[error])
            page = data.get('page')
            page = page.get('type', '') if isinstance(page, dict) else ''
            if page in ('email_otp_verification', 'add_phone', 'phone_verification', 'phone_number_required'):
                raise AuthStop('additional_verification')
        if self.status < 400 and path in operations:
            self.stage = stage
            self.emit('stage', stage=stage)

    def diagnostic(self):
        now = time.monotonic()
        data = dict(stage=self.stage, action=self.action, elapsed_ms=min(360000, int((now-self.started)*1000)),
            action_ms=min(360000, int((now-self.action_started)*1000)), http_status=self.status, response=self.response)
        if self.evidence is not None:
            data['evidence'] = self.evidence
        return data


def seed_clearance(claim, chat):
    """Seed a pre-solved Cloudflare clearance onto the shared login session.

    Best effort only: solving is disabled by default, and a miss leaves the
    existing security_check stop untouched. The solved user agent is pinned to
    the session so the replayed cookies keep the address, agent and clearance
    consistent with the browser that produced them.
    """
    try:
        solution = interstitial_solver.solve(claim, chat.ua)
    except Exception:
        return False
    return interstitial_solver.apply(chat.session, solution) if solution else False


def authorize(claim, progress):
    if not isinstance(claim, dict) or not claim.get('login_email') or not claim.get('password'):
        raise AuthStop('worker_failed')
    chat, oauth = None, None
    try:
        chat = ChatGPTClient(proxy=claim.get('proxy_url', ''), verbose=False, impersonate='chrome133a')
        chat.current_email = claim['login_email']
        seed_clearance(claim, chat)
        oauth = WorkspaceOAuthClient({'oauth_phone_strategy': 'sms', 'sub2api_oauth_max_hops': 20,
            'sub2api_oauth_impersonates': 'chrome133a', 'sub2api_redirect_uri': OAUTH_REDIRECT_URI},
            claim=claim, proxy=claim.get('proxy_url', ''), verbose=False)
        oauth.session.close()
        oauth.session = chat.session
        chat.session.observer = progress.observe
        verifier, challenge = generate_pkce()
        state = secrets.token_urlsafe(32)
        params = {'response_type': 'code', 'client_id': OAUTH_CLIENT_ID, 'redirect_uri': OAUTH_REDIRECT_URI,
            'scope': 'openid profile email offline_access', 'code_challenge': challenge,
            'code_challenge_method': 'S256', 'state': state}
        progress.emit('stage', stage='protocol_connecting')
        result = oauth.authorize_external_oauth_url(OAUTH_ISSUER + '/oauth/authorize?' + urlencode(params), chat,
            expected_state=state, user_agent=chat.ua, impersonate=chat.impersonate,
            email=claim['login_email'], password=claim['password'], mfa_client=TotpMfaClient(claim.get('totp_secret', '')))
        if not isinstance(result, dict) or not result.get('ok'):
            raise AuthStop(ERRORS.get(str((result or {}).get('error', '')), 'verification_failed'))
        if not result.get('callback_url_validated') or not result.get('code') or result.get('state') != state:
            raise AuthStop('identity_mismatch')
        tokens = oauth._exchange_code_for_tokens(result['code'], verifier, chat.ua, chat.impersonate)
        return credentials_from_tokens(tokens, claim, oauth.selection)
    finally:
        if chat is not None:
            chat.session.close()


def main():
    output = sys.stdout
    # Even vendored verbose/exception output must not reach worker logs.
    sys.stdout = open(os.devnull, 'w')
    sys.stderr = open(os.devnull, 'w')
    logging.disable(logging.CRITICAL)
    def emit(kind, **data):
        output.write(json.dumps({'type': kind, **data}, separators=(',', ':')) + '\n')
        output.flush()
    progress = Progress(emit)
    claim = {}
    try:
        if hasattr(signal, 'SIGALRM'):
            def timeout(*_args):
                raise AuthStop('timeout')
            signal.signal(signal.SIGALRM, timeout)
            signal.alarm(180)
        raw = sys.stdin.buffer.read(32769)
        if len(raw) > 32768:
            raise AuthStop('worker_failed')
        claim = json.loads(raw)
        credentials = authorize(claim, progress)
        emit('result', credentials=credentials)
        credentials.clear()
    except AuthStop as exc:
        emit('error', code=exc.code, diagnostic=progress.diagnostic())
    except Exception:
        emit('error', code='transport_error', diagnostic=progress.diagnostic())
    finally:
        if hasattr(signal, 'SIGALRM'):
            signal.alarm(0)
        if isinstance(claim, dict):
            claim.clear()


if __name__ == '__main__':
    main()
