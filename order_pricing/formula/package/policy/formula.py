"""Offline effort baseline. Coefficients are policy assumptions, not observations."""

from collections.abc import Mapping
from datetime import datetime
from decimal import Decimal, ROUND_CEILING
import json
from pathlib import Path
from types import MappingProxyType

from pricing_contracts import PolicyInput, PolicyResult, PolicyUnavailable, UnsupportedRoute

FEATURE_SCHEMA_VERSION = "effort-1"
DEFAULT_CONFIG = {
    "baseCredits": 2,
    "creditsPerHour": 15,
    "walkingMetresPerMinute": 75,
    "approachMinutes": 5,
    "pickupMinutes": 3,
    "extraItemCredits": 0.5,
    "maxExtraItems": 6,
    "minimumCredits": 5,
    "maximumCredits": 30,
}


def _object(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("duplicate asset key")
        value[key] = item
    return value


def _read(path):
    return json.loads(Path(path).read_text(), object_pairs_hook=_object)


def _number(value, *, positive=False):
    if type(value) not in (int, float, Decimal):
        raise ValueError("expected numeric policy value")
    number = Decimal(str(value))
    if not number.is_finite() or number < 0 or (positive and number == 0):
        raise ValueError("invalid numeric policy value")
    return number


def validate_config(value):
    if not isinstance(value, Mapping):
        raise ValueError("invalid formula configuration")
    config = {}
    for key in DEFAULT_CONFIG:
        if key not in value:
            raise ValueError("missing formula coefficient")
        config[key] = _number(value[key], positive=key in ("walkingMetresPerMinute", "creditsPerHour"))
    for key in ("maxExtraItems", "minimumCredits", "maximumCredits"):
        if type(value[key]) is not int:
            raise ValueError("integer policy coefficient required")
        config[key] = value[key]
    if config["minimumCredits"] != 5 or config["maximumCredits"] != 30:
        raise ValueError("initial effort release requires 5–30 credit bounds")
    return MappingProxyType(config)


def load_config(package_dir):
    return validate_config(_read(Path(package_dir) / "artifacts" / "config.json"))


def _identifier(value):
    if not isinstance(value, str) or not value:
        raise ValueError("invalid static location identity")


def _time(value):
    if not isinstance(value, str):
        raise ValueError("invalid reference timestamp")
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("reference timestamp requires timezone")
    return parsed


def validate_static_context(value):
    if not isinstance(value, Mapping):
        raise ValueError("invalid static context")
    if not isinstance(value.get("routes"), list) or not value["routes"]:
        raise ValueError("nonempty static routes required")
    pairs = set()
    for route in value["routes"]:
        if not isinstance(route, Mapping):
            raise ValueError("invalid static route")
        pair = route.get("pickupLocationId"), route.get("deliveryLocationId")
        for identity in pair:
            _identifier(identity)
        if pair in pairs:
            raise ValueError("duplicate directed static route")
        pairs.add(pair)
        _number(route.get("walkableMetres"))
    if not isinstance(value.get("footfall"), list):
        raise ValueError("static footfall configuration required")
    for row in value["footfall"]:
        _identifier(row.get("locationId"))
        _number(row.get("estimatedPeople"))
        if _time(row.get("windowStart")) >= _time(row.get("windowEnd")):
            raise ValueError("invalid footfall reference window")
    if not isinstance(value.get("sources"), list):
        raise ValueError("static source stamps required")
    sources = {}
    for source in value["sources"]:
        if source.get("source") not in ("routes", "footfall"):
            continue
        name = source["source"]
        if name in sources:
            raise ValueError("duplicate static source stamp")
        _identifier(source.get("snapshotId"))
        _time(source.get("asOf"))
        if source.get("provenance") not in ("synthetic", "estimated", "observed"):
            raise ValueError("invalid source provenance")
        sources[name] = source
    if set(sources) != {"routes", "footfall"}:
        raise ValueError("missing static source stamp")


def _freeze(value):
    if isinstance(value, dict):
        return MappingProxyType({key: _freeze(item) for key, item in value.items()})
    if isinstance(value, list):
        return tuple(_freeze(item) for item in value)
    return value


def _effort(value, config):
    if value.contract_version.split(".")[0] != "1" or value.context["schemaVersion"].split(".")[0] != "1":
        raise PolicyUnavailable()
    places = value.resolved_places
    candidates = [
        route for route in value.context["routes"]
        if route["pickupLocationId"] == places["pickupLocationId"]
        and route["deliveryLocationId"] == places["deliveryLocationId"]
    ]
    if not candidates:
        raise UnsupportedRoute()
    if len(candidates) != 1:
        raise PolicyUnavailable()
    distance = _number(candidates[0]["walkableMetres"])
    travel = distance / config["walkingMetresPerMinute"]
    total = config["approachMinutes"] + config["pickupMinutes"] + travel
    quantity = value.request.get("totalItemQuantity")
    if "totalItemQuantity" in value.request and (type(quantity) is not int or quantity <= 0 or quantity > 9007199254740991):
        raise PolicyUnavailable()
    extra = min(max(quantity - 1, 0), config["maxExtraItems"]) if quantity is not None else None
    handling = config["extraItemCredits"] * extra if extra is not None else None
    effort = config["creditsPerHour"] * total / Decimal(60)
    unrounded = config["baseCredits"] + effort + (handling if handling is not None else Decimal(0))
    return distance, travel, total, quantity, extra, handling, effort, unrounded


def effort_features(value, config):
    """Reusable offline features. Unknown quantity/handling stays explicitly unknown."""
    distance, travel, total, quantity, extra, handling, effort, unrounded = _effort(value, config)
    route_sources = [source for source in value.context["sources"] if source["source"] == "routes"]
    return {
        "walkableMetres": float(distance),
        "travelMinutes": float(travel),
        "approachMinutes": float(config["approachMinutes"]),
        "pickupMinutes": float(config["pickupMinutes"]),
        "totalEffortMinutes": float(total),
        "quantityKnown": quantity is not None,
        "totalItemQuantity": quantity,
        "additionalItemCount": extra,
        "handlingCredits": float(handling) if handling is not None else None,
        "effortCredits": float(effort),
        "unroundedCredits": float(unrounded),
        "effortProvenance": "estimated",
        "routeProvenance": route_sources[0]["provenance"] if route_sources else "estimated",
        "footfallUsed": False,
    }


class FormulaPolicy:
    def __init__(self, config):
        self.config = config

    def warm_up(self, example: PolicyInput) -> None:
        self.calculate(example)

    def calculate(self, value: PolicyInput) -> PolicyResult:
        try:
            *_, unrounded = _effort(value, self.config)
            features = effort_features(value, self.config)
            rounded = int(unrounded.to_integral_value(rounding=ROUND_CEILING))
            credits = max(self.config["minimumCredits"], min(self.config["maximumCredits"], rounded))
            state = value.context["orderContext"]["state"]
            if state not in ("fresh", "stale", "unavailable"):
                raise PolicyUnavailable()
            warnings = []
            if not features["quantityKnown"]:
                warnings.append("unknown_item_quantity")
            if state != "fresh":
                warnings.append("stale_order_context" if state == "stale" else "order_context_unavailable")
            features["orderContextState"] = state
            return PolicyResult(credits, "baseline", tuple(warnings), FEATURE_SCHEMA_VERSION, _freeze(features))
        except (KeyError, TypeError, ValueError, ArithmeticError) as error:
            raise PolicyUnavailable() from error

    def close(self) -> None:
        pass


def load_policy(package_dir: Path) -> FormulaPolicy:
    config = load_config(package_dir)
    validate_static_context(_read(Path(package_dir) / "artifacts" / "context.json"))
    warmup = _read(Path(package_dir) / "artifacts" / "warmup.json")
    if not isinstance(warmup, dict) or not {"contractVersion", "calculatedAt", "request", "resolvedPlaces", "context"} <= warmup.keys():
        raise ValueError("invalid warm-up input")
    return FormulaPolicy(config)
