BIN := tinyagent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test dist clean test-race lint fmt

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/$(BIN)

test:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

lint:
	go vet ./...
	$(GOPATH)/bin/staticcheck ./...

fmt:
	gofmt -l .

dist:
	@mkdir -p dist
	@rm -f dist/SHA256SUMS
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "dist/$(BIN)-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o dist/$(BIN)-$$os-$$arch$$ext ./cmd/$(BIN) || exit 1; \
	done
	@cd dist && { command -v sha256sum >/dev/null 2>&1 \
		&& sha256sum $(BIN)-* > SHA256SUMS \
		|| shasum -a 256 $(BIN)-* > SHA256SUMS; }

clean:
	rm -rf $(BIN) dist
