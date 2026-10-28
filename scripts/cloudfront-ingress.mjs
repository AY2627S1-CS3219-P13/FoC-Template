import assert from 'node:assert/strict';
import http from 'node:http';
import os from 'node:os';
import { setTimeout as delay } from 'node:timers/promises';

const mode = process.argv[2];
if (mode === 'server') {
  const handler = (request, response) => {
    response.setHeader('Content-Type', 'application/json');
    response.end(JSON.stringify(request.headers));
  };
  http.createServer(handler).listen(8080, '0.0.0.0');
  http.createServer(handler).listen(3000, '0.0.0.0');
} else {
  const request = (path, headers = {}) => fetch(`http://gateway:8080${path}`, {
    headers: { Host: 'demo.cloudfront.net', ...headers },
    signal: AbortSignal.timeout(5000),
  });
  let ready = false;
  for (let attempt = 0; attempt < 30; attempt++) {
    try {
      const result = await request('/');
      await result.text();
      if (result.ok) { ready = true; break; }
    } catch { /* Wait for Docker DNS and the disposable upstream. */ }
    await delay(500);
  }
  assert.ok(ready, 'Nginx and the echo upstream must become ready');

  const headers = (client, forged = '203.0.113.200') => ({
    // A viewer may supply a forged prefix. CloudFront appends the actual viewer,
    // then ALB appends the private CloudFront ENI address.
    'X-Forwarded-For': `${forged}, ${client}, 10.20.17.37`,
    'CloudFront-Forwarded-Proto': 'https',
    'X-Forwarded-Proto': 'https',
    'X-Real-IP': forged,
  });
  if (mode === 'trusted') {
    const first = await request('/', headers('198.51.100.10'));
    const echoed = await first.json();
    assert.equal(echoed['x-real-ip'], '198.51.100.10');
    assert.equal(echoed['x-forwarded-for'], '198.51.100.10');
    assert.equal(echoed['x-forwarded-proto'], 'https');
    assert.equal(echoed.host, 'demo.cloudfront.net');
    const malformed = await request('/', {
      ...headers('198.51.100.10'), 'CloudFront-Forwarded-Proto': 'HTTPS, http',
    });
    assert.equal((await malformed.json())['x-forwarded-proto'], 'http');
    const ipv6 = await request('/', headers('2001:db8::10'));
    assert.equal((await ipv6.json())['x-real-ip'], '2001:db8::10');
    const health = await request('/healthz');
    assert.equal(health.status, 200, 'ALB health checks need no viewer headers');
    await Promise.all(Array.from({ length: 60 }, async (_, i) => {
      const response = await request('/api/v1/auth/login', headers('198.51.100.10', `203.0.113.${i + 1}`));
      assert.equal(response.status, 200);
      await response.text();
    }));
    const blocked = await request('/api/v1/auth/login', headers('198.51.100.10', '192.0.2.99'));
    assert.equal(blocked.status, 429, 'changing a forged prefix must not reset the viewer quota');
    assert.equal((await blocked.json()).error.code, 'rate_limited');
    const other = await request('/api/v1/auth/login', headers('198.51.100.11'));
    assert.equal(other.status, 200, 'another viewer behind the same proxy needs its own quota');
    await other.text();
    console.log('PASS: trusted chain, protocol, host, health and independent viewer quotas');
  } else {
    assert.equal(mode, 'untrusted');
    const ownIP = Object.values(os.networkInterfaces()).flat()
      .find(address => address.family === 'IPv4' && !address.internal).address;
    const first = await request('/', headers('198.51.100.10'));
    const echoed = await first.json();
    assert.equal(echoed['x-real-ip'], ownIP);
    assert.equal(echoed['x-forwarded-for'], ownIP);
    assert.equal(echoed['x-forwarded-proto'], 'http');
    await Promise.all(Array.from({ length: 60 }, async (_, i) => {
      const response = await request('/api/v1/auth/login', headers(`198.51.100.${i + 1}`));
      assert.equal(response.status, 200);
      await response.text();
    }));
    const blocked = await request('/api/v1/auth/login', headers('192.0.2.99'));
    assert.equal(blocked.status, 429, 'untrusted callers must not choose a new rate-limit identity');
    await blocked.text();
    console.log('PASS: untrusted identity/protocol headers ignored and quota cannot be bypassed');
  }
}
