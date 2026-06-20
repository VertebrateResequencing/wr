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

# The jobqueue package is by far the slowest and, unlike the others, binds a
# fixed manager port and uses a single manager dir (~/.wr_development), so its
# tests can't overlap each other on that shared port. We therefore split it into
# two groups, each run as its own `go test` process isolated onto a private
# manager port, web port, manager dir and TMPDIR via env vars (so their servers,
# bolt DBs, TLS certs and job working dirs don't collide). Using separate
# processes (rather than t.Parallel within one) also keeps each group's
# package-level timing globals -- ServerItemTTR, ClientTouchInterval etc. --
# independent, which tests rely on setting per-scenario. Those two groups run in
# parallel with each other and with a third lane that covers every other
# package. The two heaviest, subprocess-spawning jobqueue tests (Runners and
# Signal) are deliberately kept together in one group so they run one-at-a-time
# and don't oversubscribe the CPUs alongside each other. This roughly halves the
# wall-clock time versus running everything serially behind the jobqueue package.
JQ_GROUP := TestJobqueueRunners|TestJobqueueSignal|TestJobqueueProduction
OTHER_PKGS := $(shell go list ${PKG}/... | grep -v /vendor/ | grep -v '^${PKG}/jobqueue$$')
GO_TEST := go test -tags netgo -timeout 40m --count 1 -failfast

test: export CGO_ENABLED = 0
test:
	@set -e; \
	base=$$(mktemp -d "$${TMPDIR:-/tmp}/wrtest.XXXXXX"); \
	mkdir -p "$$base/g1d" "$$base/g2d" "$$base/g1t" "$$base/g2t"; \
	echo "testing: 2 parallel jobqueue groups + other packages ($$base)"; \
	rc=0; \
	TMPDIR="$$base/g1t" WR_MANAGERPORT=55001 WR_MANAGERWEB=55002 WR_MANAGERDIR="$$base/g1d" \
		$(GO_TEST) -run '$(JQ_GROUP)' ${PKG}/jobqueue >"$$base/g1.log" 2>&1 & p1=$$!; \
	TMPDIR="$$base/g2t" WR_MANAGERPORT=55101 WR_MANAGERWEB=55102 WR_MANAGERDIR="$$base/g2d" \
		$(GO_TEST) -skip '$(JQ_GROUP)' ${PKG}/jobqueue >"$$base/g2.log" 2>&1 & p2=$$!; \
	nice -n 19 $(GO_TEST) -p 2 $(OTHER_PKGS) >"$$base/o.log" 2>&1 & p3=$$!; \
	wait $$p1 || rc=1; \
	wait $$p2 || rc=1; \
	wait $$p3 || rc=1; \
	echo "===== jobqueue group 1 (Runners/Signal/Production) ====="; cat "$$base/g1.log"; \
	echo "===== jobqueue group 2 (rest of jobqueue) ====="; cat "$$base/g2.log"; \
	echo "===== other packages ====="; cat "$$base/o.log"; \
	rm -rf "$$base"; \
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
