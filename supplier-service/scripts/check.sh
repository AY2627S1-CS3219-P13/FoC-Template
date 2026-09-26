#!/bin/sh
set -eu
if [ -n "$(gofmt -l cmd internal)" ]; then
  echo 'Run gofmt on supplier-service/cmd and supplier-service/internal.'
  exit 1
fi
go vet ./...
go test -race -count=1 -timeout=90s ./...
go build -o /tmp/foc-supplier-service ./cmd/server
