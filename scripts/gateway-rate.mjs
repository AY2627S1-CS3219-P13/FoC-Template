import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';

const base = process.env.SMOKE_BASE_URL;
const origin = process.env.SMOKE_ORIGIN;
assert(base && origin, 'Gateway test configuration missing');

async function call(path, method = 'GET', body, extra = {}) {
  const headers = { Origin: origin, ...extra };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  return fetch(base + path, {
    method, headers, body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(5000),
  });
}
async function expectLimited(response) {
  assert.equal(response.status, 429);
  assert.match(response.headers.get('content-type') || '', /application\/json/);
  assert.equal(response.headers.get('retry-after'), '1', 'Gateway must produce the IP rejection');
  assert.equal(response.headers.get('cache-control'), 'no-store');
  assert.equal((await response.json()).error.code, 'rate_limited');
}
async function exhaust(path, method, upstreamStatus) {
  const deadline = Date.now() + 10000;
  let admitted = 0;
  while (Date.now() < deadline && admitted < 120) {
    const response = await call(path, method, {}, {
      'X-Forwarded-For': `198.51.100.${admitted % 254 + 1}`,
      'X-Real-IP': `203.0.113.${admitted % 254 + 1}`,
    });
    if (response.status === 429) {
      await expectLimited(response);
      return admitted;
    }
    assert.equal(response.status, upstreamStatus, `Unexpected ${method} ${path} response`);
    await response.arrayBuffer();
    admitted++;
  }
  throw new Error('Authentication quota was bypassed by changing forwarded headers');
}

switch (process.argv[2]) {
  case 'quota': {
    const admitted = await exhaust('/api/v1/auth/register', 'POST', 400);
    // These entry points share the same authentication zone, not separate budgets.
    await exhaust('/api/v1/users/me/password', 'PUT', 401);
    await exhaust('/api/v1/admin/users/11111111-1111-4111-8111-111111111111/role', 'PUT', 401);
    for (const path of ['/api/v1/users/me', '/api/v1/suppliers', '/api/v1/wallets/me', '/api/v1/orders?scope=mine']) {
      const response = await call(path);
      assert.equal(response.status, 401, `${path} must reach its normal authentication check`);
      await response.arrayBuffer();
    }
    assert.equal((await call('/orders')).status, 200);
    assert.equal((await call('/api/v1/auth/logout', 'POST')).status, 204);
    console.log(`PASS: client A is limited after ${admitted} admitted requests; spoofed headers cannot reset its quota`);
    console.log('PASS: password/role writes share the quota; reads, Order UI and logout remain accessible');
    break;
  }
  case 'independent': {
    const email = process.env.GATEWAY_RATE_EMAIL;
    assert(email, 'Unique fixture email missing');
    const response = await call('/api/v1/auth/register', 'POST', {
      email, displayName: email.split('@')[0], password: randomBytes(24).toString('hex'),
    });
    assert.equal(response.status, 202, 'Client A must not exhaust client B’s registration quota');
    console.log('PASS: client B registers with 202 after client A is limited');
    break;
  }
  case 'account': {
    const body = { email: process.env.GATEWAY_RATE_ACCOUNT_EMAIL, password: randomBytes(24).toString('hex') };
    assert(body.email, 'Unique account-rate fixture missing');
    for (let i = 0; i < 10; i++) {
      const response = await call('/api/v1/auth/login', 'POST', body);
      assert.equal(response.status, 401);
      await response.arrayBuffer();
    }
    console.log('PASS: ten failed login attempts reach User’s account counter');
    break;
  }
  case 'account-other-ip': {
    const response = await call('/api/v1/auth/login', 'POST', {
      email: process.env.GATEWAY_RATE_ACCOUNT_EMAIL, password: randomBytes(24).toString('hex'),
    });
    assert.equal(response.status, 429, 'Another client IP must not reset the account counter');
    assert.equal(response.headers.get('retry-after'), '60', 'User must produce the account rejection');
    assert.equal((await response.json()).error.code, 'rate_limited');
    console.log('PASS: another client IP receives User’s account-based 429');
    break;
  }
  default: throw new Error('Specify a gateway regression phase');
}
