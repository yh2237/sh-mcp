VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS_BUILD := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test check cross clean

build:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o bin/sh-mcp ./cmd/sh-mcp

test:
	go test ./...

# Formatting, go.mod tidiness and go vet for every target OS.
check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go mod tidy -diff
	GOOS=linux go vet ./...
	GOOS=darwin go vet ./...
	GOOS=windows go vet ./...

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_BUILD) \
			-o dist/sh-mcp-$$os-$$arch$$ext ./cmd/sh-mcp || exit 1; \
	done

clean:
	rm -rf bin dist
