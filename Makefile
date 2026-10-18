.PHONY: proto web build test docker release

VERSION ?= dev
IMAGE_PREFIX ?= ghcr.io/matelko123/nexosv2

proto:
	buf lint && buf generate

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -ldflags "-X nexos/internal/version.Version=$(VERSION)" -o bin/nexos-hub ./hub/cmd/nexos-hub
	CGO_ENABLED=0 go build -ldflags "-X nexos/internal/version.Version=$(VERSION)" -o bin/nexos-agent ./agent/cmd/nexos-agent

test:
	go vet ./... && go test ./...
	cd web && npx tsc -b

# Local images, tagged like the published ones so compose files work as-is.
docker:
	docker build --build-arg VERSION=$(VERSION) -f Dockerfile.hub -t $(IMAGE_PREFIX)-hub:latest .
	docker build --build-arg VERSION=$(VERSION) -f Dockerfile.agent -t $(IMAGE_PREFIX)-agent:latest .

# Publish a release: make release VERSION=v1.2.0 (GitHub Actions builds the images).
release:
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-.+)?$$' || (echo "VERSION must look like v1.2.0"; exit 1)
	git tag -a $(VERSION) -m "NexOS $(VERSION)" && git push origin $(VERSION)
