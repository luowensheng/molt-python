.PHONY: build test lint release-dry clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u +%Y-%m-%d)
LDFLAGS  = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) -s -w

build:
	go build -ldflags "$(LDFLAGS)" -o molt .

test:
	go test ./...

lint:
	go fmt ./...
	go vet ./...

integration-test: build
	go test -tags integration ./...

release-dry:
	mkdir -p dist
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/molt-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/molt-darwin-amd64 .
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/molt-linux-amd64  .
	GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/molt-linux-arm64  .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/molt-windows-amd64.exe .
	cd dist && sha256sum molt-* > checksums.txt
	@echo "Binaries written to dist/"

clean:
	rm -f molt
	rm -rf dist/
