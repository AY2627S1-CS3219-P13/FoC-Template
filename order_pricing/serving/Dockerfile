FROM python:3.13.9-slim-bookworm AS runtime
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/serving:/app/contracts/python \
    PRICING_PACKAGE_DIR=/opt/policy
WORKDIR /app
COPY serving/requirements.lock /tmp/requirements.lock
RUN pip install --no-cache-dir --no-deps -r /tmp/requirements.lock && pip check
ARG POLICY_PACKAGE=formula/releases/effort-traffic-001
COPY ${POLICY_PACKAGE}/ /opt/policy/
RUN pip install --no-cache-dir --no-deps -r /opt/policy/requirements.lock && pip check
COPY contracts ./contracts
COPY serving ./serving
USER 65532:65532
EXPOSE 8080
CMD ["uvicorn", "pricing_server.app:app", "--host", "0.0.0.0", "--port", "8080", "--no-access-log", "--timeout-graceful-shutdown", "3"]

# Explicitly selected only by compose.pricing.yaml for local form exploration.
# The runtime target and immutable production packages stay unchanged.
FROM runtime AS local-demo
USER 0:0
COPY formula ./formula
COPY tests/fixtures/policy-input.json ./tests/fixtures/policy-input.json
RUN PYTHONPATH=/app:/app/contracts/python python serving/local_demo.py
ENV PRICING_PACKAGE_DIR=/opt/local-demo
USER 65532:65532

FROM runtime AS tests
ENV PYTHONPATH=/app/serving:/app/contracts/python:/app
COPY tests ./tests
CMD ["python", "-m", "unittest", "discover", "-s", "tests/serving", "-v"]
