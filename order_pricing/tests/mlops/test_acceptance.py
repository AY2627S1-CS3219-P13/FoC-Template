"""LightGBM acceptance model: recovery, gates, export and training/serving parity."""
import copy
import importlib
import json
import math
from pathlib import Path
import random
import sys
import tempfile
import unittest

from formula.build import build
from pricing_mlops import acceptance, acceptance_policy, pipeline
from pricing_mlops.sim import records, rows as independent
from pricing_mlops.sim.world import World
from tests.support import decode_input, load_fixture, validate_package, validate_result

COMMIT = "e" * 40


def logit(p):
    return math.log(p / (1 - p))


class RecoveryTests(unittest.TestCase):
    def test_known_price_effect_is_recovered_from_independent_rows(self):
        with tempfile.TemporaryDirectory() as temporary:
            formula = Path(temporary) / "formula"
            build(formula, COMMIT, "recovery")
            baseline = pipeline.baseline_module(formula).load_policy(formula)
            world = World()
            shared = records.shared_context(world, formula)
            data = independent.generate(random.Random(5), world, baseline, shared, rows=4000, per_credit=0.35)
            prepared = acceptance.prepare(data, shared, baseline)
            # Method 1 has one generator; give it the agent label so weights stay defined.
            for row in prepared:
                row["generator"] = acceptance.GENERATORS[0]
            booster = acceptance.fit(prepared, 5)
            matrix, prices = acceptance.price_matrix(booster, prepared[:400])
            slopes = []
            for row, item in zip(matrix, prepared[:400]):
                effort = item["context"]["effortCredits"]
                low, high = math.ceil(effort) - 2, math.ceil(effort) + 2
                if 5 <= low and high <= 30:
                    p_low = min(max(row[low - 5], 1e-3), 1 - 1e-3)
                    p_high = min(max(row[high - 5], 1e-3), 1 - 1e-3)
                    slopes.append((logit(p_high) - logit(p_low)) / (high - low))
            self.assertGreater(len(slopes), 100)
            average = sum(slopes) / len(slopes)
            # True slope 0.35 per credit; trees are coarse, so allow a wide band.
            self.assertTrue(0.15 < average < 0.6, average)
            self.assertTrue(all(b >= a - 1e-12 for row in matrix for a, b in zip(row, row[1:])))


class AcceptancePipelineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.formula = cls.root / "formula"
        build(cls.formula, COMMIT, "acceptance-test")
        cls.dataset = cls.root / "dataset"
        sizes = {"graph_rows": 2500, "graph_batches": 5, "agent_days": 10, "agent_batches": 5}
        acceptance.simulate(cls.dataset, cls.formula, 11, **sizes)
        cls.result = acceptance.train(cls.root / "work", cls.formula, COMMIT, cls.dataset, seed=11)
        cls.work = cls.root / "work"

    def selection(self):
        path = self.root / (next(tempfile._get_candidate_names()) + ".selection.json")
        acceptance.select(self.work, self.result["runId"], path)
        return path

    def test_metrics_gates_and_mlflow_run(self):
        import mlflow
        metrics = self.result["metrics"]
        self.assertEqual(metrics["monotonicity_violations"], 0)
        self.assertLess(metrics["log_loss"], metrics["base_rate_log_loss"])
        self.assertAlmostEqual(metrics["mean_acceptance_at_chosen"], 0.8, delta=0.03)
        for key in ("cross_causal-graph_to_agent-based_auc", "cross_agent-based_to_causal-graph_auc"):
            self.assertIn(key, metrics)
        run = mlflow.tracking.MlflowClient().get_run(self.result["runId"])
        self.assertEqual(run.data.tags["kind"], "acceptance-gbm")
        self.assertEqual(run.data.tags["provenance"], "synthetic")

    def test_training_is_reproducible(self):
        second = acceptance.train(self.work, self.formula, COMMIT, self.dataset, seed=11)
        first = acceptance.read_candidate(self.work, self.result["runId"])
        again = acceptance.read_candidate(self.work, second["runId"])
        self.assertEqual(first["modelSha256"], again["modelSha256"])
        self.assertEqual(first["spec"], again["spec"])

    def test_gates_reject_bad_candidates(self):
        metrics = dict(self.result["metrics"], monotonicity_violations=1, mean_acceptance_at_chosen=0.5)
        self.assertEqual(len(acceptance.gate_failures(metrics)), 2)
        model = self.work / "candidates" / self.result["runId"] / "model.txt"
        original = model.read_text()
        model.write_text(original + "\n")
        try:
            with self.assertRaisesRegex(ValueError, "model integrity"):
                acceptance.read_candidate(self.work, self.result["runId"])
        finally:
            model.write_text(original)

    def load(self, output):
        sys.path.insert(0, str(output))
        self.addCleanup(sys.path.remove, str(output))
        for name in list(sys.modules):
            if name == "policy" or name.startswith("policy."):
                del sys.modules[name]
        module = importlib.import_module("policy.acceptance")
        policy = module.load_policy(output)
        self.addCleanup(policy.close)
        return module, policy

    def test_export_adapter_and_training_serving_parity(self):
        selection = self.selection()
        self.assertFalse(json.loads(selection.read_text())["productionApproved"])
        with self.assertRaises(FileExistsError):
            acceptance.select(self.work, self.result["runId"], selection)
        output = self.root / (next(tempfile._get_candidate_names()) + "-package")
        manifest = acceptance.export(self.work, selection, self.formula, output, "acceptance-test-1")
        validate_package(output)
        self.assertEqual(manifest["entryPoint"], "policy.acceptance:load_policy")
        self.assertIn("lightgbm==", (output / "requirements.lock").read_text())
        module, policy = self.load(output)
        value = decode_input(load_fixture("policy-input.json"))
        policy.warm_up(value)
        result = policy.calculate(value)
        validate_result(result, manifest)
        self.assertEqual(result.pricing_mode, "model")
        self.assertEqual(policy.calculate(value), result)
        # The exported module computes exactly the features training used.
        self.assertEqual(module.FEATURES, acceptance_policy.FEATURES)
        records_, shared, _ = acceptance.dataset.read(self.dataset)
        booster = policy.booster
        decided = [r for r in records_ if r["outcome"] != "cancelled"][:25]
        prepared = acceptance.prepare(decided, shared, policy.baseline)
        for record, row in zip(decided, prepared):
            served = policy.calculate(records.policy_input(record, shared))
            matrix, prices = acceptance.price_matrix(booster, [row])
            expected = prices[acceptance_policy.choose(matrix[0], prices, policy.penalty)]
            self.assertEqual(served.recommended_credits, expected)
            self.assertAlmostEqual(served.feature_snapshot["acceptanceProbability"],
                                   matrix[0][expected - 5], places=12)
        for state in ("stale", "unavailable"):
            body = copy.deepcopy(load_fixture("policy-input.json"))
            body["context"]["orderContext"] = ({"state": "unavailable"} if state == "unavailable"
                                               else dict(body["context"]["orderContext"], state="stale"))
            fallback = policy.calculate(decode_input(body))
            self.assertEqual(fallback.pricing_mode, "baseline")
            validate_result(fallback, manifest)
        with self.assertRaises(FileExistsError):
            acceptance.export(self.work, selection, self.formula, output, "acceptance-test-1")

    def test_export_rejects_changed_selection(self):
        path = self.selection()
        value = json.loads(path.read_text())
        value["candidateSha256"] = "0" * 64
        path.write_text(json.dumps(value))
        with self.assertRaises(ValueError):
            acceptance.export(self.work, path, self.formula, self.root / "bad-package", "bad")


if __name__ == "__main__":
    unittest.main()
