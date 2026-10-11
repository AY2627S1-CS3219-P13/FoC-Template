FROM golang:1.27.1-bookworm AS development
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM development AS build
RUN CGO_ENABLED=0 go build -trimpath -o /order-service ./cmd/server

FROM mcr.microsoft.com/playwright:v1.58.2-noble AS ui-tests
WORKDIR /app/ui-tests
COPY ui-tests/package*.json ./
RUN npm ci
COPY ui-tests/ ./
COPY internal/order/web/ /app/internal/order/web/
CMD ["npm", "test"]

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /order-service /usr/local/bin/order-service
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["order-service"]
