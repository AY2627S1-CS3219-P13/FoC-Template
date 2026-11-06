FROM python:3.13.9-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/checks/contracts/python:/checks

WORKDIR /checks
COPY tests/requirements.lock /tmp/requirements.lock
RUN pip install --no-cache-dir -r /tmp/requirements.lock
COPY contracts ./contracts
COPY tests ./tests
USER 65532:65532

CMD ["python", "-m", "unittest", "tests.test_contracts", "tests.test_exports", "tests.test_policy_conformance", "-v"]
