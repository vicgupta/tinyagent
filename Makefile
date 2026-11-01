BIN := tinyagent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build test test-race lint fmt dist clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/$(BIN)

# test, test-race and fmt run the same commands as the CI jobs in
# .github/workflows/ci.yml, so a local run reproduces CI.
test:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

# lint runs go vet, then staticcheck when it is installed. staticcheck is not a
# module dependency; install it with
#   go install honnef.co/go/tools/cmd/staticcheck@latest
lint:
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		echo "staticcheck ./..."; staticcheck ./...; \
	else \
		echo "staticcheck not found; skipping (install: go install honnef.co/go/tools/cmd/staticcheck@latest)"; \
	fi

# fmt checks formatting like the CI Format step: it lists unformatted files
# and fails if there are any. It does not rewrite files; run gofmt -w to fix.
fmt:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		printf '%s\n' "$$files"; \
		exit 1; \
	fi

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
