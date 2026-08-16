# Go Idiomatic Development Guidelines

These guidelines are derived from recurring review feedback across 800+ PR comments, aligned with the [Google Go Style Guide](https://google.github.io/styleguide/go/guide), [Decisions](https://google.github.io/styleguide/go/decisions), and [Best Practices](https://google.github.io/styleguide/go/best-practices), incorporating Verily-specific standards from `docs/languages/go/`, `docs/testing/`, and `docs/decision-log/`, and enriched with patterns from 120+ Google Go Tips (`go/gotip`). Follow every rule — violations will be caught in review.

---

## 1. Error Handling

### 1.1 Always check errors — never discard with `_ =`

Every function that returns an error must have that error checked. No exceptions. This is the single most common mistake. The `errcheck` linter is enforced in CI to catch this.

> Google Style Decisions > Handle Errors · Verily `docs/languages/go/linting.md`

```go
// WRONG — silent failure
_ = file.Close()
_ = writer.Flush()
fmt.Fprintf(w, "hello")

// RIGHT
if err := file.Close(); err != nil {
    return fmt.Errorf("closing file: %w", err)
}
```

If you deliberately ignore an error, add a comment explaining why it is safe:

```go
n, _ := b.Write(p) // never returns a non-nil error
```

**Errors in `defer`**: The linter still flags these. Wrap in an inline function:

```go
// WRONG — flagged by errcheck
defer os.Chdir("prev")

// RIGHT
defer func() { _ = os.Chdir("prev") }()
```

### 1.2 Wrap errors with context

Bare `return err` loses the call chain. Always wrap with `fmt.Errorf` and `%w` so callers can trace the origin. Place `%w` at the end of the error string so text mirrors the error chain structure.

> Google Style Best Practices > Adding Information to Errors

```go
// WRONG
return err

// RIGHT
return fmt.Errorf("loading config from %s: %w", path, err)
```

**`%w` vs `%v`**: Use `%w` when callers need programmatic access to the underlying error via `errors.Is` / `errors.As` — but only when you explicitly document and test the errors you expose. Use `%v` at system boundaries (RPC, IPC, storage) to translate into canonical error space.

**Sentinel errors at the beginning**: When wrapping with a sentinel, place it first for immediate category identification:

```go
return fmt.Errorf("%w: column %q not found in table", ErrSchema, col)
```

### 1.3 Use sentinel errors, not string comparison

Never assert on error message text. Define sentinel errors and use `errors.Is` / `errors.As`.

> Google Style Best Practices > Error Structure · Verily `docs/testing/unit-testing/best-practices.md` > Avoid testing error messages

```go
// WRONG
if err.Error() == "not found" { ... }
if strings.Contains(err.Error(), "timeout") { ... }

// RIGHT
var ErrNotFound = errors.New("not found")

if errors.Is(err, ErrNotFound) { ... }
```

Use `cmp.Equal(got, want, cmpopts.EquateErrors())` for error comparison in tests.

### 1.4 Test for specific errors, not existence of errors

Avoid `wantErr bool` patterns that merely check `err != nil`. Always compare against a specific error value.

> Verily `docs/testing/unit-testing/best-practices.md` > Test for specific errors

```go
// WRONG
wantErr: true,
// ...
if (err != nil) != tt.wantErr { ... }

// RIGHT
wantErr: ErrNotFound,
// ...
if got, want := err, tt.wantErr; !cmp.Equal(got, want, cmpopts.EquateErrors()) { ... }
```

### 1.5 Use `errors.Join` for order-independent validation

When a function validates multiple fields, collect all errors with `errors.Join` instead of returning on the first failure. This makes tests order-independent and reports all problems at once.

> Verily `docs/testing/unit-testing/best-practices.md` > Minimize order-dependent conditions

```go
func (s *Service) validate(r *Request) error {
    var errs []error
    if r.Name == "" {
        errs = append(errs, ErrNameRequired)
    }
    if r.Age < 18 {
        errs = append(errs, ErrAgeInvalid)
    }
    return errors.Join(errs...)
}
```

### 1.6 Error strings: not capitalized, no trailing punctuation

> Google Style Decisions > Error Strings

```go
// WRONG
return errors.New("Connection failed.")

// RIGHT
return errors.New("connection failed")
```

### 1.7 Exported functions return `error`, not concrete error types

> Google Style Decisions > Returning Errors: return `error` interface, not `*MyError`, to avoid nil-pointer-in-interface bugs.

```go
// WRONG
func Open(path string) (*File, *PathError) { ... }

// RIGHT
func Open(path string) (*File, error) { ... }
```

### 1.8 Don't log and return the same error

Pick one. If you return the error, let the caller decide whether to log. Logging and returning causes duplicate noise.

> Google Style Best Practices > Logging Errors

```go
// WRONG — caller sees the error AND a duplicate log line
func (s *Service) Process(id string) error {
    err := s.db.Get(id)
    if err != nil {
        s.logger.Errorf("failed to get %s: %v", id, err)
        return fmt.Errorf("getting %s: %w", id, err)
    }
    return nil
}

// RIGHT — return only; let the caller log if needed
func (s *Service) Process(id string) error {
    if err := s.db.Get(id); err != nil {
        return fmt.Errorf("getting %s: %w", id, err)
    }
    return nil
}
```

### 1.9 Don't panic for normal error handling

Use `error` and multiple return values. Reserve `panic` for truly unrecoverable programmer bugs (invariant violations). In `main`, use `log.Fatal` or `log.Exit` for terminal errors.

> Google Style Decisions > Don't Panic

```go
// WRONG
func MustParse(s string) Config {
    cfg, err := Parse(s)
    if err != nil {
        panic(err) // crashes the whole program
    }
    return cfg
}

// RIGHT
func Parse(s string) (Config, error) {
    // ...
    return cfg, nil
}
```

### 1.10 Error naming: `ErrFoo` for sentinels, `FooError` for types

Prefix sentinel error variables with `Err`. Suffix structured error type names with `Error`.

> Go Tip #106 · Google Style Decisions > Naming

```go
// WRONG
var NotFound = errors.New("not found")
type Missing struct { Key string }

// RIGHT
var ErrNotFound = errors.New("not found")
type MissingKeyError struct { Key string }
func (e *MissingKeyError) Error() string { return fmt.Sprintf("missing key: %s", e.Key) }
```

### 1.11 Don't use stop words in error annotations

Avoid prefixes like "failed to", "unable to", "error" in `fmt.Errorf` — they add noise without information. State what was being done.

> Go Tip #100

```go
// WRONG — redundant stop words
return fmt.Errorf("failed to open file %s: %w", path, err)
return fmt.Errorf("error reading config: %w", err)

// RIGHT — concise context
return fmt.Errorf("opening file %s: %w", path, err)
return fmt.Errorf("reading config: %w", err)
```

### 1.12 `Must` functions: only for top-level constant initialization

`Must` helpers (that panic on error) are only appropriate for package-level `var` blocks with observably constant values. Always provide a non-panicking counterpart. Never call `Must` in runtime code paths.

> Go Tip #109

```go
// OK — package-level constant init
var defaultTemplate = template.Must(template.New("t").Parse("{{.Name}}"))

// WRONG — Must in runtime code
func HandleRequest(r *http.Request) {
    tmpl := template.Must(template.ParseFiles(r.URL.Path)) // panics on bad input
}
```

### 1.13 Return `(value, bool)` for single-failure-mode lookups

When a function has exactly one failure mode (e.g., "not found"), return `(T, bool)` instead of `(T, error)`.

> Go Tip #86

```go
// WRONG — error is always the same "not found"
func Lookup(key string) (string, error) {
    v, ok := cache[key]
    if !ok {
        return "", fmt.Errorf("key %q not found", key)
    }
    return v, nil
}

// RIGHT — bool signals the only possible failure
func Lookup(key string) (string, bool) {
    v, ok := cache[key]
    return v, ok
}
```

### 1.14 Convert to status codes only at system boundaries

Libraries should return plain Go errors. Convert to gRPC/HTTP status codes only in RPC handlers or API gateways.

> Go Tip #89

```go
// WRONG — status code deep in business logic
func GetUser(ctx context.Context, id string) (*User, error) {
    u, ok := db.Find(id)
    if !ok {
        return nil, status.Errorf(codes.NotFound, "user %s", id)
    }
    return u, nil
}

// RIGHT — plain error in library; handler converts
func GetUser(ctx context.Context, id string) (*User, error) {
    u, ok := db.Find(id)
    if !ok {
        return nil, fmt.Errorf("user %s: %w", id, ErrNotFound)
    }
    return u, nil
}
```

---

## 2. Naming

### 2.1 MixedCaps always — never snake_case

Go identifiers use `MixedCaps` (exported) or `mixedCaps` (unexported). Never use underscores in Go names (exception: test function names in `*_test.go` files).

> Google Style Guide > MixedCaps

```go
// WRONG
max_retries := 3
const MAX_BUFFER_SIZE = 1024

// RIGHT
maxRetries := 3
const maxBufferSize = 1024
```

### 2.2 No `Get` prefix on getters

> Google Style Decisions > Getters

```go
// WRONG
func (u *User) GetName() string { return u.name }

// RIGHT
func (u *User) Name() string { return u.name }
```

Use `Compute` or `Fetch` instead of `Get` when the operation is expensive or involves a remote call.

### 2.3 Don't repeat the package name in exported symbols

> Google Style Best Practices > Naming > Avoid Repetition

```go
// WRONG — package config
func ConfigParse(path string) (*Config, error) { ... }

// RIGHT — callers write config.Parse(path)
func Parse(path string) (*Config, error) { ... }
```

### 2.4 Variable name length proportional to scope

Short names for small scopes, descriptive names for large scopes. Single-letter names are fine for loop indices and short-lived locals.

> Google Style Decisions > Variable Names

```go
// Small scope (1-7 lines) — short name fine
for i, v := range items { ... }

// Large scope (25+ lines) — descriptive name required
var requestTimeout time.Duration
var retryBackoffMultiplier float64
```

### 2.5 Receiver names: short, consistent, never `this` or `self`

Use a one- or two-letter abbreviation of the type name. Apply it consistently across all methods.

> Google Style Decisions > Receiver Names

```go
// WRONG
func (this *Server) Handle(...) { ... }
func (server *Server) Handle(...) { ... }

// RIGHT
func (s *Server) Handle(...) { ... }
```

### 2.6 Constants use MixedCaps — no `K` prefix, no `ALL_CAPS`

> Google Style Decisions > Constant Names

```go
// WRONG
const kMaxRetries = 3
const MAX_RETRIES = 3
const KMaxBufferSize = 1024

// RIGHT
const maxRetries = 3       // unexported
const MaxRetries = 3       // exported
```

### 2.7 Initialisms: consistent case

`URL` not `Url`. `ID` not `Id`. `HTTP` not `Http`. The entire initialism has the same case.

> Google Style Decisions > Initialisms

```go
// WRONG
type HttpClient struct { ... }
func (u *User) UserId() string { ... }
var xmlApi string

// RIGHT
type HTTPClient struct { ... }
func (u *User) UserID() string { ... }
var xmlAPI string
```

### 2.8 Don't name packages `util`, `helper`, `common`

> Google Style Best Practices > Util Packages

```go
// WRONG
import "myapp/util"
util.FormatDate(t)

// RIGHT — name packages by what they provide
import "myapp/timeformat"
timeformat.Date(t)
```

### 2.9 Test naming: `Test<Behavior>_<Condition>_<Expectation>`

For complex tests, use descriptive names. For simple tests, `Test<FunctionName>` suffices.

> Verily `docs/testing/unit-testing/best-practices.md` > Use descriptive test names

```go
// Simple
func TestAdd(t *testing.T) { ... }

// Complex — describes behavior under specific conditions
func TestEnterprise_EjectsWarpCore_WhenOverheating(t *testing.T) { ... }
```

---

## 3. Testing

### 3.1 Use `want` / `got`, never `expected`

Go convention uses `want` and `got`. Never use `expected`, `actual`, `exp`, or `result`.

> Google Style Decisions > Got Before Want

```go
// WRONG
expected := 42
actual := compute()

// RIGHT
want := 42
got := compute()
```

### 3.2 Table-driven tests with `cmp.Diff`

Every test with more than one case must be table-driven. Use `google/go-cmp` for comparison. Never use `reflect.DeepEqual`. `testify` is not recommended — its many similarly-named equivalence functions add cognitive load and it puts `want` before `got`.

> Google Style Decisions > Table-Driven Tests · Verily `docs/languages/go/guidelines.md` > Assertion Libraries

```go
tests := []struct {
    name    string
    input   string
    want    Result
    wantErr error
}{
    {
        name:  "valid input",
        input: "hello",
        want:  Result{Value: "hello"},
    },
    {
        name:    "empty input",
        input:   "",
        wantErr: ErrEmpty,
    },
}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        got, err := process(tt.input)
        if got, want := err, tt.wantErr; !cmp.Equal(got, want, cmpopts.EquateErrors()) {
            t.Fatalf("process(%q) error = %v, want %v", tt.input, got, want)
        }
        if diff := cmp.Diff(tt.want, got); diff != "" {
            t.Errorf("process(%q) mismatch (-want +got):\n%s", tt.input, diff)
        }
    })
}
```

### 3.3 Don't initialize test struct fields to zero values

Go zero-value semantics mean `false`, `0`, `""`, and `nil` are the defaults. Never set them explicitly — it adds noise.

> Google Style Decisions > Zero-value Fields

```go
// WRONG
{
    name:      "no retry",
    input:     "x",
    wantRetry: false,  // zero value — drop it
    wantCount: 0,      // zero value — drop it
    wantErr:   nil,    // zero value — drop it
}

// RIGHT
{
    name:  "no retry",
    input: "x",
}
```

### 3.4 Use logical time, never real clock

Unit tests must never depend on wall clock or `time.Sleep`. Inject a fake/logical clock.

> Verily `docs/testing/unit-testing/best-practices.md` > Do not rely on timing

```go
// WRONG — flaky, slow
time.Sleep(2 * time.Second)
if time.Since(start) > timeout { ... }

// RIGHT — inject a clock interface
type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
}

// In tests, use a fake clock you advance manually
fakeClock.Advance(2 * time.Second)
```

### 3.5 Follow the AAA pattern (Arrange, Act, Assert)

Organize test cases into three distinct blocks for readability.

> Verily `docs/testing/unit-testing/best-practices.md`

```go
func TestAdd(t *testing.T) {
    // Arrange
    a, b := 2, 3

    // Act
    got := math.Add(a, b)

    // Assert
    if got, want := got, 5; got != want {
        t.Errorf("Add(%d, %d) = %d; want %d", a, b, got, want)
    }
}
```

### 3.6 Identify function and input in failure messages

> Google Style Decisions > Identify the Function, Identify the Input

```go
// WRONG
t.Errorf("unexpected result")

// RIGHT
t.Errorf("Parse(%q) = %v, want %v", input, got, want)
```

### 3.7 `t.Error` vs `t.Fatal`

Use `t.Fatal` for setup failures without which the rest of the test cannot run. Use `t.Error` to keep going and report all failures. In table-driven subtests, use `t.Fatal` (ends only the current subtest).

> Google Style Decisions > Keep Going

```go
// Setup — fatal if this fails (can't continue without the file)
f, err := os.Open(path)
if err != nil {
    t.Fatalf("setup: Open(%q) = %v", path, err)
}

// Assertion — error to keep checking remaining assertions
if diff := cmp.Diff(want, got); diff != "" {
    t.Errorf("mismatch (-want +got):\n%s", diff)
}
```

### 3.8 Mark test helpers with `t.Helper()`

> Google Style Decisions > Mark Test Helpers · Verily `docs/testing/unit-testing/best-practices.md`

```go
func mustOpen(t *testing.T, path string) *os.File {
    t.Helper()
    f, err := os.Open(path)
    if err != nil {
        t.Fatalf("Open(%q) = %v", path, err)
    }
    return f
}
```

### 3.9 Don't use assertion libraries

> Google Style Best Practices > Test Functions · Verily `docs/languages/go/guidelines.md`

`cmp` is the recommended comparison library. `testify` is explicitly not recommended.

```go
// WRONG — testify puts want before got, many confusing variants
assert.Equal(t, want, got)
assert.Exactly(t, want, got)
assert.ObjectsAreEqual(want, got)

// RIGHT — cmp with standard if
if diff := cmp.Diff(want, got); diff != "" {
    t.Errorf("mismatch (-want +got):\n%s", diff)
}
```

### 3.10 Prefer test-doubles over mocks

Test-doubles (fakes) are preferred over mocks. Mocks couple tests to call order and arguments, making them brittle. Name test-double packages `<pkg>test` (e.g., `dbtest`).

> Verily `docs/testing/unit-testing/best-practices.md` > Prefer creating test-doubles over mocks

```go
// package dbtest
type FakeDatabase struct {
    Entries   []string
    SaveError error
}

func (f *FakeDatabase) Save(data string) error {
    f.Entries = append(f.Entries, data)
    return f.SaveError
}
```

### 3.11 Test behavior, not implementation

Don't write tests whose expectations are copied from the implementation. Test observable properties and documented behavior.

> Verily `docs/testing/unit-testing/best-practices.md` > Test behavior, not implementations

```go
// WRONG — test mirrors the implementation
got := TempFile(123)
want := fmt.Sprintf("/tmp/tempfile-%d", 123) // copied from source

// RIGHT — test the documented property
got := TempFile(123)
if !strings.HasPrefix(got, "/tmp/") {
    t.Errorf("TempFile(123) = %q; want /tmp/ prefix", got)
}
```

### 3.12 Prefer black-box testing (`*_test` package)

Use the `package foo_test` pattern so tests can only access exported APIs. This ensures tests are resilient to implementation changes and don't inflate coverage artificially.

> Verily `docs/testing/unit-testing/best-practices.md` > Prefer testing public interfaces

```go
// WRONG — white-box: tests internals directly
package prvalidate

func TestValidateTitle(t *testing.T) {
    got := validateTitle("title") // unexported function
}

// RIGHT — black-box: tests public API only
package prvalidate_test

func TestValidate(t *testing.T) {
    err := prvalidate.Validate("title", "description")
}
```

### 3.13 Use `t.Parallel` for table-driven tests

When test cases are independent, add `t.Parallel()` to reduce execution time.

> Verily `docs/testing/unit-testing/best-practices.md`

```go
func TestAdd(t *testing.T) {
    t.Parallel()
    tests := []struct {
        a, b, want int
    }{
        {1, 2, 3},
        {0, 0, 0},
    }
    for _, tt := range tests {
        t.Run(fmt.Sprintf("%d+%d", tt.a, tt.b), func(t *testing.T) {
            t.Parallel()
            if got := Add(tt.a, tt.b); got != tt.want {
                t.Errorf("Add(%d,%d) = %d, want %d", tt.a, tt.b, got, tt.want)
            }
        })
    }
}
```

### 3.14 Use `testing.Short()` and `t.Skip` for long tests

Long-running tests should opt out of presubmit CI with `testing.Short()`:

> Verily `docs/languages/go/guidelines.md` > Skipping long tests in CI

```go
func TestLongIntegrationTest(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping long test in short mode")
    }
    // ...
}
```

### 3.15 Don't call `t.Fatal` from goroutines

> Google Style Best Practices: Use `t.Errorf` and `return` inside goroutines instead.

```go
// WRONG — t.Fatal in a goroutine panics the test runner
go func() {
    if err := doWork(); err != nil {
        t.Fatalf("doWork() = %v", err) // called from goroutine
    }
}()

// RIGHT — use a channel or errgroup
errc := make(chan error, 1)
go func() { errc <- doWork() }()
if err := <-errc; err != nil {
    t.Fatalf("doWork() = %v", err)
}
```

### 3.16 Subtest names should be valid identifiers

Avoid spaces — they appear in `go test -run` flags. Use underscores or camelCase.

> Google Style Decisions > Subtest Names

```go
// WRONG — spaces require quoting in -run flag
t.Run("empty input returns error", func(t *testing.T) { ... })

// RIGHT — valid Go identifier, easy to filter
t.Run("emptyInput_returnsError", func(t *testing.T) { ... })
```

### 3.17 Don't write test-aware code

Never add `IsTest bool` flags or similar logic that changes behavior under test. Use dependency injection instead.

> Verily `docs/testing/unit-testing/best-practices.md` > Do not write test-aware code

```go
// WRONG — production code knows about tests
func Connect(addr string, isTest bool) (*DB, error) {
    if isTest {
        return &DB{}, nil
    }
    return sql.Open("postgres", addr)
}

// RIGHT — inject the dependency
type Connector interface { Connect(addr string) (*DB, error) }
func NewService(c Connector) *Service { ... }
```

### 3.18 Test both happy path and error conditions

Cover edge cases, boundary conditions, and error paths — not just the golden path.

> Verily `docs/testing/unit-testing/best-practices.md`

```go
// WRONG — only tests the happy path
tests := []struct{ name, input string; want int }{
    {"valid", "42", 42},
}

// RIGHT — includes error conditions
tests := []struct{ name, input string; want int; wantErr error }{
    {"valid",        "42",  42, nil},
    {"empty",        "",    0,  ErrEmpty},
    {"overflow",     "99999999999999", 0, ErrOverflow},
    {"not_a_number", "abc", 0,  ErrInvalid},
}
```

### 3.19 Isolate tests from external dependencies

Unit tests must not hit databases, filesystems, or network services. Use dependency injection and test-doubles.

> Verily `docs/testing/unit-testing/best-practices.md` > Isolate Tests from External Dependencies

```go
// WRONG — unit test hits a real database
func TestGetUser(t *testing.T) {
    db, _ := sql.Open("postgres", "localhost:5432")
    user, err := GetUser(db, "123")
}

// RIGHT — inject a fake
func TestGetUser(t *testing.T) {
    fake := &dbtest.FakeDB{Users: map[string]User{"123": {Name: "Alice"}}}
    user, err := GetUser(fake, "123")
}
```

### 3.20 Context in tests

Don't create `context.Background()` inside test helpers. The test function owns the context. In Go 1.24+, prefer `t.Context()`.

> Google Style Decisions > Contexts

```go
// WRONG — helper creates its own context
func setupDB(t *testing.T) *DB {
    t.Helper()
    ctx := context.Background() // loses test deadline
    return connect(ctx)
}

// RIGHT — test owns the context
func TestQuery(t *testing.T) {
    ctx := t.Context() // Go 1.24+; cancelled when test ends
    db := setupDB(t, ctx)
}
```

### 3.21 Keep test bodies simple and readable

Test functions must be easy to scan. If a test body requires deep nesting, complex setup, or more than ~20 lines of non-table code, simplify it. Extract helpers. Flatten assertions.

```go
// WRONG — deeply nested, hard to scan
func TestProcess(t *testing.T) {
    for _, f := range files {
        data, err := os.ReadFile(f)
        if err == nil {
            for _, line := range strings.Split(string(data), "\n") {
                if result, err := process(line); err != nil {
                    t.Errorf(...)
                } else if result != expected {
                    t.Errorf(...)
                }
            }
        }
    }
}

// RIGHT — flat, with helpers
func TestProcess(t *testing.T) {
    lines := mustReadLines(t, "testdata/input.txt")
    for _, tt := range lines {
        got, err := process(tt)
        if err != nil {
            t.Errorf("process(%q) error: %v", tt, err)
        }
    }
}
```

### 3.22 Use `t.Cleanup` instead of returning cleanup functions

Register teardown with `t.Cleanup` inside the helper so callers can't forget to defer it.

> Go Tip #4

```go
// WRONG — caller must remember to defer
func newFakeDB(t *testing.T) (*DB, func()) {
    db := startDB()
    return db, func() { db.Close() }
}
func TestQuery(t *testing.T) {
    db, cleanup := newFakeDB(t)
    defer cleanup() // easy to forget
}

// RIGHT — cleanup is automatic
func newFakeDB(t *testing.T) *DB {
    t.Helper()
    db := startDB()
    t.Cleanup(func() { db.Close() })
    return db
}
func TestQuery(t *testing.T) {
    db := newFakeDB(t) // cleanup registered automatically
}
```

### 3.23 Use `testdata/` for test fixture files

Store test input files in a `testdata/` directory (ignored by `go build`). Use `os.CreateTemp` or `t.TempDir()` for temporary outputs.

> Go Tip #74

```go
// WRONG — inline large test data or hardcoded /tmp paths
golden := "/tmp/test_output.json"

// RIGHT
input := filepath.Join("testdata", "input.json")
tmpDir := t.TempDir()
output := filepath.Join(tmpDir, "output.json")
```

### 3.24 Extract `main` into a testable `run()` function

Keep `main()` as a thin wrapper. Extract the real logic into `run(ctx, args, stdout, stderr) error` so it can be tested directly.

> Go Tip #101

```go
// WRONG — untestable main
func main() {
    cfg := parseFlags()
    if err := process(cfg); err != nil {
        log.Fatal(err)
    }
}

// RIGHT — testable run function
func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()
    if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error { ... }
```

### 3.25 Embed the interface in test fakes for large interfaces

When faking a large interface, embed it in the struct to satisfy the compiler. Only implement the methods your test needs.

> Go Tip #7

```go
// WRONG — manually stub 20+ methods
type fakeServer struct{}
func (fakeServer) MethodA(...) { ... }
func (fakeServer) MethodB(...) { ... }
// ... 18 more stubs

// RIGHT — embed the interface; implement only what's needed
type fakeServer struct {
    pb.UnimplementedServiceServer
}
func (f *fakeServer) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
    return &pb.User{Name: "test"}, nil
}
```

---

## 4. Architecture & Dependency Injection

### 4.1 Context originates in `main()` only

Create `context.Context` via `signal.NotifyContext` in `main()` and propagate it. Never create a new context inside subcommands, handlers, or utility functions. For Cobra, use `cmd.Context()`.

> Google Style Best Practices > Complex CLIs

```go
// WRONG — context created in subcommand
func runServe(cmd *cobra.Command, args []string) {
    ctx := context.Background() // NO
}

// RIGHT — context flows from main
func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()
    rootCmd.SetContext(ctx)
}
```

### 4.2 Context is always the first function parameter, never in a struct

> Google Style Decisions > Contexts

```go
// WRONG — context stored in struct
type Server struct {
    ctx context.Context
}

// WRONG — context not first
func Query(db *DB, ctx context.Context) error { ... }

// RIGHT
func Query(ctx context.Context, db *DB) error { ... }
```

### 4.3 Don't create custom context types

> Google Style Decisions: "No exceptions."

```go
// WRONG — custom context type
type myContext struct { context.Context; userID string }

// RIGHT — use context.WithValue with a typed key
type ctxKey struct{}
ctx = context.WithValue(ctx, ctxKey{}, userID)
```

### 4.4 Logger and tracer come from `main()` via dependency injection

Create logger and tracer in `main()` and inject them. Never create a new logger or tracer inside a subpackage.

```go
// WRONG — logger created inside the package
func NewService() *Service {
    logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
    return &Service{logger: logger}
}

// RIGHT — logger injected from main
func NewService(logger *slog.Logger) *Service {
    return &Service{logger: logger}
}
```

### 4.5 Use the logger, never `fmt.Printf` or `print`

All output goes through the injected logger. `fmt.Printf` bypasses log levels, structured fields, and output routing.

```go
// WRONG
fmt.Printf("processing %d items\n", len(items))

// RIGHT
logger.Info("processing items", "count", len(items))
```

### 4.6 Logging: use severity levels correctly

> Verily `docs/decision-log/dl-31-logging-practices.md`

| Level   | Use for |
|---------|---------|
| DEBUG   | Adhoc investigations, local development. Not emitted in production. |
| INFO    | Default. Request logs and working-as-intended events. |
| WARNING | Borderline expected/unexpected. Not alarming alone. |
| ERROR   | Unexpected conditions, server internal failures. Not for expected errors (rate limiting, cache misses). |

Don't use `fmt.Printf` for debug output — use the logger at DEBUG level.

### 4.7 Never log PII or PHI

> Verily `docs/decision-log/dl-31-logging-practices.md`: "Data classified as personal, PII, or PHI should never be logged." Log opaque record identifiers instead.

```go
// WRONG — logs PII
logger.Info("created patient", "name", patient.Name, "ssn", patient.SSN)

// RIGHT — log opaque identifiers only
logger.Info("created patient", "patientID", patient.ID)
```

### 4.8 Interfaces belong at the consumer side

Define interfaces in the package that uses them, not in the package that implements them. Never share interfaces across packages.

> Google Style Decisions > Interfaces

```go
// WRONG — interface defined in the provider package
// package storage
type Store interface {
    Get(key string) ([]byte, error)
}

// RIGHT — interface defined where it's consumed
// package service
type dataStore interface {
    Get(key string) ([]byte, error)
}
func NewService(store dataStore) *Service { ... }
```

### 4.9 Don't create interfaces until a real need exists

> Google Style Decisions: "Avoid creating interfaces until a real need exists."

```go
// WRONG — premature interface with one implementation
type UserService interface { GetUser(id string) (*User, error) }
type userServiceImpl struct{}

// RIGHT — use a concrete type; extract interface later when needed
type UserService struct{}
func (s *UserService) GetUser(id string) (*User, error) { ... }
```

### 4.10 Accept interfaces, return concrete types

> Google Style Decisions

```go
// WRONG — returns an interface
func NewStore() Store { return &fileStore{} }

// RIGHT — returns a concrete type
func NewStore() *FileStore { return &FileStore{} }
```

### 4.11 Keep interfaces small and unexported when internal

> Google Style Decisions: "Design interfaces to be small."

```go
// WRONG — large exported interface
type Repository interface {
    Get(id string) (*Item, error)
    List() ([]*Item, error)
    Create(item *Item) error
    Update(item *Item) error
    Delete(id string) error
    Archive(id string) error
}

// RIGHT — small, focused, unexported
type getter interface {
    Get(id string) (*Item, error)
}
```

### 4.12 Minimize exported types

Only export what is needed by other packages.

```go
// WRONG — internal helper exported for no reason
func ParseInternalConfig(data []byte) (*config, error) { ... }

// RIGHT — keep internal helpers unexported
func parseConfig(data []byte) (*config, error) { ... }
```

### 4.13 Use existing libraries — don't reinvent

Prefer core language constructs first, then standard library, then established third-party libraries.

> Google Style Guide > Least Mechanism

```go
// WRONG — hand-rolled JSON parsing
func parseJSON(data []byte) map[string]any {
    // 50 lines of custom parsing...
}

// RIGHT — use standard library
var result map[string]any
if err := json.Unmarshal(data, &result); err != nil { ... }
```

### 4.14 Move business logic out of `cmd/`

The `cmd/` package wires dependencies and parses flags. Business logic belongs in `internal/`.

> Google Style Best Practices > Complex CLIs

```go
// WRONG — business logic in cmd/
// cmd/serve.go
func runServe(cmd *cobra.Command, args []string) {
    users := db.Query("SELECT * FROM users")
    for _, u := range users { /* process... */ }
}

// RIGHT — cmd/ only wires; logic in internal/
// cmd/serve.go
func runServe(cmd *cobra.Command, args []string) {
    svc := internal.NewService(db)
    svc.ProcessUsers(cmd.Context())
}
```

### 4.15 Use `errgroup` for concurrent execution

For N concurrent operations with error aggregation, use `golang.org/x/sync/errgroup`.

> Verily `docs/languages/go/guidelines.md` > Concurrent Execution

```go
g, ctx := errgroup.WithContext(ctx)
for _, url := range urls {
    g.Go(func() error {
        return fetch(ctx, url)
    })
}
if err := g.Wait(); err != nil {
    return fmt.Errorf("fetching urls: %w", err)
}
```

### 4.16 Use variadic options for flexible APIs

When callers need optional configuration, use the variadic options pattern.

> Verily `docs/languages/go/guidelines.md` > Variadic Options · Google Style Best Practices > Variadic Options

```go
type options struct { timeout time.Duration }
type Option func(*options)

func WithTimeout(d time.Duration) Option {
    return func(o *options) { o.timeout = d }
}

func NewClient(opts ...Option) *Client {
    o := &options{timeout: 30 * time.Second}
    for _, opt := range opts {
        opt(o)
    }
    return &Client{timeout: o.timeout}
}
```

### 4.17 Method receiver types: prefer pointer receivers

Only use value receivers for small typedef-style types (e.g., `type Counter int`). Otherwise use pointer receivers to avoid undesired copies (especially important for types containing `sync.Mutex`).

> Verily `docs/languages/go/guidelines.md` > Method Receiver Types · Google Style Decisions > Receiver Type

```go
// OK — small typedef, value receiver is fine
type Status int
func (s Status) String() string { ... }

// WRONG — struct with mutex, value receiver copies mutex
type Cache struct { mu sync.Mutex; data map[string]string }
func (c Cache) Get(key string) string { ... }

// RIGHT — pointer receiver
func (c *Cache) Get(key string) string { ... }
```

### 4.18 Avoid package-level mutable state

Refactor package-level `var` into struct fields with methods. If backward compatibility is needed, delegate package-level functions to a default instance.

> Go Tip #36

```go
// WRONG — package-level mutable state
var db *sql.DB
func Query(q string) (*sql.Rows, error) { return db.Query(q) }

// RIGHT — state lives in a struct
type Store struct{ db *sql.DB }
func NewStore(db *sql.DB) *Store { return &Store{db: db} }
func (s *Store) Query(q string) (*sql.Rows, error) { return s.db.Query(q) }
```

### 4.19 Keep context keys unexported — provide accessor functions

Never export the context key type. Provide `FromContext` and `NewContext` functions.

> Go Tip #52

```go
// WRONG — exported key, callers can collide or misuse
var UserIDKey = contextKey("userID")

// RIGHT — unexported key with typed accessors
type ctxKey struct{}
func WithUserID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, ctxKey{}, id)
}
func UserIDFrom(ctx context.Context) (string, bool) {
    id, ok := ctx.Value(ctxKey{}).(string)
    return id, ok
}
```

### 4.20 Minimize `func init` — never read flags in `init`

Reserve `func init` for trivial, stateless setup (e.g., registering codecs, lookup tables). Flag values are not yet parsed during `init`. Prefer explicit initialization in `main`.

> Go Tip #123

```go
// WRONG — flags are not parsed yet in init
var configPath = flag.String("config", "", "path")
func init() {
    cfg := loadConfig(*configPath) // *configPath is always ""
}

// RIGHT — initialize in main after flag.Parse
func main() {
    flag.Parse()
    cfg := loadConfig(*configPath)
}
```

### 4.21 Don't use `sync.Once` for fallible operations

`sync.Once` runs the function exactly once — if it fails, retries are impossible. Use a mutex with check-on-demand for operations that can fail transiently.

> Go Tip #91

```go
// WRONG — if Connect fails, it's never retried
var once sync.Once
var conn *grpc.ClientConn
func GetConn() *grpc.ClientConn {
    once.Do(func() { conn, _ = grpc.Dial("addr") })
    return conn // may be nil forever
}

// RIGHT — retry on failure
var (
    mu   sync.Mutex
    conn *grpc.ClientConn
)
func GetConn() (*grpc.ClientConn, error) {
    mu.Lock()
    defer mu.Unlock()
    if conn != nil { return conn, nil }
    var err error
    conn, err = grpc.Dial("addr")
    return conn, err
}
```

### 4.22 Design APIs with explicit `Close`/`Stop` for resource lifetime

Types that hold resources (connections, goroutines, file handles) must provide an explicit `Close()` or `Stop()` method. Never rely on finalizers.

> Go Tip #81

```go
// RIGHT — explicit lifecycle
type Worker struct { cancel context.CancelFunc }
func NewWorker(ctx context.Context) *Worker {
    ctx, cancel := context.WithCancel(ctx)
    w := &Worker{cancel: cancel}
    go w.run(ctx)
    return w
}
func (w *Worker) Stop() { w.cancel() }
```

### 4.23 Don't mix `os.Exit` with `defer`

`os.Exit` terminates immediately — deferred functions never run. Move exit logic to `main` and return errors from `run()` instead.

> Go Tip #110

```go
// WRONG — defer never executes
func run() {
    f, _ := os.Create("out.txt")
    defer f.Close() // never called
    // ...
    os.Exit(1)
}

// RIGHT — let main handle the exit
func main() {
    if err := run(); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1) // defers in run() already completed
    }
}
```

---

## 5. Code Style & Structure

### 5.1 Flatten control flow — maximize line of sight

Never nest `if/else` blocks. Use early returns and guard clauses. Handle errors before proceeding.

> Google Style Decisions > Indent Error Flow

```go
// WRONG — nested
func process(input string) (Result, error) {
    if input != "" {
        if isValid(input) {
            result, err := transform(input)
            if err == nil {
                return result, nil
            } else {
                return Result{}, err
            }
        } else {
            return Result{}, ErrInvalid
        }
    } else {
        return Result{}, ErrEmpty
    }
}

// RIGHT — flat
func process(input string) (Result, error) {
    if input == "" {
        return Result{}, ErrEmpty
    }
    if !isValid(input) {
        return Result{}, ErrInvalid
    }
    return transform(input)
}
```

### 5.2 No magic strings or magic numbers

Every literal string or number that carries meaning must be a named constant.

```go
// WRONG
if resp.StatusCode == 429 { time.Sleep(5 * time.Second) }

// RIGHT
const maxRetryDelay = 5 * time.Second
if resp.StatusCode == http.StatusTooManyRequests {
    time.Sleep(maxRetryDelay)
}
```

### 5.3 Use a struct parameter when functions have many arguments

If a function signature breaks across multiple lines, use a config/options struct. Pass it as the last argument.

> Google Style Best Practices > Option Structures

```go
// WRONG — too many positional arguments
func NewServer(host string, port int, timeout time.Duration,
    maxConns int, tlsCert string, logger *slog.Logger) *Server { ... }

// RIGHT — struct parameter
type ServerConfig struct {
    Host     string
    Port     int
    Timeout  time.Duration
    MaxConns int
    TLSCert  string
}
func NewServer(cfg ServerConfig, logger *slog.Logger) *Server { ... }
```

### 5.4 No Yoda conditions

> Google Style Decisions > Conditionals: "Place the variable value on the left hand side."

```go
// WRONG — Yoda
if nil == err { ... }
if 0 == len(items) { ... }

// RIGHT
if err == nil { ... }
if len(items) == 0 { ... }
```

### 5.5 Don't use `break` without a label at end of `switch` cases

> Google Style Decisions: "They are redundant."

```go
// WRONG — redundant break
switch action {
case "start":
    start()
    break
case "stop":
    stop()
    break
}

// RIGHT — no break needed (Go doesn't fall through by default)
switch action {
case "start":
    start()
case "stop":
    stop()
}
```

### 5.6 Prefer synchronous functions over async

> Google Style Decisions > Synchronous Functions

```go
// WRONG — unnecessary channel-based async
func Fetch(url string) <-chan Result {
    ch := make(chan Result, 1)
    go func() { ch <- doFetch(url) }()
    return ch
}

// RIGHT — synchronous; caller can add concurrency if needed
func Fetch(ctx context.Context, url string) (Result, error) {
    return doFetch(ctx, url)
}
```

### 5.7 Goroutine lifetimes must be obvious

When you spawn goroutines, make it clear when or whether they exit. Use `context.Context` to manage lifetime.

> Google Style Decisions > Goroutine Lifetimes

```go
// WRONG — goroutine lifetime unclear, potential leak
func StartWorker() {
    go func() {
        for { processQueue() }
    }()
}

// RIGHT — context controls lifetime
func StartWorker(ctx context.Context) {
    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            default:
                processQueue()
            }
        }
    }()
}
```

### 5.8 Prefer nil slices over empty slice literals

> Google Style Decisions > Nil Slices: `var t []string` not `t := []string{}`. Check with `len(s) == 0`.

```go
// WRONG
t := []string{}
if t == nil { ... } // never true

// RIGHT
var t []string          // nil slice — marshals to JSON null
if len(t) == 0 { ... } // works for both nil and empty
```

### 5.9 Pass values, not pointers, for small read-only types

> Google Style Decisions > Pass Values

```go
// WRONG — unnecessary pointer for small read-only struct
func FormatAddr(a *Address) string { return a.City + ", " + a.State }

// RIGHT — pass by value (small, read-only)
func FormatAddr(a Address) string { return a.City + ", " + a.State }
```

### 5.10 Specify channel direction where possible

> Google Style Best Practices > Channel Direction

```go
// WRONG
func consume(ch chan Event) { ... }

// RIGHT
func consume(ch <-chan Event) { ... }
```

### 5.11 Remove unused code and dependencies

Dead code, unused variables, empty files, and unreferenced dependencies must be removed.

```go
// WRONG — unused helper left behind after refactor
func legacyParse(data []byte) (*Config, error) { ... } // never called

// RIGHT — delete it. Git has the history if you need it later.
```

### 5.12 Use `any` instead of `interface{}`

> Google Style Decisions > Use any

```go
// WRONG
func Print(v interface{}) { fmt.Println(v) }

// RIGHT
func Print(v any) { fmt.Println(v) }
```

### 5.13 Use `%q` for quoting strings in output

> Google Style Decisions > Use %q

```go
// WRONG — ambiguous if name is empty or has spaces
fmt.Sprintf("user not found: %s", name)

// RIGHT — %q reveals empty strings and whitespace
fmt.Sprintf("user not found: %q", name)
// output: user not found: ""  or  user not found: "Jo hn"
```

### 5.14 Function signatures should fit on a single line

> Google Style Decisions > Function Formatting. If it can't, use an options struct (5.3).

```go
// WRONG — wrapping signature
func ProcessData(ctx context.Context, input []byte,
    format string, validate bool, maxRetries int) (Result, error) { ... }

// RIGHT — use a struct if it doesn't fit (see 5.3)
func ProcessData(ctx context.Context, cfg ProcessConfig) (Result, error) { ... }
```

### 5.15 Don't break long strings (especially URLs) across lines

> Google Style Guide > Line Length

```go
// WRONG — broken URL is unsearchable and un-clickable
url := "https://example.com/api/v2/" +
    "users?active=true&limit=100"

// RIGHT — one long line is fine for string literals
url := "https://example.com/api/v2/users?active=true&limit=100"
```

### 5.16 Make hardcoded operational values configurable

Retry counts, timeouts, concurrency limits should come from configuration.

```go
// WRONG — hardcoded operational values
func Retry(fn func() error) error {
    for i := 0; i < 3; i++ {
        if err := fn(); err == nil { return nil }
        time.Sleep(2 * time.Second)
    }
    return errors.New("failed after retries")
}

// RIGHT — configurable
type RetryConfig struct {
    MaxAttempts int
    Delay       time.Duration
}
func Retry(cfg RetryConfig, fn func() error) error { ... }
```

### 5.17 Use `time.Duration` and `time.Time` — never raw integers for time

> Go Tip #75

```go
// WRONG — what unit is this? seconds? milliseconds?
func SetTimeout(ms int) { ... }
func SetDeadline(epoch int64) { ... }

// RIGHT — self-documenting types
func SetTimeout(d time.Duration) { ... }
func SetDeadline(t time.Time) { ... }
```

### 5.18 Use `strings.Builder` for string concatenation in loops

Concatenating with `+=` in a loop creates O(n^2) allocations. Use `strings.Builder`.

> Go Tip #29

```go
// WRONG — O(n²) allocations
var result string
for _, s := range items {
    result += fmt.Sprintf("- %s\n", s)
}

// RIGHT — O(n) with Builder
var sb strings.Builder
for _, s := range items {
    fmt.Fprintf(&sb, "- %s\n", s)
}
result := sb.String()
```

### 5.19 Use `strings.Cut` over `strings.SplitN` for key-value parsing

`strings.Cut` avoids magic indices and handles missing separators cleanly.

> Go Tip #112

```go
// WRONG — magic index, off-by-one risk
parts := strings.SplitN(line, "=", 2)
if len(parts) != 2 { return }
key, value := parts[0], parts[1]

// RIGHT
key, value, ok := strings.Cut(line, "=")
if !ok { return }
```

### 5.20 Use underscores in long numeric literals

> Go Tip #113

```go
// WRONG — hard to count digits, easy to mistype
const maxFileSize = 1073741824

// RIGHT — underscores make magnitude obvious
const maxFileSize = 1_073_741_824 // 1 GiB
```

### 5.21 Remove `v := v` copies in range loops (Go 1.22+)

Since Go 1.22, loop variables are per-iteration. The `v := v` shadow is dead code.

> Go Tip #114

```go
// WRONG — unnecessary since Go 1.22
for _, v := range items {
    v := v // no longer needed
    go func() { process(v) }()
}

// RIGHT
for _, v := range items {
    go func() { process(v) }()
}
```

### 5.22 Use `slices` and `maps` standard library packages

Prefer `slices.Contains`, `slices.SortFunc`, `maps.Keys` over hand-rolled loops.

> Go Tip #99

```go
// WRONG — hand-rolled search
found := false
for _, v := range items {
    if v == target { found = true; break }
}

// RIGHT
found := slices.Contains(items, target)
```

### 5.23 Use `cmp.Or` for first-non-zero defaults

> Go Tip #124

```go
// WRONG — verbose if/else chain
name := cfg.Name
if name == "" {
    name = envName
}
if name == "" {
    name = "default"
}

// RIGHT
name := cmp.Or(cfg.Name, envName, "default")
```

### 5.24 Use `time.NewTicker` with `Stop`, not `time.Tick`

`time.Tick` leaks the underlying ticker. Use `time.NewTicker` and defer `Stop()`.

> Go Tip #46

```go
// WRONG — ticker is never stopped, resource leak
for range time.Tick(5 * time.Second) {
    poll()
}

// RIGHT
ticker := time.NewTicker(5 * time.Second)
defer ticker.Stop()
for range ticker.C {
    poll()
}
```

### 5.25 Enum zero value should be Unknown or Invalid

Define the zero value of an enum as `Unknown` or `Invalid` so uninitialized values are never silently valid.

> Go Tip #73

```go
// WRONG — zero value is a valid status
type Status int
const (
    Active  Status = iota // 0 = Active — uninitialized fields look Active
    Inactive
)

// RIGHT — zero value is explicitly invalid
type Status int
const (
    StatusUnknown Status = iota // 0 = Unknown
    StatusActive
    StatusInactive
)
```

### 5.26 Beware `append` sharing the underlying array

`append` may return a slice that shares memory with the original. Use `slices.Clip` before appending to a sub-slice to prevent mutation of other slices.

> Go Tip #119

```go
// WRONG — a and b may share underlying array
a := original[:3]
b := append(a, newItem) // may overwrite original[3]

// RIGHT — clip detaches the underlying array
a := slices.Clip(original[:3])
b := append(a, newItem) // safe
```

### 5.27 Factory functions only when zero value is unusable

Don't write `New` constructors that merely return a struct literal with no logic. Use factory functions only when the type requires initialization (maps, channels, goroutines).

> Go Tip #19

```go
// WRONG — New adds nothing over a literal
type Entry struct { Name, Email string }
func NewEntry(name, email string) *Entry {
    return &Entry{Name: name, Email: email}
}

// RIGHT — just use the literal
e := &Entry{Name: "Alice", Email: "alice@example.com"}

// OK — factory needed: map requires make
type DB struct { entries map[string]*Entry }
func NewDB() *DB { return &DB{entries: make(map[string]*Entry)} }
```

### 5.28 Use method values when signatures match

Pass `obj.Method` directly instead of wrapping it in an anonymous function.

> Go Tip #59

```go
// WRONG — unnecessary wrapper
http.HandleFunc("/debug", func(w http.ResponseWriter, r *http.Request) {
    handler.ServeDebug(w, r)
})

// RIGHT — method value
http.HandleFunc("/debug", handler.ServeDebug)
```

---

## 6. Comments & Documentation

### 6.1 All exported names must have doc comments

Doc comments are full sentences that begin with the name of the symbol being described.

> Google Style Decisions > Doc Comments

```go
// WRONG
// this function parses the config
func ParseConfig(path string) (*Config, error) { ... }

// RIGHT
// ParseConfig reads and validates the configuration file at path.
func ParseConfig(path string) (*Config, error) { ... }
```

### 6.2 Package comments must start with `Package <name>`

Must appear immediately above the `package` clause with no blank line.

> Google Style Decisions > Package Comments

```go
// WRONG
// This package handles user authentication.
package auth

// RIGHT
// Package auth handles user authentication and session management.
package auth
```

### 6.3 No banner-style divider comments

```go
// WRONG
// ========================
// HELPER FUNCTIONS
// ========================

// RIGHT — use package structure and file organization instead
// If code needs sectioning, split into separate files.
```

### 6.4 Comments must match the code

Stale comments are worse than no comments.

```go
// WRONG — comment says one thing, code does another
// retryCount defaults to 5
const retryCount = 3

// RIGHT — comment matches behavior, or just remove it
const retryCount = 3
```

### 6.5 Don't use `fmt.Sprintf` as a format string

```go
// WRONG
msg := fmt.Sprintf("failed for %s", name)
logger.Errorf(msg) // msg used as format string — breaks if name contains %

// RIGHT
logger.Errorf("failed for %s", name)
```

### 6.6 Don't restate the obvious in doc comments

Don't document that context cancellation interrupts a function, that reads are concurrent-safe, or that mutations are not.

> Google Style Best Practices > Documentation > Contexts, Concurrency

```go
// WRONG — restates what the signature already says
// Get returns the value for the given key. It takes a context and a
// string key parameter and returns a string value and an error.
func Get(ctx context.Context, key string) (string, error) { ... }

// RIGHT — documents non-obvious behavior only
// Get returns the value for the given key, or ErrNotFound if
// the key has never been set.
func Get(ctx context.Context, key string) (string, error) { ... }
```

### 6.7 Signal-boost `err == nil` checks

```go
if err == nil { // if NO error
    ...
}
```

### 6.8 Document concurrency safety when non-obvious

Explicitly document goroutine safety when a seemingly read-only method mutates internal state (e.g., caching). Assume mutative methods are NOT goroutine-safe unless documented.

> Go Tip #96

```go
// RIGHT — documents non-obvious concurrency constraint
// Lookup returns the cached value for key. It is safe for concurrent use.
// Lookup populates the cache on first access; concurrent calls for the
// same key may duplicate work but will not corrupt state.
func (c *Cache) Lookup(key string) (string, error) { ... }
```

---

## 7. Formatting & CI

### 7.1 Run `gofmt -w` on all Go files before committing

> Google Style Guide > Formatting · Verily `docs/languages/go/linting.md`

### 7.2 Ensure all CI checks pass

Linters (`golangci-lint`, `errcheck`), tests (with race detection), code coverage, and architecture lint must all pass.

> Verily `docs/languages/go/linting.md`

### 7.3 Never commit secrets, credentials, or generated files

OAuth tokens, API keys, coverage profiles (`test.cov`), and cassettes with real credentials must never be committed.

### 7.4 Use `crypto/rand`, never `math/rand`, for keys

> Google Style Decisions > crypto/rand

---

## Quick Checklist

Before submitting any Go PR, verify:

**Error Handling**
- [ ] Every error return is checked — no `_ =`
- [ ] Errors are wrapped with context using `%w`
- [ ] Error strings are lowercase, no trailing punctuation
- [ ] Exported functions return `error` interface, not concrete types
- [ ] Errors are either logged or returned, never both
- [ ] Tests compare specific errors, not `wantErr bool`
- [ ] `cmpopts.EquateErrors()` used for error comparison in tests
- [ ] Error naming: `ErrFoo` for sentinels, `FooError` for types
- [ ] No stop words in error annotations ("failed to", "unable to")
- [ ] `Must` only at package-level constant init, never runtime
- [ ] Status codes only at system boundaries, plain errors in libraries

**Naming**
- [ ] MixedCaps everywhere — no snake_case in Go identifiers
- [ ] No `Get` prefix on getters
- [ ] Package name not repeated in exported symbols
- [ ] Receiver names are 1-2 letter abbreviations, consistent
- [ ] Constants use MixedCaps — no `ALL_CAPS` or `K` prefix
- [ ] Initialisms are all-caps: `URL`, `ID`, `HTTP`

**Testing**
- [ ] Tests use `want`/`got`, never `expected`
- [ ] Tests are table-driven with `cmp.Diff` (not `testify`, not `reflect.DeepEqual`)
- [ ] Test struct fields at zero value are omitted
- [ ] No `time.Sleep` or real clock in unit tests
- [ ] Failure messages identify function and input: `Func(input) = got, want X`
- [ ] `t.Helper()` called in test helpers
- [ ] `t.Fatal` for setup, `t.Error` for assertions
- [ ] Tests follow AAA pattern (Arrange, Act, Assert)
- [ ] Test-doubles preferred over mocks
- [ ] Tests verify behavior, not implementation
- [ ] Black-box testing (`*_test` package) preferred
- [ ] `t.Parallel` used where tests are independent
- [ ] Long tests use `testing.Short()` + `t.Skip`
- [ ] No test-aware production code
- [ ] `t.Cleanup` used instead of returning cleanup functions
- [ ] Test fixtures in `testdata/`, temp files via `t.TempDir()`
- [ ] `main` extracted into testable `run()` function

**Architecture**
- [ ] `context.Context` comes from `main()` or the test, never created mid-stack
- [ ] Context is first param, never stored in a struct
- [ ] Context keys unexported; accessor functions provided
- [ ] Logger/tracer injected from `main()`, no `fmt.Printf`
- [ ] Logging uses correct severity levels; no PII logged
- [ ] Interfaces defined at consumer side, not shared
- [ ] Interfaces only created when a real need exists
- [ ] Accept interfaces, return concrete types
- [ ] Pointer receivers used (except small typedef types)
- [ ] No package-level mutable state — use struct fields
- [ ] `func init` minimized; flags never read in `init`
- [ ] `sync.Once` not used for fallible operations
- [ ] Types with resources have explicit `Close`/`Stop`
- [ ] No `os.Exit` in functions that use `defer`

**Style**
- [ ] Control flow is flat — no nested `if/else`, use early returns
- [ ] No magic strings or numbers — use constants
- [ ] Function signatures fit one line, or use a struct param
- [ ] No Yoda conditions
- [ ] Nil slices preferred over `[]T{}`
- [ ] Use `any` not `interface{}`
- [ ] Channel direction specified
- [ ] Unused code and dependencies removed
- [ ] `time.Duration`/`time.Time` used, not raw ints for time
- [ ] `strings.Builder` for string concatenation in loops
- [ ] `strings.Cut` for key-value parsing, not `SplitN`
- [ ] Underscores in long numeric literals (`1_000_000`)
- [ ] No `v := v` copies in range loops (Go 1.22+)
- [ ] `slices`/`maps` stdlib functions used over hand-rolled loops
- [ ] `cmp.Or` for first-non-zero defaults
- [ ] `time.NewTicker` with `Stop`, not `time.Tick`
- [ ] Enum zero value is Unknown/Invalid
- [ ] Factory `New` only when zero value is unusable
- [ ] Method values used instead of wrapper closures

**Documentation**
- [ ] All exported names have doc comments starting with the symbol name
- [ ] Package comments start with `Package <name>`
- [ ] No banner divider comments
- [ ] Comments match the code they describe
- [ ] Concurrency safety documented when non-obvious

**Formatting & CI**
- [ ] `gofmt -w` has been run
- [ ] No secrets or generated files committed
- [ ] All CI checks pass (errcheck, golangci-lint, race, coverage)
