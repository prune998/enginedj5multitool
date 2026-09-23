BINARY_NAME := enginedj5multitool
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.appVersion=$(VERSION)
APP_NAME := Engine DJ Multi Tool

# Cross-compile targets (CGO is not needed for any of them).
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64
DIST_DIR := dist

.DEFAULT_GOAL := help

.PHONY: help build test vet fmt fmtcheck check snapshot icon docs macapp appbundle appzip release clean snapshot

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

icon: ## Generate assets/icon.png + assets/icon.icns (skull-and-crossbones)
	go run ./cmd/genicon -png assets/icon.png -icns assets/icon.icns

DOCS_DB := /tmp/enginedj5multitool-docs

docs: ## Regenerate the README screenshots in docs/ (fictitious demo data)
	@rm -rf "$(DOCS_DB)"
	@go run . -gendemo "$(DOCS_DB)/Engine Library/Database2/m.db"
	@set -e; for t in 0:cues 1:tags 2:global 3:relink 4:dedup 5:playlists 6:settings; do \
		n=$${t%%:*}; f=$${t##*:}; \
		echo "== docs/screenshot-$$f.png"; \
		go run . -db "$(DOCS_DB)/Engine Library/Database2/m.db" -musicroot "$(DOCS_DB)/Music" \
			-tool $$n -snapshot "docs/screenshot-$$f.png"; \
	done
	@echo "== screenshots written to docs/"

macapp: ## Build "Engine DJ Multi Tool.app" for ARCH (default: host arch)
	@$(MAKE) appbundle ARCH=$(if $(ARCH),$(ARCH),$(shell uname -m | sed 's/x86_64/amd64/'))
	@echo "If macOS calls the app damaged after a download, run:"; \
	echo "  xattr -cr \"$(APP_NAME).app\""

appbundle: ## Build + ad-hoc sign one .app bundle (ARCH=amd64|arm64)
	@test -n "$(ARCH)" || { echo "usage: make appbundle ARCH=amd64|arm64"; exit 1; }
	@$(MAKE) --no-print-directory icon
	@rm -rf "$(APP_NAME).app"
	@mkdir -p "$(APP_NAME).app/Contents/MacOS" "$(APP_NAME).app/Contents/Resources"
	CGO_ENABLED=0 GOOS=darwin GOARCH=$(ARCH) go build -trimpath -ldflags '$(LDFLAGS)' -o "$(APP_NAME).app/Contents/MacOS/$(BINARY_NAME)" .
	sed 's/@VERSION@/$(VERSION)/' assets/Info.plist.in > "$(APP_NAME).app/Contents/Info.plist"
	cp assets/icon.icns "$(APP_NAME).app/Contents/Resources/icon.icns"
	@# Seal the bundle with an ad-hoc signature. This must run on macOS
	@# (Gatekeeper reports unsigned bundles as "damaged"); on other hosts the
	@# bundle is still produced, just unsigned.
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --force -s - "$(APP_NAME).app" && echo "== signed (ad-hoc)"; \
	else \
		echo "== warning: codesign not available, bundle is UNSIGNED"; \
	fi
	@echo "== built $(APP_NAME).app (darwin/$(ARCH), $(VERSION))"

appzip: ## Build + sign + zip one .app bundle into dist/ (ARCH=amd64|arm64)
	@test -n "$(ARCH)" || { echo "usage: make appzip ARCH=amd64|arm64"; exit 1; }
	@$(MAKE) --no-print-directory appbundle ARCH=$(ARCH)
	@mkdir -p $(DIST_DIR)
	@# ditto preserves the signature metadata that plain zip may drop.
	ditto -c -k --keepParent "$(APP_NAME).app" "$(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-darwin-$(ARCH).app.zip"
	@echo "== wrote $(DIST_DIR)/$(BINARY_NAME)-$(VERSION)-darwin-$(ARCH).app.zip"

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
	@echo "== artifacts (darwin .app.zip is built by the macos-app CI job;"
	@echo "   locally: make appzip ARCH=arm64|amd64):"; ls -la $(DIST_DIR)

clean: ## Remove build outputs
	rm -rf $(DIST_DIR) $(BINARY_NAME) $(BINARY_NAME).exe "$(APP_NAME).app"
