BINARY_NAME := vergeos-cloud-controller-manager
IMAGE_NAME := ghcr.io/encore-platform-engineering/vergeos-cloud-controller-manager
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GOFLAGS := -ldflags="-s -w"

.PHONY: all build build-linux test vet lint clean docker-build docker-push help

all: test build

## Build the binary for the current platform
build:
	go build $(GOFLAGS) -o bin/$(BINARY_NAME) ./cmd/vergeos-cloud-controller-manager/

## Build a Linux amd64 binary (for Docker / Kubernetes)
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(GOFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/vergeos-cloud-controller-manager/

## Run all tests
test:
	go test ./... -v -race

## Run go vet
vet:
	go vet ./...

## Build Docker image for linux/amd64 (local only)
docker-build:
	docker buildx build --platform linux/amd64 --provenance=false -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest --load .

## Build and push Docker image for linux/amd64
docker-push:
	docker buildx build --platform linux/amd64 --provenance=false -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest --push .

## Remove build artifacts
clean:
	rm -rf bin/

## Show help
help:
	@echo "Targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
