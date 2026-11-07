FROM golang:1.27.1-bookworm AS development
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["sh", "scripts/check.sh"]
