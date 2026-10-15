.PHONY: build vet desktop-smoke desktop-image-smoke
SANDBOX_IMAGE ?= agent-runtime:sandbox
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/agent-runtime ./cmd/agent-runtime
	CGO_ENABLED=0 go build -trimpath -o bin/sandboxd sigs.k8s.io/agent-sandbox/packages/sandboxd/cmd/sandboxd
vet:
	go vet ./...
desktop-smoke:
	python3 desktop/smoke.py $(DESKTOP_SMOKE_ARGS)
desktop-image-smoke:
	docker build --target sandbox --tag $(SANDBOX_IMAGE) .
	docker run --rm --init --shm-size=1g --security-opt seccomp=unconfined \
		--mount type=bind,src=$(CURDIR)/desktop/smoke.py,dst=/tmp/desktop-smoke.py,readonly \
		--entrypoint python3 $(SANDBOX_IMAGE) /tmp/desktop-smoke.py \
		--start /usr/local/bin/agent-runtime --check-failure
