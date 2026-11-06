"""Run explicitly against a real formula; not satisfied by the bundled double."""

import copy
import os

import tests.test_policy_conformance as conformance


class FormulaConformanceTests(conformance.PolicyConformanceTests):
    def setUp(self):
        if not os.environ.get("PRICING_PACKAGE_DIR"):
            raise RuntimeError("formula conformance requires a real PRICING_PACKAGE_DIR")
        super().setUp()

    def test_relative_distance_effort_is_monotonic_and_nonconstant(self):
        prices = []
        for distance in (100, 900, 1800, 3600):
            value = copy.deepcopy(self.base)
            value["context"]["routes"][0]["walkableMetres"] = distance
            prices.append(self.calculate(value).recommended_credits)
        self.assertEqual(prices, sorted(prices))
        self.assertGreater(prices[-1], prices[0], "a constant-price stub is not an effort formula")

    def test_quantity_effort_cannot_reduce_price(self):
        prices = []
        for quantity in (1, 2, 6, 20):
            value = copy.deepcopy(self.base)
            value["request"]["totalItemQuantity"] = quantity
            prices.append(self.calculate(value).recommended_credits)
        self.assertEqual(prices, sorted(prices))

    def test_legacy_people_counts_do_not_override_release_owned_traffic_policy(self):
        original = self.calculate(self.base).recommended_credits
        for footfall in (0, 1000, 10000):
            value = copy.deepcopy(self.base)
            value["context"]["footfall"][0]["estimatedPeople"] = footfall
            result = self.calculate(value)
            self.assertEqual(result.recommended_credits, original)
            self.assertEqual(result.pricing_mode, "baseline")

    def test_quieter_time_has_bounded_premium(self):
        noon = copy.deepcopy(self.base)
        noon["calculatedAt"] = "2026-11-05T04:00:00Z"  # 12:00 Singapore
        night = copy.deepcopy(self.base)
        night["calculatedAt"] = "2026-11-05T17:00:00Z"  # 01:00 Singapore
        first, second = self.calculate(noon), self.calculate(night)
        self.assertGreaterEqual(second.recommended_credits, first.recommended_credits)
        self.assertLessEqual(second.feature_snapshot["trafficMultiplier"], 1.2)
        self.assertIn("estimated_supply", second.warnings)
