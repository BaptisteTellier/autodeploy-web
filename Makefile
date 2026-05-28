.PHONY: build run image image-local push push-nas clean test fmt vet tidy vendor

VERSION            ?= dev
AUTODEPLOY_VERSION ?= dev
IMAGE              ?= ghcr.io/baptistetellier/autodeploy-web
DATA_DIR           ?= $(PWD)/data
# Adresse de ton registre Synology, ex: make push-nas NAS_REGISTRY=192.168.1.64:5000
NAS_REGISTRY       ?=
NAS_IMAGE          ?= autodeploy-web

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

# Push vers GHCR (CI normal)
push:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

# Build et push directement vers le registre Synology
# Usage : make push-nas NAS_REGISTRY=192.168.1.64:5000
push-nas:
	@if [ -z "$(NAS_REGISTRY)" ]; then \
	  echo "NAS_REGISTRY requis.  Ex: make push-nas NAS_REGISTRY=192.168.1.64:5000"; \
	  exit 1; \
	fi
	docker buildx build \
	    --platform linux/amd64,linux/arm64 \
	    --build-arg VERSION=$(VERSION) \
	    --build-arg AUTODEPLOY_VERSION=$(AUTODEPLOY_VERSION) \
	    --push \
	    -t $(NAS_REGISTRY)/$(NAS_IMAGE):$(VERSION) \
	    -t $(NAS_REGISTRY)/$(NAS_IMAGE):latest \
	    .
	@echo ""
	@echo "Pushé vers $(NAS_REGISTRY) :"
	@echo "  $(NAS_REGISTRY)/$(NAS_IMAGE):$(VERSION)"
	@echo "  $(NAS_REGISTRY)/$(NAS_IMAGE):latest"

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
