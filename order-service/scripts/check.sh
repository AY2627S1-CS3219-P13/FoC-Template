#!/bin/sh
set -eu
: "${ORDER_TEST_DATABASE_URL:?Set the dedicated order_service_test database URL; database tests must not be skipped}"
if [ -n "$(gofmt -l cmd internal)" ]; then
  echo 'Run gofmt on cmd and internal before committing.'
  exit 1
fi
go vet ./...
go test -race -count=1 ./...
go build -o /tmp/foc-order-service ./cmd/server
