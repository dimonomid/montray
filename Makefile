SHELL := bash

VERSION != git describe --dirty --tags --always
COMMIT != git rev-parse HEAD
DATE != date -u +"%Y-%m-%dT%H:%M:%SZ"

GOEXE :=
ifeq ($(OS),Windows_NT)
GOEXE := .exe
endif

LDFLAGS := -s -w \
	-X 'github.com/dimonomid/montray/version.version=$(patsubst v%,%,$(VERSION))' \
	-X 'github.com/dimonomid/montray/version.commit=$(COMMIT)' \
	-X 'github.com/dimonomid/montray/version.date=$(DATE)' \
	-X 'github.com/dimonomid/montray/version.builtBy=make'

.PHONY: all
all: clean montray-server montray-ui

.PHONY: test
test:
	go test --count 1 --race ./...
	node --test cmd/montray-ui-legacy/jstest/*.js
	cargo test --manifest-path cmd/montray-ui/Cargo.toml

.PHONY: generate
generate:
	go generate ./...

.PHONY: montray-server
montray-server: generate
	@echo Building bin/montray-server$(GOEXE)
	@# Keep the server portable across Linux distributions instead of linking it
	@# to the glibc version provided by the build host.
	@CGO_ENABLED=0 go build \
		-trimpath \
		-o bin/montray-server$(GOEXE) \
		-ldflags "$(LDFLAGS)" \
		./cmd/montray-server

.PHONY: montray-ui-legacy
montray-ui-legacy: generate
	@echo Building bin/montray-ui-legacy$(GOEXE)
	@go build \
		-trimpath \
		-o bin/montray-ui-legacy$(GOEXE) \
		-ldflags "$(LDFLAGS)" \
		./cmd/montray-ui-legacy

.PHONY: montray-ui
montray-ui:
	@echo Building bin/montray-ui$(GOEXE)
	@MONTRAY_BUILD_VERSION='$(patsubst v%,%,$(VERSION))' \
		MONTRAY_BUILD_COMMIT='$(COMMIT)' \
		MONTRAY_BUILD_DATE='$(DATE)' \
		MONTRAY_BUILT_BY='make' \
		cargo build --release --manifest-path cmd/montray-ui/Cargo.toml
	@mkdir -p bin
	@cp cmd/montray-ui/target/release/montray-ui$(GOEXE) bin/montray-ui$(GOEXE)

.PHONY: montray-ui-debug
montray-ui-debug:
	@echo Building bin/montray-ui-debug$(GOEXE)
	@MONTRAY_BUILD_VERSION='$(patsubst v%,%,$(VERSION))' \
		MONTRAY_BUILD_COMMIT='$(COMMIT)' \
		MONTRAY_BUILD_DATE='$(DATE)' \
		MONTRAY_BUILT_BY='make' \
		cargo build --manifest-path cmd/montray-ui/Cargo.toml
	@mkdir -p bin
	@cp cmd/montray-ui/target/debug/montray-ui$(GOEXE) bin/montray-ui-debug$(GOEXE)

.PHONY: clean
clean:
	rm -rf bin

PREFIX ?= /usr/local
DESTDIR ?=
BINDIR := $(DESTDIR)$(PREFIX)/bin
INSTALL := install
INSTALL_FLAGS := -m 755

.PHONY: install
install: install-montray-server install-montray-ui

.PHONY: install-montray-server
install-montray-server:
	$(INSTALL) $(INSTALL_FLAGS) -D bin/montray-server$(GOEXE) $(BINDIR)/montray-server$(GOEXE)

.PHONY: install-montray-ui
install-montray-ui:
	$(INSTALL) $(INSTALL_FLAGS) -D bin/montray-ui$(GOEXE) $(BINDIR)/montray-ui$(GOEXE)

.PHONY: install-montray-ui-legacy
install-montray-ui-legacy:
	$(INSTALL) $(INSTALL_FLAGS) -D bin/montray-ui-legacy$(GOEXE) $(BINDIR)/montray-ui-legacy$(GOEXE)
