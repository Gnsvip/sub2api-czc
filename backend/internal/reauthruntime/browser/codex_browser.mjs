// Official OAuth in isolated CloakBrowser sessions; challenges still stop login.
import { createHash, createHmac, randomBytes, randomInt, timingSafeEqual } from 'node:crypto';
import { pathToFileURL } from 'node:url';

export const CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann';
export const REDIRECT = 'http://localhost:1455/auth/callback';
const AUTH = 'https://auth.openai.com';
const AUTH_CLAIM = 'https://api.openai.com/auth';
const failures = new Set(['browser_start_failed', 'security_check', 'additional_verification',
  'password_rejected', 'totp_rejected', 'identity_mismatch', 'workspace_unavailable', 'proxy_unavailable', 'timeout',
  'transport_error', 'account_blocked', 'rate_limited', 'service_error', 'worker_failed']);

export class LoginFailure extends Error {
  constructor(code) { super(code); this.code = failures.has(code) ? code : 'worker_failed'; }
}
const fail = code => { throw new LoginFailure(code); };

export function proxyOptions(raw) {
  if (!raw) return undefined;
  try {
    const url = new URL(raw);
    if (!['http:', 'https:', 'socks5:'].includes(url.protocol) || !url.hostname ||
        url.search || url.hash || !['', '/'].includes(url.pathname)) fail('proxy_unavailable');
    if (url.protocol === 'socks5:' && (url.username || url.password)) fail('proxy_unavailable');
    return { server: `${url.protocol}//${url.host}`,
      ...(url.username ? { username: decodeURIComponent(url.username), password: decodeURIComponent(url.password) } : {}) };
  } catch { fail('proxy_unavailable'); }
}

export function callbackCode(raw, state) {
  const url = new URL(raw);
  if (url.origin + url.pathname !== REDIRECT || url.username || url.password || url.hash ||
      url.searchParams.getAll('state').length !== 1 || url.searchParams.getAll('code').length !== 1 ||
      url.searchParams.has('error')) fail('identity_mismatch');
  const actual = Buffer.from(url.searchParams.get('state'));
  const expected = Buffer.from(state);
  if (actual.length !== expected.length || !timingSafeEqual(actual, expected)) fail('identity_mismatch');
  const code = url.searchParams.get('code');
  if (!code || code.length > 4096) fail('identity_mismatch');
  return code;
}

export function totp(secret, now = Date.now()) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  const clean = secret.replace(/\s|=+$/g, '').toUpperCase();
  if (!/^[A-Z2-7]{16,256}$/.test(clean)) fail('totp_rejected');
  let bits = 0, value = 0;
  const bytes = [];
  for (const char of clean) {
    value = (value << 5) | alphabet.indexOf(char);
    bits += 5;
    if (bits >= 8) { bits -= 8; bytes.push((value >>> bits) & 255); }
  }
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(now / 30000)));
  const digest = createHmac('sha1', Buffer.from(bytes)).update(counter).digest();
  return String((digest.readUInt32BE(digest[19] & 15) & 0x7fffffff) % 1000000).padStart(6, '0');
}

function jwt(raw) {
  try {
    if (typeof raw !== 'string' || raw.length > 32768 || raw.split('.').length !== 3) fail('identity_mismatch');
    const payload = JSON.parse(Buffer.from(raw.split('.')[1], 'base64url'));
    if (!(payload.exp > Date.now() / 1000)) fail('identity_mismatch');
    return payload;
  } catch { fail('identity_mismatch'); }
}

export function credentialsFromTokens(tokens, input, selection) {
  if (!tokens || typeof tokens.refresh_token !== 'string' || !tokens.refresh_token) fail('identity_mismatch');
  const access = jwt(tokens.access_token), id = jwt(tokens.id_token);
  const a = access[AUTH_CLAIM], i = id[AUTH_CLAIM];
  const email = String(id.email || '').toLowerCase();
  const user = a?.chatgpt_user_id || a?.user_id;
  const idUser = i?.chatgpt_user_id || i?.user_id;
  const fallback = selection?.fallbackFrom && selection.fallbackFrom === input.expected_workspace &&
    selection.workspace === a?.chatgpt_account_id && selection.workspace !== input.expected_workspace &&
    ['free', 'go', 'plus', 'pro'].includes(a?.chatgpt_plan_type) &&
    ['free', 'go', 'plus', 'pro'].includes(i?.chatgpt_plan_type);
  if ((access.client_id || access.azp) !== CLIENT_ID || ![id.aud].flat().includes(CLIENT_ID) ||
      !a?.chatgpt_account_id || !user || email !== input.login_email.toLowerCase() ||
      (access.email && access.email.toLowerCase() !== email) ||
      (i?.chatgpt_account_id && i.chatgpt_account_id !== a.chatgpt_account_id) ||
      (idUser && idUser !== user) ||
      (input.expected_workspace && input.expected_workspace !== a.chatgpt_account_id && !fallback) ||
      (selection?.fallbackFrom && !fallback)) fail('identity_mismatch');
  return { access_token: tokens.access_token, refresh_token: tokens.refresh_token, id_token: tokens.id_token,
    client_id: CLIENT_ID, email: id.email, chatgpt_account_id: a.chatgpt_account_id,
    chatgpt_user_id: user, expires_at: access.exp, plan_type: a.chatgpt_plan_type || '',
    ...(fallback ? { codex_workspace_fallback_from: input.expected_workspace,
      codex_personal_workspace_only: selection.personalOnly === true } : {}) };
}

export function pageFailure(text, title = '', url = '') {
  if (/just a moment|checking your browser|security check|verify (?:that )?you (?:are|’re|'re) human|验证码挑战|安全校验|验证您是真人/i.test(title + '\n' + text) ||
      url.includes('/cdn-cgi/')) return 'security_check';
  if (/incorrect password|invalid password|wrong password|密码不正确|密码错误/i.test(text)) return 'password_rejected';
  if (/invalid (?:verification |authentication )?code|incorrect code|code (?:is )?(?:incorrect|invalid|expired)|验证码(?:无效|错误|已过期)/i.test(text)) return 'totp_rejected';
  if (/account (?:has been |is )?(?:deactivated|deleted|disabled)|账号已(?:停用|删除)/i.test(text)) return 'account_blocked';
  if (/verify your phone|add a phone|check your (?:email|inbox)|email verification|验证手机号|查看您的邮箱/i.test(text)) return 'additional_verification';
  return '';
}

async function visible(page, selector) {
  const elements = page.locator(selector);
  for (let index = 0; index < Math.min(await elements.count(), 20); index++) {
    const element = elements.nth(index);
    if (await element.isVisible() && await element.isEnabled()) return element;
  }
  return null;
}

async function advance(page) {
  const submit = await visible(page, 'button[type="submit"],input[type="submit"]');
  const button = submit || page.getByRole('button', { name: /^(continue|allow|authorize|verify|继续|同意|授权|验证)$/i }).first();
  await button.click({ timeout: 5000 });
}

async function selectWorkspace(page, expected) {
  if (!expected) return;
  const choices = [];
  const select = await visible(page, 'select');
  if (select) {
    const options = await select.locator('option').evaluateAll(items => items
      .filter(option => !option.disabled && option.value)
      .map(option => ({ value: option.value, label: option.textContent })));
    choices.push(...options.map(option => ({ ...option, choose: () => select.selectOption(option.value) })));
  } else {
    const radios = page.locator('input[type="radio"]');
    for (let index = 0; index < await radios.count(); index++) {
      const radio = radios.nth(index);
      const checked = await radio.isChecked();
      // With one workspace, the live consent page locks its already-selected
      // radio. It is still the active choice; do not try to click a disabled UI.
      if (!(await radio.isEnabled()) && !checked) continue;
      const choice = await radio.evaluate(element => ({ value: element.value,
        label: [...(element.labels || [])].map(label => label.innerText || label.textContent).join(' ') ||
          element.getAttribute('aria-label') || (element.getAttribute('aria-labelledby') || '')
            .split(/\s+/).map(id => document.getElementById(id)?.textContent || '').join(' ') }));
      if (choice.value) choices.push({ ...choice, choose: async () => { if (!checked) await radio.check(); } });
    }
  }
  if (!choices.length) return; // Keep the final token check for unrecognized widgets.
  const original = choices.find(choice => choice.value === expected);
  if (original) { await original.choose(); return { workspace: expected }; }
  // Only an explicitly labeled personal choice may replace an unavailable
  // original workspace. Never choose another team or merely the first option.
  const personal = choices.filter(choice => /(?:^|\s)(?:personal(?:\s+(?:account|workspace))?|个人(?:账号|账户|工作区))(?:$|\s)/i.test(String(choice.label || '').trim()));
  if (personal.length !== 1) fail('workspace_unavailable');
  await personal[0].choose();
  return { workspace: personal[0].value, fallbackFrom: expected, personalOnly: choices.length === 1 };
}

export async function authorize(chromium, input, options = {}) {
  const emit = options.emit || (() => {});
  const stage = name => emit({ type: 'stage', stage: name });
  const proxy = proxyOptions(input.proxy_url);
  if (!input.login_email || !input.password) fail('worker_failed');
  const verifier = randomBytes(32).toString('base64url');
  const state = randomBytes(32).toString('base64url');
  const params = new URLSearchParams({ client_id: CLIENT_ID, response_type: 'code', redirect_uri: REDIRECT,
    scope: 'openid profile email offline_access', state, code_challenge_method: 'S256',
    code_challenge: createHash('sha256').update(verifier).digest('base64url'),
    codex_cli_simplified_flow: 'true', id_token_add_organizations: 'true' });
  if (input.expected_workspace) params.set('allowed_workspace_id', input.expected_workspace);
  let browser, context;
  let callback, callbackError, workspaceSelection, blocked = '', challengeAt = 0;
  const deadline = Date.now() + (options.timeoutMs || 180000);
  try {
    stage('browser_starting');
    try {
      browser = await chromium.launch({ executablePath: options.executablePath,
        headless: options.headless ?? false, chromiumSandbox: true, timeout: 30000,
        // Match the tested Cloak 0.6 defaults, while retaining Chromium sandbox.
        ignoreDefaultArgs: ['--enable-automation', '--enable-unsafe-swiftshader'],
        args: [`--fingerprint=${randomInt(10000, 100000)}`, '--fingerprint-platform=windows',
          ...(!proxy ? ['--no-proxy-server'] : [])],
        ...(proxy ? { proxy } : {}) });
    } catch { fail('browser_start_failed'); }
    context = await browser.newContext({ locale: 'en-US', viewport: null, serviceWorkers: 'block',
      acceptDownloads: false, ...(proxy ? { proxy } : {}) });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    let callbackRequest;
    const captureCallback = request => {
      try {
        if (!request.isNavigationRequest() || request.frame() !== page.mainFrame() ||
            (callbackRequest && callbackRequest !== request)) fail('identity_mismatch');
        callbackRequest = request;
        callback = callbackCode(request.url(), state);
      } catch (error) { callbackError = error; }
    };
    page.on('request', request => {
      // Redirected navigations also emit request even when a route handler is
      // not re-entered for a redirect chain. No localhost listener is needed.
      if (request.url().startsWith(REDIRECT)) captureCallback(request);
    });
    await context.route(`${REDIRECT}**`, async route => {
      captureCallback(route.request());
      await route.fulfill({ status: 200, contentType: 'text/plain', body: 'Authorization received. You may close this page.' });
    });
    page.on('response', response => {
      if (response.request().isNavigationRequest() && response.request().frame() === page.mainFrame() &&
          response.url().startsWith(AUTH + '/')) {
        if (response.status() === 429) blocked = 'rate_limited';
        if (response.status() >= 500) blocked = 'service_error';
        if (response.headers()['cf-mitigated'] === 'challenge') {
          blocked = 'security_check';
          challengeAt ||= Date.now();
        } else if (response.status() < 400 && blocked === 'security_check') {
          blocked = '';
        }
      }
    });
    stage('protocol_connecting');
    await page.goto(`${AUTH}/oauth/authorize?${params}`, { waitUntil: 'domcontentloaded', timeout: 30000 });
    const sent = new Set();
    let idleSince = Date.now();
    while (!callback) {
      if (callbackError) throw callbackError;
      if (blocked && blocked !== 'security_check') fail(blocked);
      if (Date.now() > deadline) fail('timeout');
      if (page.isClosed()) fail('transport_error');
      const current = new URL(page.url());
      if (callback) break;
      if (current.origin !== AUTH) fail('additional_verification');
      const text = (await page.locator('body').innerText()).slice(0, 24000);
      if (!sent.has('password') && !sent.has('password_method')) {
        const usePassword = page.getByRole('button', { name: /^(?:use (?:your )?password(?: instead)?|log in with (?:a )?password|使用密码(?:登录)?|改用密码(?:登录)?)$/i })
          .or(page.getByRole('link', { name: /^(?:use (?:your )?password(?: instead)?|log in with (?:a )?password|使用密码(?:登录)?|改用密码(?:登录)?)$/i })).first();
        if (await usePassword.isVisible()) {
          await usePassword.click(); sent.add('password_method'); idleSince = Date.now();
          continue;
        }
      }
      const reason = pageFailure(text, await page.title(), current.href);
      const challengeVisible = await visible(page, 'iframe[src*="challenges.cloudflare.com"],iframe[src*="recaptcha"],iframe[src*="hcaptcha"]');
      const email = await visible(page, 'input[type="email"],input[name="email"]');
      const password = await visible(page, 'input[type="password"]');
      const code = await visible(page, 'input[autocomplete="one-time-code"],input[name="code"],input[name="otp"]');
      // An interstitial may replace its DOM without another navigation/200.
      // Only an actual login form with no remaining challenge clears its latch.
      if (blocked === 'security_check' && !reason && !challengeVisible && (email || password || code)) blocked = '';
      if (reason === 'security_check' || blocked === 'security_check' || challengeVisible) {
        // A normal browser may finish an initial JavaScript interstitial by
        // itself, including its iframe. Never click/solve a challenge, submit
        // credentials through it, or extend the overall authorization budget.
        challengeAt ||= Date.now();
        if (Date.now() - challengeAt >= 15000) fail('security_check');
        idleSince = Date.now();
        await new Promise(resolve => setTimeout(resolve, 400));
        continue;
      }
      challengeAt = 0;
      if (reason) fail(reason);
      let progressed = false;
      if (email && !sent.has('email')) {
        await email.fill(input.login_email);
        await advance(page); sent.add('email'); stage('email_submitted'); progressed = true;
      } else if (password && !sent.has('password')) {
        await password.fill(input.password);
        await advance(page); sent.add('password'); stage('password_submitted'); progressed = true;
      } else if (code && !sent.has('mfa')) {
        if (!input.totp_secret) fail('additional_verification');
        // Navigation may finish between the body read and locating the input.
        // Read the current label again; never send a TOTP to an email OTP form.
        const mfaText = await page.locator('body').innerText();
        if (!/authenticator|authentication app|验证器|身份验证应用/i.test(mfaText)) {
          if (Date.now() - idleSince > 30000) fail('additional_verification');
          await new Promise(resolve => setTimeout(resolve, 400));
          continue;
        }
        const remaining = 30000 - (Date.now() % 30000);
        if (remaining < 5000) await new Promise(resolve => setTimeout(resolve, remaining + 300));
        await code.fill(totp(input.totp_secret));
        await advance(page); sent.add('mfa'); stage('mfa_submitted'); progressed = true;
      } else if (/consent|select.*(?:workspace|organization)|(?:workspace|organization).*select/.test(current.pathname) && !sent.has(current.pathname)) {
        const selected = await selectWorkspace(page, input.expected_workspace);
        if (selected) workspaceSelection = selected;
        await advance(page); sent.add(current.pathname); stage('waiting_callback'); progressed = true;
      }
      if (progressed) idleSince = Date.now();
      if (Date.now() - idleSince > 30000) fail('additional_verification');
      await new Promise(resolve => setTimeout(resolve, 400));
    }
    if (callbackError) throw callbackError;
    stage('exchanging_token');
    // The browser API client inherits this context's original proxy.
    const response = await context.request.post(`${AUTH}/oauth/token`, { maxRedirects: 0, timeout: 30000,
      form: { grant_type: 'authorization_code', client_id: CLIENT_ID, redirect_uri: REDIRECT,
        code: callback, code_verifier: verifier } });
    if (response.status() !== 200) fail(response.status() === 429 ? 'rate_limited' : 'service_error');
    const body = await response.body();
    if (body.length > 1048576) fail('worker_failed');
    return credentialsFromTokens(JSON.parse(body), input, workspaceSelection);
  } catch (error) {
    if (error instanceof LoginFailure) throw error;
    // Never propagate Playwright errors: they can include URLs, form data and tokens.
    if (error?.name === 'TimeoutError') fail('timeout');
    fail(proxy ? 'proxy_unavailable' : 'transport_error');
  } finally {
    if (context) await context.close().catch(() => {});
    if (browser) await browser.close().catch(() => {});
  }
}

async function main() {
  const emit = event => process.stdout.write(JSON.stringify(event) + '\n');
  try {
    let raw = '';
    for await (const chunk of process.stdin) {
      raw += chunk;
      if (raw.length > 32768) fail('worker_failed');
    }
    const input = JSON.parse(raw); raw = '';
    const { chromium } = await import(pathToFileURL(process.argv[2]).href);
    const credentials = await authorize(chromium, input, {
      executablePath: process.env.OPENAI_CODEX_BROWSER_EXECUTABLE,
      headless: process.env.OPENAI_CODEX_BROWSER_HEADLESS === 'true', emit });
    emit({ type: 'result', credentials });
  } catch (error) {
    emit({ type: 'error', code: error instanceof LoginFailure ? error.code : 'worker_failed' });
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
