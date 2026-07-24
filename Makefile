SHELL := /bin/bash

PKG := github.com/VertebrateResequencing/wr
VERSION := $(shell git describe --tags --always --long --dirty)
LDFLAGS = -s -w -X ${PKG}/jobqueue.ServerVersion=${VERSION}
GOLANGCI_LINT_ARGS ?=
export GOPATH := $(shell go env GOPATH)
PATH := $(PATH):${GOPATH}/bin
WEBUI_TEST_STEP_TIMEOUT ?= timeout 8m
WEBUI_TEST_PLAYWRIGHT_VERSION ?= 1.59.0
# Generated artifacts (screenshots/traces/HTML) are ephemeral and live under
# .tmp/agent, which may be wiped freely. Playwright's npm package, npm cache and
# browser cache are deliberately persisted OUTSIDE .tmp so a .tmp wipe does not
# force an npm reinstall or a Chromium re-download: the browser cache defaults to
# Playwright's standard per-user location ($(HOME)/.cache/ms-playwright), so any
# existing Chromium is reused and shared. All are overridable with ?= so CI can
# pin a sandboxed location.
WEBUI_TEST_SCRATCH ?= $(CURDIR)/.tmp/agent
WEBUI_TEST_ARTIFACT_DIR ?= $(WEBUI_TEST_SCRATCH)/webui-test
WEBUI_TEST_PLAYWRIGHT_ROOT ?= $(HOME)/.cache/wr-webui-playwright
WEBUI_TEST_NPM_CACHE ?= $(WEBUI_TEST_PLAYWRIGHT_ROOT)/npm-cache
WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR ?= $(WEBUI_TEST_PLAYWRIGHT_ROOT)/node_modules/playwright
WEBUI_TEST_BROWSER_CACHE ?= $(HOME)/.cache/ms-playwright
WEBUI_TEST_DEPENDENT_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-dependent-job-details.png
WEBUI_TEST_SUSPENDED_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-suspended-job-actions.png
WEBUI_TEST_RECONNECT_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-reconnect-warnings.png
WEBUI_TEST_LIVE_HEARTBEAT_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-live-heartbeat-details.png
WEBUI_TEST_COMPLETED_REPGROUP_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-completed-repgroup.png
WEBUI_TEST_COMPLETED_REPGROUP_TRACE ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-completed-repgroup-trace.json
WEBUI_TEST_COMPLETED_REPGROUP_DELETED_REFRESH_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-completed-repgroup-deleted-refresh-post-fix.png
WEBUI_TEST_COMPLETED_REPGROUP_DELETED_REFRESH_TRACE ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-completed-repgroup-deleted-refresh-post-fix-trace.json
WEBUI_TEST_BAR_FLICKER_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-repgroup-bar-flicker.png
WEBUI_TEST_BAR_FLICKER_TRACE ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-repgroup-bar-flicker-trace.json
WEBUI_TEST_REMOVED_REFRESH_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-removed-jobs-refresh.png
WEBUI_TEST_REMOVED_REFRESH_TRACE ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-removed-jobs-refresh-trace.json
WEBUI_TEST_COUNT_RECONCILE_SCREENSHOT ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-count-reconcile.png
WEBUI_TEST_COUNT_RECONCILE_TRACE ?= $(WEBUI_TEST_ARTIFACT_DIR)/status-webui-count-reconcile-trace.json

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

# Throughput benchmarks for the jobqueue manager's critical persistence paths
# (add, per-job state-change, archive), guarding against performance regressions
# such as a loss of BoltDB write-coalescing. They are plain Go benchmarks, so
# test/race never run them; this target runs them directly (not via
# wr-testsuite). CGO is off to match test; the race detector is deliberately not
# enabled, as benchmarks add no production logic. Each reports ns/op, -benchmem
# allocations and BoltDB writes/pages per job. Use BENCH=Name to pick one,
# BENCHTIME=Nx to control iterations.
BENCH ?= .
BENCHTIME ?= 1x
bench: export CGO_ENABLED = 0
bench:
	@go test -tags netgo -run='^$$' -bench='$(BENCH)' -benchmem -benchtime=$(BENCHTIME) ./jobqueue/

# curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.12.2
lint:
	@golangci-lint run ${GOLANGCI_LINT_ARGS}

# Browser-only status page regression gate. It is intentionally not a
# prerequisite of test/race because it may install the Playwright npm package
# (cached persistently outside .tmp) and, only if a matching build is missing,
# download Chromium into the shared per-user browser cache. Generated artifacts
# go under .tmp/agent and may be wiped.
browser-test:
	@mkdir -p "$(WEBUI_TEST_PLAYWRIGHT_ROOT)" "$(WEBUI_TEST_ARTIFACT_DIR)" "$(WEBUI_TEST_NPM_CACHE)" "$(WEBUI_TEST_BROWSER_CACHE)"
	@if [ ! -d "$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" ]; then \
		cd "$(WEBUI_TEST_PLAYWRIGHT_ROOT)" && \
		PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm_config_cache="$(WEBUI_TEST_NPM_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) npm install --no-audit --no-fund "playwright@$(WEBUI_TEST_PLAYWRIGHT_VERSION)"; \
	fi
	@PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) "$(WEBUI_TEST_PLAYWRIGHT_ROOT)/node_modules/.bin/playwright" install chromium
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/dependent-job-details/screenshot.mjs "$(WEBUI_TEST_DEPENDENT_SCREENSHOT)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/suspended-job-actions/screenshot.mjs "$(WEBUI_TEST_SUSPENDED_SCREENSHOT)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/websocket-reconnect-warnings/screenshot.mjs "$(WEBUI_TEST_RECONNECT_SCREENSHOT)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/live-heartbeat-details/screenshot.mjs "$(WEBUI_TEST_LIVE_HEARTBEAT_SCREENSHOT)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/completed-repgroup-visibility/screenshot.mjs "$(WEBUI_TEST_COMPLETED_REPGROUP_SCREENSHOT)" "$(WEBUI_TEST_COMPLETED_REPGROUP_TRACE)"
	@WR_FIXTURE_SCENARIO=deleted-refresh PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/completed-repgroup-visibility/screenshot.mjs "$(WEBUI_TEST_COMPLETED_REPGROUP_DELETED_REFRESH_SCREENSHOT)" "$(WEBUI_TEST_COMPLETED_REPGROUP_DELETED_REFRESH_TRACE)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/repgroup-bar-flicker/screenshot.mjs "$(WEBUI_TEST_BAR_FLICKER_SCREENSHOT)" "$(WEBUI_TEST_BAR_FLICKER_TRACE)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/removed-jobs-refresh/screenshot.mjs "$(WEBUI_TEST_REMOVED_REFRESH_SCREENSHOT)" "$(WEBUI_TEST_REMOVED_REFRESH_TRACE)"
	@PLAYWRIGHT_PACKAGE_DIR="$(WEBUI_TEST_PLAYWRIGHT_PACKAGE_DIR)" PLAYWRIGHT_BROWSERS_PATH="$(WEBUI_TEST_BROWSER_CACHE)" $(WEBUI_TEST_STEP_TIMEOUT) node jobqueue/testdata/status-count-reconcile/screenshot.mjs "$(WEBUI_TEST_COUNT_RECONCILE_SCREENSHOT)" "$(WEBUI_TEST_COUNT_RECONCILE_TRACE)"
	@echo "browser-test artifacts:"
	@echo "  $(WEBUI_TEST_DEPENDENT_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_SUSPENDED_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_RECONNECT_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_LIVE_HEARTBEAT_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_COMPLETED_REPGROUP_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_COMPLETED_REPGROUP_TRACE)"
	@echo "  $(WEBUI_TEST_COMPLETED_REPGROUP_DELETED_REFRESH_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_COMPLETED_REPGROUP_DELETED_REFRESH_TRACE)"
	@echo "  $(WEBUI_TEST_BAR_FLICKER_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_BAR_FLICKER_TRACE)"
	@echo "  $(WEBUI_TEST_REMOVED_REFRESH_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_REMOVED_REFRESH_TRACE)"
	@echo "  $(WEBUI_TEST_COUNT_RECONCILE_SCREENSHOT)"
	@echo "  $(WEBUI_TEST_COUNT_RECONCILE_TRACE)"

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

.PHONY: browser-test build test race bench lint lintextra install clean dist webui-test
