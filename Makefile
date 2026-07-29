# There is no CI, so `make verify` is the bar. Run it before pushing.

GO ?= go
BINARY ?= kowl
VERSION ?=
LDFLAGS := -X main.version=$(VERSION)

# The image Docker targets test in. Kept in step with the go directive in go.mod.
GO_IMAGE ?= golang:1.26

# Called by full path: go install puts it in GOPATH/bin, which is often not on PATH.
STATICCHECK := $(shell $(GO) env GOPATH)/bin/staticcheck

.PHONY: all build test race cover fmt vet staticcheck verify verify-linux docker docker-shell clean

all: verify

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

cover:
	$(GO) test -cover ./...

fmt:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

vet:
	$(GO) vet ./...

# Installed on demand rather than vendored, so a clone needs nothing extra to build.
staticcheck:
	@[ -x "$(STATICCHECK)" ] || $(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	$(STATICCHECK) ./...

verify: fmt vet staticcheck race cover

# The same checks on Linux, where fsnotify uses inotify rather than the kqueue macOS and
# the BSDs use. Everything else here only ever proves the tool works on one backend.
verify-linux:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) \
		sh -c 'go vet ./... && go test -race ./... && go test -cover ./...'

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY) .

# A shell in the build image, for reproducing something that only happens on Linux.
docker-shell:
	docker run --rm -it -v "$(CURDIR)":/src -w /src $(GO_IMAGE) bash

clean:
	rm -f $(BINARY)
	$(GO) clean -testcache
