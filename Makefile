# BIN-124 shared targets / 共享目标.
# `check` delegates to scripts/check.sh so local runs and CI share one gate.

.PHONY: check test build

# Full quality gate: gofmt, mod verify, vet, race tests, build, smoke.
check:
	./scripts/check.sh

# Race-enabled Go test suite only.
test:
	go test -race ./...

# Build to a caller-selected OUTPUT, otherwise a temporary directory, so a
# plain `make build` never leaves artifacts in the repository.
# Usage: make build OUTPUT=/path/to/binflow
OUTPUT =
build:
	@set -e; \
	if [ -n "$(OUTPUT)" ]; then \
		go build -o "$(OUTPUT)" ./cmd/binflow; \
		echo "built $(OUTPUT)"; \
	else \
		tmp=$$(mktemp -d "$${TMPDIR:-/tmp}/binflow-build.XXXXXX"); \
		go build -o "$$tmp/binflow" ./cmd/binflow; \
		echo "built $$tmp/binflow"; \
	fi
