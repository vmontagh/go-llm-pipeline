.PHONY: build test test-contrib test-unit-cover test-bdd test-bdd-live lint lint-arch vuln secrets clean fmt vet tidy

GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.10.0
GITLEAKS := go run github.com/zricethezav/gitleaks/v8@v8.30.1
# SECRETS_REVS selects the commits `make secrets` scans, as `git log` revision
# arguments: SECRETS_REVS=--all scans every branch and tag, HEAD..HEAD only
# the tree.
SECRETS_REVS ?= origin/main..HEAD

build:
	go build ./...

test:
	go test ./... -v -count=1 -race

# test-contrib tests the optional contrib modules, which have their own go.mod
# so their dependencies stay out of the core module.
CONTRIB_MODULES := contrib/tiktoken
test-contrib:
	@for m in $(CONTRIB_MODULES); do (cd $$m && go vet ./... && go test ./... -count=1 -race) || exit 1; done

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

# vuln reports known vulnerabilities in code paths the module actually calls.
vuln:
	$(GOVULNCHECK) ./...

# secrets scans the working tree and the commits in SECRETS_REVS for
# credentials. Deliberate fake keys in tests carry a `gitleaks:allow` comment.
# gitleaks treats an unknown revision as zero commits and passes, so verify the
# revisions first. -m includes changes made in merge commits, such as conflict
# fixes.
secrets:
	$(GITLEAKS) dir . --no-banner --redact
	@git rev-list --quiet $(SECRETS_REVS) || \
		{ echo "secrets: SECRETS_REVS '$(SECRETS_REVS)' names a commit missing from this clone" >&2; exit 1; }
	$(GITLEAKS) git . --no-banner --redact --log-opts="-m $(SECRETS_REVS)"

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f coverage.out
