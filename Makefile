.PHONY: help build run clean test

help:
	@echo "Available commands:"
	@echo "  make build    - Build the application"
	@echo "  make run      - Run the application"
	@echo "  make clean    - Clean build artifacts"
	@echo "  make test     - Run tests"
	@echo "  make deps     - Install dependencies"

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

lint:
	golangci-lint run
