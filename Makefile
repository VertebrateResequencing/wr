PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -s -w -X ${PKG}/jobqueue.ServerVersion=${VERSION}
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

compile_k8s_tmp: /tmp/wr
/tmp/wr:
	export CGO_ENABLED=0	&& \
	go build -i -o /tmp/wr

test: export CGO_ENABLED = 0
test:
	@go test -p 1 -tags netgo -timeout 40m --count 1 -failfast ${PKG_LIST}

test-e2e: compile_k8s_tmp ## Run E2E tests. E2E tests may be destructive. Requires working Kubernetes cluster and a Kubeconfig file.
	./kubernetes/run-e2e.sh

test-k8s-unit: compile_k8s_tmp ## Run the unit and integration tests for the kubernetes driver
	./kubernetes/run-unit.sh

race: export CGO_ENABLED = 1
race:
	go test -p 1 -tags netgo -race --count 1 -failfast ./
	go test -p 1 -tags netgo -race --count 1 -failfast ./queue
	go test -p 1 -tags netgo -race --count 1 -failfast -timeout 30m ./jobqueue
	go test -p 1 -tags netgo -race --count 1 -failfast -timeout 40m ./jobqueue/scheduler
	go test -p 1 -tags netgo -race --count 1 -failfast -timeout 40m ./cloud
	go test -p 1 -tags netgo -race --count 1 -failfast ./rp
	go test -p 1 -tags netgo -race --count 1 -failfast ./limiter

# curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v1.50.1
lint:
	@golangci-lint run

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

.PHONY: build test race lint lintextra install clean dist compile_k8s_tmp test-e2e test-k8s-unit
