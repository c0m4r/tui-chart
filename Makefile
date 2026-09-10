# tui-chart development tasks.
#
# The Go toolchain caches live outside the repository. When the shared caches
# are not writable (sandboxed sessions), point them into .gocache with:
#   . ./scripts/env.sh

BIN      := tui-chart
PKG      := ./cmd/tui-chart
GOFLAGS  ?=
GO       := go

.PHONY: all build test test-update vet fmt lint clean screenshots integration run demo

all: build

build:
	$(GO) build -o $(BIN) $(PKG)

# Build with a stripped binary and version stamp.
release:
	$(GO) build -trimpath -ldflags "-s -w -X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o $(BIN) $(PKG)

run: build
	./$(BIN)

demo: build
	./$(BIN) --demo --theme midnight

test:
	$(GO) test ./...

# Rewrite the golden chart frames after an intentional rendering change.
test-update:
	$(GO) test ./internal/chart/ -run TestGoldenFrames -update

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w cmd internal

lint: vet
	gofmt -l cmd internal | (! grep .) || (echo "gofmt needed"; exit 1)

clean:
	rm -f $(BIN)
	rm -rf .preview

# Regenerate the README screenshots from the frame renderer.
screenshots: build
	./scripts/screenshots.sh

# Drive the real binary under a pty and capture it (see tools/ptytest).
integration: build
	./scripts/uitest.sh
