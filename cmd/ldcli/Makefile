# Makefile for ldcli tool

.PHONY: all build install uninstall clean test

all: build

build:
	go build -v .

test:
	go test -v .

install:
	go install -v .

uninstall:
	rm -f $(shell go env GOPATH)/bin/ldcli$(shell go env GOEXE)

clean:
	go clean -v .
