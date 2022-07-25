#
# Intro

go.modname := $(shell go list -m)
go.tidyflags = -go=1.18 -compat=1.18

help:
	@echo 'Usage:'
	@echo '  make help'
	@echo '  make check'
	@echo '  make $(notdir $(go.modname)).cov.html'
	@echo '  make lint'
	@echo '  make go-mod-tidy'
.PHONY: help

.SECONDARY:
.PHONY: FORCE
SHELL = bash

#
# Test suite

$(notdir $(go.modname)).cov: check
	test -e $@
	touch $@
check:
	go test -count=1 -coverprofile=$(notdir $(go.modname)).cov -coverpkg=./... -race ./...
.PHONY: check

%.cov.html: %.cov
	go tool cover -html=$< -o=$@

#
# Lint

lint: tools/bin/golangci-lint
	tools/bin/golangci-lint run ./...
.PHONY: lint

#
# go mod tidy

go-mod-tidy:
.PHONY: go-mod-tidy

go-mod-tidy: go-mod-tidy/main
go-mod-tidy/main:
	rm -f go.sum
	GOFLAGS=-mod=mod go mod tidy $(go.tidyflags)
.PHONY: go-mod-tidy/main

#
# Tools

go-mod-tidy: $(patsubst tools/src/%/go.mod,go-mod-tidy/tools/%,$(wildcard tools/src/*/go.mod))
go-mod-tidy/tools/%:
	rm -f tools/src/$*/go.sum
	cd tools/src/$* && GOFLAGS=-mod=mod go mod tidy $(go.tidyflags)
.PHONY: go-mod-tidy/tools/%

tools/bin/%: tools/src/%/go.mod tools/src/%/pin.go
	cd $(<D) && GOOS= GOARCH= go build -o $(abspath $@) $$(sed -En 's,^import "(.*)".*,\1,p' pin.go)
