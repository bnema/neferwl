.PHONY: build test vet race mocks mocks-check spv-check fakes-check arch check perf-check bin tty logs pkg install

# 0 runs until quit; set e.g. TTY_TIMEOUT=60s for a safety net.
TTY_TIMEOUT ?= 0
comma := ,
# Go runtime profiles for tty runs; set PPROF= to disable.
PPROF ?= localhost:6060
RUNS := $(or $(XDG_STATE_HOME),$(HOME)/.local/state)/neferwl/runs/drm

# Build a fresh binary with the git revision embedded.
bin:
	CGO_ENABLED=0 go build -o bin/neferwl ./cmd/neferwl

# Run on the current TTY. Quit: Ctrl+Alt+Backspace (or the quit bind).
# TTY_DEBUG adds narrow categories, e.g. TTY_DEBUG=drm-flip.
tty: bin
	./bin/neferwl --backend=drm --debug=all$(if $(TTY_DEBUG),$(comma)$(TTY_DEBUG)) --timeout=$(TTY_TIMEOUT) $(if $(PPROF),--pprof=$(PPROF)); \
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
# Committed SPIR-V must match the GLSL shaders (needs glslc).
spv-check:
	go generate ./internal/adapters/vulkan
	git diff --exit-code -- 'internal/adapters/vulkan/shaders/*.spv'
# Test doubles come from Mockery only: no handwritten fake/stub/spy types.
# Matches `type fakeX`, `\ttype RendererStub[T any]` and `spyX struct` in `type (` blocks.
# grep exit 1 (no match) passes; 0 (match) and 2 (error) fail.
FAKES := ^\s*type\s+\w*(fake|stub|spy|dummy|mock)\w*|^\s*\w*(fake|stub|spy|dummy|mock)\w*(\[[^]]*\])?\s+(struct|interface)\b
fakes-check:
	@rc=0; grep -rniE '$(FAKES)' --include='*_test.go' --exclude='*_mock_test.go' internal cmd || rc=$$?; \
	[ $$rc -eq 1 ] || { [ $$rc -eq 0 ] && echo 'handwritten test double: add an interface and a Mockery entry (see AGENTS.md)' >&2; exit 1; }
arch:
	$(HOME)/go/bin/hexcheck -hexcheck.config .hexcheck.yaml -hexcheck.root . ./...
# Fast, mandatory guards for tiled content publications and SHM copies.
perf-check:
	CGO_ENABLED=0 go test ./internal/app ./internal/adapters/drm ./internal/adapters/vulkan ./internal/adapters/wayland -run '^(TestOutputRoutingAllocations|TestReportSeenAllocations|TestFrameLifecycleTransitionsAllocations|TestFullscreenShownAllocations|TestFrameDecisionAllocations|TestCommitFrameAllocations|TestColorPipelineAllocations|TestShownBySnapshotAllocations|TestDueFramesAllocations|TestRenderSteadyStateAllocations|TestAccountFlipAllocations|TestFlipDoneAllocations|TestSceneWalkUnchangedTiledSHM|TestTiledCommitPublishAllocations|TestCapturedCommitApplyAllocations|TestCapturedViewportApplyAllocations|TestEffectiveInputEmptyTreeAllocations|TestHeadlessTiledSHMCallbackAndCopyBudget)$$' -count=1
check: vet test arch fakes-check perf-check

# Arch package of the committed HEAD (packaging/arch/PKGBUILD). Go modules
# come from the module proxy in prepare(); the build itself runs offline.
# pacman-ordered version: 0.0.0.r<commits>.g<hash>; a tag replaces 0.0.0.
pkg: SHELL := bash
pkg: .SHELLFLAGS := -eo pipefail -c
pkg:
	@test -z "$$(git status --porcelain)" || echo "warning: uncommitted changes are not packaged" >&2
	v=$$(t=$$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//; s/-/_/g'); \
		echo "$${t:-0.0.0}.r$$(git rev-list --count HEAD).g$$(git rev-parse --short HEAD)"); \
	d=$$(mktemp -d /tmp/neferwl-pkg.XXXXXX); trap 'rm -rf "$$d"' EXIT; \
	git archive --prefix=neferwl-$$v/ -o "$$d/neferwl-$$v.tar.gz" HEAD; \
	cp packaging/arch/PKGBUILD packaging/neferwl.install "$$d/"; \
	cd "$$d" && sed -i "s/^pkgver=.*/pkgver=$$v/; s/^sha256sums=.*/sha256sums=('$$(sha256sum *.tar.gz | cut -d' ' -f1)')/" PKGBUILD; \
	makepkg -f --noconfirm; mkdir -p $(CURDIR)/dist; rm -f $(CURDIR)/dist/neferwl-*.pkg.tar.zst; mv *.pkg.tar.zst $(CURDIR)/dist/
	@ls dist/*.pkg.tar.zst

# Build the package, then install it with pacman (asks for sudo).
install: pkg
	sudo pacman -U dist/neferwl-*.pkg.tar.zst
