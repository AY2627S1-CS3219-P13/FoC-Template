"""Wait for real services before running the HTTP/integration suites."""

import os
import time
import unittest
from uuid import uuid4

from tests.integration.test_stack import BASES, FALLBACK, LEGACY, MODEL, call, quote_body


def main():
    for base in [*BASES, LEGACY, MODEL, FALLBACK]:
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            try:
                if call(base, "/readyz")[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(0.2)
        else:
            raise RuntimeError(f"Pricing test service did not become ready: {base}")
    # Readiness intentionally does not require Order availability. Wait separately
    # for a fresh model calculation before asserting its integration behaviour.
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        status, result = call(MODEL, "/internal/v1/price-quotes", quote_body(),
                              token=os.environ["PRICING_SERVICE_TOKEN"], key=str(uuid4()))
        if status == 200 and result["pricingMode"] == "model":
            break
        time.sleep(0.2)
    else:
        raise RuntimeError("Model test service never received fresh Order context")
    suite = unittest.defaultTestLoader.loadTestsFromNames([
        "tests.framework_conformance", "tests.integration.test_stack",
    ])
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    if not result.wasSuccessful():
        raise SystemExit(1)


if __name__ == "__main__":
    main()
