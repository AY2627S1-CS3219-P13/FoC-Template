"""Turn observed Order outcomes and Pricing quotes into acceptance-dataset records.

Inputs are operator exports, never live service access:

- ``orders.csv`` from ``mlflow/sql/extract-orders.sql`` (read-only, run against
  Order's database once);
- optionally ``pricing-events.jsonl``: Pricing training-export envelopes, one per
  line (``GET /internal/v1/training-events``). Only ``QuoteCalculated`` is used.

Order does not store which quote a user saw, so a quote is linked heuristically:
same pickup, delivery and item quantity, calculated at most MATCH_WINDOW before
order creation. When quotes from more than one draft match, the link is ambiguous
and dropped. Every observed record is kept whether or not a quote matched: the
model's features do not need the quote.

The observed records are appended to an existing simulated dataset, so the result
trains through the same ``prepare`` path. Synthetic rows keep their share of the
training weight; observed rows get an explicit share recorded in the manifest.
"""

import csv
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path

from .sim import dataset

NAME = "observed"
VERSION = "order-export-1"
CSV_COLUMNS = (
    "order_id", "created_at", "expires_at", "pickup_type", "pickup_id", "pickup_location_id",
    "delivery_type", "delivery_id", "delivery_location_id", "total_item_quantity", "offered_credits",
    "final_status", "accepted_at", "cancelled_at",
)
# Quote validity (120 s) plus one 30 s automatic refresh.
MATCH_WINDOW = timedelta(seconds=150)
# Order's pricing-context endpoint counts orders created in the last 15 minutes.
CREATED_WINDOW = timedelta(minutes=15)
PLACE_TYPES = ("supplier", "location")
NIL_DRAFT = "00000000-0000-4000-8000-000000000000"


def instant(text):
    text = text.strip()
    moment = datetime.fromisoformat(text.replace("Z", "+00:00").replace(" ", "T", 1))
    if moment.tzinfo is None:
        raise ValueError(f"timestamp without a time zone: {text}")
    return moment.astimezone(timezone.utc)


def stamp(moment):
    return moment.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def _positive_int(text, field):
    value = int(text)
    if value <= 0:
        raise ValueError(f"{field} must be positive")
    return value


def read_orders(path):
    with Path(path).open(newline="") as stream:
        reader = csv.DictReader(stream)
        if tuple(reader.fieldnames or ()) != CSV_COLUMNS:
            raise ValueError("orders CSV header must be: " + ",".join(CSV_COLUMNS))
        orders = []
        for line, row in enumerate(reader, start=2):
            try:
                orders.append(_order(row))
            except (ValueError, TypeError) as error:
                raise ValueError(f"orders CSV line {line}: {error}") from None
    if len({o["id"] for o in orders}) != len(orders):
        raise ValueError("orders CSV repeats an order_id")
    return orders


def _order(row):
    places = {}
    for side in ("pickup", "delivery"):
        kind, ident = row[f"{side}_type"], row[f"{side}_id"]
        if kind not in PLACE_TYPES or not ident:
            raise ValueError(f"invalid {side} place")
        location = ident if kind == "location" else row[f"{side}_location_id"]
        places[side] = {"type": kind, "id": ident, "locationId": location or None}
    created, expires = instant(row["created_at"]), instant(row["expires_at"])
    if expires <= created:
        raise ValueError("expiry must be after creation")
    quantity = row["total_item_quantity"].strip()
    return {
        "id": row["order_id"], "createdAt": created, "expiresAt": expires, **places,
        "quantity": _positive_int(quantity, "total_item_quantity") if quantity else None,
        "offeredCredits": _positive_int(row["offered_credits"], "offered_credits"),
        "status": row["final_status"],
        "acceptedAt": instant(row["accepted_at"]) if row["accepted_at"].strip() else None,
        "cancelledAt": instant(row["cancelled_at"]) if row["cancelled_at"].strip() else None,
    }


def read_quotes(path):
    quotes = []
    with Path(path).open() as stream:
        for line, text in enumerate(stream, start=1):
            if not text.strip():
                continue
            event = json.loads(text)
            if event.get("source") != "pricing" or event.get("type") != "QuoteCalculated":
                continue
            if event.get("schemaVersion") != 1:
                raise ValueError(f"pricing events line {line}: unsupported QuoteCalculated schema")
            quotes.append(event)
    return quotes


def outcome(order):
    """Label as the simulator does; None means the outcome cannot be used."""
    if order["acceptedAt"] is not None:
        return "accepted" if order["acceptedAt"] <= order["expiresAt"] else None
    if order["cancelledAt"] is not None:
        return "cancelled"  # Censored before anyone accepted; kept, never a rejection.
    return "expired" if order["status"] == "expired" else None


def _closed_at(order):
    ends = [t for t in (order["acceptedAt"], order["cancelledAt"]) if t is not None]
    return min(ends + [order["expiresAt"]])


def order_context(order, orders):
    """Order's pricing-context aggregate for the pickup area, rebuilt from history."""
    now, location = order["createdAt"], order["pickup"]["locationId"]
    open_count = created_count = 0
    for other in orders:
        if other["pickup"]["locationId"] != location or other["createdAt"] >= now:
            continue
        if _closed_at(other) > now:
            open_count += 1
        if other["createdAt"] >= now - CREATED_WINDOW:
            created_count += 1
    areas = [{"locationId": location, "openRequestCount": open_count, "createdOrderCount": created_count}]
    return {"state": "fresh", "windowStart": stamp(now - CREATED_WINDOW), "windowEnd": stamp(now),
            "areas": areas if open_count or created_count else []}


def _place_key(place):
    return place["type"], place["id"]


def match_quote(order, quotes):
    """(quote, status): status is matched, ambiguous or none."""
    candidates = []
    for quote in quotes:
        request = quote["data"]["request"]
        at = instant(quote["occurredAt"])
        if (_place_key(request["pickup"]) == _place_key(order["pickup"])
                and _place_key(request["delivery"]) == _place_key(order["delivery"])
                and request.get("totalItemQuantity") == order["quantity"]
                and order["createdAt"] - MATCH_WINDOW <= at <= order["createdAt"]):
            candidates.append((at, request["draft"]["revision"], quote))
    if not candidates:
        return None, "none"
    if len({c[2]["data"]["request"]["draft"]["id"] for c in candidates}) > 1:
        return None, "ambiguous"
    return max(candidates, key=lambda c: (c[0], c[1]))[2], "matched"


def split_for(order_id):
    # One in five orders is held out, chosen by a stable hash of the order ID.
    return "validation" if hashlib.sha256(order_id.encode()).digest()[0] % 5 == 0 else "train"


def build_records(orders, quotes, known_locations):
    records, suppliers = [], {}
    report = {"orders": len(orders), "skipped": {}, "quotes": {"matched": 0, "ambiguous": 0, "none": 0}}

    def skip(reason):
        report["skipped"][reason] = report["skipped"].get(reason, 0) + 1

    for order in orders:
        label = outcome(order)
        pickup, delivery = order["pickup"]["locationId"], order["delivery"]["locationId"]
        if label is None:
            skip("undecided")
            continue
        if pickup not in known_locations or delivery not in known_locations:
            skip("unknown_location")
            continue
        if pickup == delivery:
            skip("same_location")
            continue
        for place in (order["pickup"], order["delivery"]):
            if place["type"] == "supplier":
                if suppliers.setdefault(place["id"], place["locationId"]) != place["locationId"]:
                    raise ValueError("a supplier appears at two locations; export a consistent snapshot")
        quote, status = match_quote(order, quotes)
        report["quotes"][status] += 1
        request = {
            "draft": dict(quote["data"]["request"]["draft"]) if quote else {"id": NIL_DRAFT, "revision": 1},
            "pickup": {"type": order["pickup"]["type"], "id": order["pickup"]["id"]},
            "delivery": {"type": order["delivery"]["type"], "id": order["delivery"]["id"]},
            "orderExpiresAt": stamp(order["expiresAt"]),
        }
        if order["quantity"] is not None:
            request["totalItemQuantity"] = order["quantity"]
        quoted_context = (quote or {}).get("data", {}).get("contextSnapshot", {}).get("orderContext")
        record = {
            "provenance": NAME, "generator": NAME, "generatorVersion": VERSION,
            "scenarioId": "observed", "thetaId": "observed", "split": split_for(order["id"]),
            "orderId": order["id"],
            # Features describe the moment of pricing: the quote if linked, else creation.
            "calculatedAt": quote["occurredAt"] if quote else stamp(order["createdAt"]),
            "request": request,
            "resolvedPlaces": {"pickupLocationId": pickup, "deliveryLocationId": delivery},
            "orderContext": quoted_context or order_context(order, orders),
            "orderContextSource": "quote" if quoted_context else "reconstructed",
            "offeredCredits": order["offeredCredits"], "expiresAt": stamp(order["expiresAt"]),
            "outcome": label, "quoteMatch": status,
        }
        if quote:
            response = quote["data"]["response"]
            record["quote"] = {"quoteId": response["quoteId"], "recommendedCredits": response["recommendedCredits"],
                               "pricingMode": response["pricingMode"], "releaseId": response["releaseId"]}
        if label == "accepted":
            record["acceptedAt"] = stamp(order["acceptedAt"])
        records.append(record)
    return records, suppliers, report


def _sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest() if path else None


def build(synthetic_dir, orders_path, output, pricing_events=None, observed_weight=0.2):
    """Write a mixed dataset: the simulated one plus observed records."""
    if not 0 < observed_weight < 1:
        raise ValueError("observed weight must be strictly between 0 and 1")
    base_records, shared, base = dataset.read(synthetic_dir)
    if base["provenance"] != "synthetic":
        raise ValueError("the base dataset must be simulated; observed data is added once")
    orders = read_orders(orders_path)
    quotes = read_quotes(pricing_events) if pricing_events else []
    known = {row["id"] for row in shared["catalogue"]["locations"]}
    records, suppliers, report = build_records(orders, quotes, known)
    if not any(r["split"] == "train" and r["outcome"] != "cancelled" for r in records) or \
            not any(r["split"] == "validation" and r["outcome"] != "cancelled" for r in records):
        raise ValueError("observed data needs decided orders in both the training and validation splits")
    listed = {row["id"] for row in shared["catalogue"]["suppliers"]}
    shared["catalogue"]["suppliers"] += [{"id": ident, "locationId": location, "active": True}
                                         for ident, location in sorted(suppliers.items()) if ident not in listed]
    shared["sources"].append({"source": "observed-orders", "snapshotId": "orders-" + _sha256(orders_path)[:16],
                              "asOf": stamp(max(o["createdAt"] for o in orders)), "provenance": "observed"})
    combined = base_records + records
    counts = {}
    for record in combined:
        key = f"{record['generator']}/{record['split']}/{record['outcome']}"
        counts[key] = counts.get(key, 0) + 1
    manifest = {
        "schemaVersion": dataset.SCHEMA_VERSION, "provenance": "mixed", "seed": base["seed"],
        "baseDatasetId": base["datasetId"],
        "generators": dict(base["generators"], **{NAME: {"version": VERSION, "rows": len(records),
                                                         "ordersSha256": _sha256(orders_path),
                                                         "pricingEventsSha256": _sha256(pricing_events)}}),
        "observedWeight": observed_weight, "observedReport": report,
        "worldSha256": base["worldSha256"], "footTrafficSha256": base["footTrafficSha256"],
        "sharedContextSha256": dataset.digest(shared), "thetas": base["thetas"], "counts": counts,
        "rowsSha256": dataset.digest(combined),
        "split": base["split"] + "; observed orders by order-ID hash modulo 5",
        "labels": base["labels"],
        "limitation": "Synthetic rows encode assumed behaviour. Observed rows are real outcomes whose "
                      "quote link is heuristic; their prices were chosen by users, not randomized.",
    }
    manifest["datasetId"] = dataset.PREFIXES["mixed"] + dataset.digest(manifest)[:32]
    dataset.write(output, combined, shared, manifest)
    return {"datasetId": manifest["datasetId"], "output": str(output), "observed": report, "counts": counts}
