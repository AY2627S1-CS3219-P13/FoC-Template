import copy
import asyncio
from datetime import timedelta
import json
from pathlib import Path
import time
import threading
import unittest
from unittest.mock import patch
from uuid import uuid4

from fastapi.testclient import TestClient
from pricing_contracts import PolicyResult
from pricing_server.app import create_app
from pricing_server.config import Settings
from pricing_server.context import Context
from pricing_server.events import normalize_event
from pricing_server.wire import APIError, decode_input, fingerprint, json_loads, normalize_request, stamp, utcnow

FIXTURE = Path(__file__).resolve().parents[1] / "fixtures/policy-input.json"


class MemoryStorage:
    def __init__(self):
        self.quotes, self.events = {}, []
        self.fail = False

    def initialize(self):
        pass

    def healthy(self):
        return True

    def replay(self, caller, key, digest):
        existing = self.quotes.get((caller, key))
        if existing:
            if existing[0] != digest:
                raise APIError(409, "idempotency_conflict")
            return existing[1]

    def issue(self, caller, key, digest, received, calculate):
        result = self.replay(caller, key, digest)
        if result:
            return result
        response, event = calculate()
        if self.fail:
            raise RuntimeError("private database details")
        self.quotes[(caller, key)] = digest, response
        self.events.append(event)
        return response


class TestPolicy:
    def __init__(self):
        self.delay = 0
        self.price = 8
        self.closed = 0

    def calculate(self, value):
        time.sleep(self.delay)
        warnings = () if "totalItemQuantity" in value.request else ("unknown_item_quantity",)
        return PolicyResult(self.price, "baseline", warnings, "test-1", {})

    def close(self):
        self.closed += 1


def settings():
    return Settings("postgresql://unused/test", Path("/unused"), "quote-secret", "write-secret", "export-secret", "http://127.0.0.1:1", "supplier-secret", "http://127.0.0.1:1", "orders-secret", .1, .1, "synthetic")


class WireTests(unittest.TestCase):
    def setUp(self):
        self.value = json.loads(FIXTURE.read_text())

    def test_credentials_with_embedded_control_characters_fail_startup(self):
        env = {
            "PRICING_DATABASE_URL": "postgresql://unused/test",
            "PRICING_QUOTE_TOKEN": "quote-secret", "PRICING_ORDER_WRITE_TOKEN": "write-secret",
            "PRICING_EXPORT_TOKEN": "export-secret", "PRICING_SUPPLIER_TOKEN": "supplier-secret",
            "PRICING_ORDER_TOKEN": "order-secret", "PRICING_SUPPLIER_URL": "http://supplier",
            "PRICING_ORDER_URL": "http://orders",
        }
        for token in ("bad\r\nheader", "bad\tvalue", "bad\x00value"):
            # A real process environment rejects NUL before Settings can inspect
            # it; use a mapping double to exercise our startup validator itself.
            with self.subTest(token_type=repr(token)), patch("pricing_server.config.os.environ", dict(env, PRICING_SUPPLIER_TOKEN=token)):
                with self.assertRaises(ValueError):
                    Settings.from_env()

    def test_canonical_expiry_and_unknown_fields_have_identical_fingerprint(self):
        original = normalize_request(self.value["request"])
        self.value["request"]["orderExpiresAt"] = "2026-11-06T02:00:00+08:00"
        self.value["request"]["pickup"]["candidatePrice"] = 999
        self.assertEqual(fingerprint(original), fingerprint(normalize_request(self.value["request"])))

    def test_duplicate_keys_and_nonfinite_numbers_are_rejected(self):
        for raw in ('{"a":1,"a":2}', '{"x":NaN}', '{"a":{"x":1,"x":2}}'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                json_loads(raw)

    def test_immutable_unknown_quantity(self):
        del self.value["request"]["totalItemQuantity"]
        value = decode_input(self.value)
        self.assertNotIn("totalItemQuantity", value.request)
        with self.assertRaises(TypeError):
            value.context["routes"][0]["walkableMetres"] = 1

    def test_event_normalization_preserves_decimal_amount_and_ignores_unknown_data(self):
        event = {"eventId": str(uuid4()), "source": "order", "type": "OfferConfirmed", "schemaVersion": 1,
                 "occurredAt": "2026-11-06T02:00:00+08:00", "data": {"draft": self.value["request"]["draft"], "offeredCredits": "9223372036854775807", "quoteId": None, "privateText": "do not record"}}
        result = normalize_event(event)
        self.assertNotIn("privateText", result["data"])
        self.assertEqual(result["data"]["offeredCredits"], "9223372036854775807")
        self.assertEqual(result["occurredAt"], "2026-11-05T18:00:00Z")
        event["data"]["offeredCredits"] = "9223372036854775808"
        with self.assertRaises(APIError):
            normalize_event(event)

    def test_source_refresh_rejects_regression_future_duplicate_and_wrong_window(self):
        context = Context(settings(), {})
        now = utcnow()
        catalogue = dict(self.value["context"]["catalogue"], schemaVersion=1, snapshotId="cat", asOf=stamp(now))
        prior = context.validate_source("catalogue", catalogue, None, now)
        bad = dict(catalogue, asOf=stamp(now - timedelta(seconds=1)))
        with self.assertRaises(ValueError):
            context.validate_source("catalogue", bad, prior, now)
        with self.assertRaises(ValueError):
            context.validate_source("catalogue", dict(catalogue, asOf=stamp(now + timedelta(seconds=6))), None, now)
        bad = copy.deepcopy(catalogue)
        bad["locations"].append(bad["locations"][0])
        with self.assertRaises(ValueError):
            context.validate_source("catalogue", bad, None, now)


class FrameworkTests(unittest.TestCase):
    def setUp(self):
        self.value = json.loads(FIXTURE.read_text())
        self.policy, self.store = TestPolicy(), MemoryStorage()
        manifest = {"releaseId": "focused-test", "featureSchemaVersion": "test-1", "bounds": {"minimum": 5, "maximum": 30}}
        static = {key: self.value["context"][key] for key in ("routes", "footfall", "sources")}
        self.app = create_app(settings(), storage=self.store, package_loader=lambda _: (manifest, self.policy, static))
        self.client = self.enterContext(TestClient(self.app, raise_server_exceptions=False))
        now = utcnow()
        self.app.state.context.catalogue = dict(self.value["context"]["catalogue"], snapshotId="cat", asOf=stamp(now))
        self.body = self.value["request"]
        self.body["orderExpiresAt"] = stamp(now + timedelta(hours=1))
        self.key = str(uuid4())

    def post(self, body=None, **kwargs):
        return self.client.post("/internal/v1/price-quotes", json=self.body if body is None else body,
                                headers={"Authorization": "Bearer quote-secret", "Idempotency-Key": self.key}, **kwargs)

    def test_durable_replay_and_changed_input_conflict(self):
        first = self.post()
        self.assertEqual(first.status_code, 200, first.text)
        self.body["unknown"] = {"candidatePrice": 999}
        self.assertEqual(self.post().json(), first.json())
        self.assertEqual(len(self.store.events), 1)
        self.body["totalItemQuantity"] += 1
        self.assertEqual(self.post().status_code, 409)

    def test_expired_replay_works_without_source_availability(self):
        first = self.post().json()
        self.app.state.context.catalogue = None
        self.assertEqual(self.post().json(), first)

    def test_recording_failure_never_returns_success_and_retry_can_succeed(self):
        self.store.fail = True
        response = self.post()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(response.json()["error"]["code"], "quote_recording_unavailable")
        self.assertNotIn("private database", response.text)
        self.assertEqual(self.store.quotes, {})
        self.store.fail = False
        self.assertEqual(self.post().status_code, 200)

    def test_invalid_policy_result_and_slow_calculation_are_unavailable(self):
        self.policy.price = 31
        self.assertEqual(self.post().json()["error"]["code"], "quote_unavailable")
        self.policy.price, self.policy.delay = 8, 1.05
        self.assertEqual(self.post().json()["error"]["code"], "quote_unavailable")
        self.assertEqual(self.store.events, [])

    def test_stale_catalogue_fails_closed(self):
        self.app.state.context.catalogue["asOf"] = stamp(utcnow() - timedelta(minutes=11))
        self.assertEqual(self.post().json()["error"]["code"], "context_unavailable")

    def test_timeout_keeps_inference_slot_until_worker_finishes(self):
        self.policy.delay = 2.0
        self.assertEqual(self.post().status_code, 503)
        self.assertEqual(self.post().json()["error"]["code"], "quote_pending")
        self.key = str(uuid4())
        self.assertEqual(self.post().status_code, 429)
        self.policy.delay = 0
        time.sleep(.6)
        self.assertEqual(self.post().status_code, 200)
        self.assertEqual(len(self.store.events), 1)

    def test_unauthorized_and_unknown_query(self):
        response = self.client.post("/internal/v1/price-quotes", json=self.body)
        self.assertEqual(response.status_code, 401)
        self.assertEqual(self.post(params={"unsupported": "1"}).status_code, 400)


class WorkerAdmissionTests(unittest.IsolatedAsyncioTestCase):
    async def test_cancelled_requests_do_not_release_live_db_worker_slots(self):
        fixture = json.loads(FIXTURE.read_text())
        static = {key: fixture["context"][key] for key in ("routes", "footfall", "sources")}
        app = create_app(settings(), storage=MemoryStorage(), package_loader=lambda _: ({}, TestPolicy(), static))
        async with app.router.lifespan_context(app):
            await asyncio.sleep(.05)
            gate = threading.Event()
            jobs = [asyncio.create_task(app.state.run_thread(gate.wait)) for _ in range(8)]
            try:
                await asyncio.sleep(.05)
                self.assertEqual(app.state.thread_count, 8)
                with self.assertRaises(APIError) as error:
                    await app.state.run_thread(lambda: None)
                self.assertEqual(error.exception.status, 429)
                jobs[0].cancel()
                await asyncio.sleep(.01)
                self.assertEqual(app.state.thread_count, 8)
            finally:
                gate.set()
                await asyncio.gather(*jobs, return_exceptions=True)
                await asyncio.sleep(.01)
            self.assertEqual(app.state.thread_count, 0)


if __name__ == "__main__":
    unittest.main()
