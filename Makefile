.PHONY: build test vet race mocks mocks-check fakes-check arch check bin tty logs dist pkg

# 0 runs until quit; set e.g. TTY_TIMEOUT=60s for a safety net.
TTY_TIMEOUT ?= 0
RUNS := $(or $(XDG_STATE_HOME),$(HOME)/.local/state)/nefertty/runs

# Build a fresh binary with the git revision embedded.
bin:
	CGO_ENABLED=0 go build -o bin/nefertty ./cmd/nefertty

# Run on the current TTY. Quit: Ctrl+Alt+Backspace (or the quit bind).
tty: bin
	./bin/nefertty --backend=drm --debug=all --timeout=$(TTY_TIMEOUT); \
	echo "exit $$? - log: $(RUNS)/latest.log"

# Summarise the last run: lifecycle, warnings and errors.
logs:
	@jq -r 'select(.level != "debug") | "\(.time) \(.level) [\(.component)] \(.message) \(del(.time,.level,.component,.message) | tostring)"' $(RUNS)/latest.log
build:
	CGO_ENABLED=0 go build ./...
test:
	CGO_ENABLED=0 go test ./...
vet:
	CGO_ENABLED=0 go vet ./...
race:
	# cgo is enabled only for the race test binary.
	CGO_ENABLED=1 go test -race ./...
MOCKS := internal/mocks ':(glob)internal/**/*_mock_test.go'
# Regenerate from scratch so mocks of removed interfaces disappear too.
mocks:
	rm -rf internal/mocks
	find internal -name '*_mock_test.go' -delete
	$(HOME)/go/bin/mockery
# Generated mocks must be committed and up to date.
mocks-check: mocks
	git diff --exit-code -- $(MOCKS)
	@test -z "$$(git ls-files --others --exclude-standard -- $(MOCKS))"
# Test doubles come from Mockery only: no handwritten fake/stub/spy types.
# Matches `type fakeX`, `\ttype RendererStub[T any]` and `spyX struct` in `type (` blocks.
# grep exit 1 (no match) passes; 0 (match) and 2 (error) fail.
FAKES := ^\s*type\s+\w*(fake|stub|spy|dummy|mock)\w*|^\s*\w*(fake|stub|spy|dummy|mock)\w*(\[[^]]*\])?\s+(struct|interface)\b
fakes-check:
	@rc=0; grep -rniE '$(FAKES)' --include='*_test.go' --exclude='*_mock_test.go' internal cmd || rc=$$?; \
	[ $$rc -eq 1 ] || { [ $$rc -eq 0 ] && echo 'handwritten test double: add an interface and a Mockery entry (see AGENTS.md)' >&2; exit 1; }
arch:
	$(HOME)/go/bin/hexcheck -hexcheck.config .hexcheck.yaml -hexcheck.root . ./...
check: vet test arch fakes-check

# Self-contained source tarball for packaging (packaging/arch/PKGBUILD): the
# sibling purego-* modules are vendored, so the package builds offline. It
# holds the working trees; the version ends in .dirty when one is not clean.
SIBLINGS := github.com/bnema/purego-libwayland github.com/bnema/purego-vulkan
SIBLING_DIRS = $(shell go list -m -f '{{.Dir}}' $(SIBLINGS))
dirty = $(shell for d in . $(SIBLING_DIRS); do git -C $$d diff --quiet HEAD && \
	test -z "$$(git -C $$d ls-files -o --exclude-standard)" || { echo .dirty; break; }; done)
# pacman-ordered: 0.0.0.r<commits>.g<hash>; a tag replaces 0.0.0.
VERSION ?= $(shell t=$$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//; s/-/_/g'); \
	echo "$${t:-0.0.0}.r$$(git rev-list --count HEAD).g$$(git rev-parse --short HEAD)")$(dirty)
DIST = dist/nefertty-$(VERSION)
dist: SHELL := bash
dist: .SHELLFLAGS := -eo pipefail -c
# Computed once per dist run (git and go list), not for every target.
ifneq ($(filter dist pkg,$(MAKECMDGOALS)),)
VERSION := $(VERSION)
endif
dist:
	@for d in $(SIBLING_DIRS); do test -d $$d || { echo "sibling module not found: $$d" >&2; exit 1; }; done
	rm -rf $(DIST) && mkdir -p $(DIST)
	git ls-files -co --exclude-standard -z | tar --null -T - -c | tar -x -C $(DIST)
	cd $(DIST) && go mod edit $(foreach m,$(SIBLINGS),-replace=$(m)=$(shell go list -m -f '{{.Dir}}' $(m))) && go mod vendor
	cd $(DIST) && $(foreach m,$(SIBLINGS),sed -i 's#=> $(shell go list -m -f '{{.Dir}}' $(m))$$#=> ../$(notdir $(m))#' go.mod vendor/modules.txt &&) true
	@! grep -rqF "$(HOME)" $(DIST) || { echo "home path leaked into $(DIST)" >&2; exit 1; }
	echo '$(VERSION)' > $(DIST)/VERSION
	tar --owner=0 --group=0 --numeric-owner --sort=name --mtime=@$$(git log -1 --format=%ct) -C dist -czf $(DIST).tar.gz nefertty-$(VERSION)
	rm -rf $(DIST)
	@echo $(DIST).tar.gz

# Arch package from the dist tarball, built outside the home directory so
# no path of it lands in the package metadata. Install it with
# `sudo pacman -U` on the printed file.
pkg: dist
	d=$$(mktemp -d /tmp/nefertty-pkg.XXXXXX) && cp packaging/arch/PKGBUILD $(DIST).tar.gz $$d/ && \
		cd $$d && sed -i "s/^pkgver=.*/pkgver=$(VERSION)/; s/^sha256sums=.*/sha256sums=('$$(sha256sum *.tar.gz | cut -d' ' -f1)')/" PKGBUILD && \
		makepkg -f --noconfirm && mv *.pkg.tar.zst $(CURDIR)/dist/ && cd / && rm -rf $$d
	@ls dist/*.pkg.tar.zst
