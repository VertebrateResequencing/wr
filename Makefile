PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -ldflags "-X ${PKG}/cmd.wrVersion=${VERSION}"
export GOPATH := $(shell go env GOPATH)
PATH := $(PATH):${GOPATH}/bin
SHELL := env PATH=${PATH} $(SHELL)
GLIDE := $(shell command -v glide 2> /dev/null)

default: install

vendor: glide.lock
ifndef GLIDE
	@mkdir -p ${GOPATH}/bin
	@curl -s https://glide.sh/get | sh
endif
	@glide -q install
	@echo installed latest dependencies

build: export CGO_ENABLED = 0
build: vendor
	go build -tags netgo ${LDFLAGS}

install: export CGO_ENABLED = 0
install: vendor
	@rm -f ${GOPATH}/bin/wr
	@go install -tags netgo ${LDFLAGS}
	@echo installed to ${GOPATH}/bin/wr

compile_k8s_tmp: /tmp/wr
/tmp/wr:
	export CGO_ENABLED=0	&& \
	go build -o /tmp/wr

test: export CGO_ENABLED = 0
test:
	@go test -p 1 -tags netgo -timeout 20m --count 1 ${PKG_LIST}

test-e2e: ## Run E2E tests. E2E tests may be destructive. Requires working Kubernetes cluster and a Kubeconfig file.
	./kubernetes/run-e2e.sh

race: export CGO_ENABLED = 1
race:
	@go test -p 1 -tags netgo -race -v --count 1 ./queue
	@go test -p 1 -tags netgo -race -v --count 1 ./jobqueue
	@go test -p 1 -tags netgo -race -v --count 1 -timeout 20m ./jobqueue/scheduler
	@go test -p 1 -tags netgo -race -v --count 1 -timeout 20m ./cloud
	@go test -p 1 -tags netgo -race -v --count 1 ./rp

# go get -u gopkg.in/alecthomas/gometalinter.v2
# gometalinter.v2 --install
lint:
	@gometalinter.v2 --vendor --aggregate --deadline=120s ./... | sort

lintextra:
	@gometalinter.v2 --vendor --aggregate --deadline=120s --disable-all --enable=gocyclo --enable=dupl ./... | sort

clean:
	@rm -f ./wr
	@rm -f ./dist.zip
	@rm -fr ./vendor

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

.PHONY: build test race lint lintextra install clean dist compile_k8s_tmp test-e2e
