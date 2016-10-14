DIST_DIRS := find * -type d -exec
VERSION := $(shell git describe --tags --always --long --dirty)
TAG := $(shell git describe --abbrev=0 --tags)
LDFLAGS = -ldflags "-X github.com/VertebrateResequencing/wr/cmd.wrVersion=${VERSION}"

default: install

build:
	go build -tags netgo ${LDFLAGS}

install:
	rm -f ${GOPATH}/bin/wr
	go install -tags netgo ${LDFLAGS}

test:
	go test -p 1 -tags netgo ./queue ./jobqueue ./jobqueue/scheduler ./cloud

clean:
	rm -f ./wr
	rm -f ./dist.zip

dist:
	# go get -u github.com/gobuild/gopack
	gopack pack -o dist.zip
	# go get -u github.com/aktau/github-release
	github-release release --tag ${TAG} --pre-release
	github-release upload --tag ${TAG} --name wr-linux-amd64.zip --file dist.zip
	rm -f wr dist.zip

.PHONY: build test install clean dist
