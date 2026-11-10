"""Exported acceptance-model policy; training imports these exact feature functions.

A LightGBM classifier estimates P(accepted before expiry | context, price). For
each quote, every whole-credit price in the release bounds is scored as
P(accept) - lambda * (price - minimum) / (maximum - minimum), and the best score
wins (ties go to the lower price). Lambda is calibrated during training.
"""

from datetime import datetime, timedelta, timezone
import json
import math
from pathlib import Path
from types import MappingProxyType

from pricing_contracts import PolicyResult

FEATURE_SCHEMA_VERSION = "acceptance-gbm-1"
FEATURES = (
    "price", "priceOverEffort", "walkableMetres", "totalEffortMinutes", "totalItemQuantity",
    "effortCredits", "localHour", "weekday", "relativePeakFootTraffic",
    "effectiveRelativeFootTraffic", "pickupOpenRequestCount", "pickupCreatedOrderCount",
)
# Higher price can never lower predicted acceptance.
MONOTONE = tuple(1 if name in ("price", "priceOverEffort") else 0 for name in FEATURES)
SINGAPORE = timezone(timedelta(hours=8))
LOW_ACCEPTANCE = 0.5


def _number(value):
    return float(value) if value is not None else math.nan


def context_features(value, baseline_features):
    """Price-independent features from the policy input and baseline diagnostics."""
    local = datetime.fromisoformat(value.calculated_at.replace("Z", "+00:00")).astimezone(SINGAPORE)
    pickup = value.resolved_places["pickupLocationId"]
    area = next((row for row in value.context["orderContext"].get("areas", ())
                 if row["locationId"] == pickup), None)
    return {
        "walkableMetres": _number(baseline_features["walkableMetres"]),
        "totalEffortMinutes": _number(baseline_features["totalEffortMinutes"]),
        "totalItemQuantity": _number(baseline_features["totalItemQuantity"]),
        "effortCredits": _number(baseline_features["unroundedCredits"]),
        "localHour": local.hour + local.minute / 60,
        "weekday": float(local.weekday()),
        "relativePeakFootTraffic": _number(baseline_features.get("relativePeakFootTraffic")),
        "effectiveRelativeFootTraffic": _number(baseline_features.get("effectiveRelativeFootTraffic")),
        # A complete fresh snapshot without this area means zero counts (API section 5).
        "pickupOpenRequestCount": float(area["openRequestCount"]) if area else 0.0,
        "pickupCreatedOrderCount": float(area["createdOrderCount"]) if area else 0.0,
    }


def feature_row(context, price):
    row = dict(context, price=float(price), priceOverEffort=float(price) - context["effortCredits"])
    return [row[name] for name in FEATURES]


def choose(probabilities, prices, penalty):
    low, high = prices[0], prices[-1]
    scores = [p - penalty * (price - low) / (high - low) for p, price in zip(probabilities, prices)]
    best = max(range(len(prices)), key=lambda i: (scores[i], -i))
    return best


def validate_spec(spec, booster_features):
    if spec.get("schemaVersion") != 1 or tuple(spec.get("features", ())) != FEATURES:
        raise ValueError("unsupported acceptance model schema")
    if list(booster_features) != list(FEATURES):
        raise ValueError("model feature order mismatch")
    prices = spec.get("prices")
    if prices != list(range(5, 31)):
        raise ValueError("price grid must be every whole credit from 5 to 30")
    penalty = spec.get("penalty")
    if type(penalty) not in (int, float) or not math.isfinite(penalty) or penalty < 0:
        raise ValueError("invalid price penalty")
    if not isinstance(spec.get("datasetId"), str) or not spec["datasetId"]:
        raise ValueError("missing model lineage")
    return prices, float(penalty)


class AcceptancePolicy:
    def __init__(self, package_dir):
        import lightgbm
        import numpy

        from .formula import load_policy as load_baseline

        package_dir = Path(package_dir)
        self.baseline = load_baseline(package_dir)
        self.spec = json.loads((package_dir / "artifacts/acceptance.json").read_text())
        self.numpy = numpy
        self.booster = lightgbm.Booster(model_file=str(package_dir / "artifacts/model.txt"))
        self.prices, self.penalty = validate_spec(self.spec, self.booster.feature_name())

    def predict(self, rows):
        matrix = self.numpy.array(rows, dtype=float)
        return [float(p) for p in self.booster.predict(matrix, num_threads=1)]

    def warm_up(self, example):
        self.calculate(example)

    def calculate(self, value):
        baseline = self.baseline.calculate(value)
        # A stale/missing snapshot never silently becomes zero Order counts.
        if value.context["orderContext"]["state"] != "fresh":
            return PolicyResult(
                baseline.recommended_credits, "baseline", baseline.warnings, FEATURE_SCHEMA_VERSION,
                MappingProxyType({"provenance": "estimated", "fallback": "order_context",
                                  "baseline": baseline.feature_snapshot}),
            )
        context = context_features(value, baseline.feature_snapshot)
        probabilities = self.predict([feature_row(context, price) for price in self.prices])
        best = choose(probabilities, self.prices, self.penalty)
        warnings = tuple(baseline.warnings)
        if probabilities[best] < LOW_ACCEPTANCE:
            warnings += ("low_predicted_acceptance",)
        return PolicyResult(
            self.prices[best], "model", warnings, FEATURE_SCHEMA_VERSION,
            MappingProxyType({
                "provenance": "synthetic", "experimental": True,
                "acceptanceProbability": probabilities[best],
                "acceptanceAtMinimum": probabilities[0], "acceptanceAtMaximum": probabilities[-1],
                "pricePenalty": self.penalty, "baselineCredits": baseline.recommended_credits,
                "features": MappingProxyType({k: (None if math.isnan(v) else v) for k, v in context.items()}),
                "modelDatasetId": self.spec["datasetId"],
            }),
        )

    def close(self):
        self.baseline.close()


def load_policy(package_dir):
    return AcceptancePolicy(package_dir)
