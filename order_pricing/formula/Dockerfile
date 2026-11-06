FROM python:3.13.9-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHONPATH=/checks/contracts/python:/checks

WORKDIR /checks
COPY tests/requirements.lock /tmp/requirements.lock
RUN pip install --no-cache-dir -r /tmp/requirements.lock
COPY contracts ./contracts
COPY tests ./tests
COPY formula ./formula
USER 65532:65532

CMD ["sh", "formula/check.sh"]
