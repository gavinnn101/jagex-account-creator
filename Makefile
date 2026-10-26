.PHONY: build release spa clean test generate cover mutate mutate-full vuln deadcode fmt lint check

BINARY  := creator
PKG     := ./cmd/creator/

build: spa
	go build -o $(BINARY) $(PKG)

release: spa
	go build -ldflags="-s -w" -o $(BINARY) $(PKG)

# The binary embeds web/dist/spa, and serve refuses to start without it.
spa:
	npm --prefix web ci
	npm --prefix web run build

test:
	go test -race ./...

# Writes internal/api/api.gen.go from internal/api/openapi.yaml.
generate:
	go generate ./internal/api

cover:
	bash scripts/coverage-gate.sh

# Mutates only the files that changed since the merge base with origin/main.
mutate:
	bash scripts/mutation-gate.sh

# Mutates every file.
mutate-full:
	bash scripts/mutation-gate.sh full

vuln:
	bash scripts/vuln-gate.sh

deadcode:
	bash scripts/deadcode-gate.sh

fmt:
	bash scripts/golangci-lint.sh fmt ./...

lint:
	bash scripts/golangci-lint.sh run ./...

check: fmt lint

clean:
	rm -f $(BINARY)
