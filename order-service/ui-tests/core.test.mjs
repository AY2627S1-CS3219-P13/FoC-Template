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
} from "../internal/order/web/core.mjs";

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
