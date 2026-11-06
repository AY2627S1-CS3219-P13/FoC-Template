import base64
import hashlib
from pathlib import Path

import psycopg
from psycopg.types.json import Jsonb

from .wire import APIError, stamp

FEED_LOCK = 667010231
MIGRATION_LOCK = 667010232


def key_lock(caller, key):
    return int.from_bytes(hashlib.sha256((caller + ":" + key).encode()).digest()[:8], signed=True)


class Storage:
    def __init__(self, database_url):
        self.database_url = database_url

    def connect(self):
        return psycopg.connect(self.database_url, connect_timeout=1, options="-c statement_timeout=1000 -c lock_timeout=400")

    def initialize(self):
        with self.connect() as conn:
            conn.execute("SELECT pg_advisory_xact_lock(%s)", (MIGRATION_LOCK,))
            conn.execute("CREATE TABLE IF NOT EXISTS pricing_schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())")
            for path in sorted((Path(__file__).parent / "migrations").glob("*.sql")):
                if not conn.execute("SELECT 1 FROM pricing_schema_migrations WHERE version=%s", (path.name,)).fetchone():
                    conn.execute(path.read_text())
                    conn.execute("INSERT INTO pricing_schema_migrations(version) VALUES (%s)", (path.name,))

    def healthy(self):
        with self.connect() as conn:
            conn.execute("SELECT caller, response FROM pricing_quotes LIMIT 0")
            conn.execute("SELECT position, payload FROM pricing_events LIMIT 0")
            row = conn.execute("SELECT has_table_privilege(current_user, 'pricing_quotes', 'INSERT') AND has_table_privilege(current_user, 'pricing_events', 'INSERT')").fetchone()
            return bool(row[0])

    @staticmethod
    def existing(conn, caller, key, fingerprint):
        row = conn.execute("SELECT fingerprint, response FROM pricing_quotes WHERE caller=%s AND idempotency_key=%s", (caller, key)).fetchone()
        if row:
            if row[0] != fingerprint:
                raise APIError(409, "idempotency_conflict")
            return row[1]
        return None

    def replay(self, caller, key, fingerprint):
        with self.connect() as conn:
            return self.existing(conn, caller, key, fingerprint)

    @staticmethod
    def append(conn, event):
        # Every writer acquires this lock and retains it until commit.
        conn.execute("SELECT pg_advisory_xact_lock(%s)", (FEED_LOCK,))
        row = conn.execute("SELECT payload FROM pricing_events WHERE source=%s AND event_id=%s", (event["source"], event["eventId"])).fetchone()
        if row:
            if row[0] != event:
                raise APIError(409, "event_conflict")
            return False
        conn.execute("INSERT INTO pricing_events(position, source, event_id, payload) SELECT COALESCE(MAX(position),0)+1, %s, %s, %s FROM pricing_events", (event["source"], event["eventId"], Jsonb(event)))
        return True

    def issue(self, caller, key, fingerprint, received, calculate):
        with self.connect() as conn:
            locked = conn.execute("SELECT pg_try_advisory_xact_lock(%s)", (key_lock(caller, key),)).fetchone()[0]
            if not locked:
                raise APIError(503, "quote_pending")
            replay = self.existing(conn, caller, key, fingerprint)
            if replay is not None:
                return replay
            response, event = calculate()
            conn.execute("INSERT INTO pricing_quotes(caller,idempotency_key,fingerprint,response,first_received_at) VALUES (%s,%s,%s,%s,%s)", (caller, key, fingerprint, Jsonb(response), received))
            self.append(conn, event)
        return response

    def ingest(self, events):
        accepted = duplicates = 0
        with self.connect() as conn:
            conn.execute("SELECT pg_advisory_xact_lock(%s)", (FEED_LOCK,))
            for event in events:
                if self.append(conn, event):
                    accepted += 1
                else:
                    duplicates += 1
        return {"accepted": accepted, "duplicates": duplicates}

    @staticmethod
    def cursor(position):
        return base64.urlsafe_b64encode(f"pricing-v1:{position}".encode()).decode().rstrip("=")

    def export(self, after, limit):
        position = 0
        if after is not None:
            try:
                raw = base64.urlsafe_b64decode(after + "=" * (-len(after) % 4)).decode()
                prefix, numeric = raw.split(":")
                position = int(numeric)
                if prefix != "pricing-v1" or not 1 <= position <= 9223372036854775807 or self.cursor(position) != after:
                    raise ValueError("invalid cursor")
            except (ValueError, UnicodeError):
                raise APIError(400, "invalid_cursor") from None
        with self.connect() as conn:
            if position:
                exists = conn.execute("SELECT 1 FROM pricing_events WHERE position=%s", (position,)).fetchone()
                if not exists:
                    oldest = conn.execute("SELECT MIN(position) FROM pricing_events").fetchone()[0]
                    raise APIError(410 if oldest and position < oldest else 400, "cursor_expired" if oldest and position < oldest else "invalid_cursor")
            rows = conn.execute("SELECT position,payload,recorded_at FROM pricing_events WHERE position>%s ORDER BY position LIMIT %s", (position, limit + 1)).fetchall()
        events = [dict(row[1], recordedAt=stamp(row[2])) for row in rows[:limit]]
        return {"events": events, "nextCursor": self.cursor(rows[min(len(rows), limit)-1][0]) if rows else after, "hasMore": len(rows) > limit}
