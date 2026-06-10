# The git tag is the single source of truth for the version string. `git
# describe` gives `v0.1.0` at a tag, `v0.1.0-3-gabc1234` three commits later, or
# the short SHA when no tag exists yet. The value is baked in at build time via
# -ldflags so an installed binary self-describes (see cmd/stull buildVersion).
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install test vet fmt check all

all: fmt vet test build

build:
	go build $(LDFLAGS) ./...

install:
	go install $(LDFLAGS) ./cmd/stull

test:
	go test ./...

vet:
	go vet ./...

# gofmt is a gate, not a suggestion: fail if anything is unformatted.
fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

# check == the whole green bar, the same one CI enforces.
check: fmt vet test
