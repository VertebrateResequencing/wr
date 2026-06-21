PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -s -w -X ${PKG}/jobqueue.ServerVersion=${VERSION}
GOLANGCI_LINT_ARGS ?=
export GOPATH := $(shell go env GOPATH)
PATH := $(PATH):${GOPATH}/bin

default: install

build: export CGO_ENABLED = 0
build:
	go build -tags netgo -ldflags "${LDFLAGS}"

install: export CGO_ENABLED = 0
install:
	@rm -f ${GOPATH}/bin/wr
	@go install -tags netgo -ldflags "${LDFLAGS}"
	@echo installed to ${GOPATH}/bin/wr

# The jobqueue and client packages are by far the slowest: they spin up real
# servers and run jobs via subprocesses, and most of their wall-clock time is
# spent idle, waiting for timers and subprocesses rather than using CPU. Since
# every test now isolates itself onto its own free ports and temp manager dir
# (no shared fixed port or ~/.wr_development), we run many of them concurrently
# as independent `go test` processes whose idle waits overlap. The heaviest
# tests get their own lane; the lighter jobqueue tests share two lanes. The
# non-jobqueue lanes (client + everything else) are nice'd so the
# timing-sensitive jobqueue lanes get CPU priority on a busy machine.
ALL_SPLIT := TestJobqueueRunners|TestJobqueueRunners2|TestJobqueueSignal|TestJobqueueProduction|TestJobqueueMedium|TestJobqueueModify|TestServerWebI|TestJobqueueBasics|TestJobqueueLimitGroups|TestJobqueueModules|TestJobqueueHighMem|TestREST|TestJobqueueUtils|TestJobqueueMockRunner
JQ_RESTA := ^(TestJobqueueLimitGroups|TestREST|TestJobqueueHighMem|TestJobqueueUtils|TestJobqueueModules)$$
JQ_RESTB := ^(TestServerWebI|TestJobqueueBasics)$$
# jqB (the tests not named in any other lane - mostly the subscription tests) is
# split into two lanes of roughly equal duration; JQ_B1 names the first half.
JQ_B1 := TestJobSubscriptions|TestSubscriptionCatchUp|TestSubscriptionReconnectResync|TestSubscriptionTeardown|TestSubscriptionAtLeastOnceDedup|TestSubscriptionStateChangeEvents
OTHER_PKGS := $(shell go list ${PKG}/... | grep -v /vendor/ | grep -v '^${PKG}/jobqueue$$' | grep -v '^${PKG}/client$$' | grep -v '^${PKG}/jobqueue/scheduler$$')
GO_TEST := go test -tags netgo -timeout 40m --count 1 -failfast

test: export CGO_ENABLED = 0
test:
	@set -e; \
	base=$$(mktemp -d "$${TMPDIR:-/tmp}/wrtest.XXXXXX"); \
	rm -rf /tmp/jobqueue_cwd 2>/dev/null || true; \
	go test -tags netgo -c -o "$$base/jobqueue.test" ${PKG}/jobqueue; \
	go test -tags netgo -c -o "$$base/client.test" ${PKG}/client; \
	go test -tags netgo -c -o "$$base/scheduler.test" ${PKG}/jobqueue/scheduler; \
	rc=0; pids=""; \
	(cd jobqueue && WR_TEST_LANE=2 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueSignal$$') >"$$base/signal.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=4 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMedium$$') >"$$base/medium.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=5 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueModify$$') >"$$base/modify.log" 2>&1 & pids="$$pids $$!"; \
	(cd client && WR_TEST_LANE=12 GOCONVEY_REPORTER=silent nice -n 19 "$$base/client.test" -test.timeout=40m -test.failfast) >"$$base/client.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=0 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners$$') >"$$base/runners.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=1 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners2$$') >"$$base/runners2.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=3 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueProduction$$') >"$$base/production.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=6 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTA)') >"$$base/jqA1.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=7 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTB)') >"$$base/jqA2.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=8 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^($(JQ_B1))$$') >"$$base/jqB1.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=9 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.skip '$(ALL_SPLIT)|$(JQ_B1)') >"$$base/jqB2.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=10 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMockRunner$$') >"$$base/mock.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue/scheduler && GOCONVEY_REPORTER=silent "$$base/scheduler.test" -test.timeout=40m -test.failfast) >"$$base/scheduler.log" 2>&1 & pids="$$pids $$!"; \
	WR_TEST_LANE=13 nice -n 19 $(GO_TEST) -p 4 $(OTHER_PKGS) >"$$base/other.log" 2>&1 & pids="$$pids $$!"; \
	for pid in $$pids; do wait $$pid || rc=1; done; \
	if [ "$$rc" -ne 0 ]; then \
		for f in "$$base"/*.log; do echo "===== $$(basename "$$f" .log) ====="; cat "$$f"; done; \
	fi; \
	rm -rf "$$base" /tmp/jobqueue_cwd 2>/dev/null || true; \
	exit $$rc

# race benefits from the same approach as `test`: compile the race-enabled test
# binaries once, then run the heavy jobqueue tests as parallel lanes (they're
# mostly idle-bound even under -race, so their waits overlap) instead of one
# slow serial `go test`. The race detector is memory- and CPU-heavy, so we don't
# shard further than the per-test lanes. queue has a real-clock timing test that
# must not be starved (see queue_test.go), so it runs first, on its own.
race: export CGO_ENABLED = 1
race:
	@set -e; \
	base=$$(mktemp -d "$${TMPDIR:-/tmp}/wrrace.XXXXXX"); \
	rm -rf /tmp/jobqueue_cwd 2>/dev/null || true; \
	go test -tags netgo -race -c -o "$$base/jobqueue.test" ${PKG}/jobqueue; \
	go test -tags netgo -race -c -o "$$base/scheduler.test" ${PKG}/jobqueue/scheduler; \
	go test -tags netgo -race -c -o "$$base/cloud.test" ${PKG}/cloud; \
	rc=0; \
	go test -tags netgo -race --count 1 -failfast ${PKG}/queue >"$$base/queue.log" 2>&1 || rc=1; \
	pids=""; \
	(cd jobqueue && WR_TEST_LANE=0 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners$$') >"$$base/runners.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=1 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners2$$') >"$$base/runners2.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=2 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueSignal$$') >"$$base/signal.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=3 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueProduction$$') >"$$base/production.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=4 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMedium$$') >"$$base/medium.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=5 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueModify$$') >"$$base/modify.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=6 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTA)') >"$$base/jqA1.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=7 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTB)') >"$$base/jqA2.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=8 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^($(JQ_B1))$$') >"$$base/jqB1.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=9 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.skip '$(ALL_SPLIT)|$(JQ_B1)') >"$$base/jqB2.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue && WR_TEST_LANE=10 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMockRunner$$') >"$$base/mock.log" 2>&1 & pids="$$pids $$!"; \
	(cd jobqueue/scheduler && GOCONVEY_REPORTER=silent "$$base/scheduler.test" -test.timeout=40m -test.failfast) >"$$base/scheduler.log" 2>&1 & pids="$$pids $$!"; \
	(cd cloud && GOCONVEY_REPORTER=silent "$$base/cloud.test" -test.timeout=40m -test.failfast) >"$$base/cloud.log" 2>&1 & pids="$$pids $$!"; \
	nice -n 19 go test -tags netgo -race --count 1 -failfast ${PKG} ${PKG}/cmd ${PKG}/rp ${PKG}/limiter >"$$base/small.log" 2>&1 & pids="$$pids $$!"; \
	for pid in $$pids; do wait $$pid || rc=1; done; \
	if [ "$$rc" -ne 0 ]; then \
		for f in "$$base"/*.log; do echo "===== $$(basename "$$f" .log) ====="; cat "$$f"; done; \
	fi; \
	rm -rf "$$base" /tmp/jobqueue_cwd 2>/dev/null || true; \
	exit $$rc

# curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.50.1
lint:
	@golangci-lint run ${GOLANGCI_LINT_ARGS}

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

.PHONY: build test race lint lintextra install clean dist
