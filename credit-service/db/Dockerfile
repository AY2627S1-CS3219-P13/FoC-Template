# PostgreSQL for Credit Service. The password is supplied at runtime, never baked in.
FROM postgres:18-bookworm
ENV POSTGRES_USER=foc_credit \
    POSTGRES_DB=foc_credit
HEALTHCHECK --interval=3s --timeout=3s --retries=20 \
    CMD pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"
