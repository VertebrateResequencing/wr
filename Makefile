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
#
# Because of this parallelism a test can be starved of CPU at any moment, so
# tests must poll for asynchronous state rather than assert on it after a fixed
# sleep. See the "Test reliability conventions" comment near pollUntil in
# jobqueue/jobqueue_test.go before adding or changing tests.
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
	print_logs() { found=0; for marker in "$$base"/*.fail; do [ -e "$$marker" ] || continue; f="$${marker%.fail}.log"; [ -f "$$f" ] || continue; found=1; echo "===== $$(basename "$$f" .log) ====="; cat "$$f"; done; if [ "$$found" -eq 0 ]; then for f in "$$base"/*.log; do echo "===== $$(basename "$$f" .log) ====="; cat "$$f"; done; fi; }; \
	(cd jobqueue && WR_TEST_LANE=2 WR_TEST_SHARD=a GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueSignal$$') >"$$base/signal_a.log" 2>&1 & pids="$$pids $$!:signal_a"; \
	(cd jobqueue && WR_TEST_LANE=14 WR_TEST_SHARD=b GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueSignal$$') >"$$base/signal_b.log" 2>&1 & pids="$$pids $$!:signal_b"; \
	(cd jobqueue && WR_TEST_LANE=4 WR_TEST_SHARD=a GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMedium$$') >"$$base/medium_a.log" 2>&1 & pids="$$pids $$!:medium_a"; \
	(cd jobqueue && WR_TEST_LANE=15 WR_TEST_SHARD=b GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMedium$$') >"$$base/medium_b.log" 2>&1 & pids="$$pids $$!:medium_b"; \
	(cd jobqueue && WR_TEST_LANE=5 WR_TEST_SHARD=a GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueModify$$') >"$$base/modify_a.log" 2>&1 & pids="$$pids $$!:modify_a"; \
	(cd jobqueue && WR_TEST_LANE=16 WR_TEST_SHARD=b GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueModify$$') >"$$base/modify_b.log" 2>&1 & pids="$$pids $$!:modify_b"; \
	(cd client && WR_TEST_LANE=12 GOCONVEY_REPORTER=silent nice -n 19 "$$base/client.test" -test.timeout=40m -test.failfast -test.run '^TestScheduler$$') >"$$base/client_a.log" 2>&1 & pids="$$pids $$!:client_a"; \
	(cd client && WR_TEST_LANE=17 GOCONVEY_REPORTER=silent nice -n 19 "$$base/client.test" -test.timeout=40m -test.failfast -test.skip '^TestScheduler$$') >"$$base/client_b.log" 2>&1 & pids="$$pids $$!:client_b"; \
	(cd jobqueue && WR_TEST_LANE=0 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners$$') >"$$base/runners.log" 2>&1 & pids="$$pids $$!:runners"; \
	(cd jobqueue && WR_TEST_LANE=1 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners2$$') >"$$base/runners2.log" 2>&1 & pids="$$pids $$!:runners2"; \
	(cd jobqueue && WR_TEST_LANE=3 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueProduction$$') >"$$base/production.log" 2>&1 & pids="$$pids $$!:production"; \
	(cd jobqueue && WR_TEST_LANE=6 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTA)') >"$$base/jqA1.log" 2>&1 & pids="$$pids $$!:jqA1"; \
	(cd jobqueue && WR_TEST_LANE=7 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTB)') >"$$base/jqA2.log" 2>&1 & pids="$$pids $$!:jqA2"; \
	(cd jobqueue && WR_TEST_LANE=8 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^($(JQ_B1))$$') >"$$base/jqB1.log" 2>&1 & pids="$$pids $$!:jqB1"; \
	(cd jobqueue && WR_TEST_LANE=9 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.skip '$(ALL_SPLIT)|$(JQ_B1)') >"$$base/jqB2.log" 2>&1 & pids="$$pids $$!:jqB2"; \
	(cd jobqueue && WR_TEST_LANE=10 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMockRunner$$') >"$$base/mock.log" 2>&1 & pids="$$pids $$!:mock"; \
	(cd jobqueue/scheduler && GOCONVEY_REPORTER=silent "$$base/scheduler.test" -test.timeout=40m -test.failfast) >"$$base/scheduler.log" 2>&1 & pids="$$pids $$!:scheduler"; \
	WR_TEST_LANE=13 nice -n 19 $(GO_TEST) -p 4 $(OTHER_PKGS) >"$$base/other.log" 2>&1 & pids="$$pids $$!:other"; \
	for lane in $$pids; do pid=$${lane%%:*}; name=$${lane#*:}; wait $$pid || { touch "$$base/$$name.fail"; rc=1; }; done; \
	if [ "$$rc" -ne 0 ]; then \
		print_logs; \
	fi; \
	rm -rf "$$base" /tmp/jobqueue_cwd 2>/dev/null || true; \
	exit $$rc

# race benefits from the same approach as `test`: compile the race-enabled test
# binaries once, then run the heavy jobqueue tests as parallel lanes (they're
# mostly idle-bound even under -race, so their waits overlap) instead of one
# slow serial `go test`. queue has a real-clock timing test that must not be
# starved (see queue_test.go), so it runs first, on its own.
race: export CGO_ENABLED = 1
race:
	@set -e; \
	base=$$(mktemp -d "$${TMPDIR:-/tmp}/wrrace.XXXXXX"); \
	rm -rf /tmp/jobqueue_cwd 2>/dev/null || true; \
	go test -tags netgo -race -c -o "$$base/jobqueue.test" ${PKG}/jobqueue; \
	go test -tags netgo -race -c -o "$$base/scheduler.test" ${PKG}/jobqueue/scheduler; \
	go test -tags netgo -race -c -o "$$base/cloud.test" ${PKG}/cloud; \
	rc=0; \
	print_logs() { found=0; for marker in "$$base"/*.fail; do [ -e "$$marker" ] || continue; f="$${marker%.fail}.log"; [ -f "$$f" ] || continue; found=1; echo "===== $$(basename "$$f" .log) ====="; cat "$$f"; done; if [ "$$found" -eq 0 ]; then for f in "$$base"/*.log; do echo "===== $$(basename "$$f" .log) ====="; cat "$$f"; done; fi; }; \
	go test -tags netgo -race --count 1 -failfast ${PKG}/queue >"$$base/queue.log" 2>&1 || { touch "$$base/queue.fail"; rc=1; }; \
	pids=""; \
	(cd jobqueue && WR_TEST_LANE=0 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners$$') >"$$base/runners.log" 2>&1 & pids="$$pids $$!:runners"; \
	(cd jobqueue && WR_TEST_LANE=1 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueRunners2$$') >"$$base/runners2.log" 2>&1 & pids="$$pids $$!:runners2"; \
	(cd jobqueue && WR_TEST_LANE=2 WR_TEST_SHARD=a GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueSignal$$') >"$$base/signal_a.log" 2>&1 & pids="$$pids $$!:signal_a"; \
	(cd jobqueue && WR_TEST_LANE=14 WR_TEST_SHARD=b GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueSignal$$') >"$$base/signal_b.log" 2>&1 & pids="$$pids $$!:signal_b"; \
	(cd jobqueue && WR_TEST_LANE=3 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueProduction$$') >"$$base/production.log" 2>&1 & pids="$$pids $$!:production"; \
	(cd jobqueue && WR_TEST_LANE=4 WR_TEST_SHARD=a GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMedium$$') >"$$base/medium_a.log" 2>&1 & pids="$$pids $$!:medium_a"; \
	(cd jobqueue && WR_TEST_LANE=15 WR_TEST_SHARD=b GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMedium$$') >"$$base/medium_b.log" 2>&1 & pids="$$pids $$!:medium_b"; \
	(cd jobqueue && WR_TEST_LANE=5 WR_TEST_SHARD=a GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueModify$$') >"$$base/modify_a.log" 2>&1 & pids="$$pids $$!:modify_a"; \
	(cd jobqueue && WR_TEST_LANE=16 WR_TEST_SHARD=b GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueModify$$') >"$$base/modify_b.log" 2>&1 & pids="$$pids $$!:modify_b"; \
	(cd jobqueue && WR_TEST_LANE=6 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTA)') >"$$base/jqA1.log" 2>&1 & pids="$$pids $$!:jqA1"; \
	(cd jobqueue && WR_TEST_LANE=7 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '$(JQ_RESTB)') >"$$base/jqA2.log" 2>&1 & pids="$$pids $$!:jqA2"; \
	(cd jobqueue && WR_TEST_LANE=8 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^($(JQ_B1))$$') >"$$base/jqB1.log" 2>&1 & pids="$$pids $$!:jqB1"; \
	(cd jobqueue && WR_TEST_LANE=9 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.skip '$(ALL_SPLIT)|$(JQ_B1)') >"$$base/jqB2.log" 2>&1 & pids="$$pids $$!:jqB2"; \
	(cd jobqueue && WR_TEST_LANE=10 GOCONVEY_REPORTER=silent "$$base/jobqueue.test" -test.timeout=40m -test.failfast -test.run '^TestJobqueueMockRunner$$') >"$$base/mock.log" 2>&1 & pids="$$pids $$!:mock"; \
	(cd jobqueue/scheduler && GOCONVEY_REPORTER=silent "$$base/scheduler.test" -test.timeout=40m -test.failfast) >"$$base/scheduler.log" 2>&1 & pids="$$pids $$!:scheduler"; \
	(cd cloud && GOCONVEY_REPORTER=silent "$$base/cloud.test" -test.timeout=40m -test.failfast) >"$$base/cloud.log" 2>&1 & pids="$$pids $$!:cloud"; \
	nice -n 19 go test -tags netgo -race --count 1 -failfast ${PKG} ${PKG}/cmd ${PKG}/rp ${PKG}/limiter >"$$base/small.log" 2>&1 & pids="$$pids $$!:small"; \
	for lane in $$pids; do pid=$${lane%%:*}; name=$${lane#*:}; wait $$pid || { touch "$$base/$$name.fail"; rc=1; }; done; \
	if [ "$$rc" -ne 0 ]; then \
		print_logs; \
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
