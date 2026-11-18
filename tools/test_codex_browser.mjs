import assert from 'node:assert/strict';
import test from 'node:test';
import { pathToFileURL } from 'node:url';
import { CLIENT_ID, REDIRECT, authorize, callbackCode, credentialsFromTokens, pageFailure, proxyOptions, totp } from '../backend/internal/reauthruntime/browser/codex_browser.mjs';

const jwt = value => `e30.${Buffer.from(JSON.stringify(value)).toString('base64url')}.signature`;
const auth = { chatgpt_account_id: 'workspace', chatgpt_user_id: 'user', chatgpt_plan_type: 'pro' };
const exp = Math.floor(Date.now() / 1000) + 3600;
const input = { login_email: 'test@example.com', password: 'synthetic-password',
  totp_secret: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ', proxy_url: '', expected_workspace: 'workspace' };
const tokens = { access_token: jwt({ client_id: CLIENT_ID, exp, 'https://api.openai.com/auth': auth }),
  id_token: jwt({ aud: CLIENT_ID, exp, email: input.login_email, 'https://api.openai.com/auth': auth }), refresh_token: 'synthetic-refresh' };

test('callback rejects mismatch, duplicate fields, errors and another callback origin', () => {
  assert.equal(callbackCode(`${REDIRECT}?code=ok&state=state`, 'state'), 'ok');
  for (const url of [
    `${REDIRECT}?code=ok&state=other`, `${REDIRECT}?code=ok&state=state&state=state`,
    `${REDIRECT}?code=ok&code=two&state=state`, `${REDIRECT}?code=ok&state=state&error=denied`,
    `${REDIRECT}?code=ok&state=state#fragment`, 'http://evil.test/auth/callback?code=ok&state=state',
  ]) assert.throws(() => callbackCode(url, 'state'), { code: 'identity_mismatch' });
});

test('TOTP uses RFC 6238 and token output preserves Codex client and actual workspace', () => {
  assert.equal(totp(input.totp_secret, 59000), '287082');
  assert.throws(() => totp('123456'), { code: 'totp_rejected' });
  const credentials = credentialsFromTokens(tokens, input);
  assert.equal(credentials.client_id, CLIENT_ID);
  assert.equal(credentials.chatgpt_account_id, 'workspace');
  assert.equal(credentials.refresh_token, 'synthetic-refresh');
  assert.throws(() => credentialsFromTokens(tokens, { ...input, expected_workspace: 'other' }), { code: 'identity_mismatch' });
  assert.throws(() => credentialsFromTokens(tokens, { ...input, login_email: 'other@example.com' }), { code: 'identity_mismatch' });
  assert.throws(() => credentialsFromTokens({ ...tokens, access_token: jwt({ exp, client_id: 'wrong' }) }, input), { code: 'identity_mismatch' });
  assert.throws(() => credentialsFromTokens({ ...tokens, id_token: jwt({ exp: 1, email: input.login_email }) }, input), { code: 'identity_mismatch' });
});

test('proxy errors fail closed and diagnostics are fixed codes', () => {
  assert.deepEqual(proxyOptions('http://alice:password@127.0.0.1:9000'), { server: 'http://127.0.0.1:9000', username: 'alice', password: 'password' });
  assert.deepEqual(proxyOptions('socks5://127.0.0.1:9000'), { server: 'socks5://127.0.0.1:9000' });
  for (const proxy of ['file:///tmp/proxy', 'socks5://alice:password@host:9000', 'http://host/proxy']) {
    assert.throws(() => proxyOptions(proxy), { code: 'proxy_unavailable' });
  }
  for (const [text, expected] of [['Incorrect password', 'password_rejected'], ['Invalid verification code', 'totp_rejected'],
    ['Check your email', 'additional_verification'], ['Verify you are human', 'security_check']]) {
    assert.equal(pageFailure(text), expected);
  }
});

test('isolated browser executes login, TOTP, workspace, callback and token exchange', {
  skip: !process.env.CODEX_TEST_PLAYWRIGHT,
}, async t => {
  const { chromium } = await import(pathToFileURL(process.env.CODEX_TEST_PLAYWRIGHT).href);
  for (const scenario of ['success', 'password_choice', 'password_rejected', 'totp_rejected', 'security_check', 'additional_verification', 'bad_state', 'wrong_workspace']) {
    await t.test(scenario, async () => {
      let actualBrowser, actualContext, state, challenge, submitted = [], tokenCalls = 0;
      const stages = [];
      const injected = { launch: async options => {
        assert.equal(options.chromiumSandbox, true);
        actualBrowser = await chromium.launch({ ...options, executablePath: process.env.CODEX_TEST_CHROME });
        return { close: () => actualBrowser.close(), newContext: async contextOptions => {
          actualContext = await actualBrowser.newContext(contextOptions);
          await actualContext.route('https://auth.openai.com/**', async route => {
            const url = new URL(route.request().url());
            const form = (label, field, next) => `<h1>${label}</h1><form action="${next}" method="post">${field}<button type="submit">Continue</button></form>`;
            let body;
            if (url.pathname === '/oauth/authorize') {
              state = url.searchParams.get('state'); challenge = url.searchParams.get('code_challenge');
              assert.equal(url.searchParams.get('allowed_workspace_id'), input.expected_workspace);
              body = scenario === 'security_check' ? '<h1>Verify you are human</h1>' : form('Log in', '<input type="email" name="email">', '/password');
            } else if (url.pathname === '/password') {
              submitted.push(new URLSearchParams(route.request().postData()).get('email'));
              body = scenario === 'password_choice' ? '<p>Check your email</p><a href="/use-password">Use password instead</a>' : form('Password', '<input type="password" name="password">', '/mfa');
            } else if (url.pathname === '/use-password') {
              body = form('Password', '<input type="password" name="password">', '/mfa');
            } else if (url.pathname === '/mfa') {
              submitted.push(new URLSearchParams(route.request().postData()).get('password'));
              body = scenario === 'password_rejected' ? '<p>Incorrect password</p>' : scenario === 'additional_verification' ? '<p>Check your email</p>' : form('Enter the code from your authenticator app', '<input autocomplete="one-time-code" name="code">', '/consent');
            } else if (url.pathname === '/consent') {
              const code = new URLSearchParams(route.request().postData()).get('code');
              assert.match(code, /^\d{6}$/); submitted.push('totp');
              body = scenario === 'totp_rejected' ? '<p>Invalid verification code</p>' : form('Authorize Codex', '<select name="workspace"><option value="other">Other</option><option value="workspace">Default</option></select>', '/finish');
            } else if (url.pathname === '/finish') {
              assert.equal(new URLSearchParams(route.request().postData()).get('workspace'), 'workspace');
              await route.fulfill({ status: 302, headers: { location: `${REDIRECT}?code=test-code&state=${scenario === 'bad_state' ? 'wrong' : state}` } });
              return;
            } else throw new Error('unexpected fixture path');
            await route.fulfill({ status: 200, contentType: 'text/html', body });
          });
          return new Proxy(actualContext, { get(target, property) {
            if (property === 'request') return { post: async (url, request) => {
              tokenCalls++;
              assert.equal(url, 'https://auth.openai.com/oauth/token');
              assert.equal(request.maxRedirects, 0);
              assert.equal(request.form.code, 'test-code');
              const { createHash } = await import('node:crypto');
              assert.equal(createHash('sha256').update(request.form.code_verifier).digest('base64url'), challenge);
              const payload = scenario === 'wrong_workspace' ? { ...tokens, access_token: jwt({ client_id: CLIENT_ID, exp,
                'https://api.openai.com/auth': { ...auth, chatgpt_account_id: 'other' } }) } : tokens;
              return { status: () => 200, body: async () => Buffer.from(JSON.stringify(payload)) };
            } };
            const value = target[property];
            return typeof value === 'function' ? value.bind(target) : value;
          } });
        } };
      } };
      const promise = authorize(injected, input, { headless: process.env.CODEX_TEST_HEADED !== '1', timeoutMs: 30000, emit: event => stages.push(event) });
      if (scenario === 'success' || scenario === 'password_choice') {
        const result = await promise;
        assert.equal(result.client_id, CLIENT_ID);
        assert.deepEqual(submitted, [input.login_email, input.password, 'totp']);
        assert.equal(tokenCalls, 1);
        assert.ok(stages.some(event => event.stage === 'exchanging_token'));
      } else {
        const code = ['bad_state', 'wrong_workspace'].includes(scenario) ? 'identity_mismatch' : scenario;
        await assert.rejects(promise, { code });
        if (scenario !== 'wrong_workspace') assert.equal(tokenCalls, 0);
      }
      assert.equal(actualBrowser.isConnected(), false, 'browser must close on success and failure');
      assert.ok(stages.every(event => !JSON.stringify(event).includes('synthetic')));
    });
  }
});
