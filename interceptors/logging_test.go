package interceptors_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"Authorization", "X-Correlation-ID"},
		}),
	)}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/items", strings.NewReader("abc"))
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
	assertGroupPathString(t, start.attrs, "http.request.method", http.MethodPost)
	assertGroupPathString(t, start.attrs, "http.request.header.authorization", "***")
	assertGroupPathString(t, start.attrs, "http.request.header.x-correlation-id", "corr-1")
	assertGroupPathString(t, start.attrs, "user_agent.original", "interceptor-tests/1.0")
	assertGroupPathInt64(t, start.attrs, "http.request.body.size", 3)

	finish := records[1]
	if finish.level != slog.LevelInfo {
		t.Fatalf("finish level = %v, want INFO", finish.level)
	}
	assertGroupPathInt64(t, finish.attrs, "http.response.status_code", int64(http.StatusOK))
	assertGroupPathInt64(t, finish.attrs, "http.response.body.size", 2)
	if _, ok := getGroupPath(finish.attrs, "http.client.request.duration").(float64); !ok {
		t.Fatal("http.client.request.duration missing")
	}
	if hasGroupPath(finish.attrs, "error.type") {
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

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(server.Close)

			client := &http.Client{Transport: interceptor.NewTransport(nil,
				interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
			)}

			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("client.Get error: %v", err)
			}
			_ = resp.Body.Close()

			finish := sink.snapshot()[1]
			if finish.level != tc.wantLevel {
				t.Fatalf("finish level = %v, want %v", finish.level, tc.wantLevel)
			}
			if tc.wantError == "" {
				if hasGroupPath(finish.attrs, "error.type") {
					t.Fatalf("error.type should not exist: %+v", finish.attrs)
				}
			} else {
				assertGroupPathString(t, finish.attrs, "error.type", tc.wantError)
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
			transport := interceptor.NewTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return nil, tc.err
			}), interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}))

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
			assertGroupPathString(t, records[1].attrs, "error.type", tc.wantType)
		})
	}
}

func TestAddRequestLogging_RequestCloneIsolation(t *testing.T) {
	logger, _ := newCaptureLogger()

	originalReq, err := http.NewRequest(http.MethodPost, "http://example.com/v1/items", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("http.NewRequest error: %v", err)
	}
	originalReq.Header.Set("X-Original", "keep")

	transport := interceptor.NewTransport(
		roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req == originalReq {
				t.Fatal("downstream received original request pointer; expected clone")
			}
			if req.Header.Get("X-Mutated") != "yes" {
				t.Fatalf("X-Mutated header = %q, want yes", req.Header.Get("X-Mutated"))
			}
			if req.Method != http.MethodPut {
				t.Fatalf("mutated request method = %q, want %q", req.Method, http.MethodPut)
			}
			if req.URL.Path != "/mutated" {
				t.Fatalf("mutated request path = %q, want /mutated", req.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}),
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
		func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
			req.Header.Set("X-Mutated", "yes")
			req.Method = http.MethodPut
			req.URL.Path = "/mutated"
			return next(req)
		},
	)

	client := &http.Client{Transport: transport}
	resp, err := client.Do(originalReq)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	if got := originalReq.Header.Get("X-Mutated"); got != "" {
		t.Fatalf("original request header X-Mutated = %q, want empty", got)
	}
	if got := originalReq.Method; got != http.MethodPost {
		t.Fatalf("original request method = %q, want %q", got, http.MethodPost)
	}
	if got := originalReq.URL.Path; got != "/v1/items" {
		t.Fatalf("original request path = %q, want /v1/items", got)
	}
}

func TestAddRequestLogging_HTTPSDefaultPort(t *testing.T) {
	logger, sink := newCaptureLogger()

	transport := interceptor.NewTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}), interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}))

	client := &http.Client{Transport: transport}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/resource", nil)
	if err != nil {
		t.Fatalf("http.NewRequest error: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	start := sink.snapshot()[0].attrs
	assertGroupPathString(t, start, "server.address", "example.com")
	assertGroupPathInt64(t, start, "server.port", 443)
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func getGroupPath(attrs map[string]any, path string) any {
	current := any(attrs)
	for _, segment := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = m[segment]
		if !ok {
			return nil
		}
	}

	return current
}

func hasGroupPath(attrs map[string]any, path string) bool {
	return getGroupPath(attrs, path) != nil
}

func assertGroupPathString(t *testing.T, attrs map[string]any, path, want string) {
	t.Helper()

	got, ok := getGroupPath(attrs, path).(string)
	if !ok {
		t.Fatalf("%s has unexpected type: %T", path, getGroupPath(attrs, path))
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertGroupPathInt64(t *testing.T, attrs map[string]any, path string, want int64) {
	t.Helper()

	value := getGroupPath(attrs, path)
	var got int64
	switch v := value.(type) {
	case int:
		got = int64(v)
	case int64:
		got = v
	case float64:
		got = int64(v)
	default:
		t.Fatalf("%s has unexpected type: %T", path, value)
	}

	if got != want {
		t.Fatalf("%s = %d, want %d", path, got, want)
	}
}
