"""Synthetic order records shaped like joined real Order/Pricing events.

A record is what training will later receive from real data: the quote input that
priced a created order (QuoteCalculated), the offer the customer confirmed
(PricingOrderCreated) and its outcome (PricingOrderOutcome). The catalogue and
static context are identical for a whole dataset, so records reference one shared
context instead of repeating it.
"""

import copy
from datetime import datetime, timedelta
import json
from pathlib import Path
from types import MappingProxyType

from pricing_contracts import PolicyInput

from .world import supplier_id

OUTCOMES = ("accepted", "expired", "cancelled")


def shared_context(world, baseline_package):
    """Catalogue plus the release's static routes/footfall, as serving assembles it."""
    static = json.loads((Path(baseline_package) / "artifacts" / "context.json").read_text())
    stamp = "2027-01-01T00:00:00Z"
    return {
        "schemaVersion": "1.0", "snapshotId": "synthetic-context-" + world.source["worldId"],
        "sources": copy.deepcopy(static["sources"]) + [
            {"source": "catalogue", "snapshotId": "synthetic-" + world.source["worldId"], "asOf": stamp,
             "provenance": "synthetic"},
        ],
        "catalogue": {
            "locations": [
                {"id": location, "latitude": world.locations[location]["latitude"],
                 "longitude": world.locations[location]["longitude"]}
                for location in world.ids
            ],
            "suppliers": [
                {"id": supplier_id(location), "locationId": location, "active": True} for location in world.ids
            ],
        },
        "routes": copy.deepcopy(static["routes"]), "footfall": copy.deepcopy(static["footfall"]),
    }


def make_record(*, generator, version, scenario, theta_id, split, calculated_at, pickup, delivery,
                quantity, open_requests, created_orders, offered, expires_at, outcome, accepted_at=None):
    if outcome not in OUTCOMES:
        raise ValueError("unknown synthetic outcome")
    request = {
        "draft": {"id": "00000000-0000-4000-8000-000000000000", "revision": 1},
        "pickup": {"type": "supplier", "id": supplier_id(pickup)},
        "delivery": {"type": "location", "id": delivery},
        "orderExpiresAt": expires_at,
    }
    if quantity is not None:
        request["totalItemQuantity"] = quantity
    record = {
        "provenance": "synthetic", "generator": generator, "generatorVersion": version,
        "scenarioId": scenario, "thetaId": theta_id, "split": split,
        "calculatedAt": calculated_at, "request": request,
        "resolvedPlaces": {"pickupLocationId": pickup, "deliveryLocationId": delivery},
        "orderContext": {
            "state": "fresh", "windowStart": _minus_minutes(calculated_at, 15), "windowEnd": calculated_at,
            "areas": [{"locationId": pickup, "openRequestCount": open_requests,
                       "createdOrderCount": created_orders}],
        },
        "offeredCredits": offered, "expiresAt": expires_at, "outcome": outcome,
    }
    if accepted_at is not None:
        record["acceptedAt"] = accepted_at
    return record


def _minus_minutes(stamp, minutes):
    moment = datetime.fromisoformat(stamp.replace("Z", "+00:00")) - timedelta(minutes=minutes)
    return moment.isoformat().replace("+00:00", "Z")


def _freeze(value):
    if isinstance(value, dict):
        return MappingProxyType({key: _freeze(item) for key, item in value.items()})
    if isinstance(value, list):
        return tuple(_freeze(item) for item in value)
    return value


def policy_input(record, shared):
    """The PolicyInput serving would have built for this record's quote."""
    context = copy.deepcopy(shared)
    context["orderContext"] = copy.deepcopy(record["orderContext"])
    context["sources"].append({"source": "orders", "snapshotId": "synthetic-orders",
                               "asOf": record["calculatedAt"], "provenance": "synthetic"})
    return PolicyInput("1.0", record["calculatedAt"], _freeze(record["request"]),
                       _freeze(record["resolvedPlaces"]), _freeze(context))
