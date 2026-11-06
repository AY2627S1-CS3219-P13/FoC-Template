import copy
import json
from pathlib import Path
import tempfile
import unittest

from formula.build import build
from formula.policy.formula import (
    DEFAULT_CONFIG, FormulaPolicy, load_policy, validate_config, validate_foot_traffic,
)
from tests.support import decode_input, load_fixture, validate_package

DATA = Path(__file__).resolve().parents[2] / "formula/data/foot_traffic.json"


class FootTrafficTests(unittest.TestCase):
    def setUp(self):
        self.data = json.loads(DATA.read_text())
        self.traffic = validate_foot_traffic(self.data)
        self.policy = FormulaPolicy(validate_config(DEFAULT_CONFIG), self.traffic)
        self.value = load_fixture("policy-input.json")

    def result(self, local_hour, pickup="com2", minute=0):
        self.value["calculatedAt"] = f"2026-11-05T{local_hour:02}:{minute:02}:00+08:00"
        self.value["resolvedPlaces"]["pickupLocationId"] = pickup
        self.value["context"]["routes"][0]["pickupLocationId"] = pickup
        return self.policy.calculate(decode_input(self.value))

    def test_lookup_covers_the_fifteen_current_catalogue_ids(self):
        expected = {
            "blk-as8", "com2", "com3", "central-library", "engineering-block-e3",
            "engineering-block-e4", "engineering-block-ea", "frontier",
            "hon-sui-sen-memorial-library", "medicine-science-library",
            "prince-george-s-park", "terrace", "the-ridge", "yusof-ishak-house", "innovation4-0",
        }
        self.assertEqual(set(self.traffic["locations"]), expected)
        self.assertEqual(self.traffic["locations"]["com3"], self.traffic["locations"]["terrace"])
        self.assertEqual(self.data["provenance"], "estimated")

    def test_requested_lunch_and_shoulder_factors_and_boundary(self):
        for hour, minute, factor in ((9, 59, .6), (10, 0, .8), (11, 59, .8),
                                     (12, 0, 1), (13, 59, 1), (14, 0, .8),
                                     (15, 59, .8), (16, 0, .7)):
            with self.subTest(hour=hour, minute=minute):
                self.assertEqual(self.result(hour, minute=minute).feature_snapshot["footTrafficTimeFactor"], factor)

    def test_singapore_time_not_replica_timezone(self):
        local = self.result(12)
        self.value["calculatedAt"] = "2026-11-05T04:00:00Z"
        utc = self.policy.calculate(decode_input(self.value))
        self.assertEqual(local, utc)
        self.assertEqual(utc.feature_snapshot["localHour"], 12)

    def test_exact_multiplier_and_prices(self):
        noon = self.result(12)
        self.assertEqual(noon.feature_snapshot["relativePeakFootTraffic"], 65)
        self.assertEqual(noon.feature_snapshot["effectiveRelativeFootTraffic"], .65)
        self.assertEqual(noon.feature_snapshot["trafficMultiplier"], 1.07)
        self.assertEqual(noon.recommended_credits, 12)
        self.assertEqual(self.result(1).recommended_credits, 13)
        self.assertEqual(self.result(12, pickup="frontier").feature_snapshot["trafficMultiplier"], 1)

    def test_lower_location_activity_and_quieter_time_do_not_reduce_reward(self):
        busy = self.result(12, pickup="frontier")
        quiet = self.result(12, pickup="innovation4-0")
        night = self.result(1, pickup="innovation4-0")
        self.assertGreaterEqual(quiet.recommended_credits, busy.recommended_credits)
        self.assertGreaterEqual(night.recommended_credits, quiet.recommended_credits)
        for value in (busy, quiet, night):
            self.assertTrue(1 <= value.feature_snapshot["trafficMultiplier"] <= 1.2)
            self.assertIn("estimated_supply", value.warnings)

    def test_unknown_future_location_is_neutral_not_zero_traffic(self):
        result = self.result(12, pickup="future-campus-location")
        self.assertEqual(result.feature_snapshot["trafficMultiplier"], 1)
        self.assertIsNone(result.feature_snapshot["relativePeakFootTraffic"])
        self.assertIn("unknown_foot_traffic", result.warnings)
        self.assertNotIn("estimated_supply", result.warnings)

    def test_effort_bounds_and_unknown_quantity_are_preserved(self):
        del self.value["request"]["totalItemQuantity"]
        result = self.result(1)
        self.assertIn("unknown_item_quantity", result.warnings)
        self.assertIsNone(result.feature_snapshot["handlingCredits"])
        self.value["context"]["routes"][0]["walkableMetres"] = 100000
        self.assertEqual(self.result(1).recommended_credits, 30)

    def test_bad_tables_and_premiums_fail_at_startup(self):
        for mutate in (
            lambda d: d["locations"].append(d["locations"][0]),
            lambda d: d["locations"][0].update(relativePeakIndex=101),
            lambda d: d["timeBands"][0].update(startHour=1),
            lambda d: d["timeBands"][0].update(factor=1.1),
            lambda d: d.update(provenance="observed"),
        ):
            data = copy.deepcopy(self.data)
            mutate(data)
            with self.assertRaises(ValueError):
                validate_foot_traffic(data)
        with self.assertRaises(ValueError):
            validate_config(dict(DEFAULT_CONFIG, footTrafficMaxPremium=.21))
        with self.assertRaises(ValueError):
            FormulaPolicy(validate_config(DEFAULT_CONFIG))

    def test_new_release_archives_lookup_and_old_configuration_stays_effort_only(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release"
            manifest = build(path, "a" * 40, "traffic-test")
            validate_package(path)
            self.assertIn("artifacts/foot_traffic.json", manifest["files"])
            self.assertEqual(load_policy(path).calculate(decode_input(self.value)).recommended_credits, 13)
        legacy = dict(DEFAULT_CONFIG)
        del legacy["footTrafficMaxPremium"]
        policy = FormulaPolicy(validate_config(legacy))
        self.assertEqual(policy.calculate(decode_input(self.value)).recommended_credits, 11)


if __name__ == "__main__":
    unittest.main()
