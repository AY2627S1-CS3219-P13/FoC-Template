import { test, expect } from "@playwright/test";
import { writeFile } from "node:fs/promises";

async function start(page, actor = "requester") {
  await page.request.get(`/__test/session?actor=${actor}`);
  await page.goto("/orders");
  await expect(page.locator("#session-notice")).toContainText("signed in");
  await expect(page.locator("#pickup")).toBeEnabled();
}
async function seed(page, name) {
  const response = await page.request.post("/api/v1/orders", {
    headers: { Origin: new URL(page.url()).origin, "Idempotency-Key": name },
    data: { pickup: { type: "supplier", id: "22222222-2222-4222-8222-222222222222" },
      delivery: { type: "location", id: "utown" }, items: [{ name, quantity: 1 }],
      offeredCredits: 10, expiresAt: new Date(Date.now() + 3600000).toISOString() },
  });
  expect(response.status()).toBe(201);
  return (await response.json()).order;
}
async function action(page, id, action, expected = 200) {
  const response = await page.request.post(`/api/v1/orders/${id}/actions`, {
    headers: { Origin: new URL(page.url()).origin }, data: { action },
  });
  expect(response.status()).toBe(expected);
}
async function select(page, name) {
  // Cards show locations, so read their detail until the intended item is found.
  const cards = page.locator(".order-card");
  await expect(cards.first()).toBeVisible();
  for (let i = 0; i < await cards.count(); i++) {
    await cards.nth(i).click();
    if ((await page.locator("#order-detail").textContent()).includes(name)) return;
  }
  throw new Error(`Order ${name} not found`);
}

test("review07 replaying an old delivery preserves aggregate courier capacity", async ({ page }) => {
  // Read-only instrumentation of the actual module, not a replacement of its
  // capacity logic. This distinguishes a stale disabled button from correctness.
  await page.route("**/orders/app.js", async route => {
    const response = await route.fetch();
    await route.fulfill({ response, body: (await response.text()) +
      "\nglobalThis.__reviewCapacityState = () => ({ courierBusy, stale, detailStale, pending: Object.keys(pending).length > 0 });\n" });
  });
  await page.request.post("/__test/reset");
  await start(page);
  const old = await seed(page, "review-old-delivery");
  const current = await seed(page, "review-current-assignment");
  const candidate = await seed(page, "review-third-candidate");
  await start(page, "courier");
  await action(page, old.id, "accept");
  await action(page, old.id, "pick_up");
  await page.getByRole("button", { name: "My deliveries", exact: true }).click();
  await select(page, "review-old-delivery");
  await expect(page.getByRole("button", { name: "Mark delivered", exact: true })).toBeEnabled();
  let loseResponse = true;
  await page.route(`**/api/v1/orders/${old.id}/actions`, async route => {
    if (loseResponse) {
      loseResponse = false;
      expect((await route.fetch()).status()).toBe(200);
      await route.abort("failed");
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Mark delivered", exact: true }).click();
  await expect(page.locator("#order-detail")).toContainText("unresolved");
  // This authenticated request represents another tab accepting a newer errand.
  await action(page, current.id, "accept");
  await page.getByRole("button", { name: "Help a friend", exact: true }).click();
  await select(page, "review-third-candidate");
  await expect(page.locator("#order-detail")).toContainText("You have an ongoing order");
  await expect(page.getByRole("button", { name: "Accept errand", exact: true })).toBeDisabled();
  expect((await page.evaluate(() => globalThis.__reviewCapacityState())).courierBusy).toBe(true);
  // Bound the observation window: don't let the periodic session poll conceal
  // an incorrect capacity update from the old action's response.
  let resume;
  const paused = new Promise(resolve => { resume = resolve; });
  await page.route("**/api/v1/orders/session", async route => { await paused; await route.continue(); });
  try {
    await page.getByRole("button", { name: "My deliveries", exact: true }).click();
    await select(page, "review-old-delivery");
    await expect(page.getByRole("button", { name: "Retry same action", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "Retry same action", exact: true }).click();
    await expect.poll(() => page.evaluate(() => globalThis.__reviewCapacityState().courierBusy)).toBe(null);
    resume();
    await page.unroute("**/api/v1/orders/session");
    await expect(page.locator("#order-detail")).not.toContainText("unresolved");
    await page.getByRole("button", { name: "Help a friend", exact: true }).click();
    await select(page, "review-third-candidate");
    const refreshed = page.waitForResponse(response => {
      const url = new URL(response.url());
      return url.pathname === "/api/v1/orders" && url.searchParams.get("scope") === "open" && response.status() === 200;
    });
    await page.locator("#reload-list").click();
    await refreshed;
    await expect(page.locator("#sync-notice")).toBeHidden();
    await expect.poll(() => page.evaluate(() => globalThis.__reviewCapacityState()))
      .toMatchObject({ stale: false, detailStale: false, pending: false });
    // APIRequestContext is not intercepted by page.route; these are real guards.
    const session = await page.request.get("/api/v1/orders/session");
    expect((await session.json()).courierBusy).toBe(true);
    await action(page, candidate.id, "accept", 409);
    const observed = await page.evaluate(() => globalThis.__reviewCapacityState());
    const evidence = JSON.stringify({ serverCourierBusy: true, serverAcceptanceStatus: 409, ui: observed }, null, 2);
    await writeFile(test.info().outputPath("capacity-state.json"), evidence);
    await test.info().attach("actual-capacity-state", {
      body: evidence,
      contentType: "application/json",
    });
    expect.soft(observed.courierBusy, "old delivery replay must preserve aggregate busy state").toBe(true);
    await expect(page.getByRole("button", { name: "Accept errand", exact: true })).toBeDisabled({ timeout: 1000 });
  } finally {
    resume();
    await page.unroute("**/api/v1/orders/session");
  }
});
