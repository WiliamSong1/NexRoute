.PHONY: build test race vet bench up down analyze
build:
	mkdir -p bin
	go build -trimpath -o bin/lb ./cmd/lb
	go build -trimpath -o bin/backend ./cmd/backend
test:
	go test ./...
race:
	go test -race ./...
vet:
	go vet ./...
bench:
	go test -run '^$$' -bench . -benchmem ./internal/routing
up:
	docker compose up --build -d
down:
	docker compose down
analyze:
	python3 benchmarks/analyze.py benchmarks/results
