#!/bin/sh
set -eu
python -m unittest discover -s tests/formula -v
python -m formula.build --output /tmp/formula-test-package --code-commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --release-id formula-conformance
export PRICING_PACKAGE_DIR=/tmp/formula-test-package
python -m unittest tests.test_policy_conformance tests.formula_conformance -v
