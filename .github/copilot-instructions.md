# Go Idiomatic Guidelines

**Always run `make test` and `make lint` (or `golangci-lint run ./...`) before submitting. Fix all issues and failing tests. Passing tests and a clean lint are required for merge.**

---

## 1. Errors

- **Always check errors** — never `_ = fn()`. Comment if deliberately ignored.
- **Wrap with context** — `return fmt.Errorf("loading config: %w", err)`, never bare `return err`.
- **Use `%w`** for programmatic unwrapping, `%v` at system boundaries.
- **Sentinels over strings** — `errors.Is(err, ErrNotFound)`, never `err.Error() == "not found"`.
- **Test specific errors** — `wantErr: ErrNotFound` with `cmpopts.EquateErrors()`, not `wantErr: true`.
- **`errors.Join`** for multi-field validation (order-independent).
- **Lowercase, no punctuation** — `errors.New("connection failed")`.
- **Return `error` interface** from exported functions, not concrete error types.
- **Don't log and return** the same error — pick one.
- **No panic** for normal errors. Reserve for invariant violations. `Must` only at package-level `var` init.
- **Naming**: `ErrFoo` for sentinels, `FooError` for types.
- **No stop words** — `"opening file: %w"` not `"failed to open file: %w"`.
- **`(T, bool)` for single-failure lookups** (e.g. cache miss), not `(T, error)`.
- **Status codes only at boundaries** — libraries return plain `error`.

## 2. Naming

- **MixedCaps always** — `maxRetries`, never `max_retries` or `MAX_RETRIES`. Exception: test names.
- **No `Get` prefix** — `func (u *User) Name()` not `GetName()`.
- **Don't repeat package name** — `config.Parse()` not `config.ConfigParse()`.
- **Name length ∝ scope** — `i` for loops, `requestTimeout` for package-level.
- **Receivers**: 1–2 letter, consistent. Never `this`/`self`.
- **Initialisms**: `URL`, `ID`, `HTTP` — all caps or all lower.
- **No `util`/`helper`/`common` packages** — name by what they provide.

## 3. Testing

### 3.0 Structure & simplicity (apply while writing, not after)

- **One case → a plain test; two or more cases of the same behavior → one table-driven test.** Never write near-duplicate `TestFooA`/`TestFooB` funcs for one behavior — add rows.
- **Don't merge distinct behaviors** to cut count. Readability first: a shared table with per-row `act`/`assert` closures is a smell. Keep genuinely different behaviors as separate, clearly-named tests.
- **Extract ceremony into named `t.Helper()` helpers** so each test body reads Arrange→Act→Assert. Repeated open/reopen, `sync.WaitGroup`/goroutine fan-out, and raw DB or fixture setup go in helpers (e.g. `addItems`, `runConcurrentErrs`, `injectRow`) — never inline.
- **`cmp.Diff` for every want/got equality** — `cmp.Diff(want, got, cmpopts.EquateErrors())` for errors, `cmpopts.EquateApprox` for floats. Reserve `errors.Is` for negative ("must not be") or "is-one-of" assertions where a diff is meaningless.
- **Name magic literals in tests too** — `permWorldWritableDir = 0o777`, not a bare `0o777`.
- **Leave the file simpler than you found it** — when adding a case, extend the existing table instead of appending a standalone `Test` func.
- **No external assertion libraries** — use only the standard `testing` package plus `github.com/google/go-cmp`; never testify (`assert`/`require`) or gomega.
- **Test through the exported API** — prioritize covering exported functions and observable behavior; use internal (white-box `package foo`) tests only for logic that cannot be reached through the public surface.
- **Errors by identity, not text** — assert against **sentinel errors** with `errors.Is` or `cmp.Diff(wantErr, got, cmpopts.EquateErrors())`; never compare `err.Error()` strings or substrings.

- **`want`/`got`** — never `expected`/`actual`.
- **Table-driven + `cmp.Diff`** — avoid `reflect.DeepEqual`; keep assertions minimal and readable.
- **Don't set zero-value fields** in test structs.
- **No real clock** — inject fake time, never `time.Sleep` in tests.
- **AAA pattern** — Arrange, Act, Assert.
- **Identify function+input in failures** — `t.Errorf("Parse(%q) = %v, want %v", in, got, want)`.
- **`t.Fatal` for setup**, `t.Error` to keep going. Never `t.Fatal` from goroutines.
- **`t.Helper()`** on all test helpers.
- **Fakes over mocks** — mocks couple to call order and are brittle.
- **Test behavior, not implementation.**
- **Black-box testing** — prefer `package foo_test`.
- **`t.Parallel()`** when cases are independent.
- **`testing.Short()` + `t.Skip`** for long tests.
- **No test-aware code** — no `IsTest bool`; use DI.
- **Test error paths** too, not just happy path.
- **Isolate from externals** — no DB/network/filesystem in unit tests.
- **`t.Context()`** (Go 1.24+) instead of `context.Background()` in helpers.
- **`t.Cleanup`** over returning cleanup functions.
- **`testdata/`** for fixtures; `t.TempDir()` for temp output.
- **Extract `run()`** from `main` for testability.
- **Embed interface** in fakes for large interfaces (implement only needed methods).

## 4. Architecture & DI

- **Context from `main()` only** — `signal.NotifyContext`, propagate down. Never create in subpackages.
- **`ctx` is always first param**, never stored in a struct. No custom context types.
- **Logger/tracer injected from `main()`** — never create inside packages.
- **Use the logger** — never `fmt.Printf` or `print`.
- **Log levels**: DEBUG=dev, INFO=default, WARN=borderline, ERROR=unexpected failures.
- **Never log PII/PHI.**
- **Interfaces at the consumer**, not the provider. Small, unexported when internal.
- **No premature interfaces** — use concrete types until abstraction is needed.
- **Accept interfaces, return concrete types.**
- **Minimize exports** — only what other packages need.
- **Prefer stdlib** — don't reinvent. Core language → stdlib → established third-party.
- **Business logic in `internal/`**, not `cmd/`. `cmd/` only wires dependencies.
- **`errgroup`** for concurrent error aggregation.
- **Variadic options** (`WithTimeout(d)`) for flexible APIs.
- **Pointer receivers** unless small typedef-style type.
- **No package-level mutable state** — put state in structs.
- **Context keys unexported** — expose `WithFoo`/`FooFrom` accessors.
- **Minimize `func init`** — never read flags there.
- **No `sync.Once` for fallible ops** — use mutex with retry.
- **Explicit `Close`/`Stop`** for resource lifetime.
- **No `os.Exit` with `defer`** — return errors from `run()`, exit in `main`.

## 5. Style

- **Flat control flow** — early returns, guard clauses. No nested `if/else`.
- **No magic numbers/strings** — use named constants.
- **Struct param** when function has >4 args.
- **No Yoda** — `err == nil` not `nil == err`.
- **No redundant `break`** in `switch` — Go doesn't fall through.
- **Prefer sync over async** — let callers add concurrency.
- **Goroutine lifetime must be obvious** — use `context.Context`.
- **Nil slices** — `var t []string`, check `len(s) == 0`.
- **Pass by value** for small read-only types.
- **Channel direction** — `<-chan` / `chan<-` where possible.
- **Delete dead code** — git has history.
- **`any` not `interface{}`**.
- **`%q`** for string output (reveals empty/whitespace).
- **Signatures on one line** — use options struct if too long.
- **Don't break string literals** across lines (especially URLs).
- **Make operational values configurable** — retries, timeouts, concurrency.
- **`time.Duration`/`time.Time`** — never raw ints for time.
- **`strings.Builder`** for loop concatenation.
- **`strings.Cut`** over `strings.SplitN` for key=value parsing.
- **`1_073_741_824`** — underscores in long numeric literals.
- **No `v := v`** in range loops (Go 1.22+).
- **`slices`/`maps` stdlib** — `slices.Contains`, `maps.Keys`.
- **`cmp.Or`** for first-non-zero defaults.
- **`time.NewTicker` + `Stop`** — `time.Tick` leaks.
- **Enum zero = Unknown/Invalid**.
- **`slices.Clip`** before `append` to sub-slices (avoid shared backing array).
- **Factory functions** only when zero value is unusable.
- **Method values** — `http.HandleFunc("/x", handler.Serve)` not wrapping in closure.

## 6. Documentation

- **All exported names** get doc comments starting with the symbol name.
- **`// Package foo ...`** on every package.
- **Comment *why*, not *what*** — don't restate code.
- **`// TODO(owner): description (#issue)`** for actionable items.
