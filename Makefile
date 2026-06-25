SHELL := /bin/bash

PKG := github.com/VertebrateResequencing/wr
VERSION := $(shell git describe --tags --always --long --dirty)
LDFLAGS = -s -w -X ${PKG}/jobqueue.ServerVersion=${VERSION}
GOLANGCI_LINT_ARGS ?=
export GOPATH := $(shell go env GOPATH)
PATH := $(PATH):${GOPATH}/bin
WEBUI_TEST_STEP_TIMEOUT ?= timeout 8m
WEBUI_TEST_PLAYWRIGHT_VERSION ?= 1.56.1
WEBUI_TEST_SCRATCH ?= $(CURDIR)/.tmp/agent
WEBUI_TEST_ARTIFACT_DIR ?= $(WEBUI_TEST_SCRATCH)/webui-test
WEBUI_TEST_NPM_CACHE ?= $(WEBUI_TEST_SCRATCH)/npm-cache
WEBUI_TEST_PLAYWRIGHT_ROOT ?= $(WEBUI_TEST_SCRATCH)/playwright
WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR ?= $(WEBUI_TEST_PLAYWRIGHT_ROOT)/node_modules/playwright
WEBUI_TEST_BROWSER_CACHE ?= $(WEBUI_TEST_SCRATCH)/ms-playwright
WEBUI_TEST_REPRO_HTML ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-stale-counts.html
WEBUI_TEST_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-stale-running-resolved.png

default: install

build: export CGO_ENABLED = 0
build:
	go build -tags netgo -ldflags "${LDFLAGS}"

install: export CGO_ENABLED = 0
install:
	@rm -f ${GOPATH}/bin/wr
	@go install -tags netgo -ldflags "${LDFLAGS}"
	@echo installed to ${GOPATH}/bin/wr

WR_TEST_RUNNEREXECSHELL ?= /bin/bash

# The Go-side test-suite runner discovers packages with `go list ./...`, plans
# split lanes for the slow packages, and keeps the Makefile as a thin wrapper.
#
# Test-suite environment knobs:
#   WR_TEST_RUNNEREXECSHELL=/path/to/shell
#       Shell to expose to tests as WR_RUNNEREXECSHELL; defaults to bash so
#       module-based runner tests work on systems where /bin/sh is dash.
#   WR_TESTSUITE_TIMINGS=1
#       Print per-lane timings after the suite finishes.
#   WR_TESTSUITE_MAX_PARALLEL=N
#       Override the CPU-aware cap for concurrent lanes. The default scales
#       from GOMAXPROCS and is bounded to avoid overloading small CI hosts.
#   WR_TEST_PORT_BASE=N
#       Override the first port in the suite's run-specific lane port block.
#       Normally auto-selected below the OS ephemeral port range; intended for
#       debugging unusual port conflicts.
# WR_TEST_LANE, WR_TEST_SHARD and WR_TEST_RUNNER_BINARY are internal lane
# controls set by the suite runner, not normal user-facing inputs.
test: export CGO_ENABLED = 0
test: export WR_RUNNEREXECSHELL ?= $(WR_TEST_RUNNEREXECSHELL)
test:
	@go run ./cmd/wr-testsuite test

race: export CGO_ENABLED = 1
race: export WR_RUNNEREXECSHELL ?= $(WR_TEST_RUNNEREXECSHELL)
race:
	@go run ./cmd/wr-testsuite race

# curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.50.1
lint:
	@golangci-lint run ${GOLANGCI_LINT_ARGS}

# Browser-only status page regression gate. It is intentionally not a
# prerequisite of test/race because it may install Playwright/Chromium into
# repo-local scratch space under .tmp/agent.
browser-test:
	@mkdir -p "$(WEBUI_TEST_PLAYWRIGHT_ROOT)" "$(WEBUI_TEST_ARTIFACT_DIR)" "$(WEBUI_TEST_NPM_CACHE)" "$(WEBUI_TEST_BROWSER_CACHE)"
	@if [ ! -d "$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" ]; then \
		cd "$(WEBUI_TEST_PLAYWRIGHT_ROOT)" && \
		npm_config_cache="$(WEBUI_TEST_NPM_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) npm install --no-audit --no-fund "playwright@$(WEBUI_TEST_PLAYWRIGHT_VERSION)"; \
	fi
	@PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) "$(WEBUI_TEST_PLAYWRIGHT_ROOT)/node_modules/.bin/playwright" install chromium
	@$(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/status-page-stale-counts/repro.mjs --assert
	@$(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/status-page-stale-counts/repro.mjs "$(WEBUI_TEST_REPRO_HTML)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/status-page-stale-counts/screenshot.mjs "$(WEBUI_TEST_SCREENSHOT)"
	@echo "browser-test artifacts:"
	@echo "  $(WEBUI_TEST_REPRO_HTML)"
	@echo "  $(WEBUI_TEST_SCREENSHOT)"

webui-test: browser-test

clean:
	@rm -f ./wr
	@rm -f ./dist.zip
	@rm -fr ./vendor
	@rm -f /tmp/wr

dist: export CGO_ENABLED = 0
dist: export WR_LDFLAGS = $(LDFLAGS)
# go install github.com/goreleaser/goreleaser/v2@2.9.0
dist:
	goreleaser release --clean

.PHONY: browser-test build test race lint lintextra install clean dist webui-test
