.PHONY: proto web build test docker

proto:
	buf lint && buf generate

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -o bin/nexos-hub ./hub/cmd/nexos-hub
	CGO_ENABLED=0 go build -o bin/nexos-agent ./agent/cmd/nexos-agent

test:
	go vet ./... && go test ./...
	cd web && npx tsc -b

docker:
	docker build -f Dockerfile.hub -t nexos-hub:latest .
	docker build -f Dockerfile.agent -t nexos-agent:latest .
