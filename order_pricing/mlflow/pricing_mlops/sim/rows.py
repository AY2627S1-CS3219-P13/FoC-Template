"""Method 1: independent rows with a known logistic truth.

Used only by tests: checks that training recovers a known price effect and how
many rows that takes. Price is independent of everything, so this is easier than
reality and never enters a training dataset.
"""

from .graph import quantity
from .records import make_record, policy_input
from .world import instant, sigmoid

VERSION = "independent-rows-1"
NAME = "independent-rows"


def generate(rng, world, baseline, shared, *, rows, intercept=-0.5, per_credit=0.35, split="train"):
    records = []
    for index in range(rows):
        day, minute = rng.randrange(70), rng.randrange(7 * 60, 23 * 60)
        delivery = rng.choice(world.ids)
        pickup = rng.choice([location for location in world.ids if location != delivery])
        record = make_record(
            generator=NAME, version=VERSION, scenario=f"rows-{index}", theta_id="rows", split=split,
            calculated_at=instant(day, minute), pickup=pickup, delivery=delivery, quantity=quantity(rng),
            open_requests=rng.randrange(5), created_orders=rng.randrange(5), offered=rng.randint(5, 30),
            expires_at=instant(day, minute + 30), outcome="expired",
        )
        effort = baseline.calculate(policy_input(record, shared)).feature_snapshot["unroundedCredits"]
        if rng.random() < sigmoid(intercept + per_credit * (record["offeredCredits"] - effort)):
            record["outcome"] = "accepted"
            record["acceptedAt"] = instant(day, minute + 5)
        records.append(record)
    return records
