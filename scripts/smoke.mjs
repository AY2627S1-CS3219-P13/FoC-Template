import assert from "node:assert/strict";

const base = process.env.SMOKE_BASE_URL;
const origin = process.env.SMOKE_ORIGIN;
const email = process.env.SMOKE_EMAIL;
const password = process.env.SMOKE_PASSWORD;
const mailpit = process.env.MAILPIT_URL;
const supplierBase = process.env.SMOKE_SUPPLIER_URL || base;
assert(base && origin && email && password && mailpit, "Smoke configuration missing");
const pause = (ms) => new Promise(resolve => setTimeout(resolve, ms));

async function call(path, method = "GET", data, cookie, expected = 200) {
  const headers = { Origin: origin };
  if (data !== undefined) headers["Content-Type"] = "application/json";
  if (cookie) headers.Cookie = cookie;
  const service = path.startsWith("/api/v1/suppliers") || path.startsWith("/api/v1/locations") ? supplierBase : base;
  const response = await fetch(service + path, {
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
  assert(suppliers.suppliers.length > 0, "Expected seeded suppliers");
  const knownId = suppliers.suppliers[0].id;
  await call("/api/v1/suppliers/" + knownId, "PATCH", { active: false }, cookie, 403);
  await call("/api/v1/suppliers/" + knownId, "DELETE", undefined, cookie, 403);
  await call("/api/v1/auth/logout", "POST", undefined, cookie, 204);
  await call("/api/v1/suppliers", "GET", undefined, cookie, 401);
  console.log("Verified registration, browsing, denied admin write and logout revocation.");
} else if (process.argv[2] === "check") {
  const cookie = await login();
  const created = await (await call("/api/v1/suppliers", "POST", {
    name: email.split("@")[0], category: "food", locationId: "com2", description: "Local integration check",
  }, cookie, 201)).json();
  await call("/api/v1/suppliers/" + created.supplier.id, "GET", undefined, cookie);
  const path = "/api/v1/suppliers/" + created.supplier.id;
  const editedName = email.split("@")[0] + " edited";
  const edited = await (await call(path, "PATCH", { name: editedName, category: "services", locationId: "com3", openingHours: "Mon-Fri 09:00-17:00" }, cookie)).json();
  assert.equal(edited.supplier.openingHours, "Mon-Fri 09:00-17:00");
  assert.equal(edited.supplier.createdAt, created.supplier.createdAt);
  await call("/api/v1/suppliers", "POST", { name: editedName.toUpperCase(), category: "services", locationId: "com3" }, cookie, 409);
  const query = "/api/v1/suppliers?q=" + encodeURIComponent(editedName) + "&category=services&locationId=com3&sort=createdAt&direction=desc&pageSize=1";
  const matches = await (await call(query, "GET", undefined, cookie)).json();
  assert.equal(matches.total, 1); assert.equal(matches.totalPages, 1); assert.equal(matches.suppliers[0].id, created.supplier.id);
  await call("/api/v1/suppliers/" + created.supplier.id, "PATCH", { active: false }, cookie);
  const inactive = await (await call("/api/v1/suppliers/" + created.supplier.id, "GET", undefined, cookie)).json();
  assert.equal(inactive.supplier.active, false);
  assert.equal((await (await call(query, "GET", undefined, cookie)).json()).total, 0);
  assert.equal((await (await call(query + "&status=inactive", "GET", undefined, cookie)).json()).total, 1);
  await call(path, "PATCH", { active: true }, cookie);
  await call(path, "DELETE", undefined, cookie, 204);
  await call(path, "GET", undefined, cookie, 404);
  await call(path, "PATCH", { active: true }, cookie, 404);
  await call("/internal/v1/sessions/validate", "POST", { sessionToken: "invalid" }, undefined, 404);
  const noOrigin = await fetch(supplierBase + "/api/v1/suppliers", { method: "POST", headers: { Cookie: cookie, "Content-Type": "application/json" }, body: "{}" });
  assert.equal(noOrigin.status, 403, "Missing Origin must be rejected");
  assert.equal((await fetch(supplierBase + path, { method: "DELETE", headers: { Cookie: cookie } })).status, 403);
  if (process.env.SMOKE_API_ONLY !== "1") {
    const frontend = await fetch(base + "/");
    assert.equal(frontend.status, 200);
    assert((await frontend.text()).includes("Friend on Campus"), "Frontend missing");
  }
  await call("/api/v1/auth/logout", "POST", undefined, cookie, 204);
  await call("/api/v1/suppliers", "GET", undefined, cookie, 401);
  console.log("Verified persisted CRUD, duplicate rejection, combined queries, inactive reads, deletion, private route isolation and CSRF.");
} else {
  throw new Error("Use setup or check");
}
