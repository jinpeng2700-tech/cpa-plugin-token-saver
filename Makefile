GO ?= go
GOFMT ?= gofmt
DOCKER ?= docker
DIST_DIR ?= dist
FUZZTIME ?= 10s

override VERSION := 1.0.1
override GOOS := linux
override GOARCH := amd64

PLUGIN_PACKAGE := github.com/router-for-me/cpa-plugin-token-saver/internal/abi
PLUGIN_OUT := $(DIST_DIR)/token-saver-v$(VERSION)-linux-$(GOARCH).so
COMPAT_PROBE_OUT := $(DIST_DIR)/compat-probe-v$(VERSION)-linux-$(GOARCH)
UPDATE_VERIFIER_OUT := $(DIST_DIR)/update-verifier-v$(VERSION)-linux-$(GOARCH)
RELEASE_DOCKERFILE := build/release.Dockerfile

.PHONY: print-version fmt-check vet test test-portable test-race fuzz cgocheck2 ci release verify-release release-container

print-version:
	@printf '%s\n' '$(VERSION)'

fmt-check:
	test -z "$$($(GOFMT) -l .)"

vet:
	CGO_ENABLED=1 $(GO) vet ./...

test:
	CGO_ENABLED=1 $(GO) test ./...

test-portable:
	CGO_ENABLED=0 $(GO) test ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race ./...

fuzz:
	CGO_ENABLED=0 $(GO) test -run='^$$' -fuzz=FuzzViewRewrite -fuzztime=$(FUZZTIME) ./internal/protocol
	CGO_ENABLED=0 $(GO) test -run='^$$' -fuzz=FuzzRuntimeCall -fuzztime=$(FUZZTIME) ./internal/abi

cgocheck2:
	GOEXPERIMENT=cgocheck2 CGO_ENABLED=1 $(GO) test -count=1 ./test/abi-host

ci: fmt-check vet test test-portable test-race fuzz cgocheck2

release:
	mkdir -p '$(DIST_DIR)'
	CGO_ENABLED=1 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -buildmode=c-shared \
		-ldflags '-s -w -X $(PLUGIN_PACKAGE).PluginVersion=$(VERSION)' \
		-o '$(PLUGIN_OUT)' .
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags '-s -w' \
		-o '$(COMPAT_PROBE_OUT)' ./tools/compat-probe
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags '-s -w' \
		-o '$(UPDATE_VERIFIER_OUT)' ./tools/update-verifier
	rm -f '$(DIST_DIR)'/*.h

verify-release:
	test -f '$(PLUGIN_OUT)'
	test -f '$(COMPAT_PROBE_OUT)'
	test -f '$(UPDATE_VERIFIER_OUT)'
	readelf -h '$(PLUGIN_OUT)' | grep -F 'Class:' | grep -F 'ELF64'
	readelf -h '$(PLUGIN_OUT)' | grep -F 'Machine:' | grep -F 'X86-64'
	readelf -h '$(PLUGIN_OUT)' | grep -F 'Type:' | grep -F 'DYN'
	readelf -h '$(COMPAT_PROBE_OUT)' | grep -F 'Type:' | grep -F 'EXEC'
	readelf -h '$(UPDATE_VERIFIER_OUT)' | grep -F 'Type:' | grep -F 'EXEC'
	! objdump -p '$(COMPAT_PROBE_OUT)' | grep -q 'NEEDED'
	! objdump -p '$(UPDATE_VERIFIER_OUT)' | grep -q 'NEEDED'
	max_glibc="$$(objdump -T '$(PLUGIN_OUT)' | sed -n 's/.*GLIBC_\([0-9.]*\).*/\1/p' | sort -V | tail -n 1)"; \
		test -n "$$max_glibc"; \
		test "$$(printf '%s\n' "$$max_glibc" 2.32 | sort -V | tail -n 1)" = 2.32

release-container:
	$(DOCKER) build --file '$(RELEASE_DOCKERFILE)' --output 'type=local,dest=$(DIST_DIR)' .
