import { test, expect } from "@playwright/test";
import net from "node:net";

// Service workers and PushManager need a secure context, and the fixture is
// plain HTTP on a Compose hostname. localhost is always secure, so this file
// reaches the fixture through a TCP forward on localhost:8085, the origin the
// fixture already maps to its own.
const fixture = new URL(process.env.ORDER_UI_URL || "http://order-ui-fixture:8080");
let forward;
const sockets = new Set();
test.beforeAll(async () => {
  forward = net.createServer((client) => {
    const upstream = net.connect(Number(fixture.port || 80), fixture.hostname);
    for (const socket of [client, upstream]) {
      sockets.add(socket);
      socket.on("close", () => sockets.delete(socket));
    }
    client.pipe(upstream).pipe(client);
    client.on("error", () => upstream.destroy());
    upstream.on("error", () => client.destroy());
  });
  await new Promise((resolve) => forward.listen(8085, resolve));
});
test.afterAll(async () => {
  for (const socket of sockets) socket.destroy(); // browser keep-alive connections
  await new Promise((resolve) => forward.close(resolve));
});
test.use({ baseURL: "http://localhost:8085" });

// Headless Chromium has no push service, so only PushManager is stubbed. The
// stub checks the subscribe options and returns real P-256 browser keys, which
// Order Service validates exactly as it would a Chrome subscription.
async function stubPush(page) {
  await page.addInitScript(() => {
    // Headless Chromium reports notifications as denied and shows no prompt;
    // model a browser whose user allows the permission request.
    let permission = "default";
    Object.defineProperty(Notification, "permission", { configurable: true, get: () => permission });
    Notification.requestPermission = async () => (permission = "granted");
    const KEY = "ui-test-push-subscription";
    const b64 = (bytes) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    const wrap = (json) => ({
      endpoint: json.endpoint,
      toJSON: () => json,
      unsubscribe: async () => {
        localStorage.removeItem(KEY);
        return true;
      },
    });
    PushManager.prototype.subscribe = async function (options) {
      if (!options?.userVisibleOnly || !(options.applicationServerKey instanceof Uint8Array) || options.applicationServerKey.length !== 65)
        throw new DOMException("Invalid subscribe options", "NotAllowedError");
      const pair = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"]);
      const raw = new Uint8Array(await crypto.subtle.exportKey("raw", pair.publicKey));
      const json = {
        endpoint: `https://fcm.googleapis.com/fcm/send/ui-test-${crypto.randomUUID()}`,
        expirationTime: null,
        keys: { p256dh: b64(raw), auth: b64(crypto.getRandomValues(new Uint8Array(16))) },
      };
      localStorage.setItem(KEY, JSON.stringify(json));
      return wrap(json);
    };
    PushManager.prototype.getSubscription = async function () {
      const raw = localStorage.getItem(KEY);
      return raw ? wrap(JSON.parse(raw)) : null;
    };
  });
}

async function start(page, actor = "courier") {
  await page.request.get(`/__test/session?actor=${actor}`);
  await page.goto("/orders");
  await expect(page.locator("#session-notice")).toContainText("signed in");
}

async function settings(page) {
  const response = await page.request.get("/api/v1/orders/alerts");
  expect(response.status()).toBe(200);
  return response.json();
}

test.beforeEach(async ({ page }) => {
  await page.request.post("/__test/reset");
});

test("a courier turns nearby alerts on, changes them and turns them off on this device", async ({ page }) => {
  await stubPush(page);
  await start(page);
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  const panel = page.locator("#alerts-panel");
  await expect(panel).toBeVisible();
  await expect(page.locator("#alerts-state")).toContainText("Off. Get a notification");
  await expect(page.locator("#alerts-enable")).toBeDisabled();

  await page.locator("#alerts-location").selectOption("central-library");
  await page.locator("#alerts-radius").selectOption("500");
  await page.locator("#alerts-enable").click();
  await expect(page.locator("#alerts-state")).toHaveText("On for this device · within 500 m of Central Library");
  await expect(page.locator("#alerts-message")).toBeHidden();
  expect(await settings(page)).toMatchObject({
    pushAvailable: true,
    devices: 1,
    preference: { enabled: true, locationId: "central-library", radiusMeters: 500 },
  });
  const worker = await page.evaluate(async () => (await navigator.serviceWorker.getRegistration("/orders/"))?.active?.scriptURL);
  expect(worker).toMatch(/\/orders\/sw\.js$/);

  // The account preference and this browser's subscription survive a reload.
  await page.reload();
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator("#alerts-state")).toHaveText("On for this device · within 500 m of Central Library");
  await expect(page.locator("#alerts-location")).toHaveValue("central-library");

  await page.locator("#alerts-radius").selectOption("1000");
  await page.locator("#alerts-save").click();
  await expect(page.locator("#alerts-state")).toHaveText("On for this device · within 1 km of Central Library");
  expect((await settings(page)).preference.radiusMeters).toBe(1000);

  await page.locator("#alerts-disable").click();
  await expect(page.locator("#alerts-state")).toContainText("Off. Get a notification");
  expect(await settings(page)).toMatchObject({ devices: 0, preference: { enabled: false } });
  await expect(page.locator("#alerts-enable")).toBeVisible();
});

test("blocked notifications explain how to recover and keep alerts off", async ({ page }) => {
  await stubPush(page);
  await page.addInitScript(() => Object.defineProperty(Notification, "permission", { get: () => "denied" }));
  await start(page);
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator("#alerts-state")).toContainText("Notifications are blocked for this site");
  await expect(page.locator("#alerts-enable")).toBeDisabled();
  await expect(page.locator("#alerts-location")).toBeDisabled();
});

test("a server without push says alerts are unavailable", async ({ page }) => {
  await stubPush(page);
  await page.route("**/api/v1/orders/alerts", (route) =>
    route.fulfill({ json: { pushAvailable: false, vapidPublicKey: null, preference: null, devices: 0 } }),
  );
  await start(page);
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator("#alerts-state")).toContainText("not available on this server");
  await expect(page.locator("#alerts-enable")).toBeDisabled();
});

test("another device's alerts are reported, and other views hide the panel", async ({ page }) => {
  await stubPush(page);
  await page.route("**/api/v1/orders/alerts", (route) =>
    route.fulfill({
      json: { pushAvailable: true, vapidPublicKey: "B".padEnd(87, "A"), preference: { enabled: true, locationId: "com2", radiusMeters: 200 }, devices: 1 },
    }),
  );
  await start(page);
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator("#alerts-state")).toContainText("On for another of your devices");
  await expect(page.locator("#alerts-location")).toHaveValue("com2");
  await expect(page.locator("#alerts-radius")).toHaveValue("200");
  await page.getByRole("button", { name: "My deliveries", exact: true }).click();
  await expect(page.locator("#alerts-panel")).toBeHidden();
});

test("a notification link opens Help a friend with that errand selected", async ({ page }) => {
  await start(page, "requester");
  const result = await page.request.post("/api/v1/orders", {
    headers: { Origin: new URL(page.url()).origin, "Idempotency-Key": "alert-link" },
    data: {
      pickup: { type: "supplier", id: "22222222-2222-4222-8222-222222222222" },
      delivery: { type: "location", id: "utown" },
      items: [{ name: "Linked errand", quantity: 1 }],
      offeredCredits: 10,
      expiresAt: new Date(Date.now() + 3600000).toISOString(),
    },
  });
  expect(result.status()).toBe(201);
  const order = (await result.json()).order;
  await page.request.get("/__test/session?actor=courier");
  await page.goto(`/orders?view=open&order=${encodeURIComponent(order.id)}`);
  await expect(page.getByRole("button", { name: "Help a friend", exact: true })).toHaveAttribute("aria-current", "page");
  await expect(page.locator("#order-detail")).toContainText("Linked errand");
  expect(new URL(page.url()).search).toBe("");
});
