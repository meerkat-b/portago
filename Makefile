.PHONY: setup run docker-build docker-run clean

PORTAGO_DIR := $(shell cd "$(dir $(lastword $(MAKEFILE_LIST)))" && pwd)

## First-time setup: install plugins, LSPs, and treesitter parsers
setup:
	@chmod +x bin/portago scripts/setup.sh
	@scripts/setup.sh

## Launch portago
run:
	@bin/portago

## Build the Docker image
docker-build:
	docker build -t portago .

## Run portago in Docker, mounting the current directory as /work
docker-run:
	docker run -it --rm -v "$$(pwd):/work" portago
