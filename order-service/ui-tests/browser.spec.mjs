import { test, expect } from "@playwright/test";

const supplier = "supplier:22222222-2222-4222-8222-222222222222";
const expiry = () =>
  new Date(Date.now() + 9 * 3600000).toISOString().slice(0, 16);
async function start(page, actor = "requester") {
  await page.request.get(`/__test/session?actor=${actor}`);
  await page.goto("/orders");
  await expect(page.locator("#session-notice")).toContainText("signed in");
  await expect(page.locator("#pickup")).toBeEnabled();
}
async function item(page, name = "Milk Tea", quantity = "2") {
  await page.locator("#item-name").fill(name);
  await page.locator("#item-quantity").fill(quantity);
  await page.locator("#item-description").fill("Less sugar");
  await page.getByRole("button", { name: "+ Add item", exact: true }).click();
}
async function fill(page, name = "Milk Tea") {
  await page.locator("#pickup").selectOption(supplier);
  await page.locator("#delivery").selectOption("location:utown");
  await item(page, name);
  await expect(page.locator("#items .item-row")).toHaveCount(1);
  await page.locator("#offer").fill("10");
  await page.locator("#expiry").fill(expiry());
}
async function seed(page, key) {
  const result = await page.request.post("/api/v1/orders", {
    headers: { Origin: new URL(page.url()).origin, "Idempotency-Key": key },
    data: {
      pickup: { type: "supplier", id: supplier.slice(9) },
      delivery: { type: "location", id: "utown" },
      items: [{ name: key, quantity: 1 }],
      offeredCredits: 10,
      expiresAt: new Date(Date.now() + 3600000).toISOString(),
    },
  });
  expect(result.status()).toBe(201);
  return (await result.json()).order;
}
test.beforeEach(async ({ page }) => {
  await page.request.post("/__test/reset");
});

test("account header, request filters and view changes", async ({ page }) => {
  await page.route("**/api/v1/users/me", async (route) => {
    const session = await page.request.get("/api/v1/orders/session");
    await route.fulfill({ json: { user: { id: (await session.json()).userId, displayName: "Taia" } } });
  });
  await page.route("**/api/v1/wallets/me", async (route) => {
    const session = await page.request.get("/api/v1/orders/session");
    await route.fulfill({ json: { userId: (await session.json()).userId, available: "9007199254740993" } });
  });
  await start(page);
  await expect(page.locator("#account-name")).toHaveText("Taia · User");
  await expect(page.locator("#account-balance")).toHaveText("9007199254740993 credits available");
  await expect(page.getByRole("link", { name: "Back to catalogue" })).toHaveAttribute("href", "/");
  await seed(page, "current-request");
  const past = await seed(page, "past-request");
  await page.request.post(`/api/v1/orders/${past.id}/actions`, {
    headers: { Origin: new URL(page.url()).origin }, data: { action: "cancel" },
  });
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await expect(page.locator(".order-card")).toHaveCount(2);
  const openColour = await page.locator(".status-open").last().evaluate((el) => getComputedStyle(el).backgroundColor);
  const cancelledColour = await page.locator(".status-cancelled").evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(openColour).not.toBe(cancelledColour);
  await page.locator("#request-filter").selectOption("past");
  await expect(page.locator(".order-card .pill")).toHaveText("cancelled");
  await page.locator("#request-filter").selectOption("current");
  await expect(page.locator(".order-card .pill")).toHaveText("open");
  await page.locator(".order-card").click();
  await expect(page.locator("#order-detail")).toContainText("current-request");
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator("#order-detail")).toContainText("Select an errand");
  await expect(page.locator("#order-detail")).not.toContainText("current-request");
});

test("admin courier capacity survives reload and releases on delivery", async ({ page }) => {
  await start(page);
  await seed(page, "first-delivery");
  await seed(page, "second-delivery");
  await start(page, "admin");
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await page.locator(".order-card").first().click();
  await page.getByRole("button", { name: "Accept errand", exact: true }).click();
  await expect(page.locator("#order-detail .pill")).toHaveText("accepted");
  await page.reload();
  await expect(page.locator("#session-notice")).toContainText("signed in");
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await page.locator(".order-card").click();
  await expect(page.getByRole("button", { name: "Accept errand", exact: true })).toBeDisabled();
  await expect(page.locator("#order-detail")).toContainText("You have an ongoing order.");
  await page.getByRole("button", { name: "My deliveries", exact: true }).click();
  await page.locator(".order-card").click();
  await page.getByRole("button", { name: "Mark picked up", exact: true }).click();
  await page.getByRole("button", { name: "Mark delivered", exact: true }).click();
  await expect(page.locator("#order-detail .pill")).toHaveText("delivered");
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await page.locator(".order-card").click();
  await expect(page.getByRole("button", { name: "Accept errand", exact: true })).toBeEnabled();
});

test("updates arrive within five seconds despite a stalled session check", async ({ page }) => {
  await start(page);
  const order = await seed(page, "bounded-refresh");
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await expect(page.locator(".order-card .pill")).toHaveText("open");
  // Session UI refresh is independent of authentication on every list request.
  let sessionStarted;
  const stalled = new Promise((resolve) => { sessionStarted = resolve; });
  await page.route("**/api/v1/orders/session", async (route) => {
    sessionStarted();
    await new Promise((resolve) => setTimeout(resolve, 6000));
    await route.abort().catch(() => {});
  });
  await stalled;
  await page.route("**/api/v1/orders?*", async (route) => {
    const response = await route.fetch();
    await new Promise((resolve) => setTimeout(resolve, 1200));
    await route.fulfill({ response });
  });
  const started = Date.now();
  const cancelled = await page.request.post(`/api/v1/orders/${order.id}/actions`, {
    headers: { Origin: new URL(page.url()).origin }, data: { action: "cancel" },
  });
  expect(cancelled.status()).toBe(200);
  await expect(page.locator(".order-card .pill")).toHaveText("cancelled", { timeout: 4900 });
  expect(Date.now() - started).toBeLessThan(5000);
});

test("late snapshots stay stale and polling recovers", async ({ page }) => {
  await start(page);
  await seed(page, "late-snapshot");
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await expect(page.locator(".order-card")).toHaveCount(1);
  await page.locator(".order-card").click();
  await expect(page.getByRole("button", { name: "Cancel errand", exact: true })).toBeEnabled();
  let snapshotStarted;
  const delayed = new Promise((resolve) => { snapshotStarted = resolve; });
  await page.route("**/api/v1/orders?*", async (route) => {
    snapshotStarted();
    const response = await route.fetch();
    await new Promise((resolve) => setTimeout(resolve, 4000));
    await route.fulfill({ response }).catch(() => {});
  });
  await delayed;
  await expect(page.locator("#sync-notice")).toContainText("List is not updated", { timeout: 5000 });
  await expect(page.getByRole("button", { name: "Cancel errand", exact: true })).toBeDisabled();
  await page.waitForTimeout(4200);
  await expect(page.locator("#sync-notice")).toContainText("List is not updated");
  await page.unroute("**/api/v1/orders?*");
  await expect(page.getByRole("button", { name: "Cancel errand", exact: true })).toBeEnabled({ timeout: 5000 });
});

test("a rejected event connection retries after fresh session validation", async ({ page }) => {
  let connections = 0;
  await page.route("**/api/v1/orders/stream", async (route) => {
    connections++;
    if (connections === 1) {
      await route.fulfill({ status: 400, contentType: "application/json", body: '{"error":{"code":"invalid_event_cursor","message":"Reconnect"}}' });
    } else {
      await route.continue();
    }
  });
  const reconnected = page.waitForResponse((r) => r.url().endsWith("/api/v1/orders/stream") && r.status() === 200);
  await start(page, "courier");
  await reconnected;
  expect(connections).toBeGreaterThanOrEqual(2);
});

test("creation events notify other users and refresh open lists without polling", async ({ page, browser, baseURL }) => {
  // Prove the new event path: disable the existing two-second snapshot timer.
  await page.addInitScript(() => {
    const interval = window.setInterval.bind(window);
    window.setInterval = (fn, delay, ...args) => delay === 2000 ? 0 : interval(fn, delay, ...args);
  });
  const connected = page.waitForResponse((r) => r.url().endsWith("/api/v1/orders/stream"));
  await start(page, "courier");
  await connected;
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator("#orders")).toContainText("No errands here yet");
  const requester = await browser.newContext({ baseURL });
  try {
    const other = await requester.newPage();
    await start(other);
    await seed(other, "published-to-other-users");
    await expect(page.locator("#event-notice")).toContainText("A new errand is available");
    await expect(page.locator(".order-card")).toHaveCount(1);
    await expect(other.locator("#event-notice")).toBeHidden();
    // Reload/reconnect synchronizes a snapshot even with the polling timer off.
    await page.reload();
    await expect(page.locator("#session-notice")).toContainText("signed in");
    await page.getByRole("button", { name: "Help a friend", exact: true }).click();
    await expect(page.locator(".order-card")).toHaveCount(1);
  } finally {
    await requester.close();
  }
});

test("editor, duplicate decisions, safe text and responsive layout", async ({
  page,
}) => {
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await start(page);
  await fill(page, "Milk Tea");
  await item(page, " MILK\tTEA ");
  await expect(page.locator("#duplicate")).toBeVisible();
  await page
    .getByRole("button", { name: "Do not proceed", exact: true })
    .click();
  await expect(page.locator("#items .item-row")).toHaveCount(1);
  await expect(page.locator("#item-name")).toHaveValue(" MILK\tTEA ");
  await page.locator("#add-item").click();
  await page.getByRole("button", { name: "Proceed", exact: true }).click();
  await expect(page.locator("#items .item-row")).toHaveCount(2);
  await item(page, "<img src=x onerror=alert(1)>", "9007199254740993");
  await expect(page.locator("#items")).toContainText("9007199254740993");
  await expect(page.locator("#items img")).toHaveCount(0);
  await page.screenshot({ path: "/tmp/order-ui-results/desktop.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({ path: "/tmp/order-ui-results/mobile.png", fullPage: true });
  await page.locator("#submit").click();
  await expect(page.locator("#form-message")).toContainText("Errand confirmed");
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await expect(page.locator(".order-card")).toHaveCount(1);
  await page.locator(".order-card").click();
  await expect(page.locator("#order-detail")).toContainText("Level 1");
  await expect(page.locator("#order-detail")).toContainText("9007199254740993");
  expect(errors).toEqual([]);
});

test("slow creation allows navigation and prevents double submission", async ({
  page,
}) => {
  await start(page);
  await fill(page);
  let release;
  const gate = new Promise((resolve) => (release = resolve));
  let calls = 0;
  await page.route("**/api/v1/orders", async (route) => {
    if (route.request().method() === "POST") {
      calls++;
      await gate;
    }
    await route.continue();
  });
  await page.locator("#submit").click();
  await expect(page.locator("#submit")).toBeDisabled();
  await expect(page.locator("#form-message")).not.toContainText(
    "Errand confirmed",
  );
  await page
    .getByRole("button", { name: "Help a friend", exact: true })
    .click();
  await expect(page.locator("#list-view")).toBeVisible();
  await page.getByRole("button", { name: "New errand", exact: true }).click();
  await expect(page.locator("#submit")).toHaveText("Confirming…");
  release();
  await expect(page.locator("#form-message")).toContainText("Errand confirmed");
  expect(calls).toBe(1);
});

test("lost saved response survives reload and retries the same identity", async ({
  page,
}) => {
  await start(page);
  await fill(page);
  let first = true;
  const keys = [];
  await page.route("**/api/v1/orders", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    keys.push(route.request().headers()["idempotency-key"]);
    if (first) {
      first = false;
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      await route.abort("failed");
    } else await route.continue();
  });
  await page.locator("#submit").click();
  await expect(page.locator("#form-message")).toContainText("unresolved");
  await page.reload();
  await expect(page.locator("#submit")).toHaveText("Retry same attempt");
  await expect(page.locator("#item-name")).toBeDisabled();
  await page.locator("#submit").click();
  await expect(page.locator("#form-message")).toContainText("Errand confirmed");
  expect(keys).toHaveLength(2);
  expect(keys[0]).toBe(keys[1]);
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await expect(page.locator(".order-card")).toHaveCount(1);
});

test("dependency rejection retains all input without success", async ({
  page,
}) => {
  await start(page);
  await fill(page);
  await page.route("**/api/v1/orders", async (route) => {
    if (route.request().method() === "POST")
      await route.fulfill({
        status: 409,
        json: {
          error: {
            code: "insufficient_credits",
            message: "You do not have enough available credits.",
          },
        },
      });
    else await route.continue();
  });
  await page.locator("#submit").click();
  await expect(page.locator("#form-message")).toContainText(
    "enough available credits",
  );
  await expect(page.locator("#pickup")).toHaveValue(supplier);
  await expect(page.locator("#offer")).toHaveValue("10");
  await expect(page.locator("#items .item-row")).toHaveCount(1);
  await expect(page.locator("#form-message")).not.toContainText(
    "Errand confirmed",
  );
  await expect(page.locator("#submit")).toBeEnabled();
});

test("courier pending state, delivered capacity and old-action replay", async ({
  page,
}) => {
  await start(page);
  const first = await seed(page, "first-job");
  await seed(page, "second-job");
  await start(page, "courier");
  await page
    .getByRole("button", { name: "Help a friend", exact: true })
    .click();
  await expect(page.locator(".order-card")).toHaveCount(2);
  await page.locator(".order-card").first().click();
  await page
    .getByRole("button", { name: "Accept errand", exact: true })
    .click();
  await expect(page.locator("#order-detail .pill")).toHaveText("accepted");
  await page
    .getByRole("button", { name: "Mark picked up", exact: true })
    .click();
  await expect(page.locator("#order-detail .pill")).toHaveText("picked up");
  let release;
  const gate = new Promise((resolve) => (release = resolve));
  await page.route(`**/api/v1/orders/${first.id}/actions`, async (route) => {
    await gate;
    await route.continue();
  });
  await page
    .getByRole("button", { name: "Mark delivered", exact: true })
    .click();
  await expect(page.locator("#order-detail")).toContainText("Updating…");
  await expect(page.locator("#order-detail .pill")).toHaveText("picked up");
  await page.getByRole("button", { name: "New errand", exact: true }).click();
  await expect(page.locator("#create-view")).toBeVisible();
  release();
  await page
    .getByRole("button", { name: "My deliveries", exact: true })
    .click();
  await expect(page.locator("#order-detail")).toContainText("Select an errand");
  await expect(page.locator(".order-card")).toHaveCount(1);
  await page.locator(".order-card").click();
  await expect(page.locator("#order-detail .pill")).toHaveText("delivered");
  await page
    .getByRole("button", { name: "Help a friend", exact: true })
    .click();
  await expect(page.locator(".order-card")).toHaveCount(1);
  await page.locator(".order-card").click();
  await page
    .getByRole("button", { name: "Accept errand", exact: true })
    .click();
  await expect(page.locator("#order-detail .pill")).toHaveText("accepted");
  const replay = await page.request.post(`/api/v1/orders/${first.id}/actions`, {
    headers: { Origin: new URL(page.url()).origin },
    data: { action: "deliver" },
  });
  expect(replay.status()).toBe(200);
  expect((await replay.json()).order.status).toBe("delivered");
});

test("drafts are isolated across accounts", async ({ page }) => {
  await start(page);
  await fill(page, "Private draft");
  await start(page, "courier");
  await expect(page.locator("#items .item-row")).toHaveCount(0);
  await start(page, "requester");
  await expect(page.locator("#items")).toContainText("Private draft");
});

test("lost delivery response keeps a retryable action across reload", async ({ page }) => {
  await start(page);
  const order = await seed(page, "lost-delivery");
  await start(page, "courier");
  const path = `/api/v1/orders/${order.id}/actions`;
  for (const action of ["accept", "pick_up"]) {
    const result = await page.request.post(path, {
      headers: { Origin: new URL(page.url()).origin }, data: { action },
    });
    expect(result.status()).toBe(200);
  }
  await page.getByRole("button", { name: "My deliveries", exact: true }).click();
  await page.locator(".order-card").click();
  let first = true;
  await page.route(`**${path}`, async route => {
    if (first) {
      first = false;
      const result = await route.fetch();
      expect(result.status()).toBe(200);
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Mark delivered", exact: true }).click();
  await expect(page.locator("#order-detail")).toContainText("unresolved");
  await page.reload();
  await expect(page.locator("#session-notice")).toContainText("signed in");
  await page.getByRole("button", { name: "My deliveries", exact: true }).click();
  await page.locator(".order-card").click();
  await page.getByRole("button", { name: "Retry same action", exact: true }).click();
  await expect(page.locator("#order-detail .pill")).toHaveText("delivered");
  await expect(page.locator("#order-detail")).not.toContainText("unresolved");
});

test("open snapshot has no pagination, removes assignments live and reconciles reconnect", async ({ page }) => {
  await start(page);
  for (let i = 0; i < 22; i++) await seed(page, `open-${i}`);
  await start(page, "courier");
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await expect(page.locator(".order-card")).toHaveCount(22);
  await expect(page.locator("#pagination")).toBeHidden();
  await page.locator("#open-sort").selectOption("credits");
  await page.locator(".order-card").first().click();
  await expect(page.locator("#order-detail")).toContainText("Campus:");
  await expect(page.locator("#order-detail")).toContainText("Supplier:");
  await page.getByRole("button", { name: "Accept errand", exact: true }).click();
  await expect(page.locator(".order-card")).toHaveCount(21);
  // A working detail refresh cannot clear a failed list's warning.
  await page.route("**/api/v1/orders?scope=**", route => route.abort("failed"));
  await expect(page.locator("#sync-notice")).toContainText("List is not updated", { timeout: 5000 });
  await page.waitForTimeout(2300);
  await expect(page.locator("#sync-notice")).toContainText("List is not updated");
  await page.unroute("**/api/v1/orders?scope=**");
  await expect(page.locator("#sync-notice")).toBeHidden({ timeout: 5000 });
  await expect(page.locator(".order-card")).toHaveCount(21);
});

test("requester cancellation and confirmation reach terminal states", async ({ page }) => {
  await start(page);
  await seed(page, "cancel-job");
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await page.locator(".order-card").click();
  await page.getByRole("button", { name: "Cancel errand", exact: true }).click();
  await expect(page.locator("#order-detail .pill")).toHaveText("cancelled");
  const order = await seed(page, "confirm-job");
  await start(page, "courier");
  for (const action of ["accept", "pick_up", "deliver"]) {
    const result = await page.request.post(`/api/v1/orders/${order.id}/actions`, {
      headers: { Origin: new URL(page.url()).origin }, data: { action },
    });
    expect(result.status()).toBe(200);
  }
  await start(page);
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await page.locator(".order-card").first().click();
  await page.getByRole("button", { name: "Confirm delivery", exact: true }).click();
  await expect(page.locator("#order-detail")).toContainText("Confirmation is pending");
  await expect(page.locator("#order-detail .pill")).toHaveText("completed", { timeout: 7000 });
});

test("dispute is visible to admins and both resolution controls are available", async ({ page }) => {
  await start(page);
  const order = await seed(page, "disputed-job");
  await start(page, "courier");
  for (const action of ["accept", "pick_up", "deliver"]) {
    expect((await page.request.post(`/api/v1/orders/${order.id}/actions`, {
      headers: { Origin: new URL(page.url()).origin }, data: { action },
    })).status()).toBe(200);
  }
  await start(page);
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await page.locator(".order-card").click();
  await page.getByRole("button", { name: "Raise dispute", exact: true }).click();
  await expect(page.locator("#order-detail .pill")).toHaveText("dispute");
  await expect(page.getByRole("button", { name: "Confirm delivery", exact: true })).toHaveCount(0);
  await start(page, "admin");
  await page.getByRole("button", { name: "Disputes", exact: true }).click();
  await page.locator(".order-card").click();
  await expect(page.getByRole("button", { name: "Resolve as completed", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Resolve as not completed", exact: true }).click();
  await expect(page.locator("#order-detail .pill")).toHaveText("not completed");
});

test("lifecycle invalidations coalesce into authoritative refreshes and retain version fences", async ({ page }) => {
  await page.addInitScript(() => {
    const interval = window.setInterval;
    // Isolate event-driven refresh from the independent two-second poll.
    window.setInterval = (fn, ms, ...args) => ms === 2000 ? 0 : interval(fn, ms, ...args);
    window.EventSource = class extends EventTarget {
      static CLOSED = 2;
      readyState = 1;
      constructor() { super(); window.__workflowEvents = this; }
      close() { this.readyState = 2; }
    };
  });
  await start(page);
  const order = await seed(page, "workflow-browser");
  await page.getByRole("button", { name: "My requests", exact: true }).click();
  await expect(page.locator(".order-card .pill")).toHaveText("open");
  await page.locator(".order-card").click();
  let snapshots = 0;
  await page.route("**/api/v1/orders?**", async route => { snapshots++; await route.continue(); });
  const result = await page.request.post(`/api/v1/orders/${order.id}/actions`, {
    headers: { Origin: new URL(page.url()).origin }, data: { action: "cancel" },
  });
  expect(result.status()).toBe(200);
  await page.evaluate(() => {
    const source = window.__workflowEvents;
    // Metadata is intentionally insufficient to paint the new state.
    source.dispatchEvent(new MessageEvent("OrderStatusChanged", { lastEventId: "100", data: "{}" }));
    source.dispatchEvent(new MessageEvent("OrderDetailsChanged", { lastEventId: "101", data: "{}" }));
  });
  await expect(page.locator(".order-card .pill")).toHaveText("cancelled");
  expect(snapshots).toBe(1);
  const before = snapshots;
  await page.evaluate(() => {
    window.__workflowEvents.dispatchEvent(new MessageEvent("OrderStatusChanged", {
      lastEventId: "99", data: JSON.stringify({ status: "open", orderVersion: 1 }),
    }));
  });
  await expect(page.locator(".order-card .pill")).toHaveText("cancelled");
  expect(snapshots).toBe(before);
});
