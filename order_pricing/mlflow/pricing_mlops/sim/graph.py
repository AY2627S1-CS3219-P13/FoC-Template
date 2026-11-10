"""Method 2: causal-graph sampler, one independent order at a time.

context -> hidden difficulty H (never a model feature) -> effort -> unseen supply S
        -> customer offer (raised by H) -> acceptance (lowered by H)

H affects both the offer and acceptance, reproducing the price/difficulty
confounding that self-chosen real offers will have.
"""

import math

from .records import make_record, policy_input
from .world import customer_offer, instant, poisson, sigmoid

VERSION = "causal-graph-1"
NAME = "causal-graph"


def quantity(rng):
    # A few legacy clients omit quantity; it then stays unknown.
    return None if rng.random() < 0.05 else 1 + poisson(rng, 0.6)


def generate(rng, world, theta, baseline, shared, *, rows, scenario, split):
    hours = list(range(24))
    hour_weights = [sum(world.requester_weight(location, hour) for location in world.ids) for hour in hours]
    requester_noon = sum(world.requester_weight(location, 12) for location in world.ids)
    supply_noon = sum(world.supply_weight(location, 12, theta["trafficSupplyLink"]) for location in world.ids)
    records = []
    for index in range(rows):
        day = rng.randrange(70)
        load = rng.gammavariate(theta["dayShape"], 1 / theta["dayShape"])
        hour = rng.choices(hours, hour_weights)[0]
        minute = hour * 60 + rng.randrange(60)
        delivery = rng.choices(world.ids, [world.requester_weight(l, hour) for l in world.ids])[0]
        pickup = world.choose_pickup(rng, delivery, hour, theta["gravityMetres"])
        # Visible market state at the pickup: orders created there per 15 minutes,
        # and still-open requests (roughly half of an expiry window's arrivals).
        pickup_share = world.traffic_index[pickup] / sum(world.traffic_index.values())
        created_rate = theta["requestsPerPeakHour"] * load * pickup_share * hour_weights[hour] / requester_noon * 0.25
        created = poisson(rng, created_rate)
        open_requests = poisson(rng, created_rate * theta["expiryMinutes"] / 15 * 0.5)
        # Unseen deliverers online near the pickup.
        online_rate = (theta["deliverersPerPeakHour"] * load
                       * world.supply_weight(pickup, hour, theta["trafficSupplyLink"]) / supply_noon
                       * theta["sessionMinutes"] / 60 * len(world.ids) / 3)
        online = poisson(rng, online_rate)
        difficulty = rng.gauss(0, 1)
        calculated = instant(day, minute)
        expires = instant(day, minute + theta["expiryMinutes"])
        draft = make_record(
            generator=NAME, version=VERSION, scenario=f"{scenario}-{index}", theta_id=scenario, split=split,
            calculated_at=calculated, pickup=pickup, delivery=delivery, quantity=quantity(rng),
            open_requests=open_requests, created_orders=created, offered=5, expires_at=expires,
            outcome="expired",
        )
        result = baseline.calculate(policy_input(draft, shared))
        offer = customer_offer(rng, theta, result.recommended_credits, difficulty)
        effort = result.feature_snapshot["unroundedCredits"]
        score = (theta["acceptIntercept"]
                 + theta["acceptPerCredit"] * (offer - theta["costMultiplier"] * effort)
                 + theta["supplyEffect"] * math.log1p(online)
                 - theta["competitionEffect"] * math.log1p(open_requests)
                 - theta["difficultyEffect"] * difficulty)
        accepted = rng.random() < sigmoid(score)
        decided = theta["expiryMinutes"] * (rng.betavariate(1, 2) if accepted else 1)
        patience = rng.expovariate(1 / theta["patienceMinutes"])
        draft["offeredCredits"] = offer
        if patience < decided:
            draft["outcome"] = "cancelled"
        elif accepted:
            draft["outcome"] = "accepted"
            draft["acceptedAt"] = instant(day, minute + decided)
        records.append(draft)
    return records
