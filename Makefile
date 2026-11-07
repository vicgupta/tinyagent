BIN := tinyagent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
DISTDIR ?= dist

.PHONY: build test test-race lint fmt dist windows-zips clean

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
	@mkdir -p $(DISTDIR)
	@rm -f $(DISTDIR)/SHA256SUMS $(DISTDIR)/$(BIN)-windows-*.zip $(DISTDIR)/$(BIN)-windows-*.exe
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		if [ $$os != windows ]; then echo "$(DISTDIR)/$(BIN)-$$os-$$arch$$ext"; fi; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o $(DISTDIR)/$(BIN)-$$os-$$arch$$ext ./cmd/$(BIN) || exit 1; \
	done
	@$(MAKE) --no-print-directory windows-zips
	@cd $(DISTDIR) && { command -v sha256sum >/dev/null 2>&1 \
		&& sha256sum $(BIN)-* > SHA256SUMS \
		|| shasum -a 256 $(BIN)-* > SHA256SUMS; }

# windows-zips packs each Windows exe into a zip whose only entry is
# tinyagent.exe, then deletes the bare exe. The release ships the zip only.
# Requires the zip command.
windows-zips:
	@set -eu; \
	command -v zip >/dev/null 2>&1 || { echo "windows-zips: zip is required to pack tinyagent.exe for WinGet" >&2; exit 1; }; \
	mkdir -p "$(DISTDIR)"; \
	for arch in amd64 arm64; do \
		exe="$(DISTDIR)/$(BIN)-windows-$$arch.exe"; \
		zipf="$(DISTDIR)/$(BIN)-windows-$$arch.zip"; \
		stage="$(DISTDIR)/.zip-$$arch"; \
		test -f "$$exe" || { echo "windows-zips: missing $$exe" >&2; exit 1; }; \
		rm -rf "$$stage" "$$zipf"; \
		mkdir -p "$$stage"; \
		cp "$$exe" "$$stage/$(BIN).exe"; \
		( cd "$$stage" && zip -q -X "../$(BIN)-windows-$$arch.zip" "$(BIN).exe" ); \
		rm -rf "$$stage" "$$exe"; \
		echo "$$zipf"; \
	done

clean:
	rm -rf $(BIN) dist
