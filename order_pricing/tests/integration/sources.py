"""Synthetic Supplier/Order HTTP projections; never a production data source."""

from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os

from tests.support import load_fixture


def utc(value):
    return value.isoformat().replace("+00:00", "Z")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def respond(self, status, body):
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path == "/healthz":
            self.respond(200, {"status": "ok"})
            return
        if self.path.startswith("/unavailable/"):
            self.respond(503, {"error": {"code": "fixture_unavailable", "message": "Unavailable."}})
            return
        if self.headers.get("Authorization") != "Bearer " + os.environ["SOURCE_TOKEN"]:
            self.respond(401, {"error": {"code": "invalid_service_credential", "message": "Unauthorized."}})
            return
        now = datetime.now(timezone.utc)
        fixture = load_fixture("policy-input.json")["context"]
        snapshot_id = "synthetic-" + now.strftime("%Y%m%dT%H%M%S.%f")
        if self.path == "/internal/v1/pricing-catalogue":
            self.respond(200, {
                "schemaVersion": 1, "snapshotId": snapshot_id, "asOf": utc(now),
                **fixture["catalogue"], "futureCatalogueField": "ignored",
            })
        elif self.path == "/internal/v1/pricing-context":
            self.respond(200, {
                "schemaVersion": 1, "snapshotId": snapshot_id, "asOf": utc(now),
                "windowStart": utc(now - timedelta(minutes=15)), "windowEnd": utc(now),
                "areas": fixture["orderContext"]["areas"], "futureOrderField": "ignored",
            })
        else:
            self.respond(404, {"error": {"code": "not_found", "message": "Not found."}})


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8090), Handler).serve_forever()
