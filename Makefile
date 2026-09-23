.PHONY: build test vet race mocks mocks-check arch check
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
