.PHONY: build run image image-local push clean test fmt vet tidy vendor

VERSION            ?= dev
AUTODEPLOY_VERSION ?= dev
IMAGE              ?= ghcr.io/baptistetellier/autodeploy-web
DATA_DIR           ?= $(PWD)/data

vendor:
	sh scripts/fetch-vendor.sh

build: vendor
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$(VERSION)" -o bin/autodeploy-web ./cmd/autodeploy-web

run: build
	LISTEN_ADDR=:8080 DATA_DIR=$(DATA_DIR) AUTODEPLOY_DIR=/opt/autodeploy ./bin/autodeploy-web

image:
	docker buildx build \
	    --platform linux/amd64,linux/arm64 \
	    --build-arg VERSION=$(VERSION) \
	    --build-arg AUTODEPLOY_VERSION=$(AUTODEPLOY_VERSION) \
	    -t $(IMAGE):$(VERSION) \
	    -t $(IMAGE):latest \
	    .

image-local: image
	@echo "Local image built: $(IMAGE):$(VERSION)"

push:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

test:
	go test ./... -race -count=1

fmt:
	gofmt -s -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin dist

dev-up:
	docker compose up -d --build

dev-down:
	docker compose down

dev-logs:
	docker compose logs -f
