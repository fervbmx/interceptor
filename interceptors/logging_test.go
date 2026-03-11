package interceptors_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/fervbmx/interceptor"
	"github.com/fervbmx/interceptor/interceptors"
)

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
	level   slog.Level
}

func (h *captureHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		resolveAttr(attrs, a)
		return true
	})

	h.records = append(h.records, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *captureHandler) snapshot() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]capturedRecord, len(h.records))
	copy(out, h.records)
	return out
}

func resolveAttr(dest map[string]any, a slog.Attr) {
	if a.Value.Kind() == slog.KindGroup {
		group := make(map[string]any)
		for _, ga := range a.Value.Group() {
			resolveAttr(group, ga)
		}
		dest[a.Key] = group
		return
	}

	dest[a.Key] = a.Value.Any()
}

func newCaptureLogger() (*slog.Logger, *captureHandler) {
	h := &captureHandler{level: slog.LevelDebug}
	return slog.New(h), h
}

func TestAddRequestLogging_SuccessWithHeaders(t *testing.T) {
	logger, sink := newCaptureLogger()

	transport := interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"Authorization", "X-Correlation-ID"},
		}),
		func(req *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: 2,
				Body:          io.NopCloser(strings.NewReader("ok")),
			}, nil
		},
	)

	client := &http.Client{Transport: transport}

	req, err := http.NewRequest(http.MethodPost, "https://api.example.com/v1/items", strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("http.NewRequest error: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Correlation-ID", "corr-1")
	req.Header.Set("User-Agent", "interceptor-tests/1.0")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	start := records[0]
	if start.msg != "http request started" {
		t.Fatalf("start message = %q, want %q", start.msg, "http request started")
	}
	httpAttrs := mustMapAttr(t, start.attrs, "http")
	requestAttrs := mustMapAttr(t, httpAttrs, "request")
	headers := mustMapAttr(t, requestAttrs, "header")
	userAgent := mustMapAttr(t, start.attrs, "user_agent")

	if got, ok := requestAttrs["method"].(string); !ok {
		t.Fatalf("http.request.method has unexpected type: %T", requestAttrs["method"])
	} else if got != http.MethodPost {
		t.Fatalf("http.request.method = %q, want %q", got, http.MethodPost)
	}
	if got, ok := headers["authorization"].(string); !ok {
		t.Fatalf("http.request.header.authorization has unexpected type: %T", headers["authorization"])
	} else if got != "***" {
		t.Fatalf("http.request.header.authorization = %q, want %q", got, "***")
	}
	if got, ok := headers["x-correlation-id"].(string); !ok {
		t.Fatalf("http.request.header.x-correlation-id has unexpected type: %T", headers["x-correlation-id"])
	} else if got != "corr-1" {
		t.Fatalf("http.request.header.x-correlation-id = %q, want %q", got, "corr-1")
	}
	if got, ok := userAgent["original"].(string); !ok {
		t.Fatalf("user_agent.original has unexpected type: %T", userAgent["original"])
	} else if got != "interceptor-tests/1.0" {
		t.Fatalf("user_agent.original = %q, want %q", got, "interceptor-tests/1.0")
	}
	bodyAttrs := mustMapAttr(t, requestAttrs, "body")
	if got := mustInt64Attr(t, bodyAttrs, "size", "http.request.body.size"); got != 3 {
		t.Fatalf("http.request.body.size = %d, want %d", got, 3)
	}

	finish := records[1]
	if finish.level != slog.LevelInfo {
		t.Fatalf("finish level = %v, want INFO", finish.level)
	}
	httpFinishAttrs := mustMapAttr(t, finish.attrs, "http")
	responseAttrs := mustMapAttr(t, httpFinishAttrs, "response")
	if got := mustInt64Attr(t, responseAttrs, "status_code", "http.response.status_code"); got != int64(http.StatusOK) {
		t.Fatalf("http.response.status_code = %d, want %d", got, int64(http.StatusOK))
	}
	responseBodyAttrs := mustMapAttr(t, responseAttrs, "body")
	if got := mustInt64Attr(t, responseBodyAttrs, "size", "http.response.body.size"); got != 2 {
		t.Fatalf("http.response.body.size = %d, want %d", got, 2)
	}
	clientAttrs := mustMapAttr(t, httpFinishAttrs, "client")
	clientReqAttrs := mustMapAttr(t, clientAttrs, "request")
	if _, ok := clientReqAttrs["duration"].(float64); !ok {
		t.Fatal("http.client.request.duration missing")
	}
	if _, ok := finish.attrs["error"]; ok {
		t.Fatalf("error.type should not exist on success: %+v", finish.attrs)
	}
}

func TestAddRequestLogging_StatusLevels(t *testing.T) {
	testCases := []struct {
		name      string
		status    int
		wantLevel slog.Level
		wantError string
	}{
		{name: "2xx", status: http.StatusOK, wantLevel: slog.LevelInfo},
		{name: "4xx", status: http.StatusNotFound, wantLevel: slog.LevelWarn, wantError: "404"},
		{name: "5xx", status: http.StatusInternalServerError, wantLevel: slog.LevelError, wantError: "500"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger, sink := newCaptureLogger()
			transport := interceptor.NewTransport(nil,
				interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
				func(req *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
					return &http.Response{
						StatusCode: tc.status,
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				},
			)

			client := &http.Client{Transport: transport}

			resp, err := client.Get("http://example.com")
			if err != nil {
				t.Fatalf("client.Get error: %v", err)
			}
			_ = resp.Body.Close()

			finish := sink.snapshot()[1]
			if finish.level != tc.wantLevel {
				t.Fatalf("finish level = %v, want %v", finish.level, tc.wantLevel)
			}
			errorAttrs, hasError := finish.attrs["error"].(map[string]any)
			if tc.wantError == "" {
				if hasError {
					t.Fatalf("error.type should not exist: %+v", finish.attrs)
				}
			} else {
				if !hasError {
					t.Fatal("error.type missing")
				}
				if got, ok := errorAttrs["type"].(string); !ok {
					t.Fatalf("error.type has unexpected type: %T", errorAttrs["type"])
				} else if got != tc.wantError {
					t.Fatalf("error.type = %q, want %q", got, tc.wantError)
				}
			}
		})
	}
}

type typedErr struct{}

func (typedErr) Error() string { return "typed" }

func (typedErr) ErrorType() string { return "custom_error" }

func TestAddRequestLogging_TransportErrors(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		wantType string
	}{
		{name: "typed error", err: typedErr{}, wantType: "custom_error"},
		{name: "generic error", err: errors.New("dial failed"), wantType: "errorString"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger, sink := newCaptureLogger()
			transport := interceptor.NewTransport(nil,
				interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
				func(req *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
					return nil, tc.err
				},
			)

			client := &http.Client{Transport: transport}
			_, err := client.Get("http://example.com")
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			records := sink.snapshot()
			if len(records) != 2 {
				t.Fatalf("len(records) = %d, want 2", len(records))
			}
			if records[1].level != slog.LevelError {
				t.Fatalf("level = %v, want ERROR", records[1].level)
			}
			errorAttrs := mustMapAttr(t, records[1].attrs, "error")
			if got, ok := errorAttrs["type"].(string); !ok {
				t.Fatalf("error.type has unexpected type: %T", errorAttrs["type"])
			} else if got != tc.wantType {
				t.Fatalf("error.type = %q, want %q", got, tc.wantType)
			}
		})
	}
}

func TestAddRequestLogging_RequestCloneIsolation(t *testing.T) {
	logger, _ := newCaptureLogger()

	req, err := http.NewRequest(http.MethodPost, "http://example.com/v1/items", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("http.NewRequest error: %v", err)
	}
	req.Header.Set("X-Original", "keep")

	transport := interceptor.NewTransport(
		nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
		func(nextReq *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
			nextReq.Header.Set("X-Mutated", "yes")
			nextReq.Method = http.MethodPut
			nextReq.URL.Path = "/mutated"
			return next(nextReq)
		},
		func(nextReq *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
			if nextReq == req {
				t.Fatal("downstream received original request pointer; expected clone")
			}
			if nextReq.Header.Get("X-Mutated") != "yes" {
				t.Fatalf("X-Mutated header = %q, want yes", nextReq.Header.Get("X-Mutated"))
			}
			if nextReq.Method != http.MethodPut {
				t.Fatalf("mutated request method = %q, want %q", nextReq.Method, http.MethodPut)
			}
			if nextReq.URL.Path != "/mutated" {
				t.Fatalf("mutated request path = %q, want /mutated", nextReq.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		},
	)

	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	if got := req.Header.Get("X-Mutated"); got != "" {
		t.Fatalf("original request header X-Mutated = %q, want empty", got)
	}
	if got := req.Method; got != http.MethodPost {
		t.Fatalf("original request method = %q, want %q", got, http.MethodPost)
	}
	if got := req.URL.Path; got != "/v1/items" {
		t.Fatalf("original request path = %q, want /v1/items", got)
	}
}

func TestAddRequestLogging_HTTPSDefaultPort(t *testing.T) {
	testCases := []struct {
		name       string
		url        string
		wantHost   string
		wantPort   int64
		portLogged bool
	}{
		{name: "https default port", url: "https://example.com/resource", wantHost: "example.com", wantPort: 443, portLogged: true},
		{name: "http default port", url: "http://example.com/resource", wantHost: "example.com", wantPort: 80, portLogged: true},
		{name: "explicit port", url: "https://example.com:8443/resource", wantHost: "example.com", wantPort: 8443, portLogged: true},
		{name: "unknown scheme", url: "ftp://example.com/resource", wantHost: "example.com", portLogged: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger, sink := newCaptureLogger()

			transport := interceptor.NewTransport(nil,
				interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
				func(req *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
				},
			)

			client := &http.Client{Transport: transport}
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatalf("http.NewRequest error: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("client.Do error: %v", err)
			}
			_ = resp.Body.Close()

			start := sink.snapshot()[0].attrs
			serverAttrs := mustMapAttr(t, start, "server")
			if got, ok := serverAttrs["address"].(string); !ok {
				t.Fatalf("server.address has unexpected type: %T", serverAttrs["address"])
			} else if got != tc.wantHost {
				t.Fatalf("server.address = %q, want %q", got, tc.wantHost)
			}

			portValue, exists := serverAttrs["port"]
			if tc.portLogged {
				if !exists {
					t.Fatal("server.port missing")
				}
				if got := mustInt64Attr(t, serverAttrs, "port", "server.port"); got != tc.wantPort {
					t.Fatalf("server.port = %d, want %d", got, tc.wantPort)
				}
			} else if exists {
				t.Fatalf("server.port should not be logged for %q, got %v", tc.url, portValue)
			}
		})
	}
}

func mustMapAttr(t *testing.T, attrs map[string]any, key string) map[string]any {
	t.Helper()

	group, ok := attrs[key].(map[string]any)
	if !ok {
		t.Fatalf("%s has unexpected type: %T", key, attrs[key])
	}

	return group
}

func mustInt64Attr(t *testing.T, attrs map[string]any, key string, path string) int64 {
	t.Helper()

	value := attrs[key]
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		t.Fatalf("%s has unexpected type: %T", path, value)
		return 0
	}
}
