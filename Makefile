VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X saahil.local/brain/internal/rpc.Version=$(VERSION)
DIST    := /home/test-user/.local/share/brain/dist

.PHONY: build build-linux build-darwin test publish

build: build-linux build-darwin

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/brain-linux-amd64 ./cmd/brain

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/brain-darwin-arm64 ./cmd/brain

test:
	go test -p 6 ./...

# publish: install the devbox binary and stage the laptop one (the laptop pulls it with `brain self-update`)
publish: build
	scripts/publish.sh $(DIST)
