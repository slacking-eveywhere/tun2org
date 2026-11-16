# tun2org developer tasks.
#
# End users do NOT use this file. They install with:
#   curl -fsSL https://raw.githubusercontent.com/slacking-eveywhere/tun2org/main/install.sh | sh
#
# The repo is the package source: `make build` cross-compiles the release
# targets into dist/ (+ SHA256SUMS); commit + push dist/, and the one-liner
# serves the new version to everyone.

BINS    := ntun ntun-agent
DIST    := dist
BIN_DIR ?= $(HOME)/.local/bin
TARGETS := linux_amd64 linux_arm64 darwin_arm64
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DIRTY   := $(shell test -n "$$(git status --porcelain 2>/dev/null)" && echo -dirty || true)
LDFLAGS := -s -w -X ntun-ng/internal/version.Commit=$(COMMIT)$(DIRTY)

UNAME   := $(shell uname -s)
UNAME_M := $(shell uname -m)
ifeq ($(UNAME)/$(UNAME_M),Linux/x86_64)
PLAT := linux_amd64
else ifeq ($(UNAME)/$(UNAME_M),Linux/aarch64)
PLAT := linux_arm64
else ifeq ($(UNAME)/$(UNAME_M),Darwin/arm64)
PLAT := darwin_arm64
else ifeq ($(UNAME),Linux)
PLAT := linux_arm64
else
PLAT := unsupported
endif

.ONESHELL:
.PHONY: help fmt vet test test-race e2e build install uninstall clean

help:
	@echo "tun2org dev tasks (users: curl .../install.sh | sh):"
	@echo "  fmt        gofmt all sources"
	@echo "  vet        go vet ./..."
	@echo "  test       go test ./... (with -timeout)"
	@echo "  test-race  race detector on the concurrency-critical pkgs"
	@echo "  e2e        dockerized real-tunnel end-to-end tests (docker required)"
	@echo "  build      cross-compile release targets into dist/ + SHA256SUMS"
	@echo "  install    install the local platform from dist/ into BIN_DIR"
	@echo "  uninstall  remove binaries from BIN_DIR"
	@echo "  clean      remove dist/ artifacts"
	@echo "vars: BIN_DIR=$(BIN_DIR) DIST=$(DIST) TARGETS='$(TARGETS)'"

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

test:
	go test -timeout 300s ./...

test-race:
	go test -race -count=2 -timeout 300s ./internal/mux/ ./internal/relay/

e2e:
	./testenv/run.sh

build:
	rm -f $(DIST)/ntun_* $(DIST)/ntun-agent_* $(DIST)/SHA256SUMS
	mkdir -p $(DIST)
	echo "ntun: building static binaries (commit $(COMMIT)$(DIRTY)) into $(DIST)/..."
	for t in $(TARGETS); do
		os=$${t%%_*}; arch=$${t##*_}
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags '$(LDFLAGS)' -o $(DIST)/ntun_$$t ./cmd/ntun
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags '$(LDFLAGS)' -o $(DIST)/ntun-agent_$$t ./cmd/ntun-agent
	done
	(cd $(DIST) && { sha256sum ntun_* ntun-agent_* 2>/dev/null || shasum -a 256 ntun_* ntun-agent_*; } > SHA256SUMS)
	ls -l $(DIST)/ntun_* $(DIST)/ntun-agent_* $(DIST)/SHA256SUMS
	echo "ntun: commit $(DIST)/ (binaries + SHA256SUMS) and push; users update via the one-liner."

install: build
	if [ "$(PLAT)" = "unsupported" ]; then
		echo "ntun: unsupported platform $(UNAME)/$(UNAME_M)" >&2; exit 1
	fi
	mkdir -p $(BIN_DIR)
	cp $(DIST)/ntun_$(PLAT) $(BIN_DIR)/ntun
	cp $(DIST)/ntun-agent_$(PLAT) $(BIN_DIR)/ntun-agent
	chmod +x $(BIN_DIR)/ntun $(BIN_DIR)/ntun-agent
	if [ "$(UNAME)" = "Linux" ] && command -v setcap >/dev/null 2>&1; then
		setcap cap_net_admin+ep $(BIN_DIR)/ntun 2>/dev/null \
			|| echo "ntun: setcap failed; use 'sudo ntun up'" >&2
	fi
	mkdir -p $(HOME)/.tun2org
	if [ ! -f "$(HOME)/.tun2org/config.json" ]; then echo '{}' > $(HOME)/.tun2org/config.json; fi
	echo "ntun: installed into $(BIN_DIR):"
	$(BIN_DIR)/ntun version

uninstall:
	rm -f $(BIN_DIR)/ntun $(BIN_DIR)/ntun-agent
	echo "ntun: removed binaries from $(BIN_DIR)"

clean:
	rm -f $(DIST)/ntun_* $(DIST)/ntun-agent_* $(DIST)/SHA256SUMS
