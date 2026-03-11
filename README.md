# HTTP Client interceptor

HTTP client interceptor system for Go. Compose middleware around http.RoundTripper to inspect, modify, retry, or short-circuit requests and responses, enabling a clean and extensible way to enrich and control outgoing HTTP calls.

## Install

```bash
go get github.com/fervbmx/interceptor
```

## Usage

```go
// Flow: AddRequestLogging → AddHeader → AddBasicAuth → http.DefaultTransport
client := &http.Client{
    Transport: interceptor.NewTransport(
        nil,
        interceptors.AddRequestLogging(nil),
        interceptors.AddHeader("X-API-KEY", "secret"),
        interceptors.AddBasicAuth("user", "pass"),
    ),
}
```

Pass `nil` as the first argument to use `http.DefaultTransport`, or provide your own `http.RoundTripper`.

## Built-in interceptors

| Interceptor | Description |
|---|---|
| `AddHeader(key, value)` | Sets a header on every request |
| `AddBasicAuth(user, password)` | Sets Basic authentication |
| `AddRequestLogging(opts)` | Emits structured `slog` attributes |

`AddRequestLogging` emits request duration as `http.client.request.duration` using seconds as the unit (UCUM `s`).

## Custom interceptors

Write your own `interceptor.Middleware` to hook into the request/response lifecycle. Call `next` to continue the chain, or return early to short-circuit it.

```go
// Log every request and its status code.
func Logging(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
    log.Printf("→ %s %s", req.Method, req.URL)

    resp, err := next(req)
    if err != nil {
        return nil, err
    }
    log.Printf("%s %s → %d", req.Method, req.URL, resp.StatusCode)
    return resp, nil
}

client := &http.Client{
    Transport: interceptor.NewTransport(
        http.DefaultTransport,
        Logging
    ),
}
```

## License

MIT
