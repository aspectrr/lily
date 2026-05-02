BINARY  := lily
BINDIR  := bin
GO      := go
GOFLAGS := -trimpath -ldflags="-s -w"

.PHONY: build install install-go clean test test-e2e fmt vet all

all: test build

build:
	@mkdir -p $(BINDIR)
	$(GO) build $(GOFLAGS) -o $(BINDIR)/$(BINARY) ./cmd/lily

install: build
	cp $(BINDIR)/$(BINARY) /usr/local/bin/

install-go:
	$(GO) install ./cmd/lily

clean:
	rm -rf $(BINDIR)

test:
	$(GO) test ./... -v -count=1

test-e2e:
	LILY_E2E=1 $(GO) test ./test/e2e/ -v -count=1 -timeout 20m

fmt:
	$(GO)fmt -w .

vet:
	$(GO) vet ./...
