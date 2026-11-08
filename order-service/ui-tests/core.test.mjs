import { test } from "node:test";
import assert from "node:assert/strict";
import {
  positiveInteger,
  encodeJSON,
  decodeJSON,
  expiryISO,
  newer,
  creationBody,
  newestFirst,
  scheduledHistoryPage,
  distanceMeters,
  pickupLocationId,
  formatDistance,
  openComparator,
  alertState,
  radiusLabel,
  base64UrlBytes,
  subscriptionBody,
  deepLink,
} from "../internal/order/web/core.mjs";
import { readFileSync } from "node:fs";
import vm from "node:vm";

test("distances match the server's Haversine reference values", () => {
  const com2 = { latitude: 1.293925, longitude: 103.774153 };
  const library = { latitude: 1.296558, longitude: 103.773111 };
  assert.equal(distanceMeters(com2, library), 315);
  assert.equal(distanceMeters(library, com2), 315);
  assert.equal(distanceMeters(library, { latitude: 1.304, longitude: 103.7735 }), 829);
  assert.equal(distanceMeters(com2, com2), 0);
  assert.equal(formatDistance(0), null);
  assert.equal(formatDistance(49), null);
  assert.equal(formatDistance(50), "50 m");
  assert.equal(formatDistance(315), "320 m");
  assert.equal(formatDistance(829), "830 m");
  assert.equal(formatDistance(1123), "1.1 km");
});

test("supplier pickups use their registered campus location", () => {
  assert.equal(pickupLocationId({ pickup: { type: "location", id: "com2" } }), "com2");
  assert.equal(pickupLocationId({ pickup: { type: "supplier", id: "uuid", locationId: "central-library" } }), "central-library");
});

test("nearest pickup sorts by distance, then expiry, with unknown distances last", () => {
  const order = (id, expiry, distance) => ({
    id, distance, offeredCredits: "10",
    expiresAt: `2030-11-04T10:${expiry}:00Z`, createdAt: "2030-11-04T09:00:00Z",
  });
  const rows = [order("unknown", "05", null), order("far", "01", 900), order("near-late", "30", 100), order("near-soon", "10", 100)];
  const distanceOf = (o) => o.distance;
  assert.deepEqual(rows.toSorted(openComparator("distance", distanceOf)).map((o) => o.id), ["near-soon", "near-late", "far", "unknown"]);
  // Other sorts ignore distance.
  assert.deepEqual(rows.toSorted(openComparator("expiry", distanceOf)).map((o) => o.id), ["far", "unknown", "near-soon", "near-late"]);
});

test("scheduled history keeps creation order across regular-history page boundaries", () => {
  const order = (id, minute) => ({ id, createdAt: `2030-11-04T10:${minute}:00Z` });
  const regular = [order("r-newest", "50"), order("r-middle", "30"), order("r-oldest", "10")];
  const scheduled = [order("s-oldest", "00"), order("s-gap", "40"), order("s-newest", "59"), order("s-equal", "30")];
  const pages = regular.map((start, index) => scheduledHistoryPage(scheduled, index + 1, start, regular[index + 1]));
  assert.deepEqual(pages.map(page => page.map(o => o.id)), [["s-gap", "s-newest"], ["s-equal"], ["s-oldest"]]);
  const merged = pages.flatMap((page, index) => [...page, regular[index]].sort(newestFirst));
  assert.deepEqual(merged.map(o => o.id), [...regular, ...scheduled].sort(newestFirst).map(o => o.id));
  assert.deepEqual(scheduledHistoryPage(scheduled, 1, null, null), scheduled);
});

test("64-bit integers remain exact in both directions", () => {
  const max = "9223372036854775807";
  const text = encodeJSON({
    name: "quantity: 9223372036854775807",
    quantity: positiveInteger(max),
  });
  assert.equal(
    text,
    '{"name":"quantity: 9223372036854775807","quantity":9223372036854775807}',
  );
  assert.equal(decodeJSON(text).quantity, max);
  assert.equal(decodeJSON(text).name, "quantity: 9223372036854775807");
  for (const invalid of [
    "0",
    "-1",
    "1.2",
    "1e3",
    "NaN",
    "9223372036854775808",
    "",
  ])
    assert.throws(() => positiveInteger(invalid));
});
test("Singapore input is independent of the machine timezone", () => {
  assert.equal(expiryISO("2026-10-09T18:30"), "2026-10-09T10:30:00.000Z");
});
test("delayed and duplicate responses cannot undo newer status", () => {
  const current = { id: "one", version: 4, status: "delivered" };
  assert.equal(
    newer(current, { id: "one", version: 2, status: "accepted" }),
    current,
  );
  assert.deepEqual(newer(current, { ...current }), current);
  assert.equal(
    newer(current, { id: "two", version: 1, status: "open" }).id,
    "two",
  );
});
test("creation serializes typed records, names and full precision amounts", () => {
  const draft = {
    pickup: "supplier:one",
    delivery: "location:com2",
    items: [{ name: '<script>"x"</script>', quantity: "9007199254740993" }],
    offer: "9223372036854775807",
    expiry: "2026-10-09T18:30",
  };
  const body = decodeJSON(creationBody(draft));
  assert.equal(body.items[0].quantity, "9007199254740993");
  assert.equal(body.offeredCredits, "9223372036854775807");
  assert.equal(body.items[0].name, draft.items[0].name);
  assert.deepEqual(body.pickup, { type: "supplier", id: "one" });
  assert.throws(() => creationBody({ ...draft, delivery: draft.pickup }));
  assert.throws(() => creationBody({ ...draft, items: [] }));
});

test("scheduled creation preserves int64 values and replaces manual expiry",()=>{
 const draft={pickup:"location:com2",delivery:"location:utown",items:[{name:"Tea",quantity:"9223372036854775807"}],offer:"9223372036854775807",expiry:""};
 const raw=creationBody(draft,"2030-11-04T09:00:00+08:00");
 assert.match(raw,/"offeredCredits":9223372036854775807/);
 assert.match(raw,/"quantity":9223372036854775807/);
 assert.equal(JSON.parse(raw).windowStart,"2030-11-04T09:00:00+08:00");
 assert.ok(!raw.includes('"expiresAt"'));
});

test("alert panel states follow browser support, permission and the account", () => {
  const base = { supported: true, secure: true, permission: "default", pushAvailable: true, preference: null, subscribed: false, devices: 0 };
  const on = { enabled: true, locationId: "com2", radiusMeters: 500 };
  assert.equal(alertState({ ...base, supported: false }).key, "unsupported");
  assert.equal(alertState({ ...base, secure: false }).key, "insecure");
  assert.equal(alertState({ ...base, pushAvailable: false }).key, "unavailable");
  assert.equal(alertState({ ...base, permission: "denied", preference: on, subscribed: true }).key, "blocked");
  assert.equal(alertState(base).key, "off");
  assert.equal(alertState({ ...base, preference: { ...on, enabled: false }, subscribed: true }).key, "off");
  assert.deepEqual(alertState({ ...base, preference: on, subscribed: true }), { key: "on", message: "On for this device · within 500 m" });
  assert.equal(alertState({ ...base, preference: on, devices: 2 }).key, "elsewhere");
  assert.equal(alertState({ ...base, preference: on, devices: 0 }).key, "off");
  assert.equal(radiusLabel(200), "within 200 m");
  assert.equal(radiusLabel(1000), "within 1 km");
});

test("subscriptions send only the documented fields and keys decode to bytes", () => {
  assert.deepEqual(subscriptionBody({ endpoint: "https://fcm.googleapis.com/x", expirationTime: null, keys: { p256dh: "BA", auth: "AA" } }),
    { endpoint: "https://fcm.googleapis.com/x", keys: { p256dh: "BA", auth: "AA" } });
  assert.deepEqual([...base64UrlBytes("-_8")], [0xfb, 0xff]);
  assert.equal(base64UrlBytes("B".padEnd(87, "A")).length, 65);
});

test("notification links open Help a friend for one errand", () => {
  assert.deepEqual(deepLink("?view=open&order=abc%2F1"), { view: "open", order: "abc/1" });
  assert.deepEqual(deepLink("?view=open"), { view: "open", order: null });
  assert.equal(deepLink("?view=mine&order=abc"), null);
  assert.equal(deepLink(""), null);
  assert.deepEqual(deepLink(`?view=open&order=${"x".repeat(161)}`), { view: "open", order: null });
});

// Run the real service worker against a minimal worker global.
function loadWorker() {
  const listeners = {}, shown = [], opened = [], messages = [];
  let windows = [];
  const self = {
    location: { origin: "https://foc.example" },
    addEventListener: (type, fn) => (listeners[type] = fn),
    skipWaiting: () => {},
    registration: { showNotification: async (title, options) => shown.push({ title, options }) },
    clients: {
      claim: async () => {},
      matchAll: async () => windows,
      openWindow: async (url) => opened.push(url),
    },
  };
  vm.runInNewContext(readFileSync(new URL("../internal/order/web/sw.js", import.meta.url), "utf8"), { self, URL });
  const fire = async (type, event) => {
    let work;
    listeners[type]({ ...event, waitUntil: (p) => (work = p) });
    await work;
  };
  return { self, fire, shown, opened, messages, setWindows: (w) => (windows = w) };
}

test("the service worker shows payload text and falls back safely", async () => {
  const sw = loadWorker();
  const payload = { v: 1, orderId: "o-1", title: "New errand nearby", body: "Campus Cafe (COM2) · about 320 m", url: "/orders?view=open&order=o-1" };
  await sw.fire("push", { data: { json: () => payload } });
  await sw.fire("push", { data: { json: () => ({ v: 1, orderId: "o-2", url: "https://evil.example/" }) } });
  await sw.fire("push", { data: { json: () => { throw new SyntaxError("not JSON"); } } });
  await sw.fire("push", { data: null });
  assert.deepEqual(JSON.parse(JSON.stringify(sw.shown[0])), {
    title: "New errand nearby",
    options: { body: "Campus Cafe (COM2) · about 320 m", tag: "errand-o-1", data: { url: "/orders?view=open&order=o-1", orderId: "o-1" } },
  });
  assert.equal(sw.shown[1].options.data.url, "/orders?view=open");
  for (const fallback of sw.shown.slice(2)) {
    assert.equal(fallback.title, "New errand nearby");
    assert.equal(fallback.options.tag, "errand");
  }
});

test("notification clicks reuse an open Orders tab or open the errand", async () => {
  const sw = loadWorker();
  const click = (data) => ({ notification: { data, close: () => {} } });
  await sw.fire("notificationclick", click({ url: "/orders?view=open&order=o-1", orderId: "o-1" }));
  assert.deepEqual(sw.opened, ["https://foc.example/orders?view=open&order=o-1"]);
  let focused = false;
  const posted = [];
  sw.setWindows([
    { url: "https://foc.example/", focus: async () => assert.fail("wrong tab") },
    { url: "https://foc.example/orders", focus: async () => (focused = true), postMessage: (m) => posted.push(m) },
  ]);
  await sw.fire("notificationclick", click({ url: "/orders?view=open&order=o-2", orderId: "o-2" }));
  assert.ok(focused);
  assert.deepEqual(JSON.parse(JSON.stringify(posted)), [{ type: "open-errand", orderId: "o-2" }]); // objects from the worker realm
  assert.equal(sw.opened.length, 1);
});
