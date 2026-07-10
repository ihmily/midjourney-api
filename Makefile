SWAG_VERSION ?= v1.16.6

.PHONY: help build run clean test deps docs lint

help:
	@echo "Available commands:"
	@echo "  make build    - Build the application"
	@echo "  make run      - Run the application"
	@echo "  make clean    - Clean build artifacts"
	@echo "  make test     - Run tests"
	@echo "  make deps     - Install dependencies"
	@echo "  make docs     - Generate Swagger docs"

deps:
	go mod download
	go mod tidy

build:
	go build -o bin/server ./cmd/server

run:
	go run cmd/server/main.go

clean:
	rm -rf bin/
	rm -rf dist/

test:
	go test -v ./...

docs:
	go run github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION) init -g cmd/server/main.go -o docs

lint:
	golangci-lint run
