PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -ldflags "-X ${PKG}/cmd.wrVersion=${VERSION}"

default: install

build:
	go build -tags netgo ${LDFLAGS}

install:
	@rm -f ${GOPATH}/bin/wr
	@go install -tags netgo ${LDFLAGS}
	@echo installed to ${GOPATH}/bin/wr

test:
	@go test -p 1 -tags netgo ${PKG_LIST}

report: lint vet inef spell

lint:
	@for file in ${GO_FILES} ;  do \
		gofmt -s -l $$file ; \
		golint $$file ; \
	done

vet:
	@go vet ${PKG_LIST}

inef:
	@ineffassign ./

spell:
	@misspell ${PKG_LIST}

clean:
	@rm -f ./wr
	@rm -f ./dist.zip

dist:
	# go get -u github.com/gobuild/gopack
	gopack pack -o dist.zip
	# go get -u github.com/aktau/github-release
	github-release release --tag ${TAG} --pre-release
	github-release upload --tag ${TAG} --name wr-linux-x86-64.zip --file dist.zip
	@rm -f wr dist.zip

.PHONY: build test report lint vet inef spell install clean dist
