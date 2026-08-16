---
name: go-idioms
description: >-
  Go idiomatic code review guidelines derived from 800+ PR review comments and
  aligned with the Google Go Style Guide. Use whenever writing, reviewing, or
  refactoring Go code, or when the user asks to check Go code for idiomatic
  style, error handling, naming, testing, dependency injection, or formatting.
---

# Go Idiomatic Development Guidelines

Use these guidelines to write and review idiomatic Go. When reviewing code,
flag every violation and cite the relevant rule.

## When to use

- Reviewing a Go diff, file, or PR for idiomatic style.
- Writing or refactoring Go code.
- Answering "is this idiomatic Go?" questions.

## How to use

The full ruleset lives in `guidelines.md` next to this file. Read it before
reviewing, then check the code against each section. Cite the specific rule
(e.g. "§1.1 Always check errors") in your feedback.

The guidelines cover:

1. **Error Handling** — always check errors, wrap with `%w` + context, sentinel
   placement, `errcheck` in CI, errors in `defer`.
2. **Naming** — package/variable/receiver naming, initialisms, getters,
   avoiding stutter.
3. **Testing** — table-driven tests, `t.Run`, `cmp`/`cmpopts`, test helpers,
   `t.Cleanup`, avoiding brittle assertions.
4. **Architecture & Dependency Injection** — accept interfaces/return structs,
   constructor injection, avoiding global state.
5. **Code Style & Structure** — early returns, zero values, slice/map idioms,
   context usage, concurrency patterns.
6. **Comments & Documentation** — doc comments, exported-symbol docs, comment
   style.
7. **Formatting & CI** — `gofmt`/`goimports`, linters enforced in CI.

A condensed pass/fail checklist is at the end of `guidelines.md` ("Quick
Checklist").
