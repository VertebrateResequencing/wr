PKG := github.com/VertebrateResequencing/wr
PKG_LIST := $(shell go list ${PKG}/... | grep -v /vendor/)
GO_FILES := $(shell find . -name '*.go' | grep -v /vendor/)
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -ldflags "-X ${PKG}/cmd.wrVersion=${VERSION}"
GLIDE := $(shell command -v glide 2> /dev/null)

default: install

vendor: glide.lock
ifndef GLIDE
	@curl -s https://glide.sh/get | sh
endif
	@${GOPATH}/bin/glide -q install
	@echo installed latest dependencies

build: vendor
	go build -tags netgo ${LDFLAGS}

install: vendor
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
	@rm -fr ./vendor

dist:
	# go get -u github.com/gobuild/gopack
	gopack pack --os linux --arch amd64 -o linux-dist.zip
	gopack pack --os darwin --arch amd64 -o darwin-dist.zip
	# go get -u github.com/aktau/github-release
	github-release release --tag ${TAG} --pre-release
	github-release upload --tag ${TAG} --name wr-linux-x86-64.zip --file linux-dist.zip
	github-release upload --tag ${TAG} --name wr-macos-x86-64.zip --file darwin-dist.zip
	@rm -f wr linux-dist.zip darwin-dist.zip

.PHONY: build test report lint vet inef spell install clean dist
