"""Method 4: agent-based, event-driven campus day.

Requesters and deliverers arrive per location and 30-minute slot as Poisson
counts whose means follow the requester profile and the foot-traffic-linked
supply profile, scaled by a shared Gamma day factor. Deliverers check the app,
take the open order with the highest positive utility and stay busy while
delivering. Acceptance is an outcome of these mechanics, not an equation.
"""

import heapq
import itertools

from .records import make_record, policy_input
from .world import customer_offer, instant, poisson

VERSION = "agent-based-1"
NAME = "agent-based"
WALKING_METRES_PER_MINUTE = 75
DETOUR = 1.3
PICKUP_MINUTES = 3


def quantity(rng):
    return None if rng.random() < 0.05 else 1 + poisson(rng, 0.6)


class Day:
    def __init__(self, rng, world, theta, baseline, shared, day, scenario, split):
        self.rng, self.world, self.theta = rng, world, theta
        self.baseline, self.shared = baseline, shared
        self.day, self.scenario, self.split = day, scenario, split
        self.events, self.sequence = [], itertools.count()
        self.orders, self.open = [], set()
        self.created_at = {location: [] for location in world.ids}

    def push(self, minute, kind, payload):
        heapq.heappush(self.events, (minute, next(self.sequence), kind, payload))

    def walk(self, first, second):
        if first == second:
            return 0.0
        return self.world.straight_metres(first, second) * DETOUR / WALKING_METRES_PER_MINUTE

    def schedule_arrivals(self):
        theta, world, rng = self.theta, self.world, self.rng
        load = rng.gammavariate(theta["dayShape"], 1 / theta["dayShape"])
        link = theta["trafficSupplyLink"]
        requester_noon = sum(world.requester_weight(l, 12) for l in world.ids)
        supply_noon = sum(world.supply_weight(l, 12, link) for l in world.ids)
        for slot in range(48):
            hour = slot // 2
            for location in world.ids:
                requests = theta["requestsPerPeakHour"] * load * world.requester_weight(location, hour) / requester_noon
                for _ in range(poisson(rng, requests * 0.5)):
                    self.push(slot * 30 + rng.uniform(0, 30), "order", location)
                deliverers = theta["deliverersPerPeakHour"] * load * world.supply_weight(location, hour, link) / supply_noon
                for _ in range(poisson(rng, deliverers * 0.5)):
                    start = slot * 30 + rng.uniform(0, 30)
                    value_of_time = theta["valueOfTimePerHourMedian"] * rng.lognormvariate(0, theta["valueOfTimeSigma"])
                    agent = {"location": location, "end": start + rng.expovariate(1 / theta["sessionMinutes"]),
                             "valueOfTime": value_of_time}
                    self.push(start, "check", agent)

    def create_order(self, minute, delivery):
        theta, rng = self.theta, self.rng
        pickup = self.world.choose_pickup(rng, delivery, int(minute // 60) % 24, theta["gravityMetres"])
        recent = self.created_at[pickup]
        open_requests = sum(1 for o in self.open if self.orders[o]["pickup"] == pickup)
        created = sum(1 for t in recent if minute - 15 <= t < minute)
        index = len(self.orders)
        record = make_record(
            generator=NAME, version=VERSION, scenario=f"{self.scenario}-d{self.day}-{index}",
            theta_id=self.scenario, split=self.split, calculated_at=instant(self.day, minute),
            pickup=pickup, delivery=delivery, quantity=quantity(rng), open_requests=open_requests,
            created_orders=created, offered=5, expires_at=instant(self.day, minute + theta["expiryMinutes"]),
            outcome="expired",
        )
        result = self.baseline.calculate(policy_input(record, self.shared))
        difficulty = rng.gauss(0, 1)
        record["offeredCredits"] = customer_offer(rng, theta, result.recommended_credits, difficulty)
        recent.append(minute)
        self.orders.append({"record": record, "pickup": pickup, "delivery": delivery, "created": minute,
                            "difficulty": difficulty, "walk": self.walk(pickup, delivery)})
        self.open.add(index)
        self.push(minute + theta["expiryMinutes"], "expire", index)
        self.push(minute + rng.expovariate(1 / theta["patienceMinutes"]), "cancel", index)

    def check(self, minute, agent):
        if minute >= agent["end"]:
            return
        theta, best, best_utility = self.theta, None, 0.0
        for index in self.open:
            order = self.orders[index]
            minutes = self.walk(agent["location"], order["pickup"]) + PICKUP_MINUTES + order["walk"]
            utility = (order["record"]["offeredCredits"] - agent["valueOfTime"] * minutes / 60
                       - theta["fixedCostCredits"] - theta["difficultyCostCredits"] * order["difficulty"])
            if utility > best_utility or (utility == best_utility and best is not None and index < best):
                best, best_utility = index, utility
        if best is None:
            self.push(minute + self.rng.expovariate(1 / theta["checkIntervalMinutes"]), "check", agent)
            return
        order = self.orders[best]
        self.open.discard(best)
        order["record"]["outcome"] = "accepted"
        order["record"]["acceptedAt"] = instant(self.day, minute)
        busy = self.walk(agent["location"], order["pickup"]) + PICKUP_MINUTES + order["walk"]
        agent["location"] = order["delivery"]
        self.push(minute + busy + self.rng.expovariate(1 / theta["checkIntervalMinutes"]), "check", agent)

    def run(self):
        self.schedule_arrivals()
        while self.events:
            minute, _, kind, payload = heapq.heappop(self.events)
            if kind == "order":
                self.create_order(minute, payload)
            elif kind == "check":
                self.check(minute, payload)
            elif payload in self.open:
                # Expiry or cancellation of a still-open order; otherwise a no-op.
                self.open.discard(payload)
                self.orders[payload]["record"]["outcome"] = "expired" if kind == "expire" else "cancelled"
        return [order["record"] for order in self.orders]


def generate(rng, world, theta, baseline, shared, *, days, first_day, scenario, split):
    records = []
    for day in range(first_day, first_day + days):
        records.extend(Day(rng, world, theta, baseline, shared, day % 70, scenario, split).run())
    return records
