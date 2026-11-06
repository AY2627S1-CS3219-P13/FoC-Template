"""Run explicitly against a ready test app with the shared source fixtures."""

from datetime import datetime, timedelta, timezone
import json
import os
import unittest
from urllib.error import HTTPError
from urllib.request import Request, urlopen
from uuid import uuid4

from tests.support import load_fixture, validate


class FrameworkConformanceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base_url = os.environ.get("PRICING_BASE_URL", "").rstrip("/")
        cls.credential = os.environ.get("PRICING_SERVICE_TOKEN", "")
        if not cls.base_url or not cls.credential:
            raise RuntimeError("explicit test URL and service credential required")

    def setUp(self):
        self.body = load_fixture("policy-input.json")["request"]
        self.body["draft"]["id"] = str(uuid4())
        self.body["orderExpiresAt"] = (
            datetime.now(timezone.utc) + timedelta(hours=1)
        ).isoformat().replace("+00:00", "Z")
        self.key = str(uuid4())

    def request(self, body=None, *, key=None, token=None,
                content_type="application/json", path="/internal/v1/price-quotes"):
        headers = {"Authorization": f"Bearer {self.credential if token is None else token}"}
        data = None
        if body is not None:
            headers.update({"Content-Type": content_type, "Idempotency-Key": key or self.key})
            data = body if isinstance(body, bytes) else json.dumps(body).encode()
        request = Request(self.base_url + path, data=data, headers=headers)
        try:
            with urlopen(request, timeout=2) as response:
                return response.status, json.load(response)
        except HTTPError as error:
            return error.code, json.load(error)

    def assert_error(self, status, code, body, **kwargs):
        actual, response = self.request(body, **kwargs)
        self.assertEqual(actual, status)
        self.assertEqual(response["error"]["code"], code)
        self.assertIsInstance(response["error"]["message"], str)
        self.assertNotIn(self.credential, json.dumps(response))

    def test_health_and_readiness(self):
        for path in ("/healthz", "/readyz"):
            with self.subTest(path=path):
                status, response = self.request(path=path, token="")
                self.assertEqual(status, 200)
                self.assertEqual(response, {"status": "ok"})

    def test_full_quote_and_replay(self):
        status, first = self.request(self.body)
        self.assertEqual(status, 200)
        validate("QuoteResponse", first)
        self.assertEqual(first["draft"], self.body["draft"])
        self.assertTrue(5 <= first["recommendedCredits"] <= 30)
        self.assertNotIn("featureSnapshot", first)
        quoted = datetime.fromisoformat(first["quotedAt"])
        valid_until = datetime.fromisoformat(first["validUntil"])
        expiry = datetime.fromisoformat(self.body["orderExpiresAt"])
        self.assertEqual(valid_until, min(quoted + timedelta(seconds=120), expiry))
        status, replay = self.request(self.body)
        self.assertEqual(status, 200)
        self.assertEqual(replay, first)

    def test_omitted_quantity_is_supported(self):
        del self.body["totalItemQuantity"]
        status, response = self.request(self.body)
        self.assertEqual(status, 200)
        validate("QuoteResponse", response)
        self.assertIn("unknown_item_quantity", response["warnings"])

    def test_unknown_nested_fields_do_not_change_replay_fingerprint(self):
        status, first = self.request(self.body)
        self.assertEqual(status, 200)
        self.body["futureFeature"] = {"candidatePrice": 999}
        self.body["pickup"]["newField"] = "ignored"
        self.body["draft"]["newField"] = "ignored"
        status, replay = self.request(self.body)
        self.assertEqual(status, 200)
        self.assertEqual(replay, first)

    def test_changed_known_input_conflicts(self):
        status, _ = self.request(self.body)
        self.assertEqual(status, 200)
        self.body["totalItemQuantity"] += 1
        self.assert_error(409, "idempotency_conflict", self.body)

    def test_missing_and_incorrect_credentials(self):
        for token in ("", "incorrect-test-credential"):
            with self.subTest(token_present=bool(token)):
                self.assert_error(401, "invalid_service_credential", self.body, token=token)

    def test_invalid_known_fields(self):
        for quantity in (None, 0, True, 2.0, 9007199254740992):
            with self.subTest(quantity=quantity):
                self.body["totalItemQuantity"] = quantity
                self.assert_error(400, "invalid_request", self.body, key=str(uuid4()))

    def test_duplicate_keys_and_media_type(self):
        duplicate = json.dumps(self.body)[:-1].encode() + b', "totalItemQuantity": 3}'
        self.assert_error(400, "invalid_request", duplicate)
        self.assert_error(415, "unsupported_media_type", self.body, content_type="text/plain")

    def test_size_limit(self):
        self.body["ignoredPadding"] = "x" * (17 * 1024)
        self.assert_error(413, "request_too_large", self.body)

    def test_invalid_place_pair_and_expiry(self):
        self.body["delivery"] = dict(self.body["pickup"])
        self.assert_error(400, "invalid_request", self.body)
        self.body = load_fixture("policy-input.json")["request"]
        self.body["orderExpiresAt"] = "2000-01-01T00:00:00Z"
        self.assert_error(400, "invalid_request", self.body, key=str(uuid4()))


if __name__ == "__main__":
    unittest.main()
