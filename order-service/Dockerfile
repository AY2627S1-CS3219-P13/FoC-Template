FROM golang:1.27.1-bookworm AS development
WORKDIR /app
COPY order-service/go.mod order-service/go.sum ./
COPY schedule-order/ /schedule-order/
RUN go mod download
COPY order-service/ .

FROM development AS build
RUN CGO_ENABLED=0 go build -trimpath -o /order-service ./cmd/server

FROM mcr.microsoft.com/playwright:v1.58.2-noble AS ui-tests
WORKDIR /app/ui-tests
COPY order-service/ui-tests/package*.json ./
RUN npm ci
COPY order-service/ui-tests/ ./
COPY order-service/internal/order/web/ /app/internal/order/web/
CMD ["npm", "test"]

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /order-service /usr/local/bin/order-service
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["order-service"]
