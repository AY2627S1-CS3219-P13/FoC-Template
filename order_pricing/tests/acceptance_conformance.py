"""Run explicitly against a real acceptance-gbm release package."""

import copy
import os

import tests.test_policy_conformance as conformance


class AcceptanceConformanceTests(conformance.PolicyConformanceTests):
    def setUp(self):
        if not os.environ.get("PRICING_PACKAGE_DIR"):
            raise RuntimeError("acceptance conformance requires a real PRICING_PACKAGE_DIR")
        super().setUp()

    def test_fresh_context_uses_model_with_probability_diagnostics(self):
        result = self.calculate(self.base)
        self.assertEqual(result.pricing_mode, "model")
        snapshot = result.feature_snapshot
        for key in ("acceptanceProbability", "acceptanceAtMinimum", "acceptanceAtMaximum"):
            self.assertTrue(0 <= snapshot[key] <= 1, key)
        # Monotone in price: the most expensive offer is never less attractive.
        self.assertGreaterEqual(snapshot["acceptanceAtMaximum"], snapshot["acceptanceAtMinimum"])
        self.assertEqual(snapshot["provenance"], "synthetic")

    def test_unknown_pickup_counts_mean_zero_only_in_fresh_snapshots(self):
        value = copy.deepcopy(self.base)
        value["context"]["orderContext"]["areas"] = []
        result = self.calculate(value)
        self.assertEqual(result.feature_snapshot["features"]["pickupOpenRequestCount"], 0.0)
