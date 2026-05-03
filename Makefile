BINARY  := lily
BINDIR  := bin
GO      := go
GOFLAGS := -trimpath -ldflags="-s -w"
VERSION ?= $(shell grep 'Version =' internal/version/version.go | sed "s/.*= \"//;s/\".*//")
LDFLAGS := -s -w -X internal/version.Version=$(VERSION)

.PHONY: build install install-go clean test test-e2e fmt vet all

all: test build

build:
	@mkdir -p $(BINDIR)
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BINDIR)/$(BINARY) ./cmd/lily

install: build
	cp $(BINDIR)/$(BINARY) /usr/local/bin/

install-go:
	$(GO) install ./cmd/lily

clean:
	rm -rf $(BINDIR)

test:
	$(GO) test ./... -count=1

test-verbose:
	$(GO) test ./... -v -count=1

test-e2e:
	LILY_E2E=1 $(GO) test ./test/e2e/ -v -count=1 -timeout 20m

fmt:
	$(GO)fmt -w .

vet:
	$(GO) vet ./...
