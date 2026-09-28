.PHONY: test lint build sqlc buf-lint buf-generate web-build agent-binaries
# Package groups, not one whole-module run: the e2e suite needs an
# otherwise-idle invocation (backlog 2.83) and internal/api needs a
# timeout the other 56 packages should not have to wait out (backlog
# 3.21). scripts/test-groups.sh owns the grouping and both timeouts, so
# this target and scripts/coverage-gate.sh cannot drift apart.
test:
	scripts/test-groups.sh | while read -r name timeout pkgs; do \
		echo "==== $$name group, -timeout $$timeout"; \
		go test -race -cover -timeout "$$timeout" $$pkgs || exit 1; \
	done
lint:
	golangci-lint run ./...
# build depends on web-build so the binary always embeds the real SPA, not
# the placeholder internal/web/dist/index.html committed so `go build`/
# `go test` work on their own without a frontend build — see
# internal/web/embed.go and internal/web/dist/index.html's own comment.
# It depends on agent-binaries for the same reason: without them the
# manager still builds and runs, but GET /agents/bin/... — what the agent
# install script downloads — answers 503 instead of serving an agent.
build: web-build agent-binaries
	CGO_ENABLED=0 go build -o bin/pulsix-manager ./cmd/manager
# Cross-compiles the agent for every target the manager serves into
# internal/agentdist/dist (gitignored build output; see the script). Run
# it before `go build ./cmd/manager` whenever the served binaries matter —
# `make build` and the manager image do.
agent-binaries:
	scripts/agent-binaries.sh
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
