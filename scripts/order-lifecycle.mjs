import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
const base = process.env.SMOKE_BASE_URL;
const origin = process.env.SMOKE_ORIGIN;
const mailpit = process.env.MAILPIT_URL;
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function call(actor, path, method = 'GET', body, expected = 200, extra = {}) {
  const headers = { Origin: origin, ...extra };
  if (actor?.cookie) headers.Cookie = actor.cookie;
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(20000) });
  assert.equal(response.status, expected, `${method} ${path}: unexpected status`);
  const data = response.status === 204 ? null : await response.json();
  return { data, response };
}
async function until(fn, message) {
  const deadline = Date.now() + 65000;
  do {
    const value = await fn();
    if (value) return value;
    await pause(500);
  } while (Date.now() < deadline);
  throw new Error(message);
}
async function account(role) {
  const email = `validation-${role}-${Date.now()}-${randomBytes(4).toString('hex')}@u.nus.edu`;
  const password = randomBytes(24).toString('hex');
  const registration = (await call(null, '/api/v1/auth/register', 'POST', { email, password, displayName: email.split('@')[0].slice(0,50) }, 202)).data;
  const code = await until(async () => {
    const inbox = await (await fetch(mailpit + '/api/v1/messages', { signal: AbortSignal.timeout(5000) })).json();
    const message = inbox.messages.find(m => m.To?.some(to => to.Address === email));
    if (!message) return null;
    const detail = await (await fetch(mailpit + '/api/v1/message/' + message.ID, { signal: AbortSignal.timeout(5000) })).json();
    return detail.Text?.match(/verification code is: (\d{8})/)?.[1];
  }, 'Verification email was not delivered');
  await call(null, '/api/v1/auth/verify-email', 'POST', { email, code, registrationToken: registration.registrationToken });
  const login = await call(null, '/api/v1/auth/login', 'POST', { email, password });
  const actor = { id: login.data.user.id, cookie: login.response.headers.getSetCookie()[0].split(';')[0] };
  await until(async () => {
    const response = await fetch(base + '/api/v1/wallets/me', { headers: { Cookie: actor.cookie }, signal: AbortSignal.timeout(5000) });
    if (response.status === 404) return false;
    assert.equal(response.status, 200);
    const wallet = await response.json();
    return wallet.available === 100 && wallet.reserved === 0;
  }, 'Initial wallet allocation did not complete');
  return actor;
}
const requester = await account('requester');
const courier = await account('courier');
console.log('PASS: two real verified accounts each receive exactly 100 initial credits');
const places = (await call(requester, '/api/v1/orders/places')).data.places;
const pickup = places.find(p => p.type === 'supplier');
const delivery = places.find(p => p.type === 'location');
assert(pickup && delivery);
const payload = { pickup: { type: pickup.type, id: pickup.id }, delivery: { type: delivery.type, id: delivery.id }, items: [{ name: 'Validation errand', quantity: 1 }], offeredCredits: 30, expiresAt: new Date(Date.now() + 3600000).toISOString() };
const key = randomBytes(16).toString('hex');
const order = (await call(requester, '/api/v1/orders', 'POST', payload, 201, { 'Idempotency-Key': key })).data.order;
const retry = (await call(requester, '/api/v1/orders', 'POST', payload, 200, { 'Idempotency-Key': key })).data.order;
assert.equal(retry.id, order.id);
const wallet = actor => call(actor, '/api/v1/wallets/me').then(r => r.data);
assert.deepEqual(await wallet(requester), { userId: requester.id, available: 70, reserved: 30 });
console.log('PASS: creation reserves credits once; the same-key retry returns the same order');
const action = (actor, id, action, expected = 200) => call(actor, `/api/v1/orders/${id}/actions`, 'POST', { action }, expected).then(r => r.data.order);
assert.equal((await action(courier, order.id, 'accept')).status, 'accepted');
assert.equal((await action(courier, order.id, 'pick_up')).status, 'picked up');
assert.equal((await action(courier, order.id, 'deliver')).status, 'delivered');
assert.equal((await call(courier, '/api/v1/orders/session')).data.courierBusy, false);
assert.equal((await wallet(requester)).reserved, 30);
assert.equal((await wallet(courier)).available, 100);
console.log('PASS: acceptance, pickup and delivery release courier capacity without premature payout');
await action(requester, order.id, 'confirm', 202);
await until(async () => {
  const [a, b] = await Promise.all([wallet(requester), wallet(courier)]);
  return a.available === 70 && a.reserved === 0 && b.available === 130 && b.reserved === 0;
}, 'Real Credit payout did not complete');
assert.equal((await action(requester, order.id, 'confirm')).status, 'completed');
const history = actor => call(actor, '/api/v1/wallets/me/transactions?limit=100').then(r => r.data.transactions);
assert.equal((await history(requester)).filter(t => t.kind === 'spend' && t.orderId === order.id).length, 1);
assert.equal((await history(courier)).filter(t => t.kind === 'receive' && t.orderId === order.id).length, 1);
console.log('PASS: confirmation settles exactly one payout and conserves the combined 200 credits');
const cancelPayload = { ...payload, offeredCredits: 10 };
const cancelled = (await call(requester, '/api/v1/orders', 'POST', cancelPayload, 201, { 'Idempotency-Key': randomBytes(16).toString('hex') })).data.order;
assert.equal((await wallet(requester)).reserved, 10);
assert.equal((await action(requester, cancelled.id, 'cancel')).status, 'cancelled');
assert.equal((await action(requester, cancelled.id, 'cancel')).status, 'cancelled');
await until(async () => { const w = await wallet(requester); return w.available === 70 && w.reserved === 0; }, 'Real Credit refund did not complete');
assert.equal((await history(requester)).filter(t => t.kind === 'release' && t.orderId === cancelled.id).length, 1);
console.log('PASS: repeated cancellation produces exactly one refund');
await call(requester, '/api/v1/auth/logout', 'POST', undefined, 204);
await call(courier, '/api/v1/auth/logout', 'POST', undefined, 204);
console.log('Real four-service Order/Credit lifecycle check passed.');
