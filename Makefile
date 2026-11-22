PROJECT := unobin-library-kubernetes
DIR_ROOT := $(realpath $(CURDIR))
DIR_OUT := _output
UID := $(shell id -u)
GID := $(shell id -g)

CTR_IMAGE_GO := ghcr.io/cloudboss/docker.io/library/golang:1.26.2-alpine3.23
CTR_IMAGE_LINT := ghcr.io/cloudboss/docker.io/golangci/golangci-lint:v2.11.4-alpine
KIND_VERSION := v0.31.0
KIND_NODE := kindest/node:v1.33.7@sha256:d26ef333bdb2cbe9862a0f7c3803ecc7b4303d8cea8e814b481b09949d353040
SHA256 ?= $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')
KIND_IMAGE_HASH := $(shell { printf '%s\n' '$(CTR_IMAGE_GO)' '$(KIND_VERSION)'; \
	cat Containerfile.integration; } | $(SHA256) | cut -c 1-40)
CTR_IMAGE_KIND := $(PROJECT)-kind:$(KIND_IMAGE_HASH)
HAS_IMAGE_KIND := $(DIR_OUT)/.image-kind-$(KIND_IMAGE_HASH)
UNOBIN_VERSION := $(shell awk '/github.com[/]cloudboss[/]unobin v/{print $$2}' go.mod)
DOCGEN ?= go run github.com/cloudboss/cloudboss-docs/unobin/cmd/docgen@v0.2.1

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

.PHONY: help deps build docs test vet lint test-integration-kind test-integration-live

help:
	@echo 'Targets:'
	@echo '  deps   Download Go module dependencies.'
	@echo '  build  Build library packages.'
	@echo '  docs   Generate the reference manual.'
	@echo '  test   Run Go tests.'
	@echo '  vet    Run go vet.'
	@echo '  lint   Run golangci-lint.'
	@echo '  test-integration-kind Run scenarios in a temporary kind cluster.'
	@echo '  test-integration-live Run scenarios against KUBECONFIG (UNOBIN_KUBERNETES_LIVE=1).'

$(DIR_OUT):
	@mkdir -p $(@)

$(DIR_OUT)/%/: | $(DIR_OUT)
	@mkdir -p $(@)

docs:
	@$(DOCGEN) --root $(DIR_ROOT) --collection docs/reference-libraries.json --out docs/reference

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

$(DIR_OUT)/integration/bin/unobin: go.mod Makefile | $(HAS_COMMAND_DOCKER) \
		$(DIR_OUT)/go/ $(DIR_OUT)/gocache/ $(DIR_OUT)/integration/bin/
	@rm -f $(@)
	@$(RUN_GO) env GOBIN=/code/$(DIR_OUT)/integration/bin \
		go install github.com/cloudboss/unobin/cmd/unobin@$(UNOBIN_VERSION)

$(HAS_IMAGE_KIND): Containerfile.integration | $(HAS_COMMAND_DOCKER) $(DIR_OUT)/dockerbuild/
	@rm -f $(@)
	@docker build --build-arg FROM=$(CTR_IMAGE_GO) --build-arg KIND_VERSION=$(KIND_VERSION) \
		-f $(DIR_ROOT)/Containerfile.integration -t $(CTR_IMAGE_KIND) $(DIR_OUT)/dockerbuild
	@touch $(@)

test-integration-kind: $(DIR_OUT)/integration/bin/unobin | $(HAS_IMAGE_KIND)
	@./hack/test-integration-kind

test-integration-live: $(DIR_OUT)/integration/bin/unobin
	@./hack/test-integration-live

export PROJECT CTR_IMAGE_GO CTR_IMAGE_KIND KIND_NODE UID GID SCENARIO
