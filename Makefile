# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT
#
# Host-build convenience verbs (build-local / install-local) mirror projectfile/cli
# so iterating on pf-ci feels identical. The m6e/container lifecycle (image build,
# the CI DAG) loads below via the framework includes.

DIST     ?= dist
BIN      ?= pf-ci
VERSION  ?= dev
LDFLAGS  := -s -w -X main.version=$(VERSION)
INSTALL_DIR ?= $(HOME)/.local/bin

build-local: $(DIST) main.go internal go.mod go.sum ## Build pf-ci host binary into dist/ (requires Go toolchain)
	@echo "Build… $(DIST)/$(BIN)"
	go build -ldflags="$(LDFLAGS)" -o $(DIST)/$(BIN) .

install-local: build-local ## "local publish": build-local + copy into ~/.local/bin (the binary `make ci-generate` host-detects)
	@mkdir -p $(INSTALL_DIR)
	cp $(DIST)/$(BIN) $(INSTALL_DIR)/

dogfood-live: build-local ## Run the live capability under act (needs act+docker+buildx; m6e-only)
	dogfood/live.sh

.PHONY: build-local install-local test lint clean dogfood-live help

# M6E Makefile framework
all: .makefile/core/initialize.mk

.makefile/core/initialize.mk:
	git submodule update --init --recursive
	$(MAKE) bootstrap

-include .makefile/core/initialize.mk
-include .makefile/container/initialize.mk
-include .makefile/b19/initialize.mk
