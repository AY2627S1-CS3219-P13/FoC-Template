#!/bin/sh
set -eu
if [ -n "$(gofmt -l cmd internal)" ]; then
  echo 'Run gofmt on cmd and internal before committing.'
  exit 1
fi
go vet ./...
go test -race -count=1 ./...
go build -o /tmp/foc-user-service ./cmd/server
