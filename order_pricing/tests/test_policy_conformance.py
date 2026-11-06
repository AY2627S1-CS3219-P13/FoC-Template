"""Runs against the bundled test double or PRICING_PACKAGE_DIR's real loader."""

import copy
import importlib
import os
from pathlib import Path
import sys
import time
import unittest
from unittest.mock import patch

from pricing_contracts import UnsupportedRoute
from tests.support import (
    FIXTURES, decode_input, load_fixture, validate_package, validate_result, variant,
)


class PolicyConformanceTests(unittest.TestCase):
    def setUp(self):
        package = Path(os.environ.get("PRICING_PACKAGE_DIR", FIXTURES / "package")).resolve()
        self.manifest = validate_package(package)
        sys.path.insert(0, str(package))
        self.addCleanup(sys.path.remove, str(package))
        module, function = self.manifest["entryPoint"].split(":")
        self.policy = getattr(importlib.import_module(module), function)(package)
        self.addCleanup(self.policy.close)
        self.base = load_fixture("policy-input.json")
        self.policy.warm_up(decode_input(self.base))

    def calculate(self, value):
        original = copy.deepcopy(value)
        started = time.monotonic()
        # Calculation is offline. This guard catches common accidental HTTP paths;
        # container --network none provides the separate integration boundary.
        with patch("socket.socket", side_effect=AssertionError("policy attempted network IO")):
            result = self.policy.calculate(decode_input(value))
        self.assertLess(time.monotonic() - started, 1.0, "calculation exceeds initial budget")
        validate_result(result, self.manifest)
        self.assertEqual(value, original)
        return result

    def test_reproducible_calculation(self):
        first = self.calculate(self.base)
        self.assertEqual(first, self.calculate(self.base))

    def test_old_minimal_input_preserves_unknown_quantity(self):
        del self.base["request"]["totalItemQuantity"]
        result = self.calculate(self.base)
        self.assertIn("unknown_item_quantity", result.warnings)

    def test_future_minor_inputs_do_not_change_price(self):
        future = next(c for c in load_fixture("compatibility-cases.json") if c["name"] == "future-minor")
        first = self.calculate(self.base)
        second = self.calculate(variant(self.base, future))
        self.assertEqual(first.recommended_credits, second.recommended_credits)
        self.assertEqual(first.pricing_mode, second.pricing_mode)
        self.assertEqual(first.warnings, second.warnings)

    def test_order_context_fallback(self):
        for state, warning in (("stale", "stale_order_context"),
                               ("unavailable", "order_context_unavailable")):
            with self.subTest(state=state):
                value = copy.deepcopy(self.base)
                value["context"]["orderContext"]["state"] = state
                if state == "unavailable":
                    value["context"]["orderContext"] = {"state": state}
                    value["context"]["sources"] = [
                        s for s in value["context"]["sources"] if s["source"] != "orders"
                    ]
                else:
                    value["context"]["sources"][1]["asOf"] = "2026-11-05T16:58:00Z"
                result = self.calculate(value)
                self.assertEqual(result.pricing_mode, "baseline")
                self.assertIn(warning, result.warnings)

    def test_missing_route_is_explicit(self):
        self.base["context"]["routes"] = []
        with self.assertRaises(UnsupportedRoute):
            self.policy.calculate(decode_input(self.base))

    def test_draft_identity_is_not_a_price_feature(self):
        first = self.calculate(self.base)
        self.base["request"]["draft"] = {
            "id": "99999999-9999-4999-8999-999999999999", "revision": 99
        }
        self.assertEqual(first.recommended_credits, self.calculate(self.base).recommended_credits)

    def test_close_is_idempotent(self):
        self.policy.close()
        self.policy.close()


if __name__ == "__main__":
    unittest.main()
