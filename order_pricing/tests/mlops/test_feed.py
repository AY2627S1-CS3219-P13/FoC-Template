import copy
from io import BytesIO
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

from pricing_mlops import feed


def event():
    return {"eventId": "44444444-4444-4444-8444-444444444444", "source": "order",
            "type": "QuoteDisplayed", "schemaVersion": 1,
            "occurredAt": "2026-11-05T17:00:00Z", "recordedAt": "2026-11-05T17:00:01Z",
            "data": {"quoteId": "33333333-3333-4333-8333-333333333333",
                     "draft": {"id": "11111111-1111-4111-8111-111111111111", "revision": 4}}}


class FeedTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / "events.sqlite"
        self.connection = feed.connect(self.path)
        self.addCleanup(self.connection.close)

    def ingest(self, events, next_cursor, current=None):
        return feed.ingest(self.connection, {"events": events, "nextCursor": next_cursor, "hasMore": False}, current)

    def test_resume_and_dedup_with_atomic_cursor(self):
        self.ingest([event()], "opaque-1")
        second = feed.connect(self.path)
        try:
            self.assertEqual(feed.cursor(second), "opaque-1")
        finally:
            second.close()
        result = self.ingest([event()], "opaque-2", "opaque-1")
        self.assertEqual(result["duplicates"], 1)
        self.assertEqual(self.connection.execute("SELECT count(*) FROM events").fetchone()[0], 1)

    def test_future_unknown_fields_do_not_create_duplicate_conflict(self):
        self.ingest([event()], "opaque-1")
        future = event()
        future["futureEnvelope"] = "ignored"
        future["data"]["newField"] = 999
        future["data"]["draft"]["newField"] = 999
        result = self.ingest([future], "opaque-2", "opaque-1")
        self.assertEqual(result["duplicates"], 1)

    def test_conflict_and_late_invalid_event_roll_back_whole_page(self):
        self.ingest([event()], "opaque-1")
        conflict = event()
        conflict["data"]["draft"]["revision"] = 5
        new = event()
        new["eventId"] = "55555555-5555-4555-8555-555555555555"
        with self.assertRaises(feed.FeedError):
            self.ingest([new, conflict], "opaque-2", "opaque-1")
        self.assertEqual(feed.cursor(self.connection), "opaque-1")
        self.assertEqual(self.connection.execute("SELECT count(*) FROM events").fetchone()[0], 1)

    def test_disk_write_failure_does_not_advance_cursor(self):
        self.connection.execute("CREATE TRIGGER reject_events BEFORE INSERT ON events BEGIN SELECT RAISE(FAIL, 'injected'); END")
        self.connection.commit()
        with self.assertRaises(sqlite3.DatabaseError):
            self.ingest([event()], "opaque-1")
        self.assertIsNone(feed.cursor(self.connection))

    def test_unknown_future_status_is_archived_and_quarantined(self):
        value = event()
        value["type"] = "PricingOrderOutcome"
        value["data"] = {"status": "future-status"}
        self.ingest([value], "opaque-1")
        self.assertEqual(self.connection.execute("SELECT quarantined FROM events").fetchone()[0], 1)

    def test_empty_page_cannot_skip_history(self):
        with self.assertRaises(feed.FeedError):
            self.ingest([], "skipped")
        self.ingest([], None)
        self.assertIsNone(feed.cursor(self.connection))

    def test_stale_local_cursor_is_rejected(self):
        self.ingest([event()], "opaque-1")
        with self.assertRaises(feed.FeedError):
            self.ingest([event()], "opaque-2", None)

    def test_expired_remote_cursor_requires_reconciliation(self):
        error = HTTPError("http://pricing", 410, "expired", {}, BytesIO())
        with patch("pricing_mlops.feed.urlopen", side_effect=error):
            with self.assertRaises(feed.ReconciliationRequired):
                feed.poll(self.path, "http://pricing", "not-a-real-token", allow_insecure_local=True)
        self.assertIsNone(feed.cursor(self.connection))

    def test_tls_and_credentials_required(self):
        with self.assertRaises(feed.FeedError):
            feed.poll(self.path, "http://untrusted.example", "credential")
        with self.assertRaises(feed.FeedError):
            feed.poll(self.path, "https://example.invalid", "")
