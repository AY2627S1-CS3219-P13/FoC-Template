import copy
import dataclasses
import unittest

from jsonschema import ValidationError

from pricing_contracts import PolicyResult
from tests.support import (
    SCHEMA, decode_input, load_fixture, project, validate, validate_result, variant,
)


class SharedContractTests(unittest.TestCase):
    def setUp(self):
        self.base = load_fixture("policy-input.json")

    def test_schema_is_valid(self):
        from jsonschema import Draft202012Validator
        Draft202012Validator.check_schema(SCHEMA)

    def test_all_versioned_input_cases(self):
        for case in load_fixture("compatibility-cases.json"):
            with self.subTest(case=case["name"]):
                value = variant(self.base, case)
                if case["valid"]:
                    decode_input(value)
                else:
                    with self.assertRaises(ValidationError):
                        decode_input(value)

    def test_http_and_policy_boundaries_are_distinct(self):
        validate("QuoteRequest", self.base["request"])
        with self.assertRaises(ValidationError):
            validate("PolicyInput", self.base["request"])
        result = {
            "recommendedCredits": 8, "pricingMode": "baseline", "warnings": [],
            "featureSchemaVersion": "example-1", "featureSnapshot": {},
        }
        validate("PolicyResult", result)
        with self.assertRaises(ValidationError):
            validate("QuoteResponse", result)
        validate("QuoteResponse", load_fixture("quote-response.json"))

    def test_old_reader_ignores_future_nested_fields(self):
        case = next(c for c in load_fixture("compatibility-cases.json") if c["name"] == "future-minor")
        future = variant(self.base, case)
        original = copy.deepcopy(future)
        decoded = decode_input(future)
        self.assertEqual(decoded.request, decode_input(self.base).request)
        self.assertNotIn("futureForecast", decoded.context)
        self.assertNotIn("experimentalMinutes", decoded.context["routes"][0])
        self.assertNotIn("potentialDemand", decoded.context["orderContext"]["areas"][0])
        self.assertNotIn("debug", decoded.context["sources"][0])
        self.assertEqual(future, original)

    def test_new_reader_accepts_old_input_without_defaulting_unknown_to_zero(self):
        newer = copy.deepcopy(SCHEMA)
        newer["$defs"]["PolicyInput"]["properties"]["optionalForecast"] = {
            "type": "number", "description": "Omitted means unknown; never zero."
        }
        old = copy.deepcopy(self.base)
        del old["request"]["totalItemQuantity"]
        validate("PolicyInput", old, schema=newer)
        self.assertNotIn("totalItemQuantity", decode_input(old).request)
        self.assertNotIn("optionalForecast", old)

    def test_dtos_are_recursively_immutable(self):
        value = decode_input(self.base)
        with self.assertRaises(dataclasses.FrozenInstanceError):
            value.calculated_at = "changed"
        with self.assertRaises(TypeError):
            value.request["totalItemQuantity"] = 99
        with self.assertRaises(TypeError):
            value.context["catalogue"]["locations"][0]["id"] = "changed"

    def test_release_owned_diagnostics_are_not_stripped(self):
        result = {
            "recommendedCredits": 8, "pricingMode": "baseline", "warnings": ["future_warning"],
            "featureSchemaVersion": "private-9", "featureSnapshot": {"new_model_feature": 99},
            "futureOuterField": "ignored",
        }
        validate("PolicyResult", result)
        normalized = project(result, SCHEMA["$defs"]["PolicyResult"])
        self.assertEqual(normalized["featureSnapshot"], {"new_model_feature": 99})
        self.assertNotIn("futureOuterField", normalized)

    def test_public_future_mode_is_displayable_but_internal_enum_is_closed(self):
        response = load_fixture("quote-response.json")
        response["pricingMode"] = "future-mode"
        response["warnings"] = ["future-warning"]
        validate("QuoteResponse", response)
        result = {
            "recommendedCredits": 8, "pricingMode": "future-mode", "warnings": [],
            "featureSchemaVersion": "v1", "featureSnapshot": {},
        }
        with self.assertRaises(ValidationError):
            validate("PolicyResult", result)

    def test_framework_must_reject_invalid_policy_outputs(self):
        manifest = {"bounds": {"minimum": 5, "maximum": 30}, "featureSchemaVersion": "v1"}
        for price in (0, 4, 31, 9007199254740992, True, 8.0, "8"):
            with self.subTest(price=price):
                with self.assertRaises((ValueError, ValidationError)):
                    validate_result(PolicyResult(price, "baseline", (), "v1", {}), manifest)
        validate_result(PolicyResult(5, "baseline", (), "v1", {}), manifest)
        validate_result(PolicyResult(30, "baseline", (), "v1", {}), manifest)
        with self.assertRaises(ValueError):
            validate_result(PolicyResult(8, "model", (), "other", {}), manifest)


if __name__ == "__main__":
    unittest.main()
