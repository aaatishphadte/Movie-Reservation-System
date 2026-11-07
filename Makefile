.PHONY: run up down logs build test test-integration lint fmt

up:            ## start postgres, redis and the api
	docker compose up -d --build

down:
	docker compose down -v

logs:
	docker compose logs -f api

run:           ## run the api locally against compose infra
	docker compose up -d postgres redis
	JWT_SECRET=dev-only-secret-change-me-0123456789abcdef ADMIN_EMAIL=admin@cinema.local ADMIN_PASSWORD=Admin@12345 go run ./cmd/api

build:
	go build -o bin/api ./cmd/api

test:
	go test -race ./...

test-integration:
	docker compose up -d postgres redis
	go test -race -tags integration -count=1 ./...

lint:
	go vet ./...

fmt:
	gofmt -s -w .
