OS_ARCH := $(shell go env GOOS)_$(shell go env GOARCH)
VERSION  := 0.1.0
PLUGIN_DIR := ~/.terraform.d/plugins/registry.terraform.io/novec-tech-io/harbour/$(VERSION)/$(OS_ARCH)

default: build

build:
	go build -o terraform-provider-harbour .

install: build
	mkdir -p $(PLUGIN_DIR)
	cp terraform-provider-harbour $(PLUGIN_DIR)/

test:
	go test ./...

lint:
	golangci-lint run ./...

.PHONY: build install test lint
