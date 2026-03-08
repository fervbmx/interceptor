# PRD: Logging Request Interceptor

## 1. Overview

Add a built-in `LoggingInterceptor` to the `github.com/fervbmx/interceptor` package that captures and logs HTTP request/response details using industry-standard structured logging formats. The interceptor plugs into the existing `InterceptorFunc` chain and requires zero external dependencies.

## 2. Problem Statement

Developers using the interceptor package currently have no built-in way to observe outgoing HTTP traffic. Debugging failed API calls, auditing third-party service communication, and measuring response latency all require writing custom one-off logging interceptors. A first-class logging interceptor would eliminate boilerplate, enforce a consistent log schema, and align with widely adopted observability practices (structured JSON logs, OpenTelemetry semantic conventions).

## 3. Goals

- Provide a ready-to-use interceptor that logs every outgoing HTTP request and its corresponding response (or error).
- Use **structured JSON** as the default output format (the de-facto industry standard for machine-parseable logs).
- Support **logfmt** (`key=value` pairs) — the structured-yet-readable format widely adopted by Grafana Loki, Heroku, and the Go ecosystem.
- Support a **text/plain** human-readable format for local development.
- Follow field naming conventions from **OpenTelemetry HTTP semantic conventions** and **ECS (Elastic Common Schema)** so logs integrate seamlessly with Elasticsearch, Datadog, Grafana Loki, and similar platforms.
- Allow developers to supply their own `*slog.Logger` (Go 1.21+ standard library) to control output destination and level.
- Remain a **zero-dependency** addition — rely only on the Go standard library.

## 4. Non-Goals

- Metric collection (histograms, counters) — that belongs in a separate `MetricsInterceptor`.
- Distributed tracing propagation (trace-id injection) — that belongs in a `TracingInterceptor`.
- Request/response body logging by default (security and performance risk); this will be opt-in only.

## 5. Logged Fields

The following fields MUST be present in every log entry. The interceptor supports two JSON key styles, configurable via the `KeyStyle` option:

- **Flat / dot notation (default)**: flat dotted keys (`"http.method"`). Field names follow [OpenTelemetry HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/). Best for Datadog, Splunk, Grafana Loki, CloudWatch.
- **Nested**: hierarchical JSON objects (`"http": {"request": {"method": ...}}`). Field structure follows [Elastic Common Schema](https://www.elastic.co/guide/en/ecs/current/). Best for Elasticsearch and Kibana.

### 5.1 Request Fields (logged before `next` is called)

| Field | Flat Key (dot notation) | Nested Key | Type | Description | Example |
|---|---|---|---|---|---|
| Timestamp | `timestamp` | `@timestamp` | string (RFC 3339) | Time the request was initiated | `2026-03-07T12:00:00.000Z` |
| Log Level | `level` | `log.level` | string | Severity level | `INFO` |
| Message | `msg` | `message` | string | Human-readable event description | `http request started` |
| HTTP Method | `http.method` | `http.request.method` | string | Request method | `GET` |
| URL | `http.url` | `url.full` | string | Full request URL | `https://api.example.com/v1/users` |
| URL Path | `http.target` | `url.path` | string | Path + query string | `/v1/users?page=2` |
| Host | `http.host` | `url.domain` | string | Host header value | `api.example.com` |
| Scheme | `http.scheme` | `url.scheme` | string | `http` or `https` | `https` |
| Request Content-Length | `http.request_content_length` | `http.request.body.bytes` | int | Body size in bytes (if known) | `256` |
| User-Agent | `user_agent.original` | `user_agent.original` | string | User-Agent header | `Go-http-client/1.1` |
| Request ID | `http.request_id` | `http.request.id` | string | Value of `X-Request-ID` header (if present) | `abc-123` |

### 5.2 Response Fields (logged after `next` returns)

| Field | Flat Key (dot notation) | Nested Key | Type | Description | Example |
|---|---|---|---|---|---|
| Timestamp | `timestamp` | `@timestamp` | string (RFC 3339) | Time the response was received | `2026-03-07T12:00:00.150Z` |
| Log Level | `level` | `log.level` | string | `INFO` for 1xx–3xx, `WARN` for 4xx, `ERROR` for 5xx or transport errors | `WARN` |
| Message | `msg` | `message` | string | Human-readable event description | `http request completed` |
| HTTP Method | `http.method` | `http.request.method` | string | Echoed from request | `GET` |
| URL | `http.url` | `url.full` | string | Echoed from request | `https://api.example.com/v1/users` |
| Status Code | `http.status_code` | `http.response.status_code` | int | Response status code | `404` |
| Response Content-Length | `http.response_content_length` | `http.response.body.bytes` | int | Body size in bytes (if known) | `128` |
| Duration | `http.duration_ms` | `event.duration` | float64 | Round-trip time in milliseconds | `142.56` |
| Error | `error.message` | `error.message` | string | Error text (only on transport failure) | `dial tcp: connection refused` |

### 5.3 Example JSON Log Lines — Flat / dot notation (default)

**Request started:**

```json
{
  "timestamp": "2026-03-07T12:00:00.000Z",
  "level": "INFO",
  "msg": "http request started",
  "http.method": "POST",
  "http.url": "https://api.example.com/v1/orders",
  "http.target": "/v1/orders",
  "http.host": "api.example.com",
  "http.scheme": "https",
  "http.request_content_length": 512,
  "user_agent.original": "Go-http-client/1.1"
}
```

**Request completed (success):**

```json
{
  "timestamp": "2026-03-07T12:00:00.150Z",
  "level": "INFO",
  "msg": "http request completed",
  "http.method": "POST",
  "http.url": "https://api.example.com/v1/orders",
  "http.status_code": 201,
  "http.response_content_length": 128,
  "http.duration_ms": 150.32
}
```

**Request completed (error):**

```json
{
  "timestamp": "2026-03-07T12:00:00.050Z",
  "level": "ERROR",
  "msg": "http request failed",
  "http.method": "GET",
  "http.url": "https://api.example.com/v1/health",
  "http.duration_ms": 50.10,
  "error.message": "dial tcp 10.0.0.1:443: connect: connection refused"
}
```

### 5.4 Example JSON Log Lines — Nested style

**Request started:**

```json
{
  "@timestamp": "2026-03-07T12:00:00.000Z",
  "log": { "level": "INFO" },
  "message": "http request started",
  "http": {
    "request": {
      "method": "POST",
      "body": { "bytes": 512 },
      "id": "abc-123"
    }
  },
  "url": {
    "full": "https://api.example.com/v1/orders",
    "path": "/v1/orders",
    "domain": "api.example.com",
    "scheme": "https"
  },
  "user_agent": {
    "original": "Go-http-client/1.1"
  }
}
```

**Request completed (success):**

```json
{
  "@timestamp": "2026-03-07T12:00:00.150Z",
  "log": { "level": "INFO" },
  "message": "http request completed",
  "http": {
    "request": { "method": "POST" },
    "response": {
      "status_code": 201,
      "body": { "bytes": 128 }
    }
  },
  "url": { "full": "https://api.example.com/v1/orders" },
  "event": { "duration": 150.32 }
}
```

**Request completed (error):**

```json
{
  "@timestamp": "2026-03-07T12:00:00.050Z",
  "log": { "level": "ERROR" },
  "message": "http request failed",
  "http": {
    "request": { "method": "GET" }
  },
  "url": { "full": "https://api.example.com/v1/health" },
  "event": { "duration": 50.10 },
  "error": { "message": "dial tcp 10.0.0.1:443: connect: connection refused" }
}
```

### 5.5 Example logfmt Log Lines

logfmt uses space-separated `key=value` pairs. String values containing spaces are quoted. This format is natively parseable by Grafana Loki, Heroku Logplex, and most log aggregation pipelines.

**Request started:**

```
timestamp=2026-03-07T12:00:00.000Z level=INFO msg="http request started" http.method=POST http.url="https://api.example.com/v1/orders" http.target="/v1/orders" http.host=api.example.com http.scheme=https http.request_content_length=512 user_agent.original="Go-http-client/1.1"
```

**Request completed (success):**

```
timestamp=2026-03-07T12:00:00.150Z level=INFO msg="http request completed" http.method=POST http.url="https://api.example.com/v1/orders" http.status_code=201 http.response_content_length=128 http.duration_ms=150.32
```

**Request completed (error):**

```
timestamp=2026-03-07T12:00:00.050Z level=ERROR msg="http request failed" http.method=GET http.url="https://api.example.com/v1/health" http.duration_ms=50.10 error.message="dial tcp 10.0.0.1:443: connect: connection refused"
```

### 5.6 Example Text Log Line (development mode)

```
2026-03-07T12:00:00.150Z INFO  http request completed  POST https://api.example.com/v1/orders 201 150.32ms
2026-03-07T12:00:00.050Z ERROR http request failed      GET  https://api.example.com/v1/health  — 50.10ms  error="connection refused"
```

## 6. Public API

All new code lives in the `interceptors` sub-package (`interceptors/logging.go`), consistent with the existing `auth.go` and `headers.go` placement.

### 6.1 Types

```go
// LogFormat controls the log output format.
type LogFormat int

const (
    LogFormatJSON   LogFormat = iota // Structured JSON (default)
    LogFormatLogfmt                  // logfmt key=value pairs
    LogFormatText                    // Human-readable plain text
)

// KeyStyle controls the JSON key naming convention.
type KeyStyle int

const (
    // KeyStyleFlat uses dot notation for keys (e.g. "http.method",
    // "http.status_code"). Field names follow OpenTelemetry HTTP semantic
    // conventions. Compatible with Datadog, Splunk, Grafana Loki, CloudWatch.
    // This is the default.
    KeyStyleFlat KeyStyle = iota

    // KeyStyleNested uses hierarchical JSON objects (e.g.
    // {"http": {"request": {"method": "POST"}}}). Field structure follows
    // the Elastic Common Schema. Compatible with Elasticsearch and Kibana.
    KeyStyleNested
)

// LoggingOptions configures the LoggingInterceptor.
type LoggingOptions struct {
    // Logger is an *slog.Logger instance. If nil, slog.Default() is used.
    Logger *slog.Logger

    // Format selects the output format. Default: LogFormatJSON.
    Format LogFormat

    // KeyStyle selects the JSON key naming convention. Only applies when
    // Format is LogFormatJSON. Default: KeyStyleFlat.
    KeyStyle KeyStyle

    // LogBody enables request/response body capture up to MaxBodyLogSize.
    // Disabled by default for security and performance.
    LogBody bool

    // MaxBodyLogSize is the maximum number of bytes to capture from the
    // request or response body when LogBody is true. Default: 1024.
    MaxBodyLogSize int

    // HeadersToLog is an explicit allowlist of header names to include in
    // log entries. Empty means no headers are logged beyond the defaults
    // defined in section 5. Useful for capturing correlation IDs.
    HeadersToLog []string

    // SensitiveHeaders lists header names whose values should be redacted
    // (replaced with "***") when logged. Default: ["Authorization", "Cookie",
    // "Set-Cookie"].
    SensitiveHeaders []string
}
```

### 6.2 Constructor

```go
// LoggingInterceptor returns an InterceptorFunc that logs HTTP request and
// response details.
//
//   interceptor.NewTransportInterceptor(nil,
//       interceptors.LoggingInterceptor(nil), // default options
//   )
//
//   // Nested keys for Elasticsearch:
//   interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
//       KeyStyle: interceptors.KeyStyleNested,
//   })
//
//   // logfmt for Grafana Loki:
//   interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
//       Format: interceptors.LogFormatLogfmt,
//   })
//
//   // Text for local development with body logging:
//   interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
//       Format:  interceptors.LogFormatText,
//       LogBody: true,
//   })
func LoggingInterceptor(opts *LoggingOptions) interceptor.InterceptorFunc
```

When `opts` is `nil`, the interceptor uses all default values (JSON format, flat key style, `slog.Default()`, no body logging, 1024-byte body limit).

### 6.3 Format Comparison

| Feature | JSON | logfmt | Text |
|---|---|---|---|
| Machine-parseable | Yes | Yes | No |
| Human-readable | Moderate | Good | Best |
| Native support | Elasticsearch, Datadog, Splunk, CloudWatch | Grafana Loki, Heroku, Prometheus | Terminal / local dev |
| Go slog handler | `slog.NewJSONHandler` | Custom formatter (std lib only) | `slog.NewTextHandler` |
| Recommended for | Production, log aggregation | Production, Kubernetes/cloud-native | Local development |

## 7. Behavior Specification

1. **Before calling `next`**: log a `"http request started"` entry at `INFO` level with all request fields from section 5.1.
2. **Record `time.Now()`** immediately before calling `next(req)`.
3. **After `next` returns**: compute duration and log a completion entry.
   - If `err != nil`: log at `ERROR` level with `"http request failed"` message and the `error.message` field.
   - If `resp.StatusCode >= 500`: log at `ERROR` level.
   - If `resp.StatusCode >= 400`: log at `WARN` level.
   - Otherwise: log at `INFO` level.
4. **Body logging** (opt-in): wrap `req.Body` and `resp.Body` with a `io.TeeReader` capped at `MaxBodyLogSize` bytes. Truncated bodies append `"...(truncated)"`. The original body streams remain intact for downstream consumers.
5. **Sensitive header redaction**: any header in `SensitiveHeaders` has its value replaced with `"***"` in the log output.
6. **Request cloning**: the interceptor MUST NOT mutate the original request. Follow the same `req.Clone(req.Context())` pattern used by the existing `HeaderInterceptor` and `BasicAuthInterceptor`.
7. **Thread safety**: the interceptor must be safe for concurrent use across goroutines sharing the same `http.Client`.

## 8. File Structure

```
interceptors/
├── auth.go              # existing
├── auth_test.go         # existing
├── headers.go           # existing
├── headers_test.go      # existing
├── logging.go           # NEW — LoggingInterceptor + LoggingOptions
└── logging_test.go      # NEW — tests
```

No new Go modules or dependencies are introduced. The implementation uses only `log/slog`, `time`, `io`, `bytes`, `fmt`, and `net/http` from the standard library.

## 9. Test Plan

### 9.1 Unit Tests (`interceptors/logging_test.go`)

| Test Case | Description |
|---|---|
| `TestLoggingInterceptor_JSON_FlatKeys` | Verifies JSON output with flat dotted keys (`http.method`) contains all required fields for a successful 200 response. |
| `TestLoggingInterceptor_JSON_NestedKeys` | Verifies JSON output with nested keys (`http.request.method`) produces correct object hierarchy and uses `@timestamp` and `message` fields. |
| `TestLoggingInterceptor_LogfmtFormat` | Verifies logfmt output contains all required `key=value` pairs and properly quotes values with spaces. |
| `TestLoggingInterceptor_TextFormat` | Verifies text-format output for a successful request. |
| `TestLoggingInterceptor_StatusLevels` | Table-driven test covering 2xx → INFO, 4xx → WARN, 5xx → ERROR level mapping. |
| `TestLoggingInterceptor_TransportError` | Simulates a connection failure and asserts `error.message` is present at ERROR level. |
| `TestLoggingInterceptor_Duration` | Asserts `http.duration_ms` is a positive number within a reasonable tolerance. |
| `TestLoggingInterceptor_BodyLogging` | Enables `LogBody`, sends a request with a known body, and asserts the body content appears in the log and remains readable by downstream consumers. |
| `TestLoggingInterceptor_BodyTruncation` | Sends a body larger than `MaxBodyLogSize` and asserts truncation with `"...(truncated)"`. |
| `TestLoggingInterceptor_SensitiveHeaderRedaction` | Sends `Authorization` and `Cookie` headers and asserts their values are replaced with `"***"`. |
| `TestLoggingInterceptor_CustomHeaders` | Uses `HeadersToLog` to include `X-Request-ID` and asserts it appears in the output. |
| `TestLoggingInterceptor_NilOptions` | Passes `nil` and asserts defaults are applied without panic. |
| `TestLoggingInterceptor_ChainPosition` | Places the logging interceptor in a chain with `HeaderInterceptor` and `BasicAuthInterceptor` and asserts all three execute correctly. |
| `TestLoggingInterceptor_Concurrent` | Fires 50 concurrent requests through the interceptor and asserts no race conditions (run with `-race`). |

### 9.2 Test Approach

All tests use `httptest.NewServer` to create ephemeral HTTP servers (same pattern as existing tests in the repository). Log output is captured by injecting a custom `*slog.Logger` that writes to a `bytes.Buffer`, enabling assertion on exact field values without relying on stdout capture.

## 10. Documentation Updates

- **README.md**: add `LoggingInterceptor` to the "Built-in interceptors" table and include a usage example in the "Usage" section.
- **Go doc comments**: every exported type, constant, and function receives a doc comment following Go conventions.

## 11. Acceptance Criteria

1. `go test ./... -race` passes with all new tests green.
2. JSON log output with `KeyStyleFlat` (default) is parseable by `encoding/json.Unmarshal` into a flat map and contains every field listed in section 5.1/5.2 using dotted keys.
3. JSON log output with `KeyStyleNested` is parseable by `encoding/json.Unmarshal` into nested objects following the field structure shown in section 5.4.
4. logfmt log output produces valid `key=value` pairs parseable by standard logfmt libraries, with proper quoting of values containing spaces.
5. Text log output matches the format shown in section 5.6.
6. Body logging is disabled by default and does not impact performance when off.
7. Sensitive headers are redacted by default.
8. No new external dependencies are introduced (`go.mod` remains unchanged).
9. The interceptor is composable — it works correctly at any position in the chain.

## 12. Future Considerations

- **Sampling**: add a `SampleRate float64` option to log only a percentage of requests in high-throughput environments.
- **Conditional logging**: add a `ShouldLog func(*http.Request) bool` predicate to skip logging for health-check endpoints or internal traffic.
- **Metrics interceptor**: a separate `MetricsInterceptor` could share duration computation utilities with this logging interceptor.
- **Trace context**: log `trace_id` and `span_id` fields when OpenTelemetry context is present in the request.
