import assert from "node:assert/strict";

const base = process.env.SMOKE_BASE_URL;
const origin = process.env.SMOKE_ORIGIN;
const email = process.env.SMOKE_EMAIL;
const password = process.env.SMOKE_PASSWORD;
const mailpit = process.env.MAILPIT_URL;
assert(base && origin && email && password && mailpit, "Smoke configuration missing");
const pause = (ms) => new Promise(resolve => setTimeout(resolve, ms));

async function call(path, method = "GET", data, cookie, expected = 200) {
  const headers = { Origin: origin };
  if (data !== undefined) headers["Content-Type"] = "application/json";
  if (cookie) headers.Cookie = cookie;
  const response = await fetch(base + path, {
    method, headers, body: data === undefined ? undefined : JSON.stringify(data),
    signal: AbortSignal.timeout(15000),
  });
  assert.equal(response.status, expected, `${method} ${path}: unexpected HTTP status`);
  return response;
}

async function login() {
  const response = await call("/api/v1/auth/login", "POST", { email, password });
  const cookie = response.headers.getSetCookie()[0]?.split(";")[0];
  assert(cookie?.startsWith("foc_session="), "Expected local session cookie");
  return cookie;
}

if (process.argv[2] === "setup") {
  let ready = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      const response = await fetch(base + "/api/v1/users/me", { signal: AbortSignal.timeout(2000) });
      if (response.status === 401) { ready = true; break; }
    } catch { /* Containers may still be starting. */ }
    await pause(1000);
  }
  assert(ready, "Gateway/User Service did not start");
  const registration = await (await call("/api/v1/auth/register", "POST", {
    email, password, displayName: email.split("@")[0],
  }, undefined, 202)).json();
  let code;
  for (let attempt = 0; attempt < 30; attempt++) {
    const inbox = await (await fetch(mailpit + "/api/v1/messages", { signal: AbortSignal.timeout(3000) })).json();
    const message = inbox.messages?.find(message => message.To?.some(to => to.Address === email));
    if (message) {
      const detail = await (await fetch(mailpit + "/api/v1/message/" + message.ID)).json();
      code = detail.Text?.match(/verification code is: (\d{8})/)?.[1];
      if (code) break;
    }
    await pause(500);
  }
  assert(code, "Verification email not found in local Mailpit");
  await call("/api/v1/auth/verify-email", "POST", { email, code, registrationToken: registration.registrationToken });
  const cookie = await login();
  const suppliers = await (await call("/api/v1/suppliers", "GET", undefined, cookie)).json();
  assert(Array.isArray(suppliers.suppliers), "Expected supplier list");
  await call("/api/v1/suppliers", "POST", { name: "Denied", category: "food", locationId: "com2" }, cookie, 403);
  await call("/api/v1/auth/logout", "POST", undefined, cookie, 204);
  await call("/api/v1/suppliers", "GET", undefined, cookie, 401);
  console.log("Verified registration, browsing, denied admin write and logout revocation.");
} else if (process.argv[2] === "check") {
  const cookie = await login();
  const created = await (await call("/api/v1/suppliers", "POST", {
    name: email.split("@")[0], category: "food", locationId: "com2", description: "Local integration check",
  }, cookie, 201)).json();
  await call("/api/v1/suppliers/" + created.supplier.id, "GET", undefined, cookie);
  await call("/api/v1/suppliers/" + created.supplier.id, "PATCH", { active: false }, cookie);
  await call("/api/v1/suppliers/" + created.supplier.id, "GET", undefined, cookie, 404);
  await call("/internal/v1/sessions/validate", "POST", { sessionToken: "invalid" }, undefined, 404);
  const noOrigin = await fetch(base + "/api/v1/suppliers", { method: "POST", headers: { Cookie: cookie, "Content-Type": "application/json" }, body: "{}" });
  assert.equal(noOrigin.status, 403, "Missing Origin must be rejected");
  const frontend = await fetch(base + "/");
  assert.equal(frontend.status, 200);
  assert((await frontend.text()).includes("Friend on Campus"), "Frontend missing");
  await call("/api/v1/auth/logout", "POST", undefined, cookie, 204);
  await call("/api/v1/suppliers", "GET", undefined, cookie, 401);
  console.log("Verified admin create/deactivate, private route isolation, CSRF and shared frontend routing.");
} else {
  throw new Error("Use setup or check");
}
