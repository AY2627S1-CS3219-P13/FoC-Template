import json
from pathlib import Path
import tempfile
import unittest

from formula.build import build
from formula.policy.formula import DEFAULT_CONFIG, FormulaPolicy, load_policy, validate_config
from pricing_contracts import UnsupportedRoute
from tests.support import LATEST, decode_input, load_fixture, validate_package


class FormulaTests(unittest.TestCase):
    def setUp(self):
        self.value = load_fixture("policy-input.json")
        # Preserve regression coverage of the earlier effort-only equation.
        self.config = validate_config(dict(DEFAULT_CONFIG, footTrafficMaxPremium=0))
        self.policy = FormulaPolicy(self.config)

    def result(self, *, distance=1800, quantity=2):
        self.value["context"]["routes"][0]["walkableMetres"] = distance
        if quantity is None:
            self.value["request"].pop("totalItemQuantity", None)
        else:
            self.value["request"]["totalItemQuantity"] = quantity
        return self.policy.calculate(decode_input(self.value))

    def test_representative_prices_and_exact_components(self):
        cases = ((150, 1, 5), (900, 2, 8), (1800, 2, 11), (3600, 7, 19), (9000, 20, 30))
        for distance, quantity, expected in cases:
            with self.subTest(distance=distance, quantity=quantity):
                result = self.result(distance=distance, quantity=quantity)
                self.assertEqual(result.recommended_credits, expected)
        features = self.result().feature_snapshot
        self.assertEqual(features["travelMinutes"], 24)
        self.assertEqual(features["totalEffortMinutes"], 32)
        self.assertEqual(features["effortCredits"], 8)
        self.assertEqual(features["handlingCredits"], 0.5)
        self.assertEqual(features["unroundedCredits"], 10.5)

    def test_distance_is_only_charged_through_travel_time(self):
        self.result(distance=0, quantity=1)
        initial = self.policy.calculate(decode_input(self.value)).feature_snapshot
        changed = self.result(distance=300, quantity=1).feature_snapshot
        self.assertEqual(changed["effortCredits"] - initial["effortCredits"], 1)
        self.assertEqual(changed["unroundedCredits"] - initial["unroundedCredits"], 1)

    def test_quantity_is_a_capped_carrying_adjustment_not_extra_time(self):
        initial = self.result(quantity=1).feature_snapshot
        changed = self.result(quantity=7).feature_snapshot
        self.assertEqual(changed["handlingCredits"], 3)
        self.assertEqual(changed["totalEffortMinutes"], initial["totalEffortMinutes"])
        self.assertEqual(self.result(quantity=9007199254740991).feature_snapshot["handlingCredits"], 3)

    def test_round_up_then_bound(self):
        self.assertEqual(self.result(distance=0, quantity=1).recommended_credits, 5)
        self.assertEqual(self.result(distance=1800, quantity=2).recommended_credits, 11)
        self.assertEqual(self.result(distance=100000, quantity=1).recommended_credits, 30)

    def test_unknown_quantity_preserves_unknown_feature_values(self):
        result = self.result(quantity=None)
        self.assertFalse(result.feature_snapshot["quantityKnown"])
        for key in ("totalItemQuantity", "additionalItemCount", "handlingCredits"):
            self.assertIsNone(result.feature_snapshot[key])
        self.assertIn("unknown_item_quantity", result.warnings)

    def test_footfall_and_order_counts_do_not_change_initial_effort(self):
        first = self.result().recommended_credits
        self.value["context"]["footfall"][0]["estimatedPeople"] = 100000
        self.value["context"]["orderContext"]["areas"][0]["openRequestCount"] = 100000
        self.assertEqual(self.policy.calculate(decode_input(self.value)).recommended_credits, first)

    def test_diagnostics_are_immutable_and_provenance_is_explicit(self):
        result = self.result()
        self.assertEqual(result.feature_snapshot["effortProvenance"], "estimated")
        self.assertEqual(result.feature_snapshot["routeProvenance"], "synthetic")
        with self.assertRaises(TypeError):
            result.feature_snapshot["travelMinutes"] = 0

    def test_invalid_coefficients_fail_before_readiness(self):
        for key, value in (("walkingMetresPerMinute", 0), ("creditsPerHour", -1),
                           ("baseCredits", True), ("maxExtraItems", 2.0),
                           ("pickupMinutes", None), ("baseCredits", float("nan")),
                           ("maximumCredits", 31)):
            with self.subTest(key=key, value=value):
                config = dict(DEFAULT_CONFIG, **{key: value})
                with self.assertRaises(ValueError):
                    validate_config(config)

    def test_unknown_optional_configuration_does_not_alter_policy(self):
        future = dict(DEFAULT_CONFIG, footTrafficMaxPremium=0, futureSurge=100)
        self.assertEqual(validate_config(future), self.config)

    def test_missing_route_is_not_a_minimum_price(self):
        self.value["context"]["routes"] = []
        with self.assertRaises(UnsupportedRoute):
            self.policy.calculate(decode_input(self.value))


class CoordinateRouteTests(unittest.TestCase):
    """Catalogue coordinates cover pairs that have no reference route."""

    def setUp(self):
        self.value = load_fixture("policy-input.json")
        self.value["context"]["catalogue"]["locations"] = [
            {"id": "com2", "latitude": 1.293925, "longitude": 103.774153},
            {"id": "central-library", "latitude": 1.296558, "longitude": 103.773111},
            {"id": "utown"},
        ]
        self.value["request"]["delivery"]["id"] = "central-library"
        self.value["resolvedPlaces"]["deliveryLocationId"] = "central-library"
        self.config = validate_config(dict(DEFAULT_CONFIG, footTrafficMaxPremium=0))

    def calculate(self, config=None):
        return FormulaPolicy(config or self.config).calculate(decode_input(self.value, LATEST))

    def test_unlisted_pair_uses_haversine_with_detour(self):
        result = self.calculate()
        features = result.feature_snapshot
        self.assertEqual(features["routeMethod"], "coordinates")
        self.assertEqual(features["routeProvenance"], "estimated")
        self.assertAlmostEqual(features["straightLineMetres"], 314.9, places=1)
        self.assertAlmostEqual(features["walkableMetres"], 314.9 * 1.3, places=1)
        # 2 + 15 * (5 + 3 + 409.37 / 75) / 60 + 0.5 = 5.86 -> 6 credits.
        self.assertEqual(result.recommended_credits, 6)
        self.assertEqual(result.feature_schema_version, "effort-1")

    def test_farther_pairs_cost_more_and_reverse_is_symmetric(self):
        near = self.calculate().recommended_credits
        self.value["context"]["catalogue"]["locations"][1].update(latitude=1.299434, longitude=103.769064)
        far = self.calculate()
        self.assertGreater(far.recommended_credits, near)
        self.value["resolvedPlaces"] = {"pickupLocationId": "central-library", "deliveryLocationId": "com2"}
        self.assertEqual(self.calculate().feature_snapshot["walkableMetres"], far.feature_snapshot["walkableMetres"])

    def test_reference_route_takes_precedence(self):
        self.value["context"]["routes"].append(
            {"pickupLocationId": "com2", "deliveryLocationId": "central-library", "walkableMetres": 900})
        features = self.calculate().feature_snapshot
        self.assertEqual(features["routeMethod"], "static")
        self.assertIsNone(features["straightLineMetres"])
        self.assertEqual(features["walkableMetres"], 900)

    def test_missing_coordinates_or_disabled_detour_is_unsupported(self):
        with self.assertRaises(UnsupportedRoute):
            self.calculate(validate_config(dict(DEFAULT_CONFIG, footTrafficMaxPremium=0, walkingDetourFactor=0)))
        old_release = {key: value for key, value in DEFAULT_CONFIG.items() if key not in ("walkingDetourFactor", "footTrafficMaxPremium")}
        with self.assertRaises(UnsupportedRoute):
            self.calculate(validate_config(old_release))
        self.value["resolvedPlaces"]["deliveryLocationId"] = "utown"
        self.value["context"]["routes"] = []
        with self.assertRaises(UnsupportedRoute):
            self.calculate()

    def test_old_schema_reader_drops_coordinates(self):
        # A v1/v1.1 framework cannot supply coordinates, so no estimate is made.
        with self.assertRaises(UnsupportedRoute):
            FormulaPolicy(self.config).calculate(decode_input(self.value))

    def test_detour_factor_bounds(self):
        for factor in (0.5, 2.5, -1, float("nan")):
            with self.subTest(factor=factor), self.assertRaises(ValueError):
                validate_config(dict(DEFAULT_CONFIG, walkingDetourFactor=factor))


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.output = Path(self.temp.name) / "release"
        self.commit = "a" * 40

    def test_export_is_reproducible_complete_and_loadable(self):
        first = build(self.output, self.commit, "formula-test-001")
        second_path = self.output.parent / "second"
        second = build(second_path, self.commit, "formula-test-001")
        self.assertEqual(first, second)
        self.assertEqual(validate_package(self.output), first)
        policy = load_policy(self.output)
        value = decode_input(load_fixture("policy-input.json"))
        policy.warm_up(value)
        self.assertEqual(policy.calculate(value).recommended_credits, 13)
        policy.close()
        policy.close()
        self.assertIn("coordinates.v1", first["requiredCapabilities"])
        self.assertEqual(first["featureSchemaVersion"], "effort-traffic-coordinates-1")

    def test_static_route_release_keeps_original_manifest(self):
        config = dict(DEFAULT_CONFIG, walkingDetourFactor=0)
        manifest = build(self.output, self.commit, "formula-test-static", config=config)
        self.assertNotIn("coordinates.v1", manifest["requiredCapabilities"])
        self.assertEqual(manifest["featureSchemaVersion"], "effort-traffic-1")

    def test_existing_release_is_never_overwritten(self):
        build(self.output, self.commit, "formula-test-001")
        before = (self.output / "manifest.json").read_bytes()
        with self.assertRaises(ValueError):
            build(self.output, self.commit, "formula-test-002")
        self.assertEqual((self.output / "manifest.json").read_bytes(), before)

    def test_invalid_release_identity_and_commit_are_rejected(self):
        for commit, release in (("short", "test"), (self.commit, "")):
            with self.assertRaises(ValueError):
                build(self.output, commit, release)
        self.assertFalse(self.output.exists())

    def test_malformed_static_assets_fail_at_load(self):
        build(self.output, self.commit, "formula-test-001")
        path = self.output / "artifacts" / "context.json"
        context = json.loads(path.read_text())
        context["routes"][0]["walkableMetres"] = -1
        path.write_text(json.dumps(context))
        with self.assertRaises(ValueError):
            load_policy(self.output)

    def test_duplicate_asset_keys_are_rejected(self):
        build(self.output, self.commit, "formula-test-001")
        path = self.output / "artifacts" / "config.json"
        path.write_text(path.read_text().rstrip()[:-1] + ',"baseCredits":100}')
        with self.assertRaises(ValueError):
            load_policy(self.output)


if __name__ == "__main__":
    unittest.main()
