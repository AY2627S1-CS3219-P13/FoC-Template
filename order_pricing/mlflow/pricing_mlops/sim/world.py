"""Shared campus world for every synthetic generator.

All numbers here are estimated simulation assumptions. Every generator draws its
parameters from the same prior ranges so that differences between generators come
from their structure, not from inconsistent numbers.
"""

from datetime import datetime, timedelta, timezone
import json
import math
from pathlib import Path
import uuid

SINGAPORE = timezone(timedelta(hours=8), "Asia/Singapore")
# Monday of a teaching week; simulated days count forward from here.
BASE_DAY = datetime(2027, 1, 11, tzinfo=SINGAPORE)
EARTH_RADIUS_METRES = 6371008.8
SUPPLIER_NAMESPACE = uuid.UUID("6f1c2d3e-4b5a-4c6d-8e7f-901234567890")
ROOT = Path(__file__).resolve().parents[3]
WORLD_PATH = Path(__file__).with_name("world.json")
FOOT_TRAFFIC_PATH = ROOT / "formula" / "data" / "foot_traffic.json"


def _bands(rows):
    end, bands = 0, []
    for row in rows:
        if row["startHour"] != end or not row["startHour"] < row["endHour"] <= 24:
            raise ValueError("time bands must cover a day without gaps")
        bands.append((row["startHour"], row["endHour"], float(row["factor"])))
        end = row["endHour"]
    if end != 24:
        raise ValueError("time bands must cover 24 hours")
    return tuple(bands)


def _band(bands, hour):
    return next(factor for start, stop, factor in bands if start <= hour < stop)


class World:
    def __init__(self, world=None, foot_traffic=None):
        self.source = json.loads(Path(world or WORLD_PATH).read_text())
        traffic = json.loads(Path(foot_traffic or FOOT_TRAFFIC_PATH).read_text())
        if self.source.get("schemaVersion") != 1 or self.source.get("provenance") != "estimated":
            raise ValueError("unsupported world configuration")
        self.locations = {row["id"]: row for row in self.source["locations"]}
        self.ids = tuple(sorted(self.locations))
        self.traffic_index = {row["locationId"]: float(row["relativePeakIndex"]) for row in traffic["locations"]}
        if set(self.ids) != set(self.traffic_index):
            raise ValueError("world and foot traffic must cover the same locations")
        self.traffic_bands = _bands(traffic["timeBands"])
        self.requester_bands = _bands(self.source["requesterTimeBands"])
        self.priors = self.source["priors"]
        self.foot_traffic_id = traffic["datasetId"]
        # Mean relative traffic over all locations and hours: the flat supply level.
        self.mean_traffic = sum(
            self.traffic(location, hour) for location in self.ids for hour in range(24)
        ) / (24 * len(self.ids))
        self.noon_traffic_total = sum(self.traffic(location, 12) for location in self.ids)

    def traffic(self, location, hour):
        return self.traffic_index[location] / 100 * _band(self.traffic_bands, hour)

    def requester_weight(self, location, hour):
        row = self.locations[location]
        factor = _band(self.requester_bands, hour)
        evening = self.source["residentialEvening"]
        if row["residential"] and hour >= evening["startHour"]:
            factor *= evening["factor"]
        return row["requesterIndex"] / 100 * factor

    def supply_weight(self, location, hour, link):
        """Relative deliverer presence; link=0 ignores foot traffic entirely."""
        return (1 - link) * self.mean_traffic + link * self.traffic(location, hour)

    def straight_metres(self, first, second):
        a, b = self.locations[first], self.locations[second]
        lat1, lon1 = math.radians(a["latitude"]), math.radians(a["longitude"])
        lat2, lon2 = math.radians(b["latitude"]), math.radians(b["longitude"])
        h = math.sin((lat2 - lat1) / 2) ** 2 + math.cos(lat1) * math.cos(lat2) * math.sin((lon2 - lon1) / 2) ** 2
        return 2 * EARTH_RADIUS_METRES * math.asin(min(1.0, math.sqrt(h)))

    def choose_pickup(self, rng, delivery, hour, gravity_metres):
        """Gravity model: busier food hubs attract more orders, distance deters them."""
        options = [location for location in self.ids if location != delivery]
        weights = [
            max(self.traffic_index[p] / 100, 0.05) * math.exp(-self.straight_metres(p, delivery) / gravity_metres)
            for p in options
        ]
        return rng.choices(options, weights)[0]

    def draw_theta(self, rng):
        theta = {}
        for name, prior in sorted(self.priors.items()):
            low, high = float(prior["low"]), float(prior["high"])
            if prior.get("scale") == "log":
                theta[name] = math.exp(rng.uniform(math.log(low), math.log(high)))
            else:
                theta[name] = rng.uniform(low, high)
        return theta


def supplier_id(location):
    return str(uuid.uuid5(SUPPLIER_NAMESPACE, location))


def instant(day, minute):
    """UTC ISO timestamp for a simulated day index and Singapore minute of day."""
    moment = BASE_DAY + timedelta(days=day, minutes=minute)
    return moment.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def poisson(rng, mean):
    """Knuth for small means, normal approximation for large ones."""
    if mean <= 0:
        return 0
    if mean > 50:
        return max(0, round(rng.gauss(mean, math.sqrt(mean))))
    limit, count, product = math.exp(-mean), 0, rng.random()
    while product > limit:
        count += 1
        product *= rng.random()
    return count


def sigmoid(value):
    if value >= 0:
        return 1 / (1 + math.exp(-value))
    exp = math.exp(value)
    return exp / (1 + exp)


def customer_offer(rng, theta, recommended, difficulty):
    """Customers mostly keep the recommendation; others adjust it, more for hard jobs."""
    draw = rng.random()
    if draw < theta["explorationShare"]:
        return rng.randint(5, 30)
    if draw < theta["explorationShare"] + (1 - theta["explorationShare"]) * theta["defaultOfferShare"]:
        return recommended
    factor = math.exp(rng.gauss(0, theta["offerSigma"]) + theta["offerDifficulty"] * difficulty)
    return min(30, max(5, round(recommended * factor)))
