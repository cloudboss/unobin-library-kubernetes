DIR_ROOT := $(realpath $(CURDIR))
DIR_OUT := _output
UID := $(shell id -u)
GID := $(shell id -g)

CTR_IMAGE_GO := ghcr.io/cloudboss/docker.io/library/golang:1.26.2-alpine3.23
CTR_IMAGE_LINT := ghcr.io/cloudboss/docker.io/golangci/golangci-lint:v2.11.4-alpine

HAS_COMMAND_DOCKER := $(DIR_OUT)/.command-docker
BUILD_INPUTS := $(shell find . \
	\( -path './$(DIR_OUT)' -o -path './.*' \) -prune -o -print)

RUN_GO = docker run --rm \
	-v $(DIR_ROOT):/code:z \
	-u $(UID):$(GID) -w /code \
	-e GOPATH=/code/$(DIR_OUT)/go \
	-e GOCACHE=/code/$(DIR_OUT)/gocache \
	-e XDG_CACHE_HOME=/code/$(DIR_OUT)/xdg-cache \
	$(CTR_IMAGE_GO)

.DEFAULT_GOAL := help

.PHONY: help deps build test vet lint

help:
	@echo 'Targets (run in Docker):'
	@echo '  deps   Download Go module dependencies.'
	@echo '  build  Build library packages.'
	@echo '  test   Run Go tests.'
	@echo '  vet    Run go vet.'
	@echo '  lint   Run golangci-lint.'

$(DIR_OUT):
	@mkdir -p $(@)

$(DIR_OUT)/%/: | $(DIR_OUT)
	@mkdir -p $(@)

$(DIR_OUT)/.command-%: | $(DIR_OUT)
	@command -v $(*) >/dev/null 2>&1 && touch $(@) || \
		{ echo 'command $(*) is required' >&2; exit 1; }

$(DIR_OUT)/.deps $(DIR_OUT)/.build test vet lint: \
		| $(HAS_COMMAND_DOCKER) $(DIR_OUT)/go/ $(DIR_OUT)/gocache/ $(DIR_OUT)/xdg-cache/

deps: $(DIR_OUT)/.deps

$(DIR_OUT)/.deps: Makefile go.mod $(wildcard go.sum) .
	@rm -f $(@)
	@$(RUN_GO) go mod download
	@touch $(@)

build: $(DIR_OUT)/.build

$(DIR_OUT)/.build: $(BUILD_INPUTS) $(DIR_OUT)/.deps
	@rm -f $(@)
	@$(RUN_GO) go build ./...
	@touch $(@)

test:
	@$(RUN_GO) go test -v ./...

vet:
	@$(RUN_GO) go vet ./...

lint:
	@docker run --rm \
		-v $(DIR_ROOT):/code:z \
		-u $(UID):$(GID) -w /code \
		-e GOPATH=/code/$(DIR_OUT)/go \
		-e GOCACHE=/code/$(DIR_OUT)/gocache \
		-e XDG_CACHE_HOME=/code/$(DIR_OUT)/xdg-cache \
		$(CTR_IMAGE_LINT) golangci-lint run -v ./...
