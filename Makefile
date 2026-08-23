.PHONY: build test test-unit-cover test-bdd test-bdd-live lint lint-arch clean fmt vet tidy

build:
	go build ./...

test:
	go test ./... -v -count=1 -race

test-unit-cover:
	go test -race -coverprofile=coverage.out ./...

# test-bdd runs the godog integration suite's @fake lane: cassettes are replayed
# in-process with no network access, so it is safe for pre-submit CI.
test-bdd:
	go test ./integ-test/... -v -count=1

# test-bdd-live runs the @live-local lane against a real Ollama. It first removes
# the scenario cassettes so record-once genuinely records live interactions
# instead of replaying an existing cassette; the target therefore fails fast if
# Ollama is unreachable. Developer-only: commit the refreshed cassettes so the
# @fake lane can replay them. Override the endpoint with OLLAMA_HOST.
test-bdd-live:
	rm -f integ-test/testdata/cassettes/*.yaml
	VCR_MODE=record go test -tags=live ./integ-test/... -v -count=1 -godog.tags="@live-local"

lint:
	golangci-lint run ./...

lint-arch:
	go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f coverage.out
