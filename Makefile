BINARY  := lily
BINDIR  := bin
GO      := go
GOFLAGS := -trimpath -ldflags="-s -w"

.PHONY: build install install-go clean test fmt vet all

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

fmt:
	$(GO)fmt -w .

vet:
	$(GO) vet ./...
