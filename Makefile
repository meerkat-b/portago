.PHONY: build build-online package install setup run docker-build docker-run clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

## Build the fully bundled binary (requires running 'make package' first)
build: bundle.tar.gz
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o dist/portago .

## Build the lightweight binary (downloads dependencies on first run)
build-online:
	@tar czf bundle.tar.gz --files-from /dev/null
	go build -ldflags "-X main.version=$(VERSION)" -o dist/portago-flatpack .

## Create the bundle (runs full setup, strips, compresses) then builds the final binary
package:
	@chmod +x scripts/package.sh
	VERSION=$(VERSION) scripts/package.sh

## Install to GOPATH/bin
install:
	go install -ldflags "-X main.version=$(VERSION)" .

## First-time setup using the shell wrapper (for development)
setup:
	@chmod +x bin/portago scripts/setup.sh
	@scripts/setup.sh

## Launch portago using the shell wrapper (for development)
run:
	@bin/portago

## Build the Docker image
docker-build:
	docker build -t portago .

## Run portago in Docker, mounting the current directory as /work
docker-run:
	docker run -it --rm -v "$$(pwd):/work" portago

## Remove build artifacts and ~/.portago runtime data
clean:
	rm -rf dist/ bundle.tar.gz .staging/ $(HOME)/.portago
