.PHONY: test lint build sqlc
test:
	go test -race -cover ./...
lint:
	golangci-lint run ./...
build:
	CGO_ENABLED=0 go build -o bin/pulsix-manager ./cmd/manager
sqlc:
	sqlc generate
