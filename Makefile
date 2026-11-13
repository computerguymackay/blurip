# blurip build targets. The binary is static (no cgo), so it runs on any
# x86-64 Linux without installing anything except MakeMKV.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
RIPPER  ?= colin@192.168.10.104
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build test deploy release clean

build:
	$(GOBUILD) -o blurip .

test:
	go vet ./...
	go test ./...

# Copy the binary to the ripping PC's home folder.
deploy: build
	scp blurip $(RIPPER):

# Tested release binaries for amd64 and arm64, with checksums, in dist/.
release: test
	rm -rf dist && mkdir -p dist
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o dist/blurip-$(VERSION)-linux-amd64 .
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o dist/blurip-$(VERSION)-linux-arm64 .
	cd dist && sha256sum blurip-* > SHA256SUMS

clean:
	rm -rf blurip dist
