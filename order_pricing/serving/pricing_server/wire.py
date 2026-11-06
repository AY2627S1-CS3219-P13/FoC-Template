"""Production JSON boundary, canonicalization and immutable adapter values."""

import hashlib
import json
from collections.abc import Mapping
from datetime import datetime, timezone
from pathlib import Path
from types import MappingProxyType
from uuid import UUID

from jsonschema import Draft202012Validator, FormatChecker, validators
from pricing_contracts import PolicyInput, PolicyResult

SCHEMA = json.loads((Path(__file__).parents[2] / "contracts/schemas/v1.json").read_text()) if (Path(__file__).parents[2] / "contracts").exists() else json.loads(Path("/app/contracts/schemas/v1.json").read_text())
StrictValidator = validators.extend(
    Draft202012Validator,
    type_checker=Draft202012Validator.TYPE_CHECKER.redefine(
        "integer", lambda checker, value: type(value) is int
    ),
)
VALIDATORS = {
    name: StrictValidator({"$ref": f"#/$defs/{name}", "$defs": SCHEMA["$defs"]}, format_checker=FormatChecker())
    for name in SCHEMA["$defs"]
}


class APIError(Exception):
    def __init__(self, status, code):
        self.status, self.code = status, code


def utcnow():
    return datetime.now(timezone.utc)


def stamp(value):
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def instant(value):
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp requires offset")
    return parsed.astimezone(timezone.utc)


def json_loads(value):
    def pairs(items):
        result = {}
        for key, item in items:
            if key in result:
                raise ValueError("duplicate key")
            result[key] = item
        return result
    def invalid_constant(value):
        raise ValueError("non-finite number")
    return json.loads(value, object_pairs_hook=pairs, parse_constant=invalid_constant)


def validate(name, value):
    VALIDATORS[name].validate(value)
    json.dumps(value, allow_nan=False)


def project(value, schema):
    while "$ref" in schema:
        schema = SCHEMA["$defs"][schema["$ref"].split("/")[-1]]
    if isinstance(value, dict) and "properties" in schema:
        return {key: project(item, schema["properties"][key]) for key, item in value.items() if key in schema["properties"]}
    if isinstance(value, list):
        return [project(item, schema.get("items", {})) for item in value]
    return value


def normalize(name, value):
    validate(name, value)
    return project(value, SCHEMA["$defs"][name])


def normalize_request(value):
    result = normalize("QuoteRequest", value)
    result["orderExpiresAt"] = stamp(instant(result["orderExpiresAt"]))
    if result["pickup"] == result["delivery"]:
        raise ValueError("identical places")
    return result


def fingerprint(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()).hexdigest()


def freeze(value):
    if isinstance(value, dict):
        return MappingProxyType({key: freeze(item) for key, item in value.items()})
    if isinstance(value, list):
        return tuple(freeze(item) for item in value)
    return value


def thaw(value):
    if isinstance(value, Mapping):
        return {key: thaw(item) for key, item in value.items()}
    if isinstance(value, (tuple, list)):
        return [thaw(item) for item in value]
    return value


def decode_input(value):
    value = normalize("PolicyInput", value)
    return PolicyInput(value["contractVersion"], value["calculatedAt"], freeze(value["request"]), freeze(value["resolvedPlaces"]), freeze(value["context"]))


def result_json(result, manifest):
    if not isinstance(result, PolicyResult) or not isinstance(result.warnings, tuple):
        raise ValueError("invalid policy result")
    value = {
        "recommendedCredits": result.recommended_credits,
        "pricingMode": result.pricing_mode,
        "warnings": list(result.warnings),
        "featureSchemaVersion": result.feature_schema_version,
        "featureSnapshot": thaw(result.feature_snapshot),
    }
    validate("PolicyResult", value)
    if not manifest["bounds"]["minimum"] <= result.recommended_credits <= manifest["bounds"]["maximum"]:
        raise ValueError("invalid recommendation")
    if result.feature_schema_version != manifest["featureSchemaVersion"]:
        raise ValueError("feature schema mismatch")
    return value


def uuid(value):
    if not isinstance(value, str) or str(UUID(value)) != value:
        raise ValueError("invalid UUID")
    return value
