"""Synthetic order generators: determinism, invariants and shared world."""
from datetime import datetime
from pathlib import Path
import random
import tempfile
import unittest

from formula.build import build
from pricing_mlops import pipeline
from pricing_mlops.sim import agents, dataset, graph, records
from pricing_mlops.sim.world import World

COMMIT = "d" * 40


def instant(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


class SimulatorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.formula = Path(cls.temp.name) / "formula"
        build(cls.formula, COMMIT, "sim-test")
        cls.baseline = pipeline.baseline_module(cls.formula).load_policy(cls.formula)
        cls.world = World()
        cls.shared = records.shared_context(cls.world, cls.formula)

    def build(self, seed=7):
        return dataset.build(seed, self.baseline, self.formula, graph_rows=500, graph_batches=5,
                             agent_days=5, agent_batches=5)

    def test_world_covers_catalogue_and_traffic(self):
        self.assertEqual(len(self.world.ids), 15)
        self.assertEqual(set(self.world.ids), set(self.world.traffic_index))
        theta = self.world.draw_theta(random.Random(1))
        for name, prior in self.world.priors.items():
            self.assertTrue(prior["low"] <= theta[name] <= prior["high"], name)

    def test_supply_link_zero_ignores_foot_traffic(self):
        flat = {self.world.supply_weight(l, h, 0) for l in self.world.ids for h in range(24)}
        self.assertEqual(len(flat), 1)
        self.assertNotEqual(self.world.supply_weight("frontier", 12, 1),
                            self.world.supply_weight("innovation4-0", 12, 1))

    def test_dataset_is_deterministic_and_batches_never_share_splits(self):
        first, _, manifest = self.build()
        second, _, again = self.build()
        self.assertEqual(manifest["datasetId"], again["datasetId"])
        self.assertEqual(first, second)
        splits = {}
        for record in first:
            splits.setdefault(record["thetaId"], set()).add(record["split"])
        self.assertTrue(all(len(value) == 1 for value in splits.values()))
        self.assertEqual({r["generator"] for r in first}, {graph.NAME, agents.NAME})
        self.assertTrue(all(r["provenance"] == "synthetic" for r in first))

    def test_records_are_valid_policy_inputs_with_bounded_offers(self):
        rows, _, _ = self.build()
        for record in rows[:200] + rows[-200:]:
            self.assertTrue(5 <= record["offeredCredits"] <= 30)
            self.assertIn(record["outcome"], records.OUTCOMES)
            result = self.baseline.calculate(records.policy_input(record, self.shared))
            self.assertTrue(5 <= result.recommended_credits <= 30)

    def test_agent_acceptance_happens_before_expiry(self):
        rng = random.Random(3)
        theta = self.world.draw_theta(rng)
        rows = agents.generate(rng, self.world, theta, self.baseline, self.shared, days=2, first_day=0,
                               scenario="t", split="train")
        accepted = [r for r in rows if r["outcome"] == "accepted"]
        self.assertTrue(accepted)
        for record in accepted:
            self.assertLessEqual(instant(record["calculatedAt"]), instant(record["acceptedAt"]))
            self.assertLess(instant(record["acceptedAt"]), instant(record["expiresAt"]))
        self.assertTrue(all("acceptedAt" not in r for r in rows if r["outcome"] != "accepted"))

    def test_written_dataset_is_verified_and_never_overwritten(self):
        rows, shared, manifest = self.build()
        output = Path(self.temp.name) / "dataset"
        dataset.write(output, rows, shared, manifest)
        self.assertEqual(dataset.read(output)[2], manifest)
        with self.assertRaises(FileExistsError):
            dataset.write(output, rows, shared, manifest)
        tampered = (output / "records.json").read_text().replace('"accepted"', '"expired"', 1)
        (output / "records.json").write_text(tampered)
        with self.assertRaises(ValueError):
            dataset.read(output)


if __name__ == "__main__":
    unittest.main()
