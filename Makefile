.PHONY: build generate vet
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/agent-runtime ./cmd/agent-runtime
	CGO_ENABLED=0 go build -trimpath -o bin/sandboxd sigs.k8s.io/agent-sandbox/packages/sandboxd/cmd/sandboxd
generate:
	go generate ./opencode
vet:
	go vet ./...
