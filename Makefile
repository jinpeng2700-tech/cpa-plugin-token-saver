GO ?= go
PLUGIN_OUT ?= build/cpa-plugin-token-saver.so

.PHONY: test test-portable build

test:
	CGO_ENABLED=1 $(GO) test ./...

test-portable:
	CGO_ENABLED=0 $(GO) test ./...

build:
	mkdir -p $(dir $(PLUGIN_OUT))
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -o $(PLUGIN_OUT) .
