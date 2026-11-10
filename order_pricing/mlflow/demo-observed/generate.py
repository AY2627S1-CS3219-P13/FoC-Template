"""Generate FAKE "observed" Order and Pricing exports for retraining demos.

Every value here is invented. The files have exactly the shape of the real
exports (mlflow/sql/extract-orders.sql and Pricing's training-export JSONL), so
`import-observed` and `scripts/retrain.sh` can be demonstrated end to end. They
are not evidence of anything and must never be presented as real outcomes.

    python generate.py OUTPUT_DIR   # writes orders.csv and pricing-events.jsonl
"""

from datetime import datetime, timedelta, timezone
import csv
import json
import math
from pathlib import Path
import random
import sys
import uuid

SEED = 20261111
ORDERS = 100
SINGAPORE = timezone(timedelta(hours=8))
START = datetime(2026, 10, 19, tzinfo=SINGAPORE)  # A Monday; three weeks of orders.
NAMESPACE = uuid.UUID("0b7f6a52-2a52-4d5e-9b8e-6d0c5f2a9e11")
WORLD = Path(__file__).resolve().parents[1] / "pricing_mlops" / "sim" / "world.json"
COLUMNS = ("order_id", "created_at", "expires_at", "pickup_type", "pickup_id", "pickup_location_id",
           "delivery_type", "delivery_id", "delivery_location_id", "total_item_quantity", "offered_credits",
           "final_status", "accepted_at", "cancelled_at")


def pg(moment):
    """Postgres CSV rendering of timestamptz in a UTC session."""
    return moment.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%S.%f+00")


def iso(moment):
    return moment.astimezone(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def metres(a, b):
    lat1, lon1, lat2, lon2 = map(math.radians, (a["latitude"], a["longitude"], b["latitude"], b["longitude"]))
    h = math.sin((lat2 - lat1) / 2) ** 2 + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2
    return 2 * 6371008.8 * math.asin(math.sqrt(h)) * 1.3


def effort(distance, quantity):
    # Rough stand-in for the formula: 5 base credits plus walking time and handling.
    return 5 + distance / 120 + 0.5 * (quantity - 1)


def main(output):
    rng = random.Random(SEED)
    locations = {row["id"]: row for row in json.loads(WORLD.read_text())["locations"]}
    ids = sorted(locations)
    suppliers = {location: [str(uuid.uuid5(NAMESPACE, f"{location}-{n}")) for n in range(2)]
                 for location in ids[::2]}
    orders, quotes = [], []
    for index in range(ORDERS):
        day = rng.randrange(21)
        created = START + timedelta(days=day, hours=rng.uniform(8, 22))
        if index == 7:  # A retired canteen the current catalogue no longer knows.
            pickup_location, delivery = "old-canteen", rng.choice(ids)
        else:
            pickup_location = rng.choice(sorted(suppliers))
            delivery = rng.choice([i for i in ids if i != pickup_location])
        by_location = rng.random() < 0.1
        pickup_id = pickup_location if by_location else rng.choice(suppliers.get(pickup_location, [str(uuid.uuid5(NAMESPACE, pickup_location))]))
        quantity = rng.choice((1, 1, 1, 2, 2, 3, 4))
        distance = metres(locations[pickup_location], locations[delivery]) if pickup_location in locations else 900
        cost = effort(distance, quantity)
        recommended = min(30, max(5, round(cost)))
        quoted = rng.random() < 0.65
        offered = max(3, recommended + rng.choice((-2, -1, 0, 0, 0, 1, 2, 3))) if quoted \
            else max(3, round(cost + rng.gauss(0, 3)))
        expires = created + timedelta(minutes=rng.choice((20, 30, 45, 60)))
        orders.append({
            "id": str(uuid.uuid5(NAMESPACE, f"order-{index}")), "created": created, "expires": expires,
            "pickup_type": "location" if by_location else "supplier", "pickup_id": pickup_id,
            "pickup_location": pickup_location, "delivery": delivery, "quantity": quantity,
            "offered": offered, "cost": cost, "quoted": quoted, "recommended": recommended,
        })
    orders.sort(key=lambda o: o["created"])
    for order in orders:
        # Fake truth: couriers respond to pay above effort, and evenings are busier.
        hour = order["created"].hour
        logit = 0.9 + 0.45 * (order["offered"] - order["cost"]) + (0.4 if 17 <= hour < 21 else 0.0)
        roll = rng.random()
        if roll < 0.08:
            order["status"], order["cancelled"] = "cancelled", order["created"] + timedelta(minutes=rng.uniform(1, 10))
        elif rng.random() < 1 / (1 + math.exp(-logit)):
            order["accepted"] = order["created"] + (order["expires"] - order["created"]) * rng.uniform(0.05, 0.8)
            order["status"] = rng.choice(("completed", "completed", "delivered"))
        else:
            order["status"] = "expired"
    for order in orders:
        if order["quoted"]:
            draft = str(uuid.UUID(int=rng.getrandbits(128), version=4))
            revisions = rng.choice((1, 2, 3))
            last = order["created"] - timedelta(seconds=rng.uniform(5, 110))
            for revision in range(revisions):
                at = last - timedelta(seconds=30 * (revisions - 1 - revision))
                # Earlier revisions may have had another quantity; only the last matches.
                quantity = order["quantity"] if revision == revisions - 1 else max(1, order["quantity"] - 1)
                quotes.append(quote(rng, orders, order, draft, revision + 1, at, quantity))
    # Two shoppers drafting the same route at once: the link is ambiguous.
    twin = next(o for o in orders if o["quoted"] and o["pickup_type"] == "supplier")
    quotes.append(quote(rng, orders, twin, str(uuid.UUID(int=rng.getrandbits(128), version=4)), 1,
                        twin["created"] - timedelta(seconds=40), twin["quantity"]))
    # Drafts that never became orders.
    for _ in range(15):
        quotes.append(quote(rng, orders, rng.choice(orders), str(uuid.UUID(int=rng.getrandbits(128), version=4)), 1,
                            START + timedelta(days=rng.randrange(21), hours=rng.uniform(8, 22)), 1))
    quotes.sort(key=lambda event: event["occurredAt"])
    for event in quotes:
        event["recordedAt"] = event["occurredAt"]
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    with (output / "orders.csv").open("w", newline="") as stream:
        writer = csv.writer(stream, lineterminator="\n")
        writer.writerow(COLUMNS)
        for o in orders:
            pickup_location = o["pickup_location"]
            writer.writerow((o["id"], pg(o["created"]), pg(o["expires"]), o["pickup_type"], o["pickup_id"],
                             pickup_location, "location", o["delivery"], o["delivery"], o["quantity"],
                             o["offered"], o["status"], pg(o["accepted"]) if "accepted" in o else "",
                             pg(o["cancelled"]) if "cancelled" in o else ""))
    with (output / "pricing-events.jsonl").open("w") as stream:
        for event in quotes:
            stream.write(json.dumps(event, sort_keys=True, separators=(",", ":")) + "\n")


def order_context(orders, location, now):
    open_count = created = 0
    for other in orders:
        if other["pickup_location"] != location or other["created"] >= now:
            continue
        closed = min([t for t in (other.get("accepted"), other.get("cancelled")) if t] + [other["expires"]])
        open_count += closed > now
        created += other["created"] >= now - timedelta(minutes=15)
    areas = [{"locationId": location, "openRequestCount": open_count, "createdOrderCount": created}]
    return {"state": "fresh", "snapshotId": "orders-" + iso(now), "asOf": iso(now),
            "windowStart": iso(now - timedelta(minutes=15)), "windowEnd": iso(now),
            "areas": areas if open_count or created else []}


def quote(rng, orders, order, draft, revision, at, quantity):
    request = {"draft": {"id": draft, "revision": revision},
               "pickup": {"type": order["pickup_type"], "id": order["pickup_id"]},
               "delivery": {"type": "location", "id": order["delivery"]}, "totalItemQuantity": quantity}
    quote_id = str(uuid.UUID(int=rng.getrandbits(128), version=4))
    response = {"quoteId": quote_id, "draft": request["draft"], "recommendedCredits": order["recommended"],
                "pricingMode": "baseline", "quotedAt": iso(at), "validUntil": iso(at + timedelta(seconds=120)),
                "releaseId": "effort-traffic-002", "contextSnapshotId": "context-" + iso(at),
                "warnings": ["estimated_supply"]}
    context = order_context(orders, order["pickup_location"], at)
    return {
        "eventId": str(uuid.UUID(int=rng.getrandbits(128), version=4)), "source": "pricing",
        "type": "QuoteCalculated", "schemaVersion": 1, "occurredAt": iso(at),
        "data": {"quoteId": quote_id, "request": request, "response": response,
                 "releaseManifestId": "effort-traffic-002", "featureSchemaVersion": "effort-traffic-coordinates-1",
                 "featureSnapshot": {"unroundedCredits": round(order["cost"], 3)},
                 "contextSources": [{"source": "orders", "snapshotId": context["snapshotId"], "asOf": context["asOf"]}],
                 "contextSnapshot": {"schemaVersion": "1.0", "snapshotId": response["contextSnapshotId"],
                                     "orderContext": context},
                 "inferenceReused": False},
    }


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(__doc__)
    main(sys.argv[1])
