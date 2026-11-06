"""Exercises real HTTP replicas, durable events and formula/model interchange."""

from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import json
import os
import time
import unittest
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen
from uuid import uuid4

from tests.support import load_fixture, validate

BASES = ["http://pricing-a:8080", "http://pricing-b:8080"]
MODEL = "http://pricing-model:8080"
LEGACY = "http://pricing-legacy:8080"
FALLBACK = "http://pricing-fallback:8080"
UNREADY = "http://pricing-unready:8080"


def call(base, path, body=None, *, token=None, key=None):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if key:
        headers["Idempotency-Key"] = key
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    request = Request(base + path, data=data, headers=headers)
    try:
        with urlopen(request, timeout=3) as response:
            return response.status, json.load(response)
    except HTTPError as error:
        return error.code, json.load(error)


def quote_body():
    value = load_fixture("policy-input.json")["request"]
    value["draft"]["id"] = str(uuid4())
    value["orderExpiresAt"] = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()
    return value


class PricingStackTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.quote_token = os.environ["PRICING_SERVICE_TOKEN"]
        cls.write_token = os.environ["PRICING_ORDER_WRITE_TOKEN"]
        cls.export_token = os.environ["PRICING_EXPORT_TOKEN"]
        for base in [*BASES, LEGACY, MODEL, FALLBACK]:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                try:
                    if call(base, "/readyz")[0] == 200:
                        break
                except OSError:
                    pass
                time.sleep(0.2)
            else:
                raise AssertionError(f"test service did not become ready: {base}")

    def quote(self, base, body, key):
        return call(base, "/internal/v1/price-quotes", body, token=self.quote_token, key=key)

    def export(self):
        events, cursor = [], None
        for _ in range(100):
            query = {"limit": 100}
            if cursor:
                query["after"] = cursor
            status, page = call(BASES[0], "/internal/v1/training-events?" + urlencode(query), token=self.export_token)
            self.assertEqual(status, 200)
            events.extend(page["events"])
            cursor = page["nextCursor"]
            if not page["hasMore"]:
                return events
        self.fail("unexpectedly large fixture export")

    def test_concurrent_replica_issuance_has_one_durable_event(self):
        body, key = quote_body(), str(uuid4())

        def attempt(index):
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                status, result = self.quote(BASES[index % 2], body, key)
                if status == 200:
                    return result
                self.assertIn(status, (429, 503))
                self.assertIn(result["error"]["code"], ("rate_limited", "quote_pending"))
                time.sleep(0.1)
            self.fail("concurrent calculation did not resolve")

        with ThreadPoolExecutor(max_workers=6) as pool:
            responses = list(pool.map(attempt, range(6)))
        self.assertTrue(all(value == responses[0] for value in responses))
        quote_id = responses[0]["quoteId"]
        calculations = [event for event in self.export() if event["type"] == "QuoteCalculated" and event["data"]["quoteId"] == quote_id]
        self.assertEqual(len(calculations), 1)
        self.assertEqual(calculations[0]["data"]["response"], responses[0])
        self.assertIn("featureSnapshot", calculations[0]["data"])
        archived = calculations[0]["data"]["contextSnapshot"]
        self.assertEqual(archived["snapshotId"], responses[0]["contextSnapshotId"])
        self.assertEqual(archived["sources"], calculations[0]["data"]["contextSources"])
        validate("ContextSnapshot", archived)

    def test_expired_quote_replays_and_equivalent_offsets_do_not_conflict(self):
        body, key = quote_body(), str(uuid4())
        expiry = datetime.now(timezone.utc) + timedelta(seconds=2)
        body["orderExpiresAt"] = expiry.isoformat()
        status, first = self.quote(BASES[0], body, key)
        self.assertEqual(status, 200)
        self.assertEqual(datetime.fromisoformat(first["validUntil"]), expiry)
        body["orderExpiresAt"] = expiry.astimezone(timezone(timedelta(hours=8))).isoformat()
        status, replay = self.quote(BASES[1], body, key)
        self.assertEqual(status, 200)
        self.assertEqual(first, replay)
        time.sleep(max(0, (expiry - datetime.now(timezone.utc)).total_seconds()) + 0.1)
        status, expired = self.quote(MODEL, body, key)
        self.assertEqual(status, 200)
        self.assertEqual(first, expired)

    def test_container_policy_replacement_preserves_replay(self):
        body, key = quote_body(), str(uuid4())
        status, first = self.quote(BASES[0], body, key)
        self.assertEqual(status, 200)
        self.assertEqual(first["pricingMode"], "baseline")
        status, replay = self.quote(MODEL, body, key)
        self.assertEqual(status, 200)
        self.assertEqual(first, replay)
        status, new = self.quote(MODEL, body, str(uuid4()))
        self.assertEqual(status, 200)
        validate("QuoteResponse", new)
        self.assertEqual(new["pricingMode"], "model")
        self.assertNotEqual(first["releaseId"], new["releaseId"])
        self.assertTrue(5 <= new["recommendedCredits"] <= 30)
        status, rollback_replay = self.quote(BASES[1], body, key)
        self.assertEqual(status, 200)
        self.assertEqual(first, rollback_replay)

    def test_model_falls_back_without_order_context(self):
        status, response = self.quote(FALLBACK, quote_body(), str(uuid4()))
        self.assertEqual(status, 200)
        self.assertEqual(response["pricingMode"], "baseline")
        self.assertIn("order_context_unavailable", response["warnings"])

    def test_traffic_release_and_legacy_container_share_durable_replay(self):
        body, key = quote_body(), str(uuid4())
        status, current = self.quote(BASES[0], body, key)
        self.assertEqual(status, 200)
        self.assertIn("estimated_supply", current["warnings"])
        status, replay = self.quote(LEGACY, body, key)
        self.assertEqual(status, 200)
        self.assertEqual(replay, current)
        status, old = self.quote(LEGACY, body, str(uuid4()))
        self.assertEqual(status, 200)
        self.assertEqual(old["recommendedCredits"], 11)
        self.assertNotIn("estimated_supply", old["warnings"])
        self.assertGreater(current["recommendedCredits"], old["recommendedCredits"])

    def test_missing_catalogue_blocks_readiness_and_quotes(self):
        self.assertEqual(call(UNREADY, "/healthz")[0], 200)
        self.assertEqual(call(UNREADY, "/readyz")[0], 503)
        status, response = self.quote(UNREADY, quote_body(), str(uuid4()))
        self.assertEqual(status, 503)
        self.assertEqual(response["error"]["code"], "context_unavailable")

    def test_out_of_order_events_deduplicate_and_conflicts_are_atomic(self):
        event = {
            "eventId": str(uuid4()), "source": "order", "type": "QuoteDisplayed",
            "schemaVersion": 1, "occurredAt": datetime.now(timezone.utc).isoformat(),
            "data": {"quoteId": str(uuid4()), "draft": {"id": str(uuid4()), "revision": 1}},
        }
        path = "/internal/v1/training-events"
        status, result = call(BASES[0], path, {"events": [event, event]}, token=self.write_token)
        self.assertEqual((status, result), (200, {"accepted": 1, "duplicates": 1}))
        status, result = call(BASES[1], path, {"events": [event]}, token=self.write_token)
        self.assertEqual((status, result), (200, {"accepted": 0, "duplicates": 1}))
        conflicting = json.loads(json.dumps(event))
        conflicting["data"]["draft"]["revision"] = 2
        fresh = json.loads(json.dumps(event))
        fresh["eventId"] = str(uuid4())
        status, result = call(BASES[0], path, {"events": [fresh, conflicting]}, token=self.write_token)
        self.assertEqual(status, 409)
        self.assertEqual(result["error"]["code"], "event_conflict")
        recorded = self.export()
        self.assertEqual(sum(item["eventId"] == event["eventId"] for item in recorded), 1)
        self.assertFalse(any(item["eventId"] == fresh["eventId"] for item in recorded))

    def test_export_credential_cannot_write_and_write_credential_cannot_export(self):
        status, _ = call(BASES[0], "/internal/v1/training-events", {"events": []}, token=self.export_token)
        self.assertEqual(status, 401)
        status, _ = call(BASES[0], "/internal/v1/training-events", token=self.write_token)
        self.assertEqual(status, 401)


if __name__ == "__main__":
    unittest.main()
