---
name: code-review
description: >-
  Review Go pull requests in this repository for idiomatic Go — error handling,
  naming, testing, dependency injection, architecture, style, and documentation.
  Use for every Copilot code review of Go changes, and whenever asked to check
  whether Go code is idiomatic.
---

# Go Code Review

Review Go changes against the idiomatic Go ruleset in `go-idioms.md`, which is
derived from 800+ PR review comments and aligned with the Google Go Style Guide.

## How to review

1. Read `go-idioms.md` (next to this file) before reviewing.
2. Check the diff against each section: error handling, naming, testing,
   architecture & dependency injection, style, and documentation.
3. Flag concrete, actionable issues with file- and line-specific feedback. Cite
   the specific rule you're applying (e.g. "§1.1 Always check errors").
4. Prioritize correctness, maintainability, error handling, and test quality
   over cosmetic nits. Skip purely stylistic comments unless they hurt
   readability, maintainability, or repository consistency (`gofmt`/`goimports`
   and linters already cover formatting).
5. Verify tests cover error paths, not just the happy path, and follow the
   table-driven + `cmp.Diff` conventions in the ruleset.

## What to look for first

- Unchecked or bare-returned errors; missing `%w` wrapping and context.
- Brittle tests (mocks over fakes, `reflect.DeepEqual`, `expected`/`actual`,
  real clocks/sleeps, network/DB/filesystem in unit tests).
- Package-level mutable state, stored `context.Context`, or context created
  outside `main()`.
- Premature interfaces, stutter (`config.ConfigParse`), and `Get` prefixes.
- Missing doc comments on exported symbols.
