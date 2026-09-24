.PHONY: build test vet race mocks mocks-check arch check bin tty logs

TTY_TIMEOUT ?= 60s
RUNS := $(or $(XDG_STATE_HOME),$(HOME)/.local/state)/nefertty/runs

# Build a fresh binary with the git revision embedded.
bin:
	CGO_ENABLED=0 go build -o bin/nefertty ./cmd/nefertty

# Run on the current TTY. Quit: Ctrl+Alt+Backspace, or wait TTY_TIMEOUT.
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
mocks:
	$(HOME)/go/bin/mockery
mocks-check: mocks
	git diff --exit-code -- internal/mocks
	@test -z "$$(git ls-files --others --exclude-standard internal/mocks)"
arch:
	$(HOME)/go/bin/hexcheck -hexcheck.config .hexcheck.yaml -hexcheck.root . ./...
check: vet test arch
