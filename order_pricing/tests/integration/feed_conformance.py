"""Real Pricing feed -> durable local training archive, in the MLOps image."""

from datetime import datetime, timezone
import json
import os
from pathlib import Path
import tempfile
import unittest
from urllib.request import Request, urlopen
from uuid import uuid4

from pricing_mlops.feed import FeedError, connect, cursor, poll


class TrainingFeedIntegrationTests(unittest.TestCase):
    def test_poll_resume_and_failed_auth_do_not_lose_records(self):
        base = os.environ["PRICING_BASE_URL"]
        token = os.environ["PRICING_EXPORT_TOKEN"]
        with tempfile.TemporaryDirectory() as directory:
            database = Path(directory) / "archive.sqlite"
            for _ in range(100):
                result = poll(database, base, token, allow_insecure_local=True)
                if not result["hasMore"]:
                    break
            else:
                self.fail("fixture archive failed to catch up")
            with connect(database) as connection:
                original_cursor = cursor(connection)
                original_count = connection.execute("SELECT COUNT(*) FROM events").fetchone()[0]
            self.assertGreater(original_count, 0)
            repeated = poll(database, base, token, allow_insecure_local=True)
            self.assertEqual(repeated["accepted"], 0)
            self.assertEqual(repeated["cursor"], original_cursor)
            with self.assertRaises(FeedError):
                poll(database, base, "invalid-fixture-token", allow_insecure_local=True)
            with connect(database) as connection:
                self.assertEqual(cursor(connection), original_cursor)
                self.assertEqual(connection.execute("SELECT COUNT(*) FROM events").fetchone()[0], original_count)

            event = {
                "eventId": str(uuid4()), "source": "order", "type": "QuoteDisplayed",
                "schemaVersion": 1, "occurredAt": datetime.now(timezone.utc).isoformat(),
                "data": {"quoteId": str(uuid4()), "draft": {"id": str(uuid4()), "revision": 1}},
            }
            request = Request(base + "/internal/v1/training-events",
                              data=json.dumps({"events": [event]}).encode(),
                              headers={"Content-Type": "application/json",
                                       "Authorization": "Bearer " + os.environ["PRICING_ORDER_WRITE_TOKEN"]})
            with urlopen(request, timeout=3) as response:
                self.assertEqual(response.status, 200)
            resumed = poll(database, base, token, allow_insecure_local=True)
            self.assertEqual(resumed["accepted"], 1)
            self.assertNotEqual(resumed["cursor"], original_cursor)
            with connect(database) as connection:
                self.assertEqual(connection.execute("SELECT COUNT(*) FROM events").fetchone()[0], original_count + 1)
                self.assertEqual(connection.execute("SELECT COUNT(*) FROM events WHERE event_id=?", (event["eventId"],)).fetchone()[0], 1)


if __name__ == "__main__":
    unittest.main()
