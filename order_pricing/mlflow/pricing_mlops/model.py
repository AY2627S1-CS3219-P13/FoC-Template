"""Exported stdlib-only inference; training imports these exact feature functions."""

import json
import math
from pathlib import Path
from types import MappingProxyType

from pricing_contracts import PolicyResult
from .formula import effort_features, load_policy as load_baseline

FEATURE_NAMES = ("travelMinutes", "handlingCredits")


def feature_vector(features):
    # Unknown handling has no allowance, with quantityKnown retained separately.
    return [features["travelMinutes"], features["handlingCredits"] or 0.0]


def predict_raw(model, features):
    return model["intercept"] + sum(
        weight * value for weight, value in zip(model["coefficients"], feature_vector(features))
    )


def price(raw, minimum=5, maximum=30):
    if not math.isfinite(raw):
        raise ValueError("nonfinite prediction")
    return min(maximum, max(minimum, math.ceil(round(raw, 8))))


class ModelPolicy:
    def __init__(self, package_dir):
        self.baseline = load_baseline(package_dir)
        self.model = json.loads((Path(package_dir) / "artifacts/model.json").read_text())
        if self.model.get("schemaVersion") != 1 or self.model.get("features") != list(FEATURE_NAMES):
            raise ValueError("unsupported model schema")
        if not isinstance(self.model.get("datasetId"), str) or not self.model["datasetId"]:
            raise ValueError("missing model lineage")
        values = [self.model.get("intercept"), *self.model.get("coefficients", [])]
        if len(values) != 3 or any(type(v) not in (int, float) or not math.isfinite(v) for v in values):
            raise ValueError("invalid model coefficients")
        self.feature_schema = "synthetic-effort-model-2"

    def warm_up(self, example):
        self.calculate(example)

    def calculate(self, value):
        baseline = self.baseline.calculate(value)
        # A stale/missing snapshot never silently becomes zero Order counts.
        if value.context["orderContext"]["state"] != "fresh":
            return PolicyResult(
                baseline.recommended_credits, "baseline", baseline.warnings,
                self.feature_schema, MappingProxyType({
                    "provenance": "estimated", "fallback": "order_context",
                    "baseline": baseline.feature_snapshot,
                }),
            )
        # The complete baseline owns the release's optional market adjustment.
        # Earlier immutable baseline packages have no traffic multiplier.
        features = baseline.feature_snapshot
        raw = predict_raw(self.model, features)
        return PolicyResult(
            price(raw * features.get("trafficMultiplier", 1)), "model", baseline.warnings, self.feature_schema,
            MappingProxyType({
                "provenance": "synthetic", "experimental": True,
                "features": MappingProxyType(features), "rawPrediction": raw,
                "modelDatasetId": self.model["datasetId"],
            }),
        )

    def close(self):
        self.baseline.close()


def load_policy(package_dir):
    return ModelPolicy(package_dir)
