.PHONY: build run test check

build:
	cd frontend && env -u NO_COLOR trunk build --release
	mkdir -p bin
	go build -o bin/west-platform ./cmd/server

run: build
	./bin/west-platform

test:
	go test -race ./cmd/server

# Read-only readiness report; exit 1 means real-payment prerequisites are missing.
check:
	go run ./cmd/server check
