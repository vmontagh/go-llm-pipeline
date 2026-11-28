## Agent guide

Instructions for coding agents working in this repository. Humans should start
with [CONTRIBUTING.md](CONTRIBUTING.md); this file is the short version.

### Rules

- Follow the Go guidelines in
  [`.github/copilot-instructions.md`](.github/copilot-instructions.md) and the
  review ruleset in
  [`.github/skills/code-review/go-idioms.md`](.github/skills/code-review/go-idioms.md).
- Tests use only `testing` and `github.com/google/go-cmp`: table-driven,
  `want`/`got`, `cmp.Diff`, sentinel errors, no network or real clock.
- Never commit real credentials. Fake keys in tests end with
  `// gitleaks:allow`.

### Commands

Run all of these before pushing; CI runs the same checks.

```sh
make build                # compile
make test                 # unit tests with -race, including the dead-link check
make test-bdd             # integration scenarios replayed from cassettes
make lint                 # golangci-lint
make lint-arch            # package dependency rules (.go-arch-lint.yml)
make vuln                 # govulncheck
make secrets              # gitleaks: tree + commits since origin/main
gomarklint .              # Markdown lint
GOOS=windows go vet ./... # cross-platform build check
```

A new package needs a component in `.go-arch-lint.yml`.

### Cassettes

Tests replay recorded HTTP interactions and never call live APIs. Cassettes are
re-recorded only with `make test-bdd-live` against a local Ollama; see
[integ-test/README.md](integ-test/README.md). Recording redacts secrets through
`pkg/llmvcr`, but review every cassette diff before committing it.

### Commits and pull requests

- Imperative, present-tense subjects; Conventional Commit prefixes such as
  `feat(scope):`, `fix(scope):` and `ci:`; `!` marks a breaking change.
- Reference the issue (`Closes #123`) and fill out
  [the pull request template](.github/pull_request_template.md), including the
  validation commands you ran and their results.
