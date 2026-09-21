BINARY_NAME := enginedj5multitool
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.appVersion=$(VERSION)

# Cross-compile targets (CGO is not needed for any of them).
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64
DIST_DIR := dist

.DEFAULT_GOAL := help

.PHONY: help build test vet fmt fmtcheck check release clean snapshot

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build for the current platform
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY_NAME) .

test: ## Run the test suite
	go test ./... -count=1

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go files
	gofmt -w .

sqlc: ## Regenerate the db package from db/schema.sql + db/queries.sql
	sqlc generate

fmtcheck: ## Fail if any Go file is unformatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

check: fmtcheck vet test ## Everything CI runs: fmt check, vet, tests

snapshot: ## Render one headless UI frame to /tmp/ui.png
	go run . -db m.db -snapshot /tmp/ui.png

release: ## Cross-compile and package all platforms into dist/
	@mkdir -p $(DIST_DIR)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		bin=$(BINARY_NAME)-$${os}-$${arch}; \
		out=$(BINARY_NAME); \
		if [ "$$os" = "windows" ]; then bin=$$bin.exe; out=$$out.exe; fi; \
		echo "== building $$bin ($(VERSION))"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o "$(DIST_DIR)/$$bin" .; \
		staged=$(BINARY_NAME)-$(VERSION)-$${os}-$${arch}; \
		rm -rf "$(DIST_DIR)/$$staged"; mkdir -p "$(DIST_DIR)/$$staged"; \
		cp "$(DIST_DIR)/$$bin" "$(DIST_DIR)/$$staged/$$out"; \
		cp README.md "$(DIST_DIR)/$$staged/"; \
		if [ "$$os" = "windows" ]; then \
			(cd "$(DIST_DIR)" && zip -qr "$$staged.zip" "$$staged"); \
		else \
			tar -czf "$(DIST_DIR)/$$staged.tar.gz" -C "$(DIST_DIR)" "$$staged"; \
		fi; \
		rm -rf "$(DIST_DIR)/$$staged" "$(DIST_DIR)/$$bin"; \
	done
	@echo "== artifacts:"; ls -la $(DIST_DIR)

clean: ## Remove build outputs
	rm -rf $(DIST_DIR) $(BINARY_NAME) $(BINARY_NAME).exe
