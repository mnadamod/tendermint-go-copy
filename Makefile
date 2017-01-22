GOTOOLS = \
					github.com/mitchellh/gox \
					github.com/Masterminds/glide
PACKAGES=$(shell go list ./... | grep -v '/vendor/')
BUILD_TAGS?=tendermint
TMROOT = $${TMROOT:-$$HOME/.tendermint}

all: get_deps install test

install: get_deps
	@go install ./cmd/tendermint

build:
	go build -o build/tendermint ./cmd/tendermint

build_race:
	go build -race -o build/tendermint ./cmd/tendermint

test: build
	@echo "--> Running go test"
	@go test $(PACKAGES)

test_race: build
	@echo "--> Running go test --race"
	@go test -race $(PACKAGES)

test_integrations:
	@bash ./test/test.sh

test100: build
	@for i in {1..100}; do make test; done

draw_deps:
	# requires brew install graphviz
	go get github.com/hirokidaichi/goviz
	goviz -i ./cmd/tendermint | dot -Tpng -o huge.png

list_deps:
	@go list -f '{{join .Deps "\n"}}' ./... | \
		grep -v /vendor/ | sort | uniq | \
		xargs go list -f '{{if not .Standard}}{{.ImportPath}}{{end}}'

get_deps:
	@go get -d $(PACKAGES)
	@go list -f '{{join .TestImports "\n"}}' ./... | \
		grep -v /vendor/ | sort | uniq | \
		xargs go get

get_vendor_deps: tools
	@rm -rf vendor/
	@echo "--> Running glide install"
	@glide install

update_deps:
	@echo "--> Updating dependencies"
	@go get -d -u ./...

revision:
	-echo `git rev-parse --verify HEAD` > $(TMROOT)/revision
	-echo `git rev-parse --verify HEAD` >> $(TMROOT)/revision_history

tools:
	go get -u -v $(GOTOOLS)

.PHONY: install build build_race dist test test_race test_integrations test100 draw_deps list_deps get_deps get_vendor_deps update_deps revision tools
