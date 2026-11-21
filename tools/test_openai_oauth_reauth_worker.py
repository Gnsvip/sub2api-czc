import json
import os
import tempfile
import threading
import time
import unittest
import base64
import importlib.util
import io
import signal
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch
from types import SimpleNamespace
from unittest.mock import MagicMock

from tools.openai_oauth_reauth_worker import (
    OTPURLProvider,
    ProtocolHTTPError,
    WorkerConfig,
    WorkerError,
    _auth_session_flow,
    _checked_bootstrap_authorize,
    _email_submission_state,
    _has_passwordless_intent,
    _start_passwordless_login,
    _is_local_codex_callback,
    _safe_openai_endpoint,
    _workspace_id_from_auth_payload,
    _siwc_password_failure,
    process_claim,
    process_password_claim,
    _wait_for_otp,
    extract_otp,
    sanitize_error,
    validate_otp_url,
    main,
    terminate_worker,
)


class OTPHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"code": "482931", "subject": "ChatGPT login code"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        pass


class CodexProtocolTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = Path(__file__).resolve().parents[1] / "backend/internal/reauthruntime/protocol"
        sys.path.insert(0, str(path))
        import codex_protocol
        from reauth import http_client
        cls.protocol, cls.http = codex_protocol, http_client

    def test_workspace_selection_preserves_business_and_original_identity(self):
        p = {'id': 'personal', 'kind': 'personal', 'name': 'Personal'}
        org = {'id': 'business', 'kind': 'organization', 'name': 'Custom name'}
        for choices in ([p, org], [org, p]):
            selection = self.protocol.choose_workspace({'workspaces': choices})
            self.assertEqual(selection['workspace'], 'business')
            self.assertTrue(selection['organization'])
            selected = self.protocol.choose_workspace({'workspaces': choices}, 'personal', 'business')
            self.assertEqual(selected['workspace'], 'personal', 'reauth ignores initial preference')
        original = self.protocol.choose_workspace({'workspaces': [p, org]}, 'business')
        self.assertEqual(original['workspace'], 'business')
        fallback = self.protocol.choose_workspace({'workspaces': [p]}, 'former-business')
        self.assertEqual(fallback['fallbackFrom'], 'former-business')
        self.assertTrue(fallback['personalOnly'])
        self.assertTrue(self.protocol.choose_workspace({'workspaces': [p]})['personalOnly'])

    def test_workspace_selection_does_not_guess_or_choose_another_team(self):
        choices = [{'id': 'a', 'kind': 'organization'}, {'id': 'b', 'kind': 'organization'}]
        cases = [({'workspaces': choices}, '', '', 'workspace_selection_required'),
                 ({'workspaces': choices}, 'gone', '', 'workspace_unavailable'),
                 ({'workspaces': choices}, '', 'gone', 'workspace_unavailable'),
                 ({'workspace_id': 'unclassified'}, '', '', 'workspace_unavailable'),
                 ({'workspaces': [{'id': 'unknown'}]}, '', '', 'workspace_selection_required'),
                 ({'workspaces': [choices[0], choices[0]]}, 'a', '', 'workspace_unavailable')]
        for data, expected, preferred, code in cases:
            with self.subTest(code=code), self.assertRaisesRegex(self.http.AuthStop, code):
                self.protocol.choose_workspace(data, expected, preferred)
        selected = self.protocol.choose_workspace({'workspaces': choices}, '', 'b')
        self.assertEqual(selected['workspace'], 'b')

    def token_fixture(self, plan='self_serve_business_prolite', workspace='business'):
        p = self.protocol
        auth = {'chatgpt_account_id': workspace, 'chatgpt_user_id': 'user-a', 'chatgpt_plan_type': plan}
        access = {'exp': time.time() + 600, 'client_id': p.OAUTH_CLIENT_ID, p.AUTH: dict(auth)}
        identity = {'exp': time.time() + 600, 'email': 'test@example.invalid', 'aud': p.OAUTH_CLIENT_ID, p.AUTH: dict(auth)}
        return access, identity

    @staticmethod
    def tokens(access, identity):
        def jwt(value):
            return 'header.' + base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip('=') + '.signature'
        return {'access_token': jwt(access), 'id_token': jwt(identity), 'refresh_token': 'synthetic-refresh'}

    def test_tokens_reject_identity_client_expiry_and_workspace_mismatches(self):
        p = self.protocol
        claim = {'login_email': 'test@example.invalid', 'expected_workspace': 'business'}
        access, identity = self.token_fixture()
        self.assertEqual(p.credentials_from_tokens(self.tokens(access, identity), claim)['chatgpt_account_id'], 'business')
        for side, field, value in [('access', 'client_id', 'another-client'), ('id', 'aud', 'another-client'),
                                   ('id', 'email', 'other@example.invalid'), ('access', 'exp', 1), ('id', 'exp', 1),
                                   ('access', p.AUTH, {}), ('id', p.AUTH, {'chatgpt_user_id': 'other-user'}),
                                   ('access', p.AUTH, {'chatgpt_account_id': 'other', 'chatgpt_user_id': 'user-a'})]:
            a, i = self.token_fixture()
            (a if side == 'access' else i)[field] = value
            with self.subTest(field=field, side=side), self.assertRaisesRegex(self.http.AuthStop, 'identity_mismatch'):
                p.credentials_from_tokens(self.tokens(a, i), claim)

    def test_personal_fallback_requires_selection_and_both_token_plans(self):
        p = self.protocol
        claim = {'login_email': 'test@example.invalid', 'expected_workspace': 'old', 'preferred_workspace_id': 'ignored'}
        a, i = self.token_fixture('plus', 'personal')
        selection = p.choose_workspace({'workspaces': [{'id': 'personal', 'kind': 'personal'}]}, 'old')
        credentials = p.credentials_from_tokens(self.tokens(a, i), claim, selection)
        self.assertEqual(credentials['codex_workspace_fallback_from'], 'old')
        self.assertTrue(credentials['codex_personal_workspace_only'])
        for bad in (None, {**selection, 'fallbackFrom': 'wrong'}, {**selection, 'workspace': 'wrong'}):
            with self.assertRaisesRegex(self.http.AuthStop, 'identity_mismatch'):
                p.credentials_from_tokens(self.tokens(a, i), claim, bad)
        i[p.AUTH]['chatgpt_plan_type'] = 'team'
        with self.assertRaisesRegex(self.http.AuthStop, 'identity_mismatch'):
            p.credentials_from_tokens(self.tokens(a, i), claim, selection)

    def test_initial_preferred_workspace_and_organization_plan_are_checked(self):
        p = self.protocol
        a, i = self.token_fixture()
        for claim, selection in [({'login_email': 'test@example.invalid', 'preferred_workspace_id': 'wrong'}, None),
                                  ({'login_email': 'test@example.invalid'}, {'workspace': 'business', 'personal': True})]:
            with self.assertRaisesRegex(self.http.AuthStop, 'identity_mismatch'):
                p.credentials_from_tokens(self.tokens(a, i), claim, selection)

    def test_auth_transport_blocks_foreign_destinations_and_post_redirects(self):
        http = self.http
        for url in ('http://auth.openai.com/login', 'https://auth.openai.com.evil.invalid/login',
                    'https://auth.openai.com@evil.invalid/login', 'https://127.0.0.1/',
                    'https://auth.openai.com:444/login'):
            with self.subTest(url=url), patch.object(http.Session, 'request') as send:
                with http.create_http_session() as session, self.assertRaises(http.AuthStop):
                    session.post(url, json={'password': 'synthetic'})
                send.assert_not_called()
        for method, destination in [('POST', 'https://auth.openai.com/next'), ('GET', 'https://evil.invalid/next'),
                                    ('GET', 'https://chatgpt.com/next')]:
            response = SimpleNamespace(status_code=302, headers={'location': destination})
            with patch.object(http.Session, 'request', return_value=response) as send:
                with http.create_http_session() as session, self.assertRaises(http.AuthStop):
                    session.request(method, 'https://auth.openai.com/login')
                self.assertEqual(send.call_count, 1)

    def test_callback_is_not_requested_and_proxy_never_falls_back_to_environment(self):
        http = self.http
        response = SimpleNamespace(status_code=302, headers={'location': 'http://localhost:1455/auth/callback?code=synthetic&state=synthetic'})
        with patch.dict(os.environ, {'HTTPS_PROXY': 'http://foreign.invalid:1234'}), patch.object(http.Session, 'request', return_value=response) as send:
            with http.create_http_session('socks5://proxy.invalid:1080') as session:
                self.assertFalse(session.trust_env)
                self.assertEqual(session.proxies['https'], 'socks5h://proxy.invalid:1080')
                self.assertIs(session.get('https://auth.openai.com/login'), response)
                self.assertEqual(send.call_count, 1)
            with http.create_http_session() as session:
                self.assertFalse(session.trust_env)
                self.assertEqual(session.proxies, {'http': '', 'https': ''})
        for value in ('file:///tmp/proxy', 'http://proxy:bad', 'http://proxy:99999', 'http://proxy/path'):
            with self.subTest(value=value), self.assertRaisesRegex(http.AuthStop, 'proxy_unavailable'):
                http.create_http_session(value)

    def test_auth_sessions_do_not_share_cookies(self):
        with self.http.create_http_session() as first, self.http.create_http_session() as second:
            first.cookies.set('login_session', 'synthetic', domain='auth.openai.com')
            self.assertFalse(list(second.cookies.jar))

    def test_security_and_additional_verification_stop_without_raw_errors(self):
        for status, headers, body, code in [(403, {'cf-mitigated': 'challenge'}, {}, 'security_check'),
                (429, {}, {}, 'rate_limited'), (503, {}, {}, 'service_error'),
                (200, {}, {'page': {'type': 'email_otp_verification'}}, 'additional_verification'),
                (401, {}, {'error': {'code': 'invalid_username_or_password', 'message': 'synthetic-secret'}}, 'password_rejected')]:
            response = SimpleNamespace(status_code=status, headers=headers, text='', json=lambda: body)
            progress = self.protocol.Progress(lambda *_args, **_kwargs: None)
            with self.subTest(code=code), self.assertRaisesRegex(self.http.AuthStop, code):
                progress.observe('POST', 'https://auth.openai.com/api/accounts/password/verify', response)
            self.assertNotIn('synthetic-secret', json.dumps(progress.diagnostic()))

    def test_interstitial_solver_stays_disabled_without_explicit_configuration(self):
        solver = self.protocol.interstitial_solver
        claim = {'login_email': 'a@example.invalid', 'password': 'synthetic'}
        with patch.dict(os.environ, {}, clear=True):
            self.assertFalse(solver.enabled())
            self.assertIsNone(solver.configuration(claim))
            self.assertIsNone(solver.solve(claim, 'synthetic-agent'))
        with patch.dict(os.environ, {'SOLVERIFY_ENABLED': '1'}, clear=True):
            self.assertIsNone(solver.configuration(claim), 'a key and a proxy are both required')
        with patch.dict(os.environ, {'SOLVERIFY_ENABLED': '1', 'SOLVERIFY_API_KEY': 'k'}, clear=True):
            self.assertIsNone(solver.configuration(claim), 'solving is unavailable without a proxy')

    def test_interstitial_solver_forwards_only_plain_http_proxies(self):
        solver = self.protocol.interstitial_solver
        claim = {'proxy_url': 'socks5h://user:pass@127.0.0.1:1080'}
        with patch.dict(os.environ, {'SOLVERIFY_ENABLED': '1', 'SOLVERIFY_API_KEY': 'k'}, clear=True):
            self.assertIsNone(solver.configuration(claim), 'the solver rejects non-HTTP proxies')
        self.assertIsNone(solver.proxy_from_url('http://127.0.0.1'))
        self.assertIsNone(solver.proxy_from_url('http://127.0.0.1:3128/path'))
        self.assertIsNone(solver.proxy_from_url('ftp://127.0.0.1:3128'))
        self.assertEqual(solver.proxy_from_url('http://u:p@15.204.82.23:3128'),
                         {'host': '15.204.82.23', 'port': '3128', 'login': 'u', 'password': 'p'})
        env = {'SOLVERIFY_ENABLED': '1', 'SOLVERIFY_API_KEY': 'k', 'SOLVERIFY_PROXY_HOST': '15.204.82.23',
               'SOLVERIFY_PROXY_PORT': '3128', 'SOLVERIFY_PROXY_LOGIN': 'u', 'SOLVERIFY_PROXY_PASSWORD': 'p',
               'SOLVERIFY_ENDPOINT': 'http://solver.example.invalid'}
        with patch.dict(os.environ, env, clear=True):
            self.assertIsNone(solver.configuration(claim), 'a plaintext solver endpoint is refused')
        env['SOLVERIFY_ENDPOINT'] = 'https://solver.solverify.net'
        with patch.dict(os.environ, env, clear=True):
            settings = solver.configuration({})
            self.assertEqual(settings['proxy']['host'], '15.204.82.23')
            self.assertEqual(settings['proxy']['login'], 'u')
            self.assertEqual(settings['timeout'], 90)
        for timeout in ('5', '900', 'soon'):
            env['SOLVERIFY_TIMEOUT'] = timeout
            with patch.dict(os.environ, env, clear=True):
                self.assertEqual(solver.configuration({})['timeout'], 90, 'out-of-range timeouts fall back')
        env['SOLVERIFY_TIMEOUT'] = '45'
        with patch.dict(os.environ, env, clear=True):
            self.assertEqual(solver.configuration({})['timeout'], 45)

    def test_interstitial_solution_requires_clearance_and_pins_the_agent(self):
        solver = self.protocol.interstitial_solver
        self.assertIsNone(solver.normalize(None))
        self.assertIsNone(solver.normalize({'cookies': [{'name': '__cf_bm', 'value': 'x'}], 'useragent': 'ua'}))
        self.assertIsNone(solver.normalize({'cookies': {'cf_clearance': 'x'}}))
        self.assertIsNone(solver.normalize({'cookies': {'cf_clearance': 'x'}, 'useragent': 'u' * 513}))
        mapping = solver.normalize({'cookies': {'cf_clearance': 'x', '__cf_bm': 'y'}, 'useragent': 'agent'})
        listing = solver.normalize({'cookies': [{'name': 'cf_clearance', 'value': 'x', 'domain': '.openai.com'}],
                                    'useragent': 'agent'})
        self.assertEqual(mapping['cookies'], [('cf_clearance', 'x'), ('__cf_bm', 'y')])
        self.assertEqual(listing['cookies'], [('cf_clearance', 'x')])
        session = MagicMock()
        self.assertTrue(solver.apply(session, mapping))
        self.assertEqual(session.headers.__setitem__.call_args[0], ('User-Agent', 'agent'))
        self.assertEqual({call[0][0] for call in session.cookies.set.call_args_list}, {'cf_clearance', '__cf_bm'})
        self.assertEqual({call[1]['domain'] for call in session.cookies.set.call_args_list},
                         set(solver.DOMAINS))
        self.assertFalse(solver.apply(session, {'useragent': 'agent'}))
        self.assertFalse(solver.apply(MagicMock(cookies=SimpleNamespace(set=MagicMock(side_effect=RuntimeError))),
                                      mapping))

    def test_seed_clearance_is_silent_and_never_replaces_the_security_stop(self):
        solver = self.protocol.interstitial_solver
        chat = SimpleNamespace(ua='agent', session=MagicMock())
        with patch.object(solver, 'solve', return_value=None):
            self.assertFalse(self.protocol.seed_clearance({'login_email': 'a@example.invalid'}, chat))
        with patch.object(solver, 'solve', side_effect=RuntimeError('synthetic-secret')):
            self.assertFalse(self.protocol.seed_clearance({'login_email': 'a@example.invalid'}, chat))
        with patch.object(solver, 'solve', return_value={'cookies': [('cf_clearance', 'x')], 'useragent': 'agent'}):
            self.assertTrue(self.protocol.seed_clearance({'login_email': 'a@example.invalid'}, chat))
            solver.solve.assert_called_once_with({'login_email': 'a@example.invalid'}, 'agent')

    def test_callback_state_and_totp_regression(self):
        from reauth.totp import totp_code
        p = self.protocol
        client = p.OAuthClient({}, verbose=False)
        try:
            for url in ('http://localhost:1455/auth/callback?code=x&state=wrong',
                        'http://localhost:9999/auth/callback?code=x&state=expected',
                        'https://evil.invalid/auth/callback?code=x&state=expected',
                        'http://localhost:1455/auth/callback?code=x'):
                self.assertIsNone(client._parse_validated_callback_url(url, 'expected', p.OAUTH_REDIRECT_URI))
            self.assertTrue(client._parse_validated_callback_url('http://localhost:1455/auth/callback?code=x&state=expected', 'expected', p.OAUTH_REDIRECT_URI))
        finally:
            client.session.close()
        self.assertEqual(totp_code('GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ', at_time=59, digits=8)[0], '94287082')

    def test_forbidden_evidence_preserves_stop_and_redacts_untrusted_fields(self):
        adapter = OpenAIOAuthReauthWorkerTest.protocol_adapter()
        url = 'https://auth.openai.com/log-in?state=synthetic-secret'
        for expected, header, body, marker in [
            ('none', '', 'ordinary denial', 'none'), ('header', 'challenge', '', 'none'),
            ('body', '', 'Just a moment cf-chl-', 'just_a_moment'),
            ('both', 'challenge', '/cdn-cgi/challenge-platform/', 'challenge_platform'),
        ]:
            emit = MagicMock()
            progress = self.protocol.Progress(emit)
            response = SimpleNamespace(status_code=403, headers={
                'cf-mitigated': header, 'cf-ray': '1234567890abcdef-SEA', 'server': 'cloudflare',
                'retry-after': '30', 'content-type': 'text/html; charset=UTF-8',
                'set-cookie': 'synthetic-secret'}, text=body + ' synthetic-secret', json=lambda: {})
            progress.observe('GET', url)
            with self.subTest(expected=expected):
                if expected == 'none':
                    progress.observe('GET', url, response)
                    emit.assert_called_once_with('forbidden', evidence=progress.diagnostic()['evidence'])
                else:
                    with self.assertRaisesRegex(self.http.AuthStop, 'security_check'):
                        progress.observe('GET', url, response)
                data = progress.diagnostic()
                self.assertIn(f'challenge_signal={expected} body_marker={marker}', data['evidence'])
                self.assertIn('cf_ray=1234567890abcdef-SEA retry_after=30 content_type=html', data['evidence'])
                raw = adapter.safe_diagnostic('verification_failed' if expected == 'none' else 'security_check', data)
                self.assertGreater(len(raw), 256)
                self.assertNotIn('synthetic-secret', raw)
                self.assertEqual(adapter.parse_diagnostic(raw), raw)
                self.assertIsNone(adapter.parse_diagnostic(raw + ' secret'))
                self.assertIsNone(adapter.parse_diagnostic(raw.replace('retry_after=30', 'retry_after=86401')))
                progress.observe('GET', url)
                self.assertNotIn('evidence', progress.diagnostic())
        normalized = self.protocol.response_evidence({'cf-ray': 'synthetic-secret', 'retry-after': 'password',
            'cf-mitigated': 'untrusted', 'server': 'private server', 'content-type': 'text/private'}, None)
        self.assertEqual(normalized, 'cf_mitigated=other challenge_signal=none body_marker=none '
            'server=other cf_ray=none retry_after=none content_type=other')


class OpenAIOAuthReauthWorkerTest(unittest.TestCase):
    def test_codex_completion_allows_manifest_and_astra_without_changing_other_calls(self):
        from tools import openai_oauth_reauth_worker as worker
        browser = self.protocol_adapter()
        for original, expected in ((30.0, 60.0), (90.0, 90.0)):
            api = worker.WorkerAPI(WorkerConfig("http://127.0.0.1", "synthetic-token", "probe", None, request_timeout=original))
            completion = browser.completion_api(api, worker)
            self.assertIsNot(completion, api)
            self.assertEqual(completion.config.request_timeout, expected)
            self.assertEqual(api.config.request_timeout, original)
            self.assertEqual(completion.config.worker_id, api.config.worker_id)
            self.assertEqual(completion.config.worker_token, api.config.worker_token)

    def test_managed_protocol_dispatch_preserves_other_profiles_and_safe_failures(self):
        browser = self.protocol_adapter()
        worker = SimpleNamespace(process_password_claim=MagicMock(), safe_worker_failure=lambda _exc: "worker_failed", WorkerError=WorkerError)
        original = worker.process_password_claim
        browser.install(worker)
        with patch.object(browser, "run_protocol") as run:
            worker.process_password_claim(None, {"oauth_profile": "codex", "engine": "local_worker"})
            run.assert_called_once()
            for profile in ("excel", "siwc"):
                worker.process_password_claim(None, {"oauth_profile": profile})
            worker.process_password_claim(None, {"oauth_profile": "codex", "engine": "session_studio"})
            self.assertEqual(original.call_count, 3)
        self.assertEqual(worker.safe_worker_failure(browser.ProtocolFailure("security_check")), "security_check")
        for code in ("security_check", "totp_rejected", "identity_mismatch", "workspace_unavailable"):
            self.assertEqual(worker.safe_worker_failure(WorkerError(code)), code)
        self.assertEqual(worker.safe_worker_failure(RuntimeError("password=synthetic-secret")), "worker_failed")
        self.assertEqual(str(browser.ProtocolFailure("synthetic-secret")), "worker_failed")

    @staticmethod
    def protocol_adapter():
        path = Path(__file__).resolve().parents[1] / "backend/internal/reauthruntime/protocol/protocol_worker.py"
        spec = importlib.util.spec_from_file_location("codex_protocol_adapter_tests", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_managed_protocol_submission_and_cleanup(self):
        browser = self.protocol_adapter()
        credentials = {key: "synthetic-" + key for key in ("access_token", "refresh_token", "id_token", "client_id")}
        for accepted, error in ((True, None), (False, None), (False, "security_check"), (False, "not-a-safe-code")):
            with self.subTest(accepted=accepted, error=error), tempfile.TemporaryDirectory() as root:
                events = [{"type": "stage", "stage": "mfa_submitted"}]
                events.append({"type": "error", "code": error} if error else {"type": "result", "credentials": credentials})
                child = MagicMock()
                child.stdout = io.BytesIO(b"".join((json.dumps(event) + "\n").encode() for event in events))
                child.stdin = io.BytesIO()
                child.poll.return_value = 0
                child.wait.return_value = 0
                api = SimpleNamespace(credentials=MagicMock(return_value={"status": "succeeded" if accepted else "failed"}))
                worker = SimpleNamespace(_best_effort_progress=MagicMock())
                claim = {"task_id": 12, "account_id": 42, "login_email": "test@example.com",
                         "password": "synthetic-password", "totp_secret": "synthetic-totp",
                         "proxy_url": "http://user:synthetic-proxy-password@127.0.0.1:9999", "expected_workspace": "workspace"}
                with patch.dict(os.environ, {"DATABASE_PASSWORD": "private-db", "CLOAKBROWSER_LICENSE_KEY": "old-secret"}), \
                     patch.object(browser.subprocess, "Popen", return_value=child) as popen:
                    if accepted:
                        browser.run_protocol(api, claim, worker)
                    else:
                        expected = error if error in browser.FAILURES else "worker_failed" if error else "verification_failed"
                        with self.assertRaisesRegex(browser.ProtocolFailure, expected):
                            browser.run_protocol(api, claim, worker)
                kwargs = popen.call_args.kwargs
                self.assertFalse(Path(kwargs["cwd"]).exists(), "temporary task directory must be removed")
                for secret in ("synthetic-password", "synthetic-totp", "synthetic-proxy-password", "private-db", "old-secret"):
                    self.assertNotIn(secret, repr(popen.call_args))
                self.assertNotIn("OPENAI_REAUTH_WORKER_TOKEN", kwargs["env"])
                self.assertEqual(api.credentials.call_count, 0 if error else 1)
                self.assertTrue(child.stdout.closed)

    def test_managed_protocol_diagnostic_survives_actual_failure_api_without_secrets(self):
        from tools import openai_oauth_reauth_worker as worker
        browser = self.protocol_adapter()
        data = dict(stage="protocol_connecting", action="submit_email", elapsed_ms=16000,
                    action_ms=15000, http_status=200, response="document", ignored="synthetic-secret")
        expected = "codex_protocol_failed stage=protocol_connecting action=submit_email reason=timeout elapsed_ms=16000 action_ms=15000 http_status=200 response=document"
        with patch.object(worker, "safe_worker_failure", worker.safe_worker_failure), \
             patch.object(worker, "process_password_claim", worker.process_password_claim):
            browser.install(worker)
            api = worker.WorkerAPI(WorkerConfig("http://127.0.0.1", "synthetic-token", "probe", None))
            with patch.object(api, "_post") as post:
                api.fail(7, worker.safe_worker_failure(browser.ProtocolFailure("timeout", data)))
                self.assertEqual(post.call_args.args[1], {"worker_id": "probe", "reason": expected})
                observed = dict(data, http_status=403, evidence="cf_mitigated=challenge challenge_signal=header "
                    "body_marker=none server=cloudflare cf_ray=1234567890abcdef-SEA retry_after=none content_type=html")
                api.fail(7, worker.safe_worker_failure(browser.ProtocolFailure("security_check", observed)))
                reported = post.call_args.args[1]["reason"]
                self.assertIn("reason=security_check", reported)
                self.assertTrue(reported.endswith(observed["evidence"]))
                self.assertNotIn("synthetic-secret", reported)
                for raw in (expected + " secret", reported + " secret", expected.replace("submit_email", "secret"),
                            expected.replace("16000", "999999"), expected.replace("http_status=200", "http_status=999"),
                            expected.replace("action_ms=15000", "action_ms=17000")):
                    api.fail(7, raw)
                    self.assertEqual(post.call_args.args[1]["reason"], "worker_failed")

    @unittest.skipUnless(sys.platform.startswith("linux"), "production supervisor is Linux-only")
    def test_managed_protocol_supervisor_reaps_detached_children(self):
        browser = self.protocol_adapter()
        # No login or network: reproduce the actual wrapper/descendant layout.
        script = '''import json,os,subprocess,sys,time
from pathlib import Path
detached=subprocess.Popen([sys.executable,'-c','import time; time.sleep(90)'],start_new_session=True)
target=Path(sys.argv[1]);pending=target.with_suffix('.tmp')
pending.write_text(json.dumps({'child':os.getpid(),'detached':detached.pid}));pending.replace(target)
if sys.argv[2]=='natural':
 print('failure',flush=True)
 raise SystemExit(1)
time.sleep(90)
'''
        for scenario in ("natural", "terminate", "child_killed", "parent_killed"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                (root / "child.py").write_text(script)
                child_command = [sys.executable, str(root / "child.py"), str(root / "pids.json"), scenario]
                command = [sys.executable, browser.__file__, "--protocol-supervisor", str(os.getpid()), *child_command]
                if scenario == "parent_killed":
                    # The owner dies without any Python finally executing.
                    (root / "owner.py").write_text("import os,subprocess,sys\ncommand=sys.argv[1:]\ncommand[3]=str(os.getpid())\nsubprocess.Popen(command,start_new_session=True).wait()\n")
                    command = [sys.executable, str(root / "owner.py"), *command]
                innocent = subprocess.Popen([sys.executable, "-c", "import time;time.sleep(90)"])
                process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
                try:
                    end = time.monotonic() + 10
                    while not (root / "pids.json").exists() and time.monotonic() < end:
                        time.sleep(0.05)
                    self.assertTrue((root / "pids.json").exists())
                    pids = json.loads((root / "pids.json").read_text())
                    if scenario == "terminate":
                        browser.stop_child(process)
                    elif scenario == "child_killed":
                        os.kill(pids["child"], signal.SIGKILL)
                    elif scenario == "parent_killed":
                        process.kill()
                    process.wait(timeout=12)
                    end = time.monotonic() + 8
                    while any(Path(f"/proc/{pid}").exists() for pid in pids.values()) and time.monotonic() < end:
                        time.sleep(0.05)
                    self.assertEqual([name for name, pid in pids.items() if Path(f"/proc/{pid}").exists()], [])
                    self.assertIsNone(innocent.poll(), "another task's process must survive cleanup")
                finally:
                    if process.poll() is None:
                        browser.stop_child(process)
                    innocent.terminate()
                    innocent.wait(timeout=5)

    def test_siwc_failures_use_fixed_stage_and_reason_without_external_output(self):
        cases = (
            ("[1/5] Password accepted TOTP 2FA challenge reached 2FA verification accepted SIWC_INTERACTION_REQUIRED", "mfa_accepted", "browser_interaction_required"),
            ("Password was rejected SIWC_INTERACTION_REQUIRED", "starting", "password_rejected"),
            ("2FA key was rejected SIWC_INTERACTION_REQUIRED", "starting", "totp_rejected"),
            ("SIWC_WORKSPACE_MISMATCH", "starting", "workspace_mismatch"),
            ("SIWC_AUTH_URL_INVALID", "starting", "invalid_auth_url"),
            ("PROXY_RISK_CONTROL", "starting", "security_check"),
            ("[proxy] 代理检测失败：HTTP 403，返回安全校验页面\n[error] 代理检测失败：HTTP 403，返回安全校验页面", "starting", "security_check"),
            ("ERR_MODULE_NOT_FOUND", "starting", "runtime_error"),
            ("ECONNREFUSED", "starting", "transport_error"),
            ("unknown upstream response", "starting", "protocol_error"),
        )
        for output, stage, reason in cases:
            with self.subTest(reason=reason):
                result = _siwc_password_failure(output + " synthetic-password synthetic-totp https://example.test/?code=secret", 1)
                self.assertIn(f"stage={stage}, reason={reason}, exit=1", result)
                self.assertIn("credentials retained", result)
                self.assertNotIn("synthetic", result)
                self.assertNotIn("secret", result)
                self.assertNotIn("https://", result)
                self.assertEqual("use browser authorization" in result, reason in {"browser_interaction_required", "security_check"})

    def test_parent_death_terminates_managed_protocol_descendants(self):
        with patch("os.getpgrp", return_value=123), patch("os.getpid", return_value=123), patch("os.killpg") as kill:
            with self.assertRaises(SystemExit):
                terminate_worker(15, None)
            kill.assert_called_once_with(123, 9)

    def test_siwc_runner_reports_browser_handoff_after_successful_mfa_without_exposing_output(self):
        api = SimpleNamespace(config=SimpleNamespace(tosub2_root=Path("synthetic-tosub2")), progress=lambda *_args: None)
        result = SimpleNamespace(returncode=1, stdout="Password accepted\n2FA verification accepted\nsynthetic-secret",
                                 stderr="SIWC_INTERACTION_REQUIRED https://example.test/?code=synthetic-code")
        with patch("tools.openai_oauth_reauth_worker.shutil.which", return_value="node"), patch.object(
            Path, "is_file", return_value=True
        ), patch("tools.openai_siwc_oauth_adapter.prepare_siwc_runtime", return_value=Path("synthetic-runner.mjs")), patch(
            "tools.openai_oauth_reauth_worker.subprocess.run", return_value=result
        ) as runner:
            with self.assertRaises(WorkerError) as failure:
                process_password_claim(api, dict(task_id=7, account_id=42, login_email="demo@example.test",
                                                password="synthetic-secret", oauth_profile="siwc",
                                                auth_url="https://auth.openai.com/api/accounts/authorize?state=synthetic"))
        self.assertIn("stage=mfa_accepted, reason=browser_interaction_required", str(failure.exception))
        self.assertIn("use browser authorization", str(failure.exception))
        self.assertNotIn("synthetic", str(failure.exception))
        self.assertEqual(runner.call_count, 1)
        self.assertEqual(runner.call_args.kwargs["timeout"], 600)

    def test_external_worker_does_not_signal_unrelated_process_group(self):
        with patch("os.getpgrp", return_value=100), patch("os.getpid", return_value=123), patch("os.killpg") as kill:
            with self.assertRaises(SystemExit):
                terminate_worker(15, None)
            kill.assert_not_called()

    def test_password_only_config_does_not_require_email_protocol(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "src"
            source.mkdir()
            (source / "protocol-login.mjs").touch()
            (source / "tls-transport.mjs").touch()
            env = {"OPENAI_REAUTH_WORKER_TOKEN": "x" * 32, "TOSUB2_ROOT": root}
            with patch.dict(os.environ, env, clear=True):
                config = WorkerConfig.from_env()
                self.assertIsNone(config.protocol_root)
                self.assertEqual(config.tosub2_root, Path(root))

    def test_rejects_missing_login_runtimes(self):
        with patch.dict(os.environ, {"OPENAI_REAUTH_WORKER_TOKEN": "x" * 32}, clear=True):
            with self.assertRaisesRegex(WorkerError, "configure TOSUB2_ROOT"):
                WorkerConfig.from_env()

    def test_explicit_invalid_email_protocol_is_not_silently_ignored(self):
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "src"
            source.mkdir()
            (source / "protocol-login.mjs").touch()
            (source / "tls-transport.mjs").touch()
            env = {"OPENAI_REAUTH_WORKER_TOKEN": "x" * 32, "TOSUB2_ROOT": root, "CODEX_PROTOCOL_ROOT": root}
            with patch.dict(os.environ, env, clear=True):
                with self.assertRaisesRegex(WorkerError, "pure protocol and Sentinel assets"):
                    WorkerConfig.from_env()

    def test_email_claim_without_protocol_has_explicit_failure(self):
        with self.assertRaisesRegex(WorkerError, "email OTP re-login requires"):
            process_claim(SimpleNamespace(), None, {"credential_mode": "email_otp_url"})

    def test_check_does_not_load_email_protocol_or_claim_tasks(self):
        config = WorkerConfig("http://127.0.0.1:4040", "x" * 32, "check", None, Path("tosub2"))
        with patch("sys.argv", ["worker", "--check"]), patch.object(WorkerConfig, "from_env", return_value=config), patch(
            "tools.openai_oauth_reauth_worker.shutil.which", return_value="node"
        ), patch("tools.openai_oauth_reauth_worker.load_protocol") as protocol, patch(
            "tools.openai_oauth_reauth_worker.run_once"
        ) as run:
            self.assertEqual(main(), 0)
            protocol.assert_not_called()
            run.assert_not_called()

    def test_password_runner_does_not_inherit_worker_token_or_expose_output(self):
        api = SimpleNamespace(
            config=SimpleNamespace(tosub2_root=Path("synthetic-tosub2")),
            progress=lambda *_args: None,
        )
        result = SimpleNamespace(returncode=1, stdout="synthetic-secret", stderr="synthetic-secret")
        with patch.dict(os.environ, {"OPENAI_REAUTH_WORKER_TOKEN": "synthetic-worker-token"}), patch(
            "tools.openai_oauth_reauth_worker.shutil.which", return_value="node"
        ), patch.object(Path, "is_file", return_value=True), patch(
            "tools.openai_codex_oauth_adapter.prepare_codex_runtime", return_value=Path("synthetic-runner.mjs")
        ), patch(
            "tools.openai_oauth_reauth_worker.subprocess.run", return_value=result
        ) as runner:
            with self.assertRaises(WorkerError) as raised:
                process_password_claim(api, {
                    "task_id": 7, "account_id": 42, "login_email": "demo@example.com",
                    "password": "synthetic-secret", "totp_secret": "synthetic-totp",
                })
        self.assertEqual(str(raised.exception), "password/TOTP protocol failed (exit 1)")
        self.assertNotIn("OPENAI_REAUTH_WORKER_TOKEN", runner.call_args.kwargs["env"])
        self.assertEqual(runner.call_args.kwargs["env"]["CHATGPT_LOGIN_PASSWORD"], "synthetic-secret")
        self.assertNotIn("synthetic-secret", runner.call_args.args[0])

    def test_workspace_id_from_auth_payload(self):
        self.assertEqual(
            _workspace_id_from_auth_payload(
                {"oai-client-auth-session": {"workspaces": [{"id": "org-test"}]}}
            ),
            "org-test",
        )
        invalid_payloads = (
            None,
            {},
            {"oai-client-auth-session": {}},
            {"oai-client-auth-session": {"workspaces": [{}]}},
        )
        for payload in invalid_payloads:
            with self.subTest(payload=payload):
                self.assertEqual(_workspace_id_from_auth_payload(payload), "")

    def test_bootstrap_reports_safe_endpoint_instead_of_circuit_cooldown(self):
        self.assertEqual(
            _safe_openai_endpoint("https://example.com/secret?state=secret"),
            "[redacted endpoint]",
        )

        class Response:
            status_code = 403
            url = "https://auth.openai.com/log-in?state=secret&code=secret"

        class Session:
            def get(self, *_args, **_kwargs):
                return Response()

        session = Session()

        def bootstrap(active_session, _state, auth_url=None):
            active_session.get(auth_url)

        with self.assertRaisesRegex(
            ProtocolHTTPError,
            r"OpenAI OAuth bootstrap returned HTTP 403 at auth\.openai\.com/log-in$",
        ):
            _checked_bootstrap_authorize(
                session,
                bootstrap,
                "state-1",
                auth_url="https://auth.openai.com/oauth/authorize?state=secret",
            )

    def test_process_claim_retries_bootstrap_403_with_a_fresh_session(self):
        events = []
        sessions = []

        class FakeSession:
            def __init__(self):
                self.number = len(sessions) + 1
                self.session = SimpleNamespace(close=lambda: events.append(("close", self.number)))
                sessions.append(self)

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()

            @staticmethod
            def network_preflight(session):
                events.append(("preflight", session.number))

            @staticmethod
            def bootstrap_authorize(session, _state, auth_url=None):
                events.append(("bootstrap", session.number))
                if session.number == 1:
                    raise ProtocolHTTPError("OpenAI OAuth bootstrap", 403, auth_url)

            submit_email = staticmethod(lambda session, _email: events.append(("email", session.number)))
            resend_email_otp = staticmethod(lambda _session: None)
            submit_email_otp = staticmethod(lambda _session, _code: None)
            select_workspace_and_get_callback = staticmethod(
                lambda _session, _state: "http://localhost:1455/auth/callback?code=callback-code&state=state-1"
            )
            extract_code = staticmethod(lambda _callback, _state: "callback-code")

        class FakeAPI:
            progress = staticmethod(lambda _task_id, _stage: None)
            callback = staticmethod(lambda _task_id, _callback: {"status": "succeeded"})

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with (
            patch("tools.openai_oauth_reauth_worker.time.sleep"),
            patch("tools.openai_oauth_reauth_worker._wait_for_otp", return_value="482931"),
        ):
            process_claim(FakeAPI(), FakeProtocol, claim)

        self.assertEqual([event for event in events if event[0] == "preflight"], [("preflight", 1), ("preflight", 2)])
        self.assertEqual([event for event in events if event[0] == "bootstrap"], [("bootstrap", 1), ("bootstrap", 2)])
        self.assertLess(events.index(("preflight", 1)), events.index(("bootstrap", 1)))
        self.assertLess(events.index(("preflight", 2)), events.index(("bootstrap", 2)))
        self.assertEqual([event for event in events if event[0] == "email"], [("email", 2)])
        self.assertEqual([event for event in events if event[0] == "close"], [("close", 1), ("close", 2)])

    def test_auth_session_flow_exposes_only_safe_state(self):
        payload = {
            "email": "mail@example.com",
            "session_id": "secret-session",
            "email_verification_mode": "passwordless_login",
            "passwordless_disabled": False,
            "signup_mode": "email_login",
        }
        encoded = base64.urlsafe_b64encode(json.dumps(payload).encode()).decode().rstrip("=")
        cookie = SimpleNamespace(name="oai-client-auth-session", value=f"{encoded}.signature")
        session = SimpleNamespace(session=SimpleNamespace(cookies=SimpleNamespace(jar=[cookie])))

        self.assertEqual(
            _auth_session_flow(session),
            {
                "email_verification_mode": "passwordless_login",
                "passwordless_disabled": False,
                "signup_mode": "email_login",
            },
        )

    def test_email_submission_state_keeps_only_page_type_and_path(self):
        self.assertEqual(
            _email_submission_state(
                {
                    "continue_url": "https://auth.openai.com/log-in/password?secret=value",
                    "page": {"type": "login_password"},
                    "email": "mail@example.com",
                }
            ),
            ("login_password", "/log-in/password"),
        )

    def test_passwordless_intent_is_detected_without_reading_form_values(self):
        html = """
        <form method="post" action="/log-in/password">
          <input type="hidden" name="username" value="mail@example.com">
          <input type="password" name="current-password" value="must-not-submit">
          <button name="intent" value="passwordless_login_send_otp">Use code</button>
        </form>
        """
        self.assertTrue(_has_passwordless_intent(html))
        self.assertFalse(_has_passwordless_intent(html.replace("passwordless_login_send_otp", "password_login")))

    def test_passwordless_login_posts_empty_passwordless_request(self):
        calls = []

        class Response:
            def __init__(self, status_code, url, text=""):
                self.status_code = status_code
                self.url = url
                self.text = text

        class Session:
            def get_auth_navigate_headers(self, *, referer):
                return {"referer": referer}

            def get_auth_headers(self, *, referer):
                return {"referer": referer}

            def get(self, url, *, headers, allow_redirects):
                calls.append(("GET", url, dict(headers), allow_redirects))
                return Response(
                    200,
                    "https://auth.openai.com/log-in/password",
                    '<button name="intent" value="passwordless_login_send_otp">Use code</button>',
                )

            def post(self, url, *, headers, allow_redirects):
                calls.append(("POST", url, dict(headers), allow_redirects))
                response = Response(200, url)
                response.json = lambda: {"page": {"type": "email_otp_verification"}}
                return response

        path = _start_passwordless_login(Session(), "https://auth.openai.com/log-in/password")

        self.assertEqual(path, "/email-verification")
        self.assertEqual(
            [(call[0], call[1]) for call in calls],
            [
                ("GET", "https://auth.openai.com/log-in/password"),
                ("POST", "https://auth.openai.com/api/accounts/passwordless/send-otp"),
            ],
        )
        self.assertEqual(calls[1][2]["referer"], "https://auth.openai.com/log-in/password")
        self.assertEqual(calls[1][2]["content-type"], "application/json")
        self.assertFalse(calls[1][3])

    def test_process_claim_preflights_before_codex_protocol_flow(self):
        events = []

        class FakeSession:
            def __init__(self):
                self.session = SimpleNamespace(close=lambda: events.append("close"))

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()

            @staticmethod
            def network_preflight(_session):
                events.append("preflight")

            @staticmethod
            def bootstrap_authorize(_session, state, auth_url=None):
                events.append(("bootstrap", state, auth_url))

            @staticmethod
            def submit_email(_session, email):
                events.append(("email", email))

            @staticmethod
            def resend_email_otp(_session):
                events.append("resend")

            @staticmethod
            def submit_email_otp(_session, code):
                events.append(("otp", code))

            @staticmethod
            def select_workspace_and_get_callback(_session, state):
                events.append(("workspace", state))
                return "http://localhost:1455/auth/callback?code=callback-code&state=state-1"

            @staticmethod
            def extract_code(callback, state):
                events.append(("extract", callback, state))
                return "callback-code"

        class FakeAPI:
            def progress(self, task_id, stage):
                events.append(("progress", task_id, stage))

            def callback(self, task_id, callback):
                events.append(("callback", task_id, callback))
                return {"status": "succeeded"}

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with patch("tools.openai_oauth_reauth_worker._wait_for_otp", return_value="482931"):
            process_claim(FakeAPI(), FakeProtocol, claim)

        names = [item[0] if isinstance(item, tuple) else item for item in events]
        self.assertLess(names.index("preflight"), names.index("bootstrap"))
        self.assertLess(names.index("bootstrap"), names.index("email"))
        self.assertLess(names.index("email"), names.index("otp"))
        self.assertLess(names.index("otp"), names.index("workspace"))
        self.assertLess(names.index("workspace"), names.index("callback"))
        self.assertNotIn("providers", names)
        self.assertNotIn("csrf", names)
        self.assertIn("close", names)

    def test_process_claim_uses_email_otp_resend_after_mailbox_timeout(self):
        events = []

        class FakeSession:
            def __init__(self):
                self.session = SimpleNamespace(close=lambda: None)

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()
            network_preflight = staticmethod(lambda _session: None)
            bootstrap_authorize = staticmethod(lambda _session, _state, auth_url=None: None)
            submit_email = staticmethod(lambda _session, _email: events.append("email"))
            resend_email_otp = staticmethod(lambda _session: events.append("resend"))
            submit_email_otp = staticmethod(lambda _session, _code: events.append("otp"))
            select_workspace_and_get_callback = staticmethod(
                lambda _session, _state: "http://localhost:1455/auth/callback?code=callback-code&state=state-1"
            )
            extract_code = staticmethod(lambda _callback, _state: "callback-code")

        class FakeAPI:
            progress = staticmethod(lambda _task_id, _stage: None)
            callback = staticmethod(lambda _task_id, _callback: {"status": "succeeded"})

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with patch(
            "tools.openai_oauth_reauth_worker._wait_for_otp",
            side_effect=(WorkerError("mailbox timeout"), "482931"),
        ):
            process_claim(FakeAPI(), FakeProtocol, claim)

        self.assertEqual(events, ["email", "resend", "otp"])

    def test_process_claim_switches_login_password_to_email_otp(self):
        events = []

        class FakeSession:
            def __init__(self):
                self.session = SimpleNamespace(close=lambda: None, cookies=SimpleNamespace(jar=[]))

        class FakeProtocol:
            class AccountUnusableError(Exception):
                pass

            ProtocolSession = lambda **_kwargs: FakeSession()
            network_preflight = staticmethod(lambda _session: None)
            bootstrap_authorize = staticmethod(lambda _session, _state, auth_url=None: None)
            submit_email = staticmethod(
                lambda _session, _email: {
                    "continue_url": "https://auth.openai.com/log-in/password",
                    "page": {"type": "login_password"},
                }
            )
            start_passwordless_login = staticmethod(
                lambda _session, _url: events.append("passwordless")
            )
            resend_email_otp = staticmethod(lambda _session: events.append("resend"))
            submit_email_otp = staticmethod(lambda _session, _code: events.append("otp"))
            select_workspace_and_get_callback = staticmethod(
                lambda _session, _state: "http://localhost:1455/auth/callback?code=callback-code&state=state-1"
            )
            extract_code = staticmethod(lambda _callback, _state: "callback-code")

        class FakeAPI:
            progress = staticmethod(lambda _task_id, _stage: None)
            callback = staticmethod(lambda _task_id, _callback: {"status": "succeeded"})

        claim = {
            "task_id": 7,
            "account_id": 42,
            "login_email": "mail@example.com",
            "otp_url": "http://127.0.0.1:12345/code",
            "auth_url": "https://auth.openai.com/authorize?state=state-1",
        }
        with patch("tools.openai_oauth_reauth_worker._wait_for_otp", return_value="482931"):
            process_claim(FakeAPI(), FakeProtocol, claim)

        self.assertEqual(events, ["passwordless", "otp"])

    def test_process_claim_uses_tosub2_for_password_totp_without_logging_secrets(self):
        events = []
        with tempfile.TemporaryDirectory() as root_dir:
            root = Path(root_dir)
            (root / "src").mkdir()
            (root / "src" / "protocol-login.mjs").write_text("// test", encoding="utf-8")

            class FakeAPI:
                config = SimpleNamespace(tosub2_root=root)

                def progress(self, task_id, stage):
                    events.append(("progress", task_id, stage))

                def credentials(self, task_id, credentials, extra):
                    events.append(("credentials", task_id, credentials, extra))
                    return {"status": "succeeded"}

            def fake_run(command, **kwargs):
                self.assertEqual(Path(kwargs["cwd"]), Path(command[command.index("--sub2api-out") + 1]).parent)
                self.assertNotEqual(Path(kwargs["cwd"]), root)
                self.assertEqual(kwargs["env"]["CHATGPT_LOGIN_PASSWORD"], "password-secret")
                self.assertEqual(kwargs["env"]["CHATGPT_TOTP_SECRET"], "totp-secret")
                output_path = Path(command[command.index("--sub2api-out") + 1])
                output_path.write_text(
                    json.dumps(
                        {
                            "accounts": [
                                {
                                    "credentials": {
                                        "access_token": "access-secret",
                                        "refresh_token": "refresh-secret",
                                        "id_token": "id-secret",
                                    },
                                    "extra": {"client_id": "client-id"},
                                }
                            ]
                        }
                    ),
                    encoding="utf-8",
                )
                return SimpleNamespace(returncode=0, stdout="", stderr="")

            claim = {
                "task_id": 8,
                "account_id": 43,
                "credential_mode": "password_totp",
                "login_email": "mail@example.com",
                "password": "password-secret",
                "totp_secret": "totp-secret",
            }
            with patch("tools.openai_codex_oauth_adapter.prepare_codex_runtime", return_value=root / "src/protocol-login.mjs"), patch(
                "tools.openai_oauth_reauth_worker.subprocess.run", side_effect=fake_run
            ):
                process_claim(FakeAPI(), None, claim)

        stages = [event[2] for event in events if event[0] == "progress"]
        self.assertEqual(stages, ["starting", "protocol_connecting", "applying_credentials"])
        credential_event = next(event for event in events if event[0] == "credentials")
        self.assertEqual(credential_event[2]["access_token"], "access-secret")
        self.assertEqual(credential_event[3]["client_id"], "client-id")

    def test_extracts_structured_and_html_otp(self):
        self.assertEqual(extract_otp('{"verification_code":"123456"}'), "123456")
        self.assertEqual(extract_otp("<p>Your ChatGPT verification code is <b>654321</b></p>"), "654321")

    def test_rejects_stale_structured_otp(self):
        old = time.time() - 60
        body = json.dumps({"code": "123456", "received_at": old})
        self.assertEqual(extract_otp(body, after_ts=time.time()), "")

    def test_fetches_otp_without_worker_credentials(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), OTPHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            provider = OTPURLProvider(f"http://127.0.0.1:{server.server_port}/code")
            self.assertEqual(provider("mail@example.com"), "482931")
        finally:
            server.shutdown()
            server.server_close()

    def test_revalidates_mailbox_address_before_every_request(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), OTPHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        url = f"http://127.0.0.1:{server.server_port}/code"
        try:
            with patch(
                "tools.openai_oauth_reauth_worker.validate_otp_url",
                wraps=validate_otp_url,
            ) as validate:
                provider = OTPURLProvider(url)
                self.assertEqual(provider("mail@example.com"), "482931")
                self.assertEqual(validate.call_count, 2)
        finally:
            server.shutdown()
            server.server_close()

    def test_rejects_short_worker_token(self):
        previous = dict(os.environ)
        try:
            os.environ["SUB2API_BASE_URL"] = "http://127.0.0.1:8080"
            os.environ["OPENAI_REAUTH_WORKER_TOKEN"] = "short"
            os.environ["CODEX_PROTOCOL_ROOT"] = os.getcwd()
            with self.assertRaises(WorkerError):
                WorkerConfig.from_env()
        finally:
            os.environ.clear()
            os.environ.update(previous)

    def test_rejects_private_https_and_non_loopback_http_mailboxes(self):
        with patch.dict(os.environ, {"OPENAI_REAUTH_TRUSTED_OTP_HOSTS": ""}):
            with self.assertRaises(WorkerError):
                validate_otp_url("https://127.0.0.1/code")
            with self.assertRaises(WorkerError):
                validate_otp_url("http://192.168.1.10/code")

    def test_allows_private_https_only_for_explicitly_trusted_mailbox_host(self):
        private_address = [(None, None, None, None, ("192.168.1.10", 443))]
        with patch.dict(os.environ, {"OPENAI_REAUTH_TRUSTED_OTP_HOSTS": "mail.internal"}), patch(
            "tools.openai_oauth_reauth_worker.socket.getaddrinfo",
            return_value=private_address,
        ):
            validate_otp_url("https://mail.internal/code")
            with self.assertRaises(WorkerError):
                validate_otp_url("https://other.internal/code")
            with self.assertRaises(WorkerError):
                validate_otp_url("http://mail.internal/code")

    def test_rejects_plain_http_remote_control_api(self):
        previous = dict(os.environ)
        try:
            os.environ["SUB2API_BASE_URL"] = "http://example.com"
            os.environ["OPENAI_REAUTH_WORKER_TOKEN"] = "x" * 32
            os.environ["CODEX_PROTOCOL_ROOT"] = os.getcwd()
            with self.assertRaises(WorkerError):
                WorkerConfig.from_env()
        finally:
            os.environ.clear()
            os.environ.update(previous)

    def test_sanitizes_urls_oauth_params_and_otps(self):
        raw = (
            'failed https://mail.test/code?key=secret&state=abc for user@example.com '
            'with OTP 123456 and {"refresh_token":"hidden"} Bearer eyJhbGciOiJub25l.eyJzdWIiOiIxIn0.sig'
        )
        safe = sanitize_error(raw)
        self.assertNotIn("secret", safe)
        self.assertNotIn("abc", safe)
        self.assertNotIn("123456", safe)
        self.assertNotIn("https://", safe)
        self.assertNotIn("user@example.com", safe)
        self.assertNotIn("hidden", safe)
        self.assertNotIn("eyJ", safe)

    def test_sanitizes_provider_html_errors(self):
        safe = sanitize_error(
            "chatgpt-login status=403, body=<html><style>body{color:red}</style><body>Access denied</body></html>"
        )
        self.assertEqual(safe, "chatgpt-login status=403, body= Access denied")

    def test_wait_for_otp_skips_a_code_that_already_failed(self):
        codes = iter(("123456", "654321"))

        def provider(_email, after_ts=None):
            return next(codes)

        self.assertEqual(
            _wait_for_otp(
                provider,
                "mail@example.com",
                time.time(),
                timeout=1,
                interval=0.001,
                excluded_codes={"123456"},
            ),
            "654321",
        )

    def test_codex_callback_requires_exact_redirect_uri(self):
        self.assertTrue(
            _is_local_codex_callback(
                "http://localhost:1455/auth/callback?code=test-code&state=test-state"
            )
        )
        for callback in (
            "https://localhost:1455/auth/callback?code=test&state=test",
            "http://127.0.0.1:1455/auth/callback?code=test&state=test",
            "http://[::1]:1455/auth/callback?code=test&state=test",
            "http://localhost:1456/auth/callback?code=test&state=test",
            "http://localhost:1455/other?code=test&state=test",
            "http://localhost:1455/auth/callback/extra?code=test&state=test",
            "http://user@localhost:1455/auth/callback?code=test&state=test",
            "http://localhost:1455/auth/callback?code=test&state=test#fragment",
        ):
            with self.subTest(callback=callback):
                self.assertFalse(_is_local_codex_callback(callback))


if __name__ == "__main__":
    unittest.main()
