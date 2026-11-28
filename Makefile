.PHONY: build test api api-diff test-contrib test-unit-cover test-bdd test-bdd-live lint lint-arch vuln secrets clean fmt vet tidy

GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.10.0
GITLEAKS := go run github.com/zricethezav/gitleaks/v8@v8.30.1
APIDIFF := go run golang.org/x/exp/cmd/apidiff@v0.0.0-20261128033645-f63ec7c7eed0
MODULE := github.com/vmontagh/go-llm-pipeline
# STABLE_PKGS are the packages under the API stability guarantee (see README).
# Keep in sync with stablePackages in internal/apisnapshot.
STABLE_PKGS := analyzer llmvcr
# API_BASE is the revision api-diff compares against.
API_BASE ?= origin/main
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

# api rewrites the API snapshots in api/ after an intended change to a stable
# package. make test fails while a snapshot is out of date.
api:
	go test ./internal/apisnapshot -count=1 -update

# api-diff fails if a stable package breaks compatibility with API_BASE.
# apidiff always exits 0, so any reported incompatible change fails the target.
# A package missing at API_BASE is new and has nothing to break.
api-diff:
	@set -e; tmp=$$(mktemp -d); trap 'git worktree remove --force '"$$tmp"'/base; rm -rf '"$$tmp" EXIT; \
	git worktree add -q --detach "$$tmp/base" "$(API_BASE)"; \
	fail=0; for p in $(STABLE_PKGS); do \
		[ -d "$$tmp/base/pkg/$$p" ] || { echo "pkg/$$p: new at $(API_BASE), skipped"; continue; }; \
		(cd "$$tmp/base" && $(APIDIFF) -w "$$tmp/$$p.export" $(MODULE)/pkg/$$p); \
		out=$$($(APIDIFF) -incompatible "$$tmp/$$p.export" $(MODULE)/pkg/$$p); \
		if [ -n "$$out" ]; then echo "pkg/$$p: incompatible changes vs $(API_BASE):"; echo "$$out"; fail=1; \
		else echo "pkg/$$p: compatible with $(API_BASE)"; fi; \
	done; exit $$fail

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
