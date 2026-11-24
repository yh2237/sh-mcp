VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build test vet cross clean

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/sh-mcp ./cmd/sh-mcp

test:
	go test ./...

vet:
	go vet ./...
	GOOS=linux go vet ./...
	GOOS=windows go vet ./...

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags="$(LDFLAGS)" \
			-o dist/sh-mcp-$$os-$$arch$$ext ./cmd/sh-mcp || exit 1; \
	done

clean:
	rm -rf bin dist
