FROM golang:1.27.1-bookworm AS development
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM development AS build
RUN CGO_ENABLED=0 go build -trimpath -o /user-service ./cmd/server

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /user-service /usr/local/bin/user-service
USER 10001:10001
EXPOSE 8080 8081
ENTRYPOINT ["user-service"]
