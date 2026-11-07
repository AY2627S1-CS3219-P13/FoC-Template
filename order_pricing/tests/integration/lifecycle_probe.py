"""Controlled restart/recording-failure probes for the isolated test stack."""

import json
import os
from pathlib import Path
import sys
import time
from uuid import uuid4

from tests.integration.test_stack import BASES, call, quote_body


def quote(body, key):
    return call(BASES[0], "/internal/v1/price-quotes", body,
                token=os.environ["PRICING_SERVICE_TOKEN"], key=key)


def main():
    phase = sys.argv[1]
    if phase in ("prepare", "recording-failure"):
        body, key = quote_body(), str(uuid4())
        status, result = quote(body, key)
        if phase == "prepare":
            if status != 200:
                raise AssertionError(f"initial issuance returned {status}")
        elif status != 503 or result["error"]["code"] != "quote_recording_unavailable":
            raise AssertionError(f"recording failure was not rejected: {status}")
        print(json.dumps({"body": body, "key": key, "response": result}))
        return
    state = json.loads(Path(sys.argv[2]).read_text())
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            if call(BASES[0], "/readyz")[0] == 200:
                break
        except OSError:
            pass
        time.sleep(0.2)
    status, response = quote(state["body"], state["key"])
    if status != 200:
        raise AssertionError(f"recovered issuance returned {status}")
    if phase == "verify-replay":
        if response != state["response"]:
            raise AssertionError("restart changed a durable quote replay")
    elif phase != "verify-recovery":
        raise ValueError("unknown lifecycle probe")
    print("Durable replay verified." if phase == "verify-replay" else "Recording recovery verified.")


if __name__ == "__main__":
    main()
