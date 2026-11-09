"""Offline effort baseline. Coefficients are policy assumptions, not observations."""

from collections.abc import Mapping
from datetime import datetime, timedelta, timezone
from decimal import Decimal, ROUND_CEILING
import json
import math
from pathlib import Path
from types import MappingProxyType

from pricing_contracts import PolicyInput, PolicyResult, PolicyUnavailable, UnsupportedRoute

FEATURE_SCHEMA_VERSION = "effort-traffic-1"
COORDINATE_FEATURE_SCHEMA_VERSION = "effort-traffic-coordinates-1"
EARTH_RADIUS_METRES = 6371008.8
SINGAPORE = timezone(timedelta(hours=8), "Asia/Singapore")
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
    "footTrafficMaxPremium": 0.2,
    # Walking distance ~= straight line x detour; campus paths, stairs and
    # covered walkways typically add 20-40 percent. Uncalibrated assumption.
    "walkingDetourFactor": 1.3,
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
        if key == "footTrafficMaxPremium":
            # Old releases omit this coefficient and retain effort-only pricing.
            config[key] = _number(value.get(key, 0))
            if config[key] > Decimal("0.2"):
                raise ValueError("foot traffic premium must not exceed 20 percent")
            continue
        if key == "walkingDetourFactor":
            # Old releases omit this coefficient and support only static routes.
            config[key] = _number(value.get(key, 0))
            if config[key] and not Decimal(1) <= config[key] <= Decimal(2):
                raise ValueError("walking detour factor must be 0 (disabled) or between 1 and 2")
            continue
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


def validate_foot_traffic(value):
    if (not isinstance(value, Mapping) or type(value.get("schemaVersion")) is not int
            or value["schemaVersion"] != 1 or value.get("provenance") != "estimated"
            or value.get("timezone") != "Asia/Singapore"):
        raise ValueError("invalid foot traffic policy")
    _identifier(value.get("datasetId"))
    _time(value.get("referenceAsOf"))
    if not isinstance(value.get("locations"), list) or not value["locations"]:
        raise ValueError("foot traffic locations required")
    locations = {}
    for row in value["locations"]:
        _identifier(row.get("locationId"))
        index = _number(row.get("relativePeakIndex"))
        if index > 100 or row["locationId"] in locations:
            raise ValueError("invalid or duplicate relative traffic index")
        locations[row["locationId"]] = index
    bands, end = [], 0
    for row in value.get("timeBands", []):
        start, stop = row.get("startHour"), row.get("endHour")
        if type(start) is not int or type(stop) is not int or start != end or not start < stop <= 24:
            raise ValueError("time bands must cover a full day without gaps/overlap")
        factor = _number(row.get("factor"))
        if factor > 1:
            raise ValueError("invalid traffic time factor")
        bands.append((start, stop, factor))
        end = stop
    if end != 24:
        raise ValueError("time bands must cover 24 hours")
    return MappingProxyType({
        "datasetId": value["datasetId"], "referenceAsOf": value["referenceAsOf"], "locations": MappingProxyType(locations),
        "timeBands": tuple(bands),
    })


def traffic_features(value, config, traffic):
    cap = config.get("footTrafficMaxPremium", Decimal(0))
    if not cap or traffic is None:
        return {"footfallUsed": False, "trafficMultiplier": 1.0}
    local = _time(value.calculated_at).astimezone(SINGAPORE)
    index = traffic["locations"].get(value.resolved_places["pickupLocationId"])
    factor = next(factor for start, stop, factor in traffic["timeBands"] if start <= local.hour < stop)
    # Unknown future catalogue locations remain neutral, never fabricated zero.
    effective = index * factor / 100 if index is not None else None
    multiplier = 1 + cap * (1 - effective) if effective is not None else Decimal(1)
    return {
        "footfallUsed": index is not None, "footTrafficKnown": index is not None,
        "footTrafficDatasetId": traffic["datasetId"], "footTrafficProvenance": "estimated",
        "footTrafficReferenceAsOf": traffic["referenceAsOf"],
        "relativePeakFootTraffic": float(index) if index is not None else None,
        "footTrafficTimeFactor": float(factor),
        "effectiveRelativeFootTraffic": float(effective) if effective is not None else None,
        "trafficMultiplier": float(multiplier), "localHour": local.hour,
        "footTrafficTimezone": "Asia/Singapore",
    }


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


def _coordinates(value, location_id):
    for row in value.context["catalogue"]["locations"]:
        if row["id"] == location_id:
            if "latitude" not in row or "longitude" not in row:
                return None
            latitude, longitude = float(_signed(row["latitude"], 90)), float(_signed(row["longitude"], 180))
            return math.radians(latitude), math.radians(longitude)
    return None


def _signed(value, limit):
    if type(value) not in (int, float, Decimal):
        raise ValueError("expected numeric coordinate")
    number = Decimal(str(value))
    if not number.is_finite() or abs(number) > limit:
        raise ValueError("invalid coordinate")
    return number


def coordinate_route(value, config):
    """Estimated walking metres from catalogue coordinates, or None if unknown."""
    factor = config.get("walkingDetourFactor", Decimal(0))
    places = value.resolved_places
    if not factor:
        return None
    start = _coordinates(value, places["pickupLocationId"])
    end = _coordinates(value, places["deliveryLocationId"])
    if start is None or end is None:
        return None
    (lat1, lon1), (lat2, lon2) = start, end
    # Haversine great-circle distance; exact enough at campus scale.
    h = math.sin((lat2 - lat1) / 2) ** 2 + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2
    straight = Decimal(str(round(2 * EARTH_RADIUS_METRES * math.asin(min(1.0, math.sqrt(h))), 1)))
    return straight, straight * factor


def feature_schema_version(config, traffic):
    if traffic is None:
        return "effort-1"
    return COORDINATE_FEATURE_SCHEMA_VERSION if config.get("walkingDetourFactor", 0) else FEATURE_SCHEMA_VERSION


def _route(value, config):
    places = value.resolved_places
    candidates = [
        route for route in value.context["routes"]
        if route["pickupLocationId"] == places["pickupLocationId"]
        and route["deliveryLocationId"] == places["deliveryLocationId"]
    ]
    if len(candidates) > 1:
        raise PolicyUnavailable()
    if candidates:
        return _number(candidates[0]["walkableMetres"]), None
    estimate = coordinate_route(value, config)
    if estimate is None:
        raise UnsupportedRoute()
    straight, walking = estimate
    return walking, straight


def _effort(value, config):
    if value.contract_version.split(".")[0] != "1" or value.context["schemaVersion"].split(".")[0] != "1":
        raise PolicyUnavailable()
    distance, _ = _route(value, config)
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


def effort_features(value, config, traffic=None):
    """Reusable offline features. Unknown quantity/handling stays explicitly unknown."""
    distance, travel, total, quantity, extra, handling, effort, unrounded = _effort(value, config)
    route_sources = [source for source in value.context["sources"] if source["source"] == "routes"]
    features = {
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
    if config.get("walkingDetourFactor", 0):
        # Coordinate estimates are new keys under their own feature schema only.
        _, straight = _route(value, config)
        features["routeMethod"] = "static" if straight is None else "coordinates"
        features["straightLineMetres"] = float(straight) if straight is not None else None
        features["walkingDetourFactor"] = float(config["walkingDetourFactor"])
        if straight is not None:
            features["routeProvenance"] = "estimated"
    features.update(traffic_features(value, config, traffic))
    features["trafficAdjustedCredits"] = float(unrounded * Decimal(str(features["trafficMultiplier"])))
    return features


class FormulaPolicy:
    def __init__(self, config, traffic=None):
        if config.get("footTrafficMaxPremium", 0) and traffic is None:
            raise ValueError("enabled foot traffic adjustment requires a lookup")
        self.config, self.traffic = config, traffic
        self.feature_schema = feature_schema_version(config, traffic)

    def warm_up(self, example: PolicyInput) -> None:
        self.calculate(example)

    def calculate(self, value: PolicyInput) -> PolicyResult:
        try:
            *_, unrounded = _effort(value, self.config)
            features = effort_features(value, self.config, self.traffic)
            unrounded *= Decimal(str(features["trafficMultiplier"]))
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
            if features["footfallUsed"]:
                warnings.append("estimated_supply")
            elif self.traffic is not None and self.config.get("footTrafficMaxPremium", 0):
                warnings.append("unknown_foot_traffic")
            features["orderContextState"] = state
            return PolicyResult(credits, "baseline", tuple(warnings), self.feature_schema, _freeze(features))
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
    path = Path(package_dir) / "artifacts" / "foot_traffic.json"
    traffic = validate_foot_traffic(_read(path)) if path.exists() else None
    return FormulaPolicy(config, traffic)
