"""Observed Order/Pricing exports: labels, quote links, context and mixed training."""
import csv
from datetime import timedelta
import json
from pathlib import Path
import tempfile
import unittest

from formula.build import build
from pricing_mlops import acceptance, observed
from pricing_mlops.sim import dataset

COMMIT = "f" * 40
DEMO = Path(observed.__file__).resolve().parents[1] / "demo-observed"
SUPPLIER = "11111111-1111-4111-8111-111111111111"


def order(**changes):
    row = {
        "order_id": "order-1", "created_at": "2026-11-02 04:00:00+00", "expires_at": "2026-11-02 04:30:00+00",
        "pickup_type": "supplier", "pickup_id": SUPPLIER, "pickup_location_id": "com2",
        "delivery_type": "location", "delivery_id": "utown", "delivery_location_id": "utown",
        "total_item_quantity": "2", "offered_credits": "9", "final_status": "expired",
        "accepted_at": "", "cancelled_at": "",
    }
    row.update(changes)
    return observed._order(row)


def quote(at, draft="d1", revision=1, quantity=2, pickup=SUPPLIER):
    request = {"draft": {"id": draft, "revision": revision}, "pickup": {"type": "supplier", "id": pickup},
               "delivery": {"type": "location", "id": "utown"}, "totalItemQuantity": quantity}
    return {"source": "pricing", "type": "QuoteCalculated", "schemaVersion": 1,
            "occurredAt": observed.stamp(at),
            "data": {"request": request, "response": {"quoteId": f"{draft}-{revision}", "recommendedCredits": 9,
                                                      "pricingMode": "baseline", "releaseId": "r"}}}


class LabelAndContextTests(unittest.TestCase):
    def test_labels_follow_the_simulator(self):
        self.assertEqual(observed.outcome(order(accepted_at="2026-11-02 04:10:00+00", final_status="completed")),
                         "accepted")
        self.assertIsNone(observed.outcome(order(accepted_at="2026-11-02 05:00:00+00")))
        self.assertEqual(observed.outcome(order(cancelled_at="2026-11-02 04:05:00+00", final_status="cancelled")),
                         "cancelled")
        self.assertEqual(observed.outcome(order()), "expired")
        self.assertIsNone(observed.outcome(order(final_status="open")))

    def test_csv_header_is_checked(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "orders.csv"
            path.write_text("order_id,created_at\nx,2026-11-02 04:00:00+00\n")
            with self.assertRaisesRegex(ValueError, "header"):
                observed.read_orders(path)

    def test_timestamps_need_a_zone(self):
        with self.assertRaises(ValueError):
            order(created_at="2026-11-02 04:00:00")

    def test_context_matches_order_pricing_context_definition(self):
        target = order(order_id="t", created_at="2026-11-02 04:00:00+00", expires_at="2026-11-02 05:00:00+00")
        still_open = order(order_id="a", created_at="2026-11-02 03:00:00+00", expires_at="2026-11-02 04:30:00+00")
        accepted = order(order_id="b", created_at="2026-11-02 03:50:00+00", expires_at="2026-11-02 04:30:00+00",
                         accepted_at="2026-11-02 03:55:00+00")
        elsewhere = order(order_id="c", created_at="2026-11-02 03:58:00+00", pickup_location_id="utown",
                          delivery_id="com2", delivery_location_id="com2")
        later = order(order_id="d", created_at="2026-11-02 04:01:00+00", expires_at="2026-11-02 05:00:00+00")
        context = observed.order_context(target, [target, still_open, accepted, elsewhere, later])
        self.assertEqual(context["areas"], [{"locationId": "com2", "openRequestCount": 1, "createdOrderCount": 1}])
        self.assertEqual(context["state"], "fresh")
        self.assertEqual(context["windowEnd"], "2026-11-02T04:00:00Z")


class QuoteMatchTests(unittest.TestCase):
    def setUp(self):
        self.order = order()
        self.created = self.order["createdAt"]

    def test_latest_revision_of_one_draft_is_linked(self):
        quotes = [quote(self.created - timedelta(seconds=90), revision=1),
                  quote(self.created - timedelta(seconds=60), revision=2),
                  quote(self.created - timedelta(seconds=10), revision=1, quantity=1)]
        match, status = observed.match_quote(self.order, quotes)
        self.assertEqual(status, "matched")
        self.assertEqual(match["data"]["request"]["draft"]["revision"], 2)

    def test_two_drafts_make_the_link_ambiguous(self):
        quotes = [quote(self.created - timedelta(seconds=30)), quote(self.created - timedelta(seconds=20), "d2")]
        self.assertEqual(observed.match_quote(self.order, quotes), (None, "ambiguous"))

    def test_quotes_outside_the_window_or_route_are_ignored(self):
        quotes = [quote(self.created - timedelta(seconds=151)), quote(self.created + timedelta(seconds=1)),
                  quote(self.created - timedelta(seconds=5), pickup="22222222-2222-4222-8222-222222222222")]
        self.assertEqual(observed.match_quote(self.order, quotes), (None, "none"))


class MixedDatasetTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temp.cleanup)
        cls.root = Path(cls.temp.name)
        cls.formula = cls.root / "formula"
        build(cls.formula, COMMIT, "observed-test")
        cls.synthetic = cls.root / "synthetic"
        sizes = {"graph_rows": 2500, "graph_batches": 5, "agent_days": 10, "agent_batches": 5}
        acceptance.simulate(cls.synthetic, cls.formula, 11, **sizes)
        cls.mixed = cls.root / "mixed"
        cls.imported = observed.build(cls.synthetic, DEMO / "orders.csv", cls.mixed, DEMO / "pricing-events.jsonl")

    def test_demo_export_converts_with_a_report(self):
        report = self.imported["observed"]
        self.assertEqual(report["orders"], 100)
        self.assertEqual(report["skipped"], {"unknown_location": 1})
        self.assertEqual(sum(report["quotes"].values()), 99)
        self.assertGreater(report["quotes"]["matched"], 40)
        self.assertEqual(report["quotes"]["ambiguous"], 1)
        records, shared, manifest = dataset.read(self.mixed)
        self.assertTrue(manifest["datasetId"].startswith("mixed-acceptance-"))
        self.assertEqual(manifest["provenance"], "mixed")
        self.assertEqual(manifest["observedWeight"], 0.2)
        rows = [r for r in records if r["generator"] == observed.NAME]
        self.assertEqual(len(rows), 99)
        linked = [r for r in rows if r["quoteMatch"] == "matched"]
        self.assertTrue(all(r["orderContextSource"] == "quote" and "quote" in r for r in linked))
        suppliers = {row["id"] for row in shared["catalogue"]["suppliers"]}
        self.assertTrue({r["request"]["pickup"]["id"] for r in rows if r["request"]["pickup"]["type"] == "supplier"}
                        <= suppliers)

    def test_converted_rows_contain_no_identities_or_items(self):
        with (DEMO / "orders.csv").open(newline="") as stream:
            self.assertEqual(tuple(next(csv.reader(stream))), observed.CSV_COLUMNS)
        text = (self.mixed / "records.json").read_text()
        for word in ("requester", "courier", "actor", "itemName", "reason"):
            self.assertNotIn(word, text)

    def test_only_simulated_datasets_are_extended(self):
        with self.assertRaisesRegex(ValueError, "simulated"):
            observed.build(self.mixed, DEMO / "orders.csv", self.root / "twice")
        with self.assertRaisesRegex(ValueError, "between 0 and 1"):
            observed.build(self.synthetic, DEMO / "orders.csv", self.root / "heavy", observed_weight=1)

    def test_observed_rows_take_their_declared_weight(self):
        rows = [{"generator": "causal-graph"}] * 6 + [{"generator": "agent-based"}] * 2 + [{"generator": "observed"}] * 2
        weights = acceptance.balanced_weights(rows, 0.2)
        self.assertAlmostEqual(sum(weights), len(rows))
        self.assertAlmostEqual(sum(weights[-2:]) / sum(weights), 0.2)
        self.assertAlmostEqual(sum(weights[:6]), sum(weights[6:8]))

    def test_mixed_training_reports_observed_metrics_and_provenance(self):
        work = self.root / "work"
        result = acceptance.train(work, self.formula, COMMIT, self.mixed, seed=11)
        metrics = result["metrics"]
        self.assertEqual(metrics["observed_rows"], sum(
            1 for r in json.loads((self.mixed / "records.json").read_text())
            if r["generator"] == "observed" and r["split"] == "validation" and r["outcome"] != "cancelled"))
        for key in ("observed_log_loss", "observed_base_rate_log_loss", "observed_brier", "observed_actual_acceptance"):
            self.assertIn(key, metrics)
        candidate = acceptance.read_candidate(work, result["runId"])
        self.assertEqual(candidate["provenance"], "mixed")
        self.assertEqual(acceptance.gate_failures(metrics), [])
        selection = self.root / "mixed.selection.json"
        self.assertEqual(acceptance.select(work, result["runId"], selection)["scope"],
                         "synthetic-and-observed-acceptance")
        manifest = acceptance.export(work, selection, self.formula, self.root / "package", "mixed-test")
        self.assertEqual(manifest["provenance"]["calibration"], "synthetic-and-observed")
        self.assertEqual(json.loads((self.root / "package/artifacts/acceptance.json").read_text())["provenance"],
                         "mixed")


if __name__ == "__main__":
    unittest.main()
