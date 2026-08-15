MODULE  := github.com/ratsdev/tvr
CMD     := ./cmd/tvr
BIN     := tvr
BINDIR  := bin
DISTDIR := dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
ifeq ($(strip $(VERSION)),)
VERSION := dev
endif

LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT)
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)'

# linux/darwin amd64+arm64, matching the GitHub Release artifacts.
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
IMAGE     ?= tvr:$(VERSION)

.PHONY: all build dist release docker clean test help

all: build

build:
	@mkdir -p $(BINDIR)
	$(GOBUILD) -o $(BINDIR)/$(BIN) $(CMD)

dist:
	@mkdir -p $(DISTDIR)
	@set -e; for pair in $(PLATFORMS); do \
		goos=$${pair%/*}; \
		goarch=$${pair#*/}; \
		name=$(BIN)_$(VERSION)_$${goos}_$${goarch}; \
		echo "building $$name"; \
		GOOS=$$goos GOARCH=$$goarch $(GOBUILD) -o $(DISTDIR)/$$name $(CMD); \
	done

release: dist
	@set -e; for f in $(DISTDIR)/$(BIN)_$(VERSION)_*; do \
		case $$f in *.tar.gz) continue ;; esac; \
		tar -C $(DISTDIR) -czf "$$f.tar.gz" "$$(basename $$f)"; \
		rm -f "$$f"; \
	done
	@cd $(DISTDIR) && { sha256sum $(BIN)_$(VERSION)_*.tar.gz 2>/dev/null || shasum -a 256 $(BIN)_$(VERSION)_*.tar.gz; } > checksums.txt

docker:
	docker build \
		--build-arg VERSION="$(VERSION)" \
		--build-arg COMMIT="$(COMMIT)" \
		-t "$(IMAGE)" .

test:
	go test ./...

clean:
	rm -rf $(BINDIR) $(DISTDIR)

help:
	@echo "make build    - native binary in $(BINDIR)/$(BIN)"
	@echo "make dist     - cross-compile $(PLATFORMS) into $(DISTDIR)/"
	@echo "make release  - dist, then tar.gz + checksums.txt"
	@echo "make docker   - image $(IMAGE)"
	@echo "make test     - go test ./..."
	@echo "make clean    - remove $(BINDIR)/ and $(DISTDIR)/"
	@echo "VERSION=$(VERSION) COMMIT=$(COMMIT)"
