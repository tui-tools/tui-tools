# tui-tools — build, test and lint.

GO      ?= go
BIN     ?= bin
TOOL    := tui-tools
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# The screenshot renderer is shared by the whole family and ships with the
# kit, which is already a dependency: ask the module cache where it landed.
KIT     = $(shell $(GO) list -m -f '{{.Dir}}' github.com/tui-tools/tui-kit)
# The family catalog, which `make catalog` snapshots into the binary.
CATALOG_URL ?= https://tui.tools/catalog.json

.PHONY: manifest readme compat catalog check-exec all build test vet fmt fmt-check lint check demo clean tidy install screenshots

all: check build

## build: compile the tool into $(BIN) as a static binary.
build:
	@mkdir -p $(BIN)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BIN)/$(TOOL) ./cmd/$(TOOL)

## test: run the unit tests.
test:
	$(GO) test ./...

## vet: run the standard static checks.
vet:
	$(GO) vet ./...

## fmt: rewrite the sources with gofmt.
fmt:
	gofmt -w .

## fmt-check: fail when something is not gofmt-clean.
fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "these files need gofmt:"; echo "$$out"; exit 1; \
	fi

## lint: fmt-check, vet and the exec boundary. golangci-lint when installed.
lint: fmt-check vet check-exec
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping"; \
	fi

## check: everything CI runs.
check: lint test

## demo: run against the in-memory sample data.
demo:
	$(GO) run ./cmd/$(TOOL) --demo

## install: install the tool into GOBIN.
install:
	CGO_ENABLED=0 $(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/$(TOOL)

## tidy: prune and refresh go.mod / go.sum.
tidy:
	$(GO) mod tidy

## screenshots: re-render the README frames from --demo (needs chrome/chromium).
## The companions frame walks the cursor to the last row so the viewport scrolls
## to the bottom, then steps back up onto headscale: that is the only position
## where the COMPANIONS header, both companion rows and the origin of the
## installed copy are on screen together.
screenshots: build
	python3 $(KIT)/tools/render-screenshots.py \
		--bin $(BIN)/$(TOOL) --name $(TOOL) --out docs/screenshots \
		--screen main= --screen install='jji' --screen repo=s \
		--screen filter='/fire' --screen help=? \
		--screen companions='jjjjjjjjjjjjjjjk'

## catalog: refresh the catalog snapshot embedded in the binary, then check
## that what was downloaded is a document this launcher can read. The snapshot
## is what --demo shows and what a machine with no network falls back to, so a
## broken download has to fail here rather than on someone's terminal.
catalog:
	curl -fsSL --retry 3 -o internal/catalog/snapshot.json $(CATALOG_URL)
	$(GO) test ./internal/catalog -run TestSnapshot -v

## readme: regenerate the generated README sections from tool.json.
readme:
	python3 $(KIT)/tools/render-install.py --manifest tool.json --readme README.md
	python3 $(KIT)/tools/render-compat.py --manifest tool.json --readme README.md

## compat: harvest the lab logs into compat/results.jsonl and regenerate the
## tested versions in tool.json. Run it after `lab.sh test $(TOOL)`.
compat:
	python3 $(KIT)/tools/compat-sync.py --manifest tool.json \
		--results compat/results.jsonl \
		--from-log $(wildcard ../tui-lab/out/results/*-$(TOOL)/*.log)

## check-exec: assert that only a backend package starts a process.
check-exec:
	bash $(KIT)/tools/check-exec.sh .


## manifest: validate tool.json against the family schema in tui-kit.
manifest:
	@curl -fsSL --retry 3 -o /tmp/tui-tool.schema.json \
		https://raw.githubusercontent.com/tui-tools/tui-kit/main/schema/tool.schema.json
	@npx --yes -p ajv-cli@5 -p ajv-formats@3 ajv validate \
		--spec=draft2020 -c ajv-formats \
		-s /tmp/tui-tool.schema.json -d tool.json
	@python3 $(KIT)/tools/check-nfpm.py --manifest tool.json \
		--config .goreleaser.yaml

## clean: remove build output.
clean:
	rm -rf $(BIN) dist
