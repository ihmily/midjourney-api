# ---- Build Stage ----
FROM golang:1.23.2-alpine AS builder

RUN apk add --no-cache git

WORKDIR /app

# Copy dependency files first to leverage Docker layer caching
COPY go.mod go.sum ./
RUN go mod download

# Install the same swag version used by this module for reproducible docs generation.
RUN go install github.com/swaggo/swag/cmd/swag@v1.16.6

COPY . .

# Generate Swagger documentation
RUN swag init -g cmd/server/main.go -o docs

# Build with CGO disabled to produce a static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/server ./cmd/server

# ---- Runtime Stage ----
FROM alpine:3.20

# Install ca-certificates (for HTTPS) and tzdata (for timezone support)
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary and default config from build stage
COPY --from=builder /app/bin/server .
COPY --from=builder /app/config ./config
RUN cp config/config.yaml.example config/config.yaml

EXPOSE 8080

CMD ["./server"]
