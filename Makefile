# Convenience wrapper around the repository scripts (AGENTS.md §4). The scripts
# stay the source of truth so local runs and CI stay aligned; this file only
# adds the host-local install step, which has no script of its own.

SHELL := /bin/bash

PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
OUT_DIR ?= dist

# Install only the binary for this host's CPU, under the plain name the CLI is
# invoked by. `uname -m` speaks the kernel's names; Go's GOARCH names are what
# build-release.sh puts in the file name.
UNAME_M := $(shell uname -m)
GOARCH  := $(patsubst x86_64,amd64,$(patsubst aarch64,arm64,$(UNAME_M)))

.PHONY: all build release install uninstall check test clean

all: build

## build: build every package (the compiler is the type checker)
build:
	go build ./...

## release: build the static release binaries into $(OUT_DIR)
release:
	OUT_DIR=$(OUT_DIR) scripts/build-release.sh

## install: build the release binaries and install this host's into $(BINDIR)
install: release
	@test -f "$(OUT_DIR)/agent-vm-linux-$(GOARCH)" || { \
		echo "no release binary for $(UNAME_M) (GOARCH=$(GOARCH)) in $(OUT_DIR)" >&2; \
		echo "build one with: ARCHES=$(GOARCH) scripts/build-release.sh" >&2; \
		exit 1; \
	}
	install -d "$(BINDIR)"
	install -m 0755 "$(OUT_DIR)/agent-vm-linux-$(GOARCH)" "$(BINDIR)/agent-vm"
	@echo "installed $(BINDIR)/agent-vm ($(GOARCH))"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; \
		*) echo "note: $(BINDIR) is not on your PATH" >&2 ;; \
	esac

## uninstall: remove the installed binary
uninstall:
	rm -f "$(BINDIR)/agent-vm"

## check: the full pre-handoff verification (gofmt, vet, lint, unit tests)
check:
	scripts/check.sh

## test: unit tests only
test:
	go test ./...

## clean: remove build output
clean:
	rm -rf "$(OUT_DIR)" bin
