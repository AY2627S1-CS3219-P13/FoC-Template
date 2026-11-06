import copy
import importlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

from formula.build import build
from pricing_mlops import pipeline
from tests.support import decode_input, load_fixture, validate_package, validate_result

COMMIT = "d" * 40


class DatasetTests(unittest.TestCase):
    def test_deterministic_dataset_and_disjoint_scenarios(self):
        from formula.policy.formula import DEFAULT_CONFIG
        rows, manifest = pipeline.generate_dataset(42, 200, DEFAULT_CONFIG)
        self.assertEqual((rows, manifest), pipeline.generate_dataset(42, 200, DEFAULT_CONFIG))
        training = {r["scenarioId"] for r in rows if r["split"] == "train"}
        validation = {r["scenarioId"] for r in rows if r["split"] == "validation"}
        self.assertFalse(training & validation)
        learned = pipeline.fit(rows)
        self.assertAlmostEqual(learned[1], 0.25)
        self.assertAlmostEqual(learned[2], 1.0)
        self.assertTrue(all(r["provenance"] == "synthetic" for r in rows))


class PipelineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.formula = cls.root / "formula"
        build(cls.formula, COMMIT, "formula-test")
        cls.result = pipeline.train(cls.root, cls.formula, COMMIT, seed=42, count=200)

    def selection(self):
        path = self.root / (next(tempfile._get_candidate_names()) + ".selection.json")
        pipeline.select(self.root, self.result["runId"], path)
        return path

    def test_actual_mlflow_run_and_reproducible_training(self):
        import mlflow
        run = mlflow.tracking.MlflowClient().get_run(self.result["runId"])
        self.assertEqual(run.info.status, "FINISHED")
        self.assertEqual(run.data.tags["provenance"], "synthetic")
        second = pipeline.train(self.root, self.formula, COMMIT, seed=42, count=200)
        first_candidate = pipeline.read_candidate(self.root, self.result["runId"])
        second_candidate = pipeline.read_candidate(self.root, second["runId"])
        self.assertEqual(first_candidate["model"], second_candidate["model"])
        self.assertEqual(first_candidate["dataset"], second_candidate["dataset"])

    def test_selection_requires_exact_run_and_is_not_overwritten(self):
        path = self.selection()
        with self.assertRaises(FileExistsError):
            pipeline.select(self.root, self.result["runId"], path)
        with self.assertRaises(ValueError):
            pipeline.select(self.root, "latest", self.root / "invalid-selection")
        value = json.loads(path.read_text())
        self.assertFalse(value["productionApproved"])

    def test_rejected_thresholds_and_candidate_integrity(self):
        with self.assertRaises(ValueError):
            pipeline.select(self.root, self.result["runId"], self.root / "invalid", maximum_mae=-1)
        run_dir = self.root / "candidates" / self.result["runId"]
        data_path = run_dir / "dataset.json"
        original = data_path.read_text()
        data_path.write_text("[]")
        try:
            with self.assertRaisesRegex(ValueError, "dataset integrity"):
                pipeline.read_candidate(self.root, self.result["runId"])
        finally:
            data_path.write_text(original)

    def test_export_adapter_and_training_serving_feature_parity(self):
        selection = self.selection()
        output = self.root / (next(tempfile._get_candidate_names()) + "-package")
        manifest = pipeline.export(self.root, selection, self.formula, output, "model-test-1")
        validate_package(output)
        sys.path.insert(0, str(output))
        self.addCleanup(sys.path.remove, str(output))
        for name in list(sys.modules):
            if name == "policy" or name.startswith("policy."):
                del sys.modules[name]
        model_module = importlib.import_module("policy.model")
        policy = model_module.load_policy(output)
        self.addCleanup(policy.close)
        value = decode_input(load_fixture("policy-input.json"))
        policy.warm_up(value)
        result = policy.calculate(value)
        validate_result(result, manifest)
        self.assertEqual(result.pricing_mode, "model")
        features = model_module.effort_features(value, policy.baseline.config, policy.baseline.traffic)
        raw = model_module.predict_raw(policy.model, features)
        self.assertAlmostEqual(raw, features["unroundedCredits"])
        self.assertEqual(result.recommended_credits, model_module.price(raw * features["trafficMultiplier"]))
        self.assertEqual(model_module.feature_vector(features), [features["travelMinutes"], features["handlingCredits"]])
        self.assertEqual(policy.calculate(value), result)
        candidate = pipeline.read_candidate(self.root, self.result["runId"])
        rows, _ = pipeline.generate_dataset(42, 200, candidate["dataset"]["config"])
        for row in rows[:30]:
            body = copy.deepcopy(load_fixture("policy-input.json"))
            body["context"]["routes"][0]["walkableMetres"] = row["walkableMetres"]
            if row["quantity"] is None:
                del body["request"]["totalItemQuantity"]
            else:
                body["request"]["totalItemQuantity"] = row["quantity"]
            features = model_module.effort_features(decode_input(body), policy.baseline.config)
            for actual, expected in zip(model_module.feature_vector(features), row["features"]):
                self.assertAlmostEqual(actual, expected)
            self.assertAlmostEqual(features["unroundedCredits"], row["targetUnroundedCredits"])
        for state in ("stale", "unavailable"):
            body = copy.deepcopy(load_fixture("policy-input.json"))
            body["context"]["orderContext"]["state"] = state
            fallback = policy.calculate(decode_input(body) if state == "stale" else self.unavailable(body))
            self.assertEqual(fallback.pricing_mode, "baseline")
            validate_result(fallback, manifest)
        with self.assertRaises(FileExistsError):
            pipeline.export(self.root, selection, self.formula, output, "model-test-1")

    def unavailable(self, body):
        body["context"]["orderContext"] = {"state": "unavailable"}
        return decode_input(body)

    def test_export_rejects_changed_selection(self):
        path = self.selection()
        value = json.loads(path.read_text())
        value["candidateSha256"] = "0" * 64
        path.write_text(json.dumps(value))
        with self.assertRaises(ValueError):
            pipeline.export(self.root, path, self.formula, self.root / "bad-package", "bad")
