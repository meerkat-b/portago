.PHONY: build build-online package package-flatpack-all install setup run test-platforms clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

## Build the fully bundled binary (requires running 'make package' first)
build: bundle.tar.gz
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago .

## Build the lightweight binary (downloads dependencies on first run)
build-online:
	@tar czf bundle.tar.gz --files-from /dev/null
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago-flatpack .

## Create the bundle and build both binaries (bundled + flatpack)
## NOTE: The bundle is platform-specific — it contains native nvim, Mason
## tools, and treesitter parsers for the build machine's OS/arch only.
## For multi-platform releases, run this on each target platform or in CI.
package:
	@chmod +x scripts/package.sh
	VERSION=$(VERSION) scripts/package.sh

## Cross-compile flatpack binaries for all supported platforms
package-flatpack-all:
	@tar czf bundle.tar.gz --files-from /dev/null
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago-flatpack-darwin-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago-flatpack-darwin-amd64 .
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago-flatpack-linux-arm64 .
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago-flatpack-linux-amd64 .
	@echo "==> Built 4 flatpack binaries in dist/"
	@ls -lh dist/portago-flatpack-*

## Install to GOPATH/bin
install:
	go install -ldflags "-s -w -X main.version=$(VERSION)" .

## First-time setup using the shell wrapper (for development)
setup:
	@chmod +x bin/portago scripts/setup.sh
	@scripts/setup.sh

## Launch portago using the shell wrapper (for development)
run:
	@bin/portago

## Test portago binary on multiple Linux distributions via Docker
test-platforms:
	@chmod +x scripts/test-platforms.sh
	@scripts/test-platforms.sh

## Remove build artifacts and ~/.portago runtime data
clean:
	@chmod -R u+w .staging/ 2>/dev/null || true
	rm -rf dist/ bundle.tar.gz .staging/ $(HOME)/.portago
