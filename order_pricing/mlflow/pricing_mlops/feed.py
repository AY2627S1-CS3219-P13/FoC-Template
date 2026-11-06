"""Durable, at-least-once training feed archive. Does not manufacture labels."""

from datetime import datetime
from contextlib import closing
import hashlib
import json
from pathlib import Path
import sqlite3
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import Request, urlopen
from uuid import UUID


class FeedError(Exception):
    pass


class ReconciliationRequired(FeedError):
    pass


def connect(database):
    Path(database).parent.mkdir(parents=True, exist_ok=True)
    connection = sqlite3.connect(database)
    connection.execute("PRAGMA journal_mode=WAL")
    connection.execute("PRAGMA synchronous=FULL")
    connection.executescript("""
      CREATE TABLE IF NOT EXISTS feed_state (id INTEGER PRIMARY KEY CHECK(id=1), cursor TEXT);
      INSERT OR IGNORE INTO feed_state VALUES (1,NULL);
      CREATE TABLE IF NOT EXISTS events (
        source TEXT NOT NULL, event_id TEXT NOT NULL, payload TEXT NOT NULL,
        fingerprint TEXT NOT NULL, quarantined INTEGER NOT NULL,
        PRIMARY KEY(source,event_id));
    """)
    connection.commit()
    return connection


def cursor(connection):
    return connection.execute("SELECT cursor FROM feed_state WHERE id=1").fetchone()[0]


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False)


def validate_event(event):
    if not isinstance(event, dict) or not {
        "eventId", "source", "type", "schemaVersion", "occurredAt", "data", "recordedAt"
    } <= event.keys():
        raise FeedError("invalid event envelope")
    try:
        if str(UUID(event["eventId"])) != event["eventId"]:
            raise ValueError()
        for key in ("occurredAt", "recordedAt"):
            if datetime.fromisoformat(event[key].replace("Z", "+00:00")).tzinfo is None:
                raise ValueError()
    except (ValueError, AttributeError, TypeError) as error:
        raise FeedError("invalid event identity/time") from error
    if (event["source"] not in ("order", "pricing") or not isinstance(event["data"], dict)
        or not isinstance(event["type"], str) or type(event["schemaVersion"]) is not int):
        raise FeedError("invalid event source/data")
    known_types = {"QuoteCalculated", "QuoteDisplayed", "OfferConfirmed", "PricingOrderCreated", "PricingOrderOutcome"}
    quarantined = event["type"] not in known_types or event["schemaVersion"] != 1
    statuses = {"open", "accepted", "picked up", "delivered", "dispute", "completed",
                "cancelled", "expired", "not completed"}
    if event["type"] == "PricingOrderOutcome" and event["data"].get("status") not in statuses:
        quarantined = True
    return quarantined


def normalized_event(event):
    """Ignore additive fields for dedup; preserve model-owned feature snapshots."""
    draft = {"id": None, "revision": None}
    place = {"type": None, "id": None}
    resolved_place = {**place, "locationId": None}
    request = {"draft": draft, "pickup": place, "delivery": place,
               "orderExpiresAt": None, "totalItemQuantity": None}
    response = {"quoteId": None, "draft": draft, "recommendedCredits": None,
                "pricingMode": None, "quotedAt": None, "validUntil": None,
                "releaseId": None, "contextSnapshotId": None, "warnings": None}
    data_shapes = {
        "QuoteCalculated": {"quoteId": None, "request": request, "response": response,
                            "releaseManifestId": None, "featureSchemaVersion": None,
                            "featureSnapshot": None, "contextSources": [
                                {"source": None, "snapshotId": None, "asOf": None}],
                            "inferenceReused": None},
        "QuoteDisplayed": {"quoteId": None, "draft": draft},
        "OfferConfirmed": {"draft": draft, "offeredCredits": None, "quoteId": None},
        "PricingOrderCreated": {"orderId": None, "orderVersion": None, "createdAt": None,
                                "expiresAt": None, "pickup": resolved_place, "delivery": resolved_place,
                                "totalItemQuantity": None, "offeredCredits": None,
                                "pricingQuoteId": None, "pricingDraft": draft},
        "PricingOrderOutcome": {"orderId": None, "orderVersion": None, "status": None,
                                "offeredCredits": None, "pricingQuoteId": None},
    }

    def project(value, shape):
        if value is None or shape is None:
            return value
        if isinstance(shape, list) and isinstance(value, list):
            return [project(v, shape[0]) for v in value]
        if isinstance(shape, dict) and isinstance(value, dict):
            return {k: project(v, shape[k]) for k, v in value.items() if k in shape}
        return value

    envelope = {key: event[key] for key in ("eventId", "source", "type", "schemaVersion", "occurredAt")}
    shape = data_shapes.get(event["type"]) if event["schemaVersion"] == 1 else None
    envelope["data"] = project(event["data"], shape)
    return envelope


def ingest(connection, response, expected_cursor=None):
    if not isinstance(response, dict) or not {"events", "nextCursor", "hasMore"} <= response.keys():
        raise FeedError("invalid feed response")
    events, next_cursor = response["events"], response["nextCursor"]
    if (not isinstance(events, list) or len(events) > 100 or type(response["hasMore"]) is not bool
        or (next_cursor is not None and not isinstance(next_cursor, str))):
        raise FeedError("invalid feed response")
    if events and (next_cursor is None or next_cursor == expected_cursor):
        raise FeedError("feed did not provide a progress cursor")
    if not events and next_cursor != expected_cursor:
        raise FeedError("empty feed unexpectedly advanced cursor")
    accepted, duplicates = 0, 0
    # Cursor comparison and all writes share one SQLite transaction. Competing
    # consumers cannot overwrite a cursor advanced by another ingestion.
    connection.execute("BEGIN IMMEDIATE")
    try:
        if cursor(connection) != expected_cursor:
            raise FeedError("local cursor changed; fetch again")
        for event in events:
            quarantined = validate_event(event)
            payload = canonical(event)
            fingerprint = hashlib.sha256(canonical(normalized_event(event)).encode()).hexdigest()
            existing = connection.execute(
                "SELECT fingerprint FROM events WHERE source=? AND event_id=?",
                (event["source"], event["eventId"]),
            ).fetchone()
            if existing:
                if existing[0] != fingerprint:
                    raise FeedError("conflicting immutable training event")
                duplicates += 1
            else:
                connection.execute("INSERT INTO events VALUES(?,?,?,?,?)", (
                    event["source"], event["eventId"], payload, fingerprint, int(quarantined)
                ))
                accepted += 1
        connection.execute("UPDATE feed_state SET cursor=? WHERE id=1", (next_cursor,))
        connection.commit()
    except BaseException:
        connection.rollback()
        raise
    return {"accepted": accepted, "duplicates": duplicates, "cursor": next_cursor,
            "hasMore": response["hasMore"]}


def poll(database, base_url, token, *, allow_insecure_local=False, page_size=1,
         max_response_bytes=64 * 1024 * 1024):
    url = urlsplit(base_url)
    if url.scheme not in ("http", "https") or not url.netloc or url.username or url.password:
        raise FeedError("invalid training feed URL")
    if url.scheme != "https" and not allow_insecure_local:
        raise FeedError("TLS required; explicitly opt in to HTTP for a private local test network")
    if not token or any(c in token for c in "\r\n"):
        raise FeedError("export credential required")
    if type(page_size) is not int or not 1 <= page_size <= 100 or type(max_response_bytes) is not int or max_response_bytes <= 0:
        raise FeedError("invalid page/response bounds")
    with closing(connect(database)) as connection:
        after = cursor(connection)
        query = {"limit": page_size}
        if after is not None:
            query["after"] = after
        request = Request(base_url.rstrip("/") + "/internal/v1/training-events?" + urlencode(query),
                          headers={"Authorization": "Bearer " + token})
        try:
            with urlopen(request, timeout=5) as response:
                raw = response.read(max_response_bytes + 1)
                if len(raw) > max_response_bytes:
                    raise FeedError("training feed response too large")
                page = json.loads(raw)
        except HTTPError as error:
            if error.code == 410:
                raise ReconciliationRequired("cursor expired; reconcile archived history before resuming") from None
            raise FeedError("training feed request failed") from None
        except (URLError, TimeoutError, ValueError):
            raise FeedError("training feed request failed") from None
        return ingest(connection, page, expected_cursor=after)
