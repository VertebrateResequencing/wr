PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -ldflags "-X ${PKG}/cmd.wrVersion=${VERSION}"
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
	@go test -p 1 -tags netgo -race -v --count 1 ./queue
	@go test -p 1 -tags netgo -race -v --count 1 ./jobqueue
	@go test -p 1 -tags netgo -race -v --count 1 -timeout 20m ./jobqueue/scheduler
	@go test -p 1 -tags netgo -race -v --count 1 -timeout 20m ./cloud
	@go test -p 1 -tags netgo -race -v --count 1 ./rp

# cd $HOME/go && curl -L https://git.io/vp6lP | sh
# until all go tools have module support:
# mkdir -p $HOME/go/src
# go mod vendor
# rsync -a vendor/ ~/go/src/
# rm -fr vendor
# ln -s $PWD $HOME/go/src/github.com/VertebrateResequencing/wr
lint: export GO111MODULE = off
lint:
	@gometalinter --vendor --aggregate --deadline=240s ./... | sort

lintextra: export GO111MODULE = off
lintextra:
	@gometalinter --vendor --aggregate --deadline=240s --disable-all --enable=gocyclo --enable=dupl ./... | sort

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
