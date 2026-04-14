# Makefile for ldviewer tool

.PHONY: all build install uninstall clean test

all: build

build:
	go build -o ldviewer$(shell go env GOEXE) -v .

test:
	go test -v .

install:
	go install -v .

uninstall:
	rm -f $(shell go env GOPATH)/bin/ldviewer$(shell go env GOEXE)

clean:
	go clean -v .
	rm -f ldviewer$(shell go env GOEXE)
