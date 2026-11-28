## Contributing to go-llm-pipeline

Thanks for your interest in contributing! This document explains how to build,
test, and lint the project, and the conventions we follow.

### Prerequisites

- Go 1.26 or newer (see [`go.mod`](go.mod)).
- [`golangci-lint`](https://golangci-lint.run/) for linting.
- [`gomarklint`](https://github.com/shinagawa-web/gomarklint) for Markdown linting
  (`go install github.com/shinagawa-web/gomarklint@v1.5.0`).

### Build, test, and lint

All common tasks are wired to `make` targets:

| Target | Description |
| --- | --- |
| `make build` | Compile all packages. |
| `make test` | Run the full test suite with the race detector. |
| `make test-unit-cover` | Run tests with a coverage profile (`coverage.out`). |
| `make lint` | Run `golangci-lint`. |
| `make lint-arch` | Run the architecture linter (`go-arch-lint`). |
| `make vuln` | Report reachable known vulnerabilities (`govulncheck`). |
| `make secrets` | Scan the tree and commits since `origin/main` (or `SECRETS_REVS`) for secrets (`gitleaks`). |
| `make fmt` | Format the code with `gofmt`. |
| `make vet` | Run `go vet`. |
| `make tidy` | Run `go mod tidy`. |
| `make clean` | Remove generated artifacts. |

### Before you submit

**Run `make test` and `make lint` before submitting. Passing tests and a clean
lint are required for merge.** Fix all failures and lint findings first. If your
change touches Markdown docs, also run `gomarklint .` and resolve any issues;
`make test` also fails on relative links to files that do not exist. Fake
credentials in tests need a trailing `// gitleaks:allow` comment so
`make secrets` accepts them.

**New features and bug fixes must include tests** in the same pull request. A
bug fix needs a test that fails without the fix. Reviewers will ask for tests
when a change adds or alters behavior without them.

### Fuzzing the analyzer schema layer

`pkg/analyzer/differential_test.go` uses Go's native fuzzing framework to pin that
`SchemaOf`, `Validate`, and `encoding/json` agree. Their seed corpora run as part of
`make test`, so no extra step is required for CI. When you change schema derivation or
validation, run a target under coverage-guided exploration to look for new drift:

```sh
go test ./pkg/analyzer -run '^$' -fuzz 'FuzzValidateImpliesDecode$' -fuzztime 2m
go test ./pkg/analyzer -run '^$' -fuzz 'FuzzSchemaAcceptsCanonical$' -fuzztime 2m
```

A failing input is saved under `pkg/analyzer/testdata/fuzz/`; commit it so it replays as
a regression seed. Only one target can be fuzzed at a time.

### Branch and pull request conventions

- Branch off `main` and use a short, descriptive kebab-case branch name.
- Keep pull requests focused; prefer small, reviewable changes.
- Reference the relevant issue number in your commits and PR description
  (for example, `Closes #123`).
- Fill out the pull request template so reviewers have the context they need.

### Commit messages

- Use present-tense, imperative subjects (for example, `add retry policy lookup`).
- Follow the repository's Go idiomatic guidelines; avoid stop words such as
  "failed to" in error strings and log messages.
- Ensure `make fmt` has been run so formatting is consistent.

### Releasing

Releases are cut by pushing a `vX.Y.Z` tag on `main`. Nothing is uploaded by
hand. The [release workflow](.github/workflows/release.yml) runs
[GoReleaser](https://goreleaser.com/) (configured in `.goreleaser.yml`) to:

1. Build a source archive and `checksums.txt`.
2. Sign `checksums.txt` keylessly with Sigstore `cosign`
   (`checksums.txt.sigstore.json`).
3. Record GitHub build provenance for the archive.
4. Publish the release, which is immutable once published, and warm the Go
   module proxy.

Run `make api-diff API_BASE=<previous tag>` before choosing the version
number. Edit the generated release notes after publishing if needed; notes stay
editable, assets don't.

To verify a release, download `checksums.txt`, `checksums.txt.sigstore.json` and
the archive, then run:

```sh
cosign verify-blob \
  --certificate-identity "https://github.com/vmontagh/go-llm-pipeline/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify go-llm-pipeline_X.Y.Z.tar.gz --repo vmontagh/go-llm-pipeline
```

### Code style

This project follows the Go idiomatic guidelines documented in
[`.github/copilot-instructions.md`](.github/copilot-instructions.md): wrapped
errors with context, table-driven tests using `want`/`got` and `cmp.Diff`,
dependency injection over package-level state, and small consumer-side
interfaces. Please read it before making substantial changes.
