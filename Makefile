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
ALL_SPLIT := TestJobqueueRunners|TestJobqueueSignal|TestJobqueueProduction|TestJobqueueMedium|TestJobqueueModify|TestServerWebI|TestJobqueueBasics|TestJobqueueLimitGroups|TestJobqueueModules|TestJobqueueHighMem|TestREST|TestJobqueueUtils|TestJobqueueMockRunner
JQ_RESTA := ^(TestServerWebI|TestJobqueueBasics|TestJobqueueLimitGroups|TestJobqueueModules|TestJobqueueHighMem|TestREST|TestJobqueueUtils)$$
OTHER_PKGS := $(shell go list ${PKG}/... | grep -v /vendor/ | grep -v '^${PKG}/jobqueue$$' | grep -v '^${PKG}/client$$' | grep -v '^${PKG}/jobqueue/scheduler$$')
GO_TEST := go test -tags netgo -timeout 40m --count 1 -failfast

test: export CGO_ENABLED = 0
test:
	@set -e; \
	base=$$(mktemp -d "$${TMPDIR:-/tmp}/wrtest.XXXXXX"); \
	rm -rf /tmp/jobqueue_cwd 2>/dev/null || true; \
	echo "testing: parallel per-test jobqueue lanes + client + other packages ($$base)"; \
	rc=0; \
	$(GO_TEST) -run '^TestJobqueueRunners$$' ${PKG}/jobqueue >"$$base/runners.log" 2>&1 & p1=$$!; \
	$(GO_TEST) -run '^TestJobqueueSignal$$' ${PKG}/jobqueue >"$$base/signal.log" 2>&1 & p2=$$!; \
	$(GO_TEST) -run '^TestJobqueueProduction$$' ${PKG}/jobqueue >"$$base/production.log" 2>&1 & p3=$$!; \
	$(GO_TEST) -run '^TestJobqueueMedium$$' ${PKG}/jobqueue >"$$base/medium.log" 2>&1 & p4=$$!; \
	$(GO_TEST) -run '^TestJobqueueModify$$' ${PKG}/jobqueue >"$$base/modify.log" 2>&1 & p5=$$!; \
	$(GO_TEST) -run '$(JQ_RESTA)' ${PKG}/jobqueue >"$$base/jqA.log" 2>&1 & p6=$$!; \
	$(GO_TEST) -skip '$(ALL_SPLIT)' ${PKG}/jobqueue >"$$base/jqB.log" 2>&1 & p7=$$!; \
	$(GO_TEST) -run '^TestJobqueueMockRunner$$' ${PKG}/jobqueue >"$$base/mock.log" 2>&1 & pm=$$!; \
	$(GO_TEST) ${PKG}/jobqueue/scheduler >"$$base/scheduler.log" 2>&1 & p8=$$!; \
	nice -n 19 $(GO_TEST) ${PKG}/client >"$$base/client.log" 2>&1 & p9=$$!; \
	nice -n 19 $(GO_TEST) -p 4 $(OTHER_PKGS) >"$$base/other.log" 2>&1 & p10=$$!; \
	for pid in $$p1 $$p2 $$p3 $$p4 $$p5 $$p6 $$p7 $$pm $$p8 $$p9 $$p10; do wait $$pid || rc=1; done; \
	for f in runners signal production medium modify jqA jqB mock scheduler client other; do \
		echo "===== $$f ====="; cat "$$base/$$f.log"; \
	done; \
	rm -rf "$$base" /tmp/jobqueue_cwd 2>/dev/null || true; \
	exit $$rc

race: export CGO_ENABLED = 1
race:
	go test -p 1 -tags netgo -race --count 1 -failfast ./
	go test -p 1 -tags netgo -race --count 1 -failfast ./cmd
	go test -p 1 -tags netgo -race --count 1 -failfast ./queue
	go test -p 1 -tags netgo -race --count 1 -failfast -timeout 30m ./jobqueue
	go test -p 1 -tags netgo -race --count 1 -failfast -timeout 40m ./jobqueue/scheduler
	go test -p 1 -tags netgo -race --count 1 -failfast -timeout 40m ./cloud
	go test -p 1 -tags netgo -race --count 1 -failfast ./rp
	go test -p 1 -tags netgo -race --count 1 -failfast ./limiter

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
