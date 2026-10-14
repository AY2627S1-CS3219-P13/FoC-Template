import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { spawn } from "node:child_process";

// Execute the existing smoke script unchanged against documented HTTP contracts.
async function runSetup(t, allocationDelay, firstFailure) {
  const email = "review-pending@u.nus.edu";
  let readyAt = Infinity;
  let signedOut = false;
  let walletReads = 0;
  let pendingWalletObserved = false;
  const server = http.createServer((req, res) => {
    const reply = (status, body) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(status === 204 ? undefined : JSON.stringify(body));
    };
    switch (`${req.method} ${req.url}`) {
      case "GET /api/v1/users/me": return reply(401, { error: { code: "invalid_session" } });
      case "POST /api/v1/auth/register": return reply(202, { registrationToken: "fixture-registration-context" });
      case "GET /api/v1/messages": return reply(200, { messages: [{ ID: "fixture-message", To: [{ Address: email }] }] });
      case "GET /api/v1/message/fixture-message": return reply(200, { Text: "Your verification code is: 12345678" });
      case "POST /api/v1/auth/verify-email":
        readyAt = allocationDelay ? Infinity : Date.now();
        return reply(200, { message: "Email verified. You can now log in." });
      case "POST /api/v1/auth/login":
        res.setHeader("Set-Cookie", "foc_session=fixture-session; Path=/; HttpOnly");
        return reply(200, { user: { id: "fixture-user" } });
      case "GET /api/v1/wallets/me":
        walletReads++;
        if (firstFailure && walletReads === 1) return reply(firstFailure.status, firstFailure.body);
        // Hold allocation until its first observation, so slow CI cannot turn
        // the pending condition into an accidental immediate-allocation control.
        if (allocationDelay && !pendingWalletObserved) {
          pendingWalletObserved = true;
          readyAt = Date.now() + allocationDelay;
        }
        return Date.now() < readyAt
          ? reply(404, { error: { code: "wallet_not_found" } })
          : reply(200, { userId: "fixture-user", available: 100, reserved: 0 });
      case "GET /api/v1/wallets/me/transactions": return reply(200, { transactions: [{ kind: "allocation" }] });
      case "GET /api/v1/suppliers": return signedOut
        ? reply(401, { error: { code: "invalid_session" } })
        : reply(200, { suppliers: [{ id: "fixture-supplier" }] });
      case "POST /api/v1/suppliers":
      case "PATCH /api/v1/suppliers/fixture-supplier":
      case "DELETE /api/v1/suppliers/fixture-supplier": return reply(403, { error: { code: "admin_required" } });
      case "POST /api/v1/auth/logout": signedOut = true; return reply(204);
      default: return reply(404, { error: { code: "not_found" } });
    }
  });
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  t.after(() => { server.closeAllConnections(); server.close(); });
  const base = `http://127.0.0.1:${server.address().port}`;
  const child = spawn(process.execPath, [new URL("./smoke.mjs", import.meta.url).pathname, "setup"], {
    env: { ...process.env, SMOKE_BASE_URL: base, SMOKE_SUPPLIER_URL: base, SMOKE_CREDIT_URL: base,
      SMOKE_ORIGIN: base, MAILPIT_URL: base, SMOKE_EMAIL: email, SMOKE_PASSWORD: "fixture-password-only" },
    stdio: ["ignore", "pipe", "pipe"],
    timeout: 10000,
  });
  let stderr = "";
  child.stdout.resume();
  child.stderr.on("data", chunk => { stderr += chunk; });
  const exitCode = await new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("exit", (code, signal) => signal ? reject(new Error(`smoke exited on ${signal}`)) : resolve(code));
  });
  if (allocationDelay) await new Promise(resolve => setTimeout(resolve, allocationDelay));
  const recovered = await fetch(base + "/api/v1/wallets/me");
  assert.equal(recovered.status, 200, "control: the temporary pending wallet becomes ready");
  return { exitCode, walletReads: walletReads - 1, stderr };
}

test("review08 immediate allocation control passes the existing smoke setup", async t => {
  assert.equal((await runSetup(t, 0)).exitCode, 0);
});
test("review08 smoke setup tolerates a briefly pending wallet", async t => {
  const result = await runSetup(t, 300);
  t.diagnostic(`wallet reads before exit=${result.walletReads}; allocation recovered to HTTP 200`);
  assert.equal(result.exitCode, 0,
    "existing smoke exits on the first 404 although verification succeeded and allocation recovers after 300ms\n" + result.stderr);
});

for (const failure of [
 { status:401, body:{error:{code:"invalid_session"}} },
 { status:503, body:{error:{code:"auth_unavailable"}} },
 { status:404, body:{error:{code:"wrong_route"}} },
 { status:200, body:{userId:"fixture-user",available:99,reserved:0} },
]) test(`smoke rejects unrelated wallet failure ${failure.status}/${failure.body.error?.code || 'wrong balance'}`, async t => {
 const result=await runSetup(t,0,failure);assert.equal(result.exitCode,1);assert.equal(result.walletReads,1,"unexpected failures must not be retried");
});
