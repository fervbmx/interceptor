# HTTP Client interceptor

HTTP client interceptor system for Go. Compose middleware around http.RoundTripper to inspect, modify, retry, or short-circuit requests and responses, enabling a clean and extensible way to enrich and control outgoing HTTP calls.

## Install

```bash
go get github.com/fervbmx/interceptor
```

## Usage

```go
// Flow: LoggingInterceptor → HeaderInterceptor → BasicAuthInterceptor → http.DefaultTransport
client := &http.Client{
    Transport: interceptor.NewTransportInterceptor(
        nil,
        interceptors.LoggingInterceptor(nil),
        interceptors.HeaderInterceptor("X-API-KEY", "secret"),
        interceptors.BasicAuthInterceptor("user", "pass"),
    ),
}
```

Pass `nil` as the first argument to use `http.DefaultTransport`, or provide your own `http.RoundTripper`.

## Built-in interceptors

| Interceptor | Description |
|---|---|
| `HeaderInterceptor(key, value)` | Sets a header on every request |
| `BasicAuthInterceptor(user, password)` | Sets Basic authentication |
| `LoggingInterceptor(opts)` | Emits structured `slog` attributes |

## Custom interceptors

Write your own `InterceptorFunc` to hook into the request/response lifecycle. Call `next` to continue the chain, or return early to short-circuit it.

```go
// Log every request and its status code.
func LogginInterceptor(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
    log.Printf("→ %s %s", req.Method, req.URL)

    resp, err := next(req)
    if err != nil {
        return nil, err
    }
    log.Printf("%s %s → %d", req.Method, req.URL, resp.StatusCode)
    return resp, nil
}

client := &http.Client{
    Transport: interceptor.NewTransportInterceptor(
        http.DefaultTransport,
        interceptor.LogginInterceptor
    ),
}
```

## License

MIT
