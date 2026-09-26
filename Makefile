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
# sibling purego-* modules are vendored, so the package builds offline.
# pacman-ordered: 0.0.0.r<commits>.g<hash>, a tag replaces 0.0.0 once there is one.
VERSION ?= $(shell t=$$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//'); \
	echo "$${t:-0.0.0}.r$$(git rev-list --count HEAD).g$$(git rev-parse --short HEAD)")
DIST := dist/nefertty-$(VERSION)
SIBLINGS := github.com/bnema/purego-libwayland github.com/bnema/purego-vulkan
dist:
	@rm -rf $(DIST) && mkdir -p $(DIST)
	git archive $$(git stash create || echo HEAD) | tar -x -C $(DIST)
	cd $(DIST) && go mod edit $$(cd $(CURDIR) && go list -m -f '-replace={{.Path}}={{.Dir}}' $(SIBLINGS)) && go mod vendor && \
		for m in $(SIBLINGS); do d=$$(cd $(CURDIR) && go list -m -f '{{.Dir}}' $$m); \
			sed -i "s#=> $$d\$$#=> ../$${m##*/}#" go.mod vendor/modules.txt; done && \
		! grep -rq "$(HOME)" go.mod vendor/modules.txt && echo '$(VERSION)' > VERSION
	tar -C dist -czf $(DIST).tar.gz nefertty-$(VERSION)
	@rm -rf $(DIST) && echo $(DIST).tar.gz

# Arch package from the dist tarball; install it with `sudo pacman -U` on
# the printed file.
pkg: dist
	rm -rf dist/pkg && mkdir -p dist/pkg && cp packaging/arch/PKGBUILD $(DIST).tar.gz dist/pkg/
	cd dist/pkg && sed -i "s/^pkgver=.*/pkgver=$(VERSION)/" PKGBUILD && makepkg -f --noconfirm
	@ls dist/pkg/*.pkg.tar.zst
