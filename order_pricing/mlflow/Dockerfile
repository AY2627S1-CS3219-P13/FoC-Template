FROM python:3.13.9-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/app/contracts/python:/app/mlflow:/app \
    HOME=/tmp \
    USER=pricing-training \
    GIT_PYTHON_REFRESH=quiet \
    MLFLOW_ENABLE_ARTIFACTS_PROGRESS_BAR=false
WORKDIR /app
COPY mlflow/requirements.lock /tmp/requirements.lock
COPY tests/requirements.lock /tmp/test-requirements.lock
RUN pip install --no-cache-dir -r /tmp/requirements.lock -r /tmp/test-requirements.lock \
    && mkdir /data && chown 65532:65532 /data
COPY contracts ./contracts
COPY mlflow ./mlflow
COPY formula ./formula
COPY tests ./tests
USER 65532:65532
CMD ["python", "-m", "pricing_mlops", "--help"]
