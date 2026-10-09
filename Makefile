VERSION := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X saahil.local/brain/internal/rpc.Version=$(VERSION)
DIST    := $(HOME)/.local/share/brain/dist

.PHONY: build build-linux build-darwin test publish dist

build: build-linux build-darwin

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/brain-linux-amd64 ./cmd/brain
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/brain-worker-linux-amd64 ./cmd/brain-worker

build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/brain-darwin-arm64 ./cmd/brain

test:
	go test -p 6 ./...

# publish: install the devbox binary and stage the laptop one (the laptop pulls it with `brain self-update`)
publish: build
	scripts/publish.sh $(DIST)

# dist: the setup kit (dist/brain-setup-$(VERSION).zip); RELEASE=1 also needs kit/README.md + kit/AGENT-SETUP.md and runs check-kit.sh
dist: build
	RELEASE=$(RELEASE) scripts/build-kit.sh $(VERSION)
