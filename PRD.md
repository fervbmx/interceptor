# PRD — `github.com/fervbmx/interceptor` Code Quality Cleanup

**Status:** Complete
**Author:** Fabian Ruiz
**Audience:** Repository maintainer(s) / contributing engineers
**Scope:** Post-review improvement pass — naming, documentation, testing, and implementation quality

---

## 1. Background

The `interceptor` library provides a composable HTTP middleware system for Go's `http.RoundTripper`. The library consists of a root package (`interceptor`) that defines the core chain-building infrastructure, and a sub-package (`interceptors`) that ships a set of ready-made implementations for auth, header injection, and structured logging.

A full code review was conducted on the library. The review found it to be **functionally correct** and architecturally sound. However, several issues were identified across four categories — naming, documentation, testing, and implementation — that need to be addressed before the library can be considered idiomatic Go and ready for wider adoption.

This document captures those findings as actionable engineering requirements organized into delivery phases.

---

## 2. Goals

- Bring the public API naming in line with idiomatic Go conventions.
- Ensure all exported symbols are properly documented.
- Eliminate test anti-patterns that hide failures or leak resources.
- Remove unnecessary abstractions and document non-obvious implementation choices.

## 3. Non-Goals

- Changing the library's architecture or adding new interceptors.
- Performance optimizations.
- Adding CI/CD pipelines or release automation.
- Any changes to behavior observable at runtime.

---

## 4. Delivery Phases

The requirements are grouped into four phases. Each phase is designed to be delivered as an independent unit — ideally a single pull request — with a clear rationale for the grouping.

```
Phase 1 → Public API + Docs      (breaking, bump major version)
Phase 2 → Test Correctness       (non-breaking, MUST fixes)
Phase 3 → Test Quality           (non-breaking, CONSIDER fixes)
Phase 4 → Implementation Polish  (non-breaking, CONSIDER fixes)
```

> **Note on breaking changes:** Phase 1 renames exported symbols. This is a semver-breaking change. Phases 2–4 are strictly internal and non-breaking for library consumers.

---

## 5. Requirements

Requirements are tagged with a severity tier:

- **[MUST]** — Required for idiomatic Go and API correctness. These block a stable release.
- **[CONSIDER]** — Improvements that meaningfully reduce maintenance burden or test reliability. Strongly recommended.

Each requirement includes an **Acceptance Criterion** that defines what "done" looks like.

---

### Phase 1 — Public API & Documentation

**Rationale:** All breaking changes to exported names are batched here so they land in a single semver bump. This phase should be merged before any other phase to avoid downstream churn.

---

#### REQ-N-01 · [MUST] Remove redundant `Interceptor` suffix from `interceptors` sub-package exports

**Category:** Naming
**Files:** `interceptors/auth.go`, `interceptors/headers.go`, `interceptors/logging.go`

**Problem:**
The exported functions `BasicAuthInterceptor`, `HeaderInterceptor`, and `LoggingInterceptor` all include the word `Interceptor` as a suffix. Because these symbols live in the `interceptors` package, callers already write `interceptors.BasicAuthInterceptor(...)`. The suffix is redundant noise — it provides no additional information. Package-qualified names should not stutter.

**Required Change:**

| Before | After |
|---|---|
| `func BasicAuthInterceptor(...)` | `func BasicAuth(...)` |
| `func HeaderInterceptor(...)` | `func Header(...)` |
| `func LoggingInterceptor(...)` | `func Logging(...)` |

**Acceptance Criterion:**
All three functions are renamed. No exported identifier in the `interceptors` package repeats the word "interceptor". All callers (including tests) are updated to use the new names. `go vet` and `go build ./...` pass with no errors.

---

#### REQ-N-02 · [MUST] Remove redundant package-name repetition from root `interceptor` package types

**Category:** Naming
**File:** `transport.go`

**Problem:**
The root package is named `interceptor`. The types `InterceptorFunc` and `TransportInterceptor`, and the constructor `NewTransportInterceptor`, all contain the word "interceptor" — which is already provided by the package qualifier. Callers write `interceptor.InterceptorFunc` and `interceptor.TransportInterceptor`, making the names stutter. The Go standard library does not repeat the package name in type names (`http.Transport`, `http.Handler`, `http.HandlerFunc` — not `http.HTTPTransport`). `HandlerFunc` is **not** affected by this issue since `Handler` does not repeat the package name.

**Required Change:**

| Before | After |
|---|---|
| `type InterceptorFunc func(...)` | `type Func func(...)` |
| `type TransportInterceptor struct` | `type Transport struct` |
| `func NewTransportInterceptor(...)` | `func New(...)` or `func NewTransport(...)` |

> **Note on constructor naming:** If more than one constructor is anticipated in the future, prefer `NewTransport`. If this is the sole constructor for the package's primary type, `New` is idiomatic (see `errors.New`, `ring.New`, `list.New`).

**Acceptance Criterion:**
Types and constructor are renamed. All references across `transport.go`, `transport_test.go`, and the `interceptors` sub-package are updated. `go build ./...` and `go test ./...` pass.

---

#### REQ-N-03 · [MUST] Rename loop variable `interceptor` in `RoundTrip` to avoid shadowing the package name

**Category:** Naming
**File:** `transport.go`, `RoundTrip` method

**Problem:**
Inside the reverse-iteration loop in `RoundTrip`, the code declares:

```go
interceptor := t.interceptors[i]
```

This local variable shadows the imported package name `interceptor` for the remainder of the loop body. Any future developer adding a reference to the `interceptor` package inside that block would silently get the local variable instead, leading to a confusing compile error or incorrect behavior. Additionally, the IIFE parameter `i` shadows the loop counter `i`.

**Required Change:**
Rename the loop variable to `fn` and the IIFE parameter accordingly:

```go
for i := len(t.interceptors) - 1; i >= 0; i-- {
    fn := t.interceptors[i]
    next := handler
    handler = func(fn Func, n HandlerFunc) HandlerFunc {
        return func(r *http.Request) (*http.Response, error) {
            return fn(r, n)
        }
    }(fn, next)
}
```

**Acceptance Criterion:**
No local variable in `transport.go` shadows an imported package name. `go vet ./...` passes. The chain-ordering behavior is unchanged and verified by existing tests.

---

### Phase 2 — Test Correctness

**Rationale:** These are the MUST-fix test issues. They either hide real failures (swallowed errors, unhandled panics) or leak resources (unclosed response bodies, invalid Go version). They should be fixed before Phase 3 so that the test suite is trustworthy as a baseline going forward. None of these changes are visible to library consumers.

---

#### REQ-T-01 · [MUST] Close response bodies in all tests that make HTTP requests

**Category:** Testing
**File:** `transport_test.go`

**Problem:**
`TestTransportInterceptor` calls `client.Get(...)` but never closes `resp.Body`. This leaks the underlying connection back to the `httptest.Server`'s pool and can cause unpredictable behavior in parallel test runs.

**Required Change:**
Add `defer resp.Body.Close()` immediately after verifying the error:

```go
resp, err := client.Get(server.URL)
if err != nil {
    t.Fatalf("client.Get() returned error: %v", err)
}
defer resp.Body.Close()
```

**Acceptance Criterion:**
All test functions that receive an `*http.Response` call `resp.Body.Close()` (via `defer` or explicit call before return). Running `go test -race ./...` produces no data-race warnings related to response body reads.

---

#### REQ-T-02 · [MUST] Replace HTTP status code literals with named constants

**Category:** Testing
**Files:** `transport_test.go`, `auth_test.go`, `headers_test.go`

**Problem:**
Multiple test assertions compare `resp.StatusCode` against the integer literal `200`. The `net/http` package exports `http.StatusOK` precisely to avoid magic numbers in code. Using the literal reduces readability and diverges from the style used everywhere else in the codebase.

**Required Change:**

```go
// Before:
if resp.StatusCode != 200 {

// After:
if resp.StatusCode != http.StatusOK {
```

**Acceptance Criterion:**
No integer literal HTTP status codes appear in any test file. All status comparisons use `http.Status*` constants.

---

#### REQ-T-03 · [MUST] Handle errors from `http.NewRequest` in all tests

**Category:** Testing
**File:** `logging_test.go`

**Problem:**
At least four test cases silently discard the error return from `http.NewRequest` using the blank identifier (`req, _ := ...`). When `http.NewRequest` fails, `req` is `nil`, and the next line that uses `req` will panic with a nil pointer dereference. This results in a cryptic, non-actionable failure message instead of a clear test diagnostic.

**Affected tests:**
- `TestLoggingInterceptor_SensitiveHeaderRedaction`
- `TestLoggingInterceptor_CustomHeaders`
- `TestLoggingInterceptor_OTelAttributeNames`
- `TestLoggingInterceptor_ServerAddressAndPort`

**Required Change:**

```go
// Before:
req, _ := http.NewRequest(http.MethodGet, server.URL, nil)

// After:
req, err := http.NewRequest(http.MethodGet, server.URL, nil)
if err != nil {
    t.Fatalf("http.NewRequest(%q) error: %v", server.URL, err)
}
```

**Acceptance Criterion:**
No test file uses `req, _` to discard errors from `http.NewRequest` or any other function that returns `error`. All such errors are checked and reported via `t.Fatalf`.

---

### Phase 3 — Test Quality

**Rationale:** These are the CONSIDER items scoped to the test layer. They improve consistency, safety, and speed of the test suite without altering any production code. Grouping them together makes for a clean, self-contained PR that is easy to review.

---

#### REQ-T-04 · [CONSIDER] Unify table-driven test loop variable name to `tc`

**Category:** Testing
**Files:** `auth_test.go`, `headers_test.go` (use `c`), `logging_test.go` (uses `tc`)

**Problem:**
The two styles are inconsistent across the same test suite. The idiomatic Go convention for table-driven tests is `tc` (short for "test case").

**Required Change:**

```go
// Before (auth_test.go, headers_test.go):
for _, c := range cases {
    t.Run(c.name, func(t *testing.T) { ... c.key ... })
}

// After:
for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) { ... tc.key ... })
}
```

**Acceptance Criterion:**
All table-driven tests in all files use `tc` as the loop variable name.

---

#### REQ-T-05 · [CONSIDER] Simplify `mustServerPort` using `net/url`

**Category:** Testing
**File:** `logging_test.go`

**Problem:**
The `mustServerPort` helper manually strips URL schemes with `strings.HasPrefix` / `strings.TrimPrefix` and then splits on `:` to find the port. This is fragile — it would silently break on URLs with user info or non-standard formatting. The standard library provides `url.Parse` precisely for this purpose.

**Required Change:**

```go
func mustServerPort(t *testing.T, rawURL string) int {
    t.Helper()
    u, err := url.Parse(rawURL)
    if err != nil {
        t.Fatalf("url.Parse(%q) error: %v", rawURL, err)
    }
    port, err := strconv.Atoi(u.Port())
    if err != nil {
        t.Fatalf("strconv.Atoi(%q) error: %v", u.Port(), err)
    }
    return port
}
```

**Acceptance Criterion:**
`mustServerPort` uses `url.Parse` and `u.Port()` with no manual string manipulation. All existing tests that call `mustServerPort` continue to pass.

---

#### REQ-T-06 · [CONSIDER] Use safe two-value type assertions in JSON-parsing tests

**Category:** Testing
**File:** `logging_test.go`

**Problem:**
`TestLoggingInterceptor_WithJSONHandler` and `TestLoggingInterceptor_HandlerOptions_ReplaceAttr` use single-value type assertions on `map[string]any` lookups:

```go
requestMap := httpMap["request"].(map[string]any)
```

If the key is absent or the underlying type does not match, this will **panic** with no test context, making the failure very hard to diagnose. The idiomatic approach is to use the comma-ok form and call `t.Fatalf`.

**Required Change:**

```go
requestMap, ok := httpMap["request"].(map[string]any)
if !ok {
    t.Fatalf("http[request] missing or wrong type, got: %T", httpMap["request"])
}
```

**Acceptance Criterion:**
No unsafe (single-value) type assertions remain in test files for keys retrieved from `map[string]any`. All assertions use the comma-ok form and call `t.Fatal` on failure.

---

#### REQ-T-07 · [CONSIDER] Remove `time.Sleep` from `TestLoggingInterceptor_Duration`

**Category:** Testing
**File:** `logging_test.go`

**Problem:**
The test makes the server sleep for 10ms before responding, then verifies that the logged duration is a float64 less than 1 second. The sleep adds unnecessary latency to the test suite and is sensitive to slow CI environments. The actual requirement — that duration is a positive float64 — does not need a sleep to be verified.

**Required Change:**
Remove the `time.Sleep` from the handler and tighten the assertion to only check `duration > 0`:

```go
// Server handler — remove the sleep:
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
}))

// Assertion — check only that duration is positive:
if duration <= 0 {
    t.Fatalf("http.client.request.duration = %v, want > 0", duration)
}
```

**Acceptance Criterion:**
`TestLoggingInterceptor_Duration` contains no `time.Sleep` calls. The test verifies that duration is a positive float64 value. `go test -count=5 ./...` passes consistently.

---

### Phase 4 — Implementation Polish

**Rationale:** These are internal code quality improvements that do not touch the public API or the tests. They reduce future maintenance cost and improve the long-term readability of `interceptors/logging.go`. Because they are the lowest urgency and most self-contained, they are best left for last so they don't block any other work.

---

#### REQ-I-01 · [CONSIDER] Inline or remove `sanitizeHeaderFieldKey`

**Category:** Implementation
**File:** `interceptors/logging.go`

**Problem:**
`sanitizeHeaderFieldKey` is a private function whose entire body is:

```go
func sanitizeHeaderFieldKey(key string) string {
    key = strings.ToLower(key)
    return key
}
```

It wraps a single call to `strings.ToLower` with no additional logic. The name implies sanitization (validation, filtering) but performs only a case conversion. This is a misleading abstraction that adds indirection without value.

**Required Change:**
Remove the function and call `strings.ToLower(key)` directly at the call site(s).

**Acceptance Criterion:**
`sanitizeHeaderFieldKey` no longer exists. All former call sites use `strings.ToLower` directly. `go build ./...` passes.

---

#### REQ-I-02 · [CONSIDER] Add a comment explaining the `reflect`-based error classification in `classifyErrorType`

**Category:** Implementation
**File:** `interceptors/logging.go`

**Problem:**
`classifyErrorType` uses `reflect.TypeOf` to derive a string label for the type of an error. This is functional but fragile: type names are implementation details that can change with refactoring, silently altering logged values in production. There is no comment explaining why this approach was chosen over alternatives (e.g., an `ErrorTyper` interface), which makes the decision invisible to future maintainers.

**Required Change — Option A (minimum):**
Add a comment documenting the tradeoff:

```go
// classifyErrorType derives a human-readable label for the concrete type of err.
// It uses reflection because error types are not required to self-describe their
// kind. Note: type names are implementation details and may change with refactors;
// callers relying on specific values in logs should define types that implement
// ErrorTyper instead.
```

**Required Change — Option B (recommended):**
Expose an `ErrorTyper` interface that error types can implement to opt into stable labels, and fall back to reflect only when the interface is not implemented:

```go
// ErrorTyper can be implemented by error types to provide a stable,
// human-readable classification label for structured logs.
type ErrorTyper interface {
    ErrorType() string
}
```

**Acceptance Criterion (Option A):** The function has a godoc comment explaining the reflect usage and its limitations.
**Acceptance Criterion (Option B):** An `ErrorTyper` interface is defined and checked before the reflect fallback. The interface is exported and documented.

---

## 6. Out of Scope

The following items were considered during the review and **deliberately excluded** from this document:

- **Architectural changes** — The split between the root `interceptor` package and the `interceptors` sub-package is sound and will not be changed.
- **New interceptor implementations** — Adding new ready-made interceptors (e.g., retry, timeout, metrics) is a feature request, not a cleanup task.
- **API versioning strategy** — The renaming in REQ-N-01 and REQ-N-02 constitutes a breaking change to the public API. How this is communicated to users (semver, deprecation notices, migration guide) is outside the scope of this document.
- **`t.Parallel()`** — Adding parallel test execution is a valid improvement but is a separate concern from the quality issues identified here.

---

## 7. Summary Table

| Phase | ID | Severity | Category | Title |
|---|---|---|---|---|
| 1 | REQ-N-01 | MUST | Naming | Remove redundant `Interceptor` suffix from `interceptors` exports |
| 1 | REQ-N-02 | MUST | Naming | Remove package-name repetition from root `interceptor` types |
| 1 | REQ-N-03 | MUST | Naming | Rename loop variable `interceptor` to avoid package shadowing |
| 2 | REQ-T-01 | MUST | Testing | Close response bodies in all tests |
| 2 | REQ-T-02 | MUST | Testing | Replace HTTP status literals with `http.Status*` constants |
| 2 | REQ-T-03 | MUST | Testing | Handle errors from `http.NewRequest` in all tests |
| 3 | REQ-T-04 | CONSIDER | Testing | Unify table-driven test loop variable name to `tc` |
| 3 | REQ-T-05 | CONSIDER | Testing | Simplify `mustServerPort` using `net/url` |
| 3 | REQ-T-06 | CONSIDER | Testing | Use safe two-value type assertions in JSON-parsing tests |
| 3 | REQ-T-07 | CONSIDER | Testing | Remove `time.Sleep` from duration test |
| 4 | REQ-I-01 | CONSIDER | Implementation | Inline or remove `sanitizeHeaderFieldKey` |
| 4 | REQ-I-02 | CONSIDER | Implementation | Document or improve `classifyErrorType` reflect usage |
