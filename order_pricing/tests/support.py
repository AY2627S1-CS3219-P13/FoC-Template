"""Reusable test helpers, not a production HTTP decoder or package loader."""

import copy
import hashlib
import json
import re
from collections.abc import Mapping
from pathlib import Path, PurePosixPath
from types import MappingProxyType

from jsonschema import Draft202012Validator, FormatChecker, validators

from pricing_contracts import PolicyInput

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "tests" / "fixtures"
SCHEMA = json.loads((ROOT / "contracts" / "schemas" / "v1.json").read_text())
CAPABILITIES = {"catalogue.v1", "orders.v1", "routes.v1", "footfall.v1"}

# JSON Schema's default integer checker also accepts 2.0; our transport does not.
StrictValidator = validators.extend(
    Draft202012Validator,
    type_checker=Draft202012Validator.TYPE_CHECKER.redefine(
        "integer", lambda checker, value: type(value) is int
    ),
)


def load_fixture(name):
    return json.loads((FIXTURES / name).read_text())


def validate(name, value, schema=SCHEMA):
    StrictValidator(
        {"$ref": f"#/$defs/{name}", "$defs": schema["$defs"]},
        format_checker=FormatChecker(),
    ).validate(value)


def resolve(schema):
    while "$ref" in schema:
        schema = SCHEMA["$defs"][schema["$ref"].split("/")[-1]]
    return schema


def project(value, schema):
    """Ignore unknown fields; preserve open, release-owned JSON diagnostics."""
    schema = resolve(schema)
    if isinstance(value, dict) and "properties" in schema:
        return {
            key: project(item, schema["properties"][key])
            for key, item in value.items()
            if key in schema["properties"]
        }
    if isinstance(value, list):
        return [project(item, schema.get("items", {})) for item in value]
    return copy.deepcopy(value)


def freeze(value):
    if isinstance(value, dict):
        return MappingProxyType({key: freeze(item) for key, item in value.items()})
    if isinstance(value, list):
        return tuple(freeze(item) for item in value)
    return value


def decode_input(value):
    validate("PolicyInput", value)
    value = project(value, SCHEMA["$defs"]["PolicyInput"])
    return PolicyInput(
        value["contractVersion"], value["calculatedAt"], freeze(value["request"]),
        freeze(value["resolvedPlaces"]), freeze(value["context"]),
    )


def thaw(value):
    if isinstance(value, Mapping):
        return {key: thaw(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [thaw(item) for item in value]
    return value


def validate_result(result, manifest):
    from pricing_contracts import PolicyResult

    if not isinstance(result, PolicyResult):
        raise ValueError("calculate must return PolicyResult")
    validate("PolicyResult", {
        "recommendedCredits": result.recommended_credits,
        "pricingMode": result.pricing_mode,
        "warnings": list(result.warnings),
        "featureSchemaVersion": result.feature_schema_version,
        "featureSnapshot": thaw(result.feature_snapshot),
    })
    bounds = manifest["bounds"]
    if not bounds["minimum"] <= result.recommended_credits <= bounds["maximum"]:
        raise ValueError("recommendation outside release bounds")
    if result.feature_schema_version != manifest["featureSchemaVersion"]:
        raise ValueError("feature schema does not match release")


def variant(base, case):
    """Apply fixture set/remove operations using simple JSON pointer paths."""
    value = copy.deepcopy(base)
    for operation in ("set", "remove"):
        changes = case.get(operation, {} if operation == "set" else [])
        for path in changes:
            parts = path.strip("/").split("/")
            target = value
            for part in parts[:-1]:
                target = target[int(part)] if isinstance(target, list) else target[part]
            key = int(parts[-1]) if isinstance(target, list) else parts[-1]
            if operation == "set":
                target[key] = copy.deepcopy(changes[path])
            else:
                del target[key]
    return value


def validate_package(package_dir):
    """Verify the export gate without executing package code."""
    package_dir = Path(package_dir).resolve()
    manifest_path = package_dir / "manifest.json"
    if manifest_path.is_symlink():
        raise ValueError("symlinks are forbidden")
    manifest = json.loads(manifest_path.read_text())
    validate("ReleaseManifest", manifest)
    if not set(manifest["requiredCapabilities"]) <= CAPABILITIES:
        raise ValueError("unsupported capability")
    if manifest["bounds"]["minimum"] > manifest["bounds"]["maximum"]:
        raise ValueError("invalid bounds")
    for name in manifest["files"]:
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or "\\" in name:
            raise ValueError("unsafe artifact path")
    actual = set()
    for path in package_dir.rglob("*"):
        if path.is_symlink():
            raise ValueError("symlinks are forbidden")
        if path.is_file() and path != package_dir / "manifest.json":
            actual.add(path.relative_to(package_dir).as_posix())
    if actual != set(manifest["files"]):
        raise ValueError("manifest must cover every payload file")
    if "requirements.lock" not in actual:
        raise ValueError("missing dependency lock")
    for name, expected in manifest["files"].items():
        if hashlib.sha256((package_dir / name).read_bytes()).hexdigest() != expected:
            raise ValueError("artifact checksum mismatch")
    for line in (package_dir / "requirements.lock").read_text().splitlines():
        requirement = line.strip()
        if requirement and not requirement.startswith("#") and not re.fullmatch(
            r"[A-Za-z0-9][A-Za-z0-9_.-]*==[A-Za-z0-9][A-Za-z0-9.+!-]*", requirement
        ):
            raise ValueError("runtime dependencies must use exact pins")
    return manifest
