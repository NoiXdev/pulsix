.PHONY: test lint build sqlc buf-lint buf-generate web-build
test:
	go test -race -cover ./...
lint:
	golangci-lint run ./...
# build depends on web-build so the binary always embeds the real SPA, not
# the placeholder internal/web/dist/index.html committed so `go build`/
# `go test` work on their own without a frontend build — see
# internal/web/embed.go and internal/web/dist/index.html's own comment.
build: web-build
	CGO_ENABLED=0 go build -o bin/pulsix-manager ./cmd/manager
web-build:
	cd web && npm ci && npm run build
	rm -rf internal/web/dist
	cp -r web/dist internal/web/dist
sqlc:
	sqlc generate
buf-lint:
	cd proto && buf lint
buf-generate:
	cd proto && buf generate
