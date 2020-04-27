PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -ldflags "-X ${PKG}/jobqueue.ServerVersion=${VERSION}"
export GOPATH := $(shell go env GOPATH)
PATH := $(PATH):${GOPATH}/bin

default: install

build: export CGO_ENABLED = 0
build:
	go build -tags netgo ${LDFLAGS}

install: export CGO_ENABLED = 0
install:
	@rm -f ${GOPATH}/bin/wr
	@go install -tags netgo ${LDFLAGS}
	@echo installed to ${GOPATH}/bin/wr

compile_k8s_tmp: /tmp/wr
/tmp/wr:
	export CGO_ENABLED=0	&& \
	go build -i -o /tmp/wr

test: export CGO_ENABLED = 0
test:
	@go test -p 1 -tags netgo -timeout 20m --count 1 ${PKG_LIST}

test-e2e: compile_k8s_tmp ## Run E2E tests. E2E tests may be destructive. Requires working Kubernetes cluster and a Kubeconfig file.
	./kubernetes/run-e2e.sh

test-k8s-unit: compile_k8s_tmp ## Run the unit and integration tests for the kubernetes driver
	./kubernetes/run-unit.sh

race: export CGO_ENABLED = 1
race:
	# *** -gcflags=all=-d=checkptr=0 is temporarily required until bbolt is fixed (<=1.3.3 has unsafe pointer usage)
	go test -p 1 -tags netgo -race --count 1 ./
	go test -p 1 -tags netgo -race --count 1 -gcflags=all=-d=checkptr=0 ./queue
	go test -p 1 -tags netgo -race --count 1 -gcflags=all=-d=checkptr=0 -timeout 30m ./jobqueue
	go test -p 1 -tags netgo -race --count 1 -gcflags=all=-d=checkptr=0 -timeout 40m ./jobqueue/scheduler
	go test -p 1 -tags netgo -race --count 1 -gcflags=all=-d=checkptr=0 -timeout 40m ./cloud
	go test -p 1 -tags netgo -race --count 1 -gcflags=all=-d=checkptr=0 ./rp
	go test -p 1 -tags netgo -race --count 1 -gcflags=all=-d=checkptr=0 ./limiter

# curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(go env GOPATH)/bin v1.16.0
lint:
	@golangci-lint run

lintextra:
	@golangci-lint run -c .golangci_extra.yml

clean:
	@rm -f ./wr
	@rm -f ./dist.zip
	@rm -fr ./vendor
	@rm -f /tmp/wr

dist: export CGO_ENABLED = 0
# go get -u github.com/gobuild/gopack
# go get -u github.com/aktau/github-release
dist:
	gopack pack --os linux --arch amd64 -o linux-dist.zip
	gopack pack --os darwin --arch amd64 -o darwin-dist.zip
	github-release release --tag ${TAG} --pre-release
	github-release upload --tag ${TAG} --name wr-linux-x86-64.zip --file linux-dist.zip
	github-release upload --tag ${TAG} --name wr-macos-x86-64.zip --file darwin-dist.zip
	@rm -f wr linux-dist.zip darwin-dist.zip

.PHONY: build test race lint lintextra install clean dist compile_k8s_tmp test-e2e test-k8s-unit
