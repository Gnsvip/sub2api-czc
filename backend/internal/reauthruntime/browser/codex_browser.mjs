// Normal OAuth in an isolated browser. No stealth, challenge solver or proxy rotation.
import { createHash, createHmac, randomBytes, timingSafeEqual } from 'node:crypto';
import { pathToFileURL } from 'node:url';

export const CLIENT_ID = 'app_EMoamEEZ73f0CkXaXp7hrann';
export const REDIRECT = 'http://localhost:1455/auth/callback';
const AUTH = 'https://auth.openai.com';
const AUTH_CLAIM = 'https://api.openai.com/auth';
const failures = new Set(['browser_start_failed', 'security_check', 'additional_verification',
  'password_rejected', 'totp_rejected', 'identity_mismatch', 'proxy_unavailable', 'timeout',
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

export function credentialsFromTokens(tokens, input) {
  if (!tokens || typeof tokens.refresh_token !== 'string' || !tokens.refresh_token) fail('identity_mismatch');
  const access = jwt(tokens.access_token), id = jwt(tokens.id_token);
  const a = access[AUTH_CLAIM], i = id[AUTH_CLAIM];
  const email = String(id.email || '').toLowerCase();
  const user = a?.chatgpt_user_id || a?.user_id;
  const idUser = i?.chatgpt_user_id || i?.user_id;
  if ((access.client_id || access.azp) !== CLIENT_ID || ![id.aud].flat().includes(CLIENT_ID) ||
      !a?.chatgpt_account_id || !user || email !== input.login_email.toLowerCase() ||
      (access.email && access.email.toLowerCase() !== email) ||
      (i?.chatgpt_account_id && i.chatgpt_account_id !== a.chatgpt_account_id) ||
      (idUser && idUser !== user) ||
      (input.expected_workspace && input.expected_workspace !== a.chatgpt_account_id)) fail('identity_mismatch');
  return { access_token: tokens.access_token, refresh_token: tokens.refresh_token, id_token: tokens.id_token,
    client_id: CLIENT_ID, email: id.email, chatgpt_account_id: a.chatgpt_account_id,
    chatgpt_user_id: user, expires_at: access.exp, plan_type: a.chatgpt_plan_type || '' };
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
  const select = await visible(page, 'select');
  if (select) {
    const values = await select.locator('option').evaluateAll(options => options.map(option => option.value));
    if (!values.includes(expected)) fail('identity_mismatch');
    await select.selectOption(expected);
    return;
  }
  const radios = page.locator('input[type="radio"]');
  for (let index = 0; index < await radios.count(); index++) {
    if (await radios.nth(index).getAttribute('value') === expected) {
      await radios.nth(index).check();
      return;
    }
  }
  // Some consent pages show no workspace selector. The token identity check
  // below still rejects another workspace before any credentials are submitted.
  if (await visible(page, '[role="combobox"],[role="radio"],select,input[type="radio"]')) fail('identity_mismatch');
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
  let browser, context;
  let callback, callbackError, blocked = '', challengeAt = 0;
  const deadline = Date.now() + (options.timeoutMs || 180000);
  try {
    stage('browser_starting');
    try {
      browser = await chromium.launch({ executablePath: options.executablePath,
        headless: options.headless ?? true, chromiumSandbox: true, timeout: 30000,
        ...(proxy ? { proxy } : {}) });
    } catch { fail('browser_start_failed'); }
    context = await browser.newContext({ locale: 'en-US', serviceWorkers: 'block',
      acceptDownloads: false, ...(proxy ? { proxy } : {}) });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    await context.route(`${REDIRECT}**`, async route => {
      try {
        if (!route.request().isNavigationRequest() || route.request().frame() !== page.mainFrame() || callback) fail('identity_mismatch');
        callback = callbackCode(route.request().url(), state);
      } catch (error) { callbackError = error; }
      await route.fulfill({ status: 200, contentType: 'text/plain', body: 'Authorization received. You may close this page.' });
    });
    page.on('response', response => {
      if (response.request().isNavigationRequest() && response.url().startsWith(AUTH + '/')) {
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
      const reason = pageFailure(text, await page.title(), current.href);
      if (await visible(page, 'iframe[src*="challenges.cloudflare.com"],iframe[src*="recaptcha"],iframe[src*="hcaptcha"]')) fail('security_check');
      if (reason === 'security_check' || blocked === 'security_check') {
        // A normal browser may finish an initial JavaScript interstitial by
        // itself. Allow one bounded page load; never click/solve a challenge.
        challengeAt ||= Date.now();
        if (Date.now() - challengeAt >= 15000) fail('security_check');
        await new Promise(resolve => setTimeout(resolve, 400));
        continue;
      }
      if (reason) fail(reason);
      let progressed = false;
      const email = await visible(page, 'input[type="email"],input[name="email"]');
      const password = await visible(page, 'input[type="password"]');
      const code = await visible(page, 'input[autocomplete="one-time-code"],input[name="code"],input[name="otp"]');
      if (email && !sent.has('email')) {
        await email.fill(input.login_email);
        await advance(page); sent.add('email'); stage('email_submitted'); progressed = true;
      } else if (password && !sent.has('password')) {
        await password.fill(input.password);
        await advance(page); sent.add('password'); stage('password_submitted'); progressed = true;
      } else if (code && !sent.has('mfa')) {
        if (!/authenticator|authentication app|验证器|身份验证应用/i.test(text) || !input.totp_secret) fail('additional_verification');
        const remaining = 30000 - (Date.now() % 30000);
        if (remaining < 5000) await new Promise(resolve => setTimeout(resolve, remaining + 300));
        await code.fill(totp(input.totp_secret));
        await advance(page); sent.add('mfa'); stage('mfa_submitted'); progressed = true;
      } else if (/consent|select.*(?:workspace|organization)|(?:workspace|organization).*select/.test(current.pathname) && !sent.has(current.pathname)) {
        await selectWorkspace(page, input.expected_workspace);
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
    return credentialsFromTokens(JSON.parse(body), input);
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
      headless: process.env.OPENAI_CODEX_BROWSER_HEADLESS !== 'false', emit });
    emit({ type: 'result', credentials });
  } catch (error) {
    emit({ type: 'error', code: error instanceof LoginFailure ? error.code : 'worker_failed' });
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main();
