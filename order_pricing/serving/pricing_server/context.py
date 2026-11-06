import asyncio
import copy
from datetime import timedelta
import random

import httpx
from jsonschema import ValidationError

from .wire import APIError, fingerprint, instant, json_loads, normalize, stamp, utcnow, validate


class Context:
    def __init__(self, settings, static):
        self.settings, self.static = settings, static
        self.catalogue = self.orders = None
        self.client = httpx.AsyncClient(timeout=1.0, follow_redirects=False, trust_env=False)

    async def close(self):
        await self.client.aclose()

    def validate_source(self, kind, value, previous, now):
        if not isinstance(value, dict) or type(value.get("schemaVersion")) is not int or value["schemaVersion"] != 1:
            raise ValueError("unsupported source schema")
        if not isinstance(value.get("snapshotId"), str) or not value["snapshotId"]:
            raise ValueError("missing snapshot identity")
        validate("time", value["asOf"])
        source_time = instant(value["asOf"])
        if source_time > now + timedelta(seconds=5) or previous and source_time < instant(previous["asOf"]):
            raise ValueError("invalid source time")
        if kind == "catalogue":
            payload = normalize("catalogue", value)
            locations = [row["id"] for row in payload["locations"]]
            suppliers = [row["id"] for row in payload["suppliers"]]
            if len(locations) != len(set(locations)) or len(suppliers) != len(set(suppliers)):
                raise ValueError("duplicate catalogue identity")
            if any(row["locationId"] not in locations for row in payload["suppliers"]):
                raise ValueError("invalid supplier location")
        else:
            payload = normalize("orderContext", dict(value, state="fresh"))
            del payload["state"]
            if instant(payload["windowEnd"]) != source_time or instant(payload["windowStart"]) != source_time - timedelta(minutes=15):
                raise ValueError("invalid aggregate window")
            ids = [row["locationId"] for row in payload["areas"]]
            if len(ids) != len(set(ids)):
                raise ValueError("duplicate aggregate area")
        return dict(payload, snapshotId=value["snapshotId"], asOf=stamp(source_time))

    async def refresh(self, kind):
        catalogue = kind == "catalogue"
        url = self.settings.supplier_url + "/internal/v1/pricing-catalogue" if catalogue else self.settings.order_url + "/internal/v1/pricing-context"
        token = self.settings.supplier_token if catalogue else self.settings.order_token
        async with self.client.stream("GET", url, headers={"Authorization": f"Bearer {token}"}) as response:
            response.raise_for_status()
            if response.headers.get("content-type", "").split(";")[0].strip().lower() != "application/json":
                raise ValueError("invalid source media type")
            body = bytearray()
            async for chunk in response.aiter_bytes(chunk_size=65536):
                body.extend(chunk)
                if len(body) > 4 * 1024 * 1024:
                    raise ValueError("source too large")
        previous = self.catalogue if catalogue else self.orders
        candidate = self.validate_source(kind, json_loads(body), previous, utcnow())
        if catalogue:
            self.catalogue = candidate
        else:
            self.orders = candidate

    async def poll(self, kind):
        interval = self.settings.supplier_refresh if kind == "catalogue" else self.settings.order_refresh
        while True:
            try:
                async with asyncio.timeout(1.5):
                    await self.refresh(kind)
            except (httpx.HTTPError, ValueError, KeyError, TypeError, ValidationError, TimeoutError):
                pass  # Keep the last fully validated snapshot; do not log secrets.
            await asyncio.sleep(interval * random.uniform(.8, 1.2))

    def ready(self, now):
        return self.catalogue is not None and now - instant(self.catalogue["asOf"]) <= timedelta(minutes=10)

    def snapshot(self, now):
        catalogue, orders = self.catalogue, self.orders
        if not self.ready(now):
            raise APIError(503, "context_unavailable")
        context = {
            "schemaVersion": "1.0", "snapshotId": "pending", "sources": copy.deepcopy(self.static["sources"]),
            "catalogue": {key: copy.deepcopy(catalogue[key]) for key in ("locations", "suppliers")},
            "orderContext": {"state": "unavailable"},
            "routes": copy.deepcopy(self.static["routes"]), "footfall": copy.deepcopy(self.static["footfall"]),
        }
        context["sources"].append({"source": "catalogue", "snapshotId": catalogue["snapshotId"], "asOf": catalogue["asOf"], "provenance": self.settings.source_provenance})
        if orders:
            context["orderContext"] = {key: copy.deepcopy(orders[key]) for key in ("windowStart", "windowEnd", "areas")}
            context["orderContext"]["state"] = "fresh" if now - instant(orders["asOf"]) <= timedelta(seconds=60) else "stale"
            context["sources"].append({"source": "orders", "snapshotId": orders["snapshotId"], "asOf": orders["asOf"], "provenance": self.settings.source_provenance})
        context["snapshotId"] = "context-" + fingerprint(context)
        return context


def resolve_places(request, context):
    catalogue = context["catalogue"]
    locations = {row["id"] for row in catalogue["locations"]}
    suppliers = {row["id"]: row for row in catalogue["suppliers"]}
    result = {}
    for name in ("pickup", "delivery"):
        place = request[name]
        if place["type"] == "location":
            resolved = place["id"] if place["id"] in locations else None
        else:
            supplier = suppliers.get(place["id"])
            resolved = supplier["locationId"] if supplier and supplier["active"] else None
        if resolved is None:
            raise APIError(422, "unsupported_place")
        result[name + "LocationId"] = resolved
    return result
