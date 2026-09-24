.PHONY: build test vet race mocks mocks-check fakes-check arch check bin tty logs

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
mocks:
	$(HOME)/go/bin/mockery
# Generated mocks must be committed and up to date.
mocks-check: mocks
	git diff --exit-code -- $(MOCKS)
	@test -z "$$(git ls-files --others --exclude-standard -- $(MOCKS))"
# Test doubles come from Mockery only: no handwritten fake/stub/spy types.
fakes-check:
	@! grep -rnE '^type +(fake|stub|spy|dummy|mock)[A-Za-z0-9_]* ' --include='*_test.go' --exclude='*_mock_test.go' internal cmd \
		|| (echo 'handwritten test double: add an interface and a Mockery entry (see AGENTS.md)' >&2; exit 1)
arch:
	$(HOME)/go/bin/hexcheck -hexcheck.config .hexcheck.yaml -hexcheck.root . ./...
check: vet test arch fakes-check
