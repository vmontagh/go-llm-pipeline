.PHONY: build test api-diff test-contrib test-unit-cover test-bdd test-bdd-live lint lint-arch vuln secrets clean fmt vet tidy

GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.10.0
GITLEAKS := go run github.com/zricethezav/gitleaks/v8@v8.30.1
APIDIFF := go run golang.org/x/exp/cmd/apidiff@v0.0.0-20261128033645-f63ec7c7eed0
MODULE := github.com/vmontagh/go-llm-pipeline
# STABLE_PKGS are the packages under the API stability guarantee (see README).
STABLE_PKGS := analyzer llmvcr policy provider
# API_BASE is the revision api-diff compares against. ALLOW_BREAKING=1 reports
# incompatible changes without failing.
API_BASE ?= origin/main
ALLOW_BREAKING ?=
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

# api-diff prints every change to the API of a stable package since API_BASE
# and fails on incompatible ones. apidiff always exits 0, so the target checks
# its report instead. A package missing at API_BASE is new and has nothing to
# break; a stable package removed since API_BASE is an incompatible change.
api-diff:
	@set -e; tmp=$$(mktemp -d); trap 'git worktree remove --force '"$$tmp"'/base; rm -rf '"$$tmp" EXIT; \
	git worktree add -q --detach "$$tmp/base" "$(API_BASE)"; \
	gofiles() { (cd "$$1" && go list -e -f '{{len .GoFiles}}' "./pkg/$$2" 2>/dev/null); }; \
	broken=0; for p in $(STABLE_PKGS); do \
		[ "$$(gofiles "$$tmp/base" $$p)" != 0 ] || { echo "pkg/$$p: new at $(API_BASE), skipped"; continue; }; \
		[ "$$(gofiles . $$p)" != 0 ] || { echo "pkg/$$p: removed since $(API_BASE)"; broken=1; continue; }; \
		(cd "$$tmp/base" && $(APIDIFF) -w "$$tmp/$$p.export" $(MODULE)/pkg/$$p); \
		out=$$($(APIDIFF) "$$tmp/$$p.export" $(MODULE)/pkg/$$p); \
		if [ -z "$$out" ]; then echo "pkg/$$p: no API changes vs $(API_BASE)"; continue; fi; \
		echo "pkg/$$p: API changes vs $(API_BASE):"; echo "$$out"; \
		case "$$out" in *"Incompatible changes:"*) broken=1;; esac; \
	done; \
	if [ $$broken = 1 ] && [ "$(ALLOW_BREAKING)" != 1 ]; then \
		echo "api-diff: incompatible changes to a stable package; set ALLOW_BREAKING=1 if intended" >&2; exit 1; fi

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
