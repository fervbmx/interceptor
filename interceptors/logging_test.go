package interceptors_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestLoggingInterceptor_StructuredAttrs(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
	)}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/users?page=2", nil)
	if err != nil {
		t.Fatalf("http.NewRequest error: %v", err)
	}
	req.Header.Set("User-Agent", "interceptor-tests/1.0")
	req.Header.Set("X-Request-ID", "abc-123")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	start := records[0].attrs
	assertGroupPathString(t, start, "http.request.method", http.MethodGet)
	assertGroupPathString(t, start, "url.full", server.URL+"/v1/users?page=2")
	assertGroupPathString(t, start, "url.scheme", "http")
	assertGroupPathString(t, start, "user_agent.original", "interceptor-tests/1.0")
	assertGroupPathString(t, start, "interceptor.request_id", "abc-123")

	finish := records[1].attrs
	assertGroupPathInt64(t, finish, "http.response.status_code", int64(http.StatusOK))
	if _, ok := getGroupPath(finish, "http.client.request.duration").(float64); !ok {
		t.Fatal("http.client.request.duration missing")
	}
}

func TestLoggingInterceptor_StatusLevels(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		wantLevel slog.Level
	}{
		{name: "2xx", status: http.StatusOK, wantLevel: slog.LevelInfo},
		{name: "4xx", status: http.StatusNotFound, wantLevel: slog.LevelWarn},
		{name: "5xx", status: http.StatusBadGateway, wantLevel: slog.LevelError},
	}

	for _, tc := range cases {
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

			records := sink.snapshot()
			if got := records[len(records)-1].level; got != tc.wantLevel {
				t.Fatalf("final level = %v, want %v", got, tc.wantLevel)
			}
			assertGroupPathInt64(t, records[len(records)-1].attrs, "http.response.status_code", int64(tc.status))
		})
	}
}

func TestLoggingInterceptor_TransportError(t *testing.T) {
	logger, sink := newCaptureLogger()

	transport := interceptor.NewTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
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

	assertGroupPathString(t, records[1].attrs, "error.type", "errorString")
	if records[1].level != slog.LevelError {
		t.Fatalf("level = %v, want ERROR", records[1].level)
	}
}

func TestLoggingInterceptor_HTTPErrorStatusSetsErrorType(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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

	records := sink.snapshot()
	assertGroupPathString(t, records[1].attrs, "error.type", "500")
}

func TestLoggingInterceptor_NoErrorTypeOnSuccess(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
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

	records := sink.snapshot()
	if hasGroupPath(records[1].attrs, "error.type") {
		t.Fatalf("error.type should not exist on successful response: %+v", records[1].attrs)
	}
}

func TestLoggingInterceptor_Duration(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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

	records := sink.snapshot()
	duration, ok := getGroupPath(records[1].attrs, "http.client.request.duration").(float64)
	if !ok {
		t.Fatalf("http.client.request.duration has unexpected type: %T", getGroupPath(records[1].attrs, "http.client.request.duration"))
	}
	if duration <= 0 {
		t.Fatalf("http.client.request.duration = %v, want > 0", duration)
	}
}

func TestLoggingInterceptor_SensitiveHeaderRedaction(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"Authorization", "Cookie"},
		}),
	)}

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest(%q) error: %v", server.URL, err)
	}
	req.Header.Set("Authorization", "Bearer top-secret")
	req.Header.Set("Cookie", "session=secret")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	start := sink.snapshot()[0].attrs
	assertGroupPathString(t, start, "http.request.header.authorization", "***")
	assertGroupPathString(t, start, "http.request.header.cookie", "***")
}

func TestLoggingInterceptor_CustomHeaders(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"X-Correlation-ID"},
		}),
	)}

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest(%q) error: %v", server.URL, err)
	}
	req.Header.Set("X-Correlation-ID", "corr-1")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	start := sink.snapshot()[0].attrs
	assertGroupPathString(t, start, "http.request.header.x-correlation-id", "corr-1")
}

func TestLoggingInterceptor_NilOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(nil),
	)}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()
}

func TestLoggingInterceptor_ChainPosition(t *testing.T) {
	logger, sink := newCaptureLogger()

	var gotAuth string
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Req-Id")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
		interceptors.AddHeader("X-Req-Id", "req-1"),
		interceptors.AddBasicAuth("user", "pass"),
	)}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	if gotAuth == "" {
		t.Fatal("Authorization header missing")
	}
	if gotHeader != "req-1" {
		t.Fatalf("X-Req-Id = %q, want req-1", gotHeader)
	}
	if len(sink.snapshot()) != 2 {
		t.Fatal("expected two logging events")
	}
}

func TestLoggingInterceptor_Concurrent(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
	)}

	const total = 50
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(server.URL)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	records := sink.snapshot()
	if len(records) != total*2 {
		t.Fatalf("len(records) = %d, want %d", len(records), total*2)
	}
}

func TestLoggingInterceptor_RequestImmutability(t *testing.T) {
	logger, _ := newCaptureLogger()

	originalReq, err := http.NewRequest(http.MethodPost, "http://example.com/v1/items", strings.NewReader("immutable-payload"))
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

func TestLoggingInterceptor_WithJSONHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
	)}

	resp, err := client.Get(server.URL + "/json")
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	lines := splitLines(out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}

	first := decodeJSONMap(t, lines[0])
	httpMap, ok := first["http"].(map[string]any)
	if !ok {
		t.Fatalf("http group missing: %v", first)
	}
	requestMap, ok := httpMap["request"].(map[string]any)
	if !ok {
		t.Fatalf("http[request] missing or wrong type, got: %T", httpMap["request"])
	}
	if requestMap["method"] != "GET" {
		t.Fatalf("http.request.method = %v, want GET", requestMap["method"])
	}
}

func TestLoggingInterceptor_WithTextHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
	)}

	resp, err := client.Get(server.URL + "/text")
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	output := out.String()
	if !strings.Contains(output, "http.request.method=GET") {
		t.Fatalf("TextHandler output missing grouped dotted key: %s", output)
	}
	if !strings.Contains(output, "url.full=") {
		t.Fatalf("TextHandler output missing url.full: %s", output)
	}
}

func TestLoggingInterceptor_HandlerOptions_ReplaceAttr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	seenHTTPRequestMethod := false
	seenInterceptorDuration := false

	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if strings.Join(groups, ".") == "http.request" && a.Key == "method" {
				seenHTTPRequestMethod = true
				a.Value = slog.StringValue("OVERRIDDEN")
			}
			if strings.Join(groups, ".") == "http.client.request" && a.Key == "duration" {
				seenInterceptorDuration = true
			}
			return a
		},
	}))

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
	)}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	if !seenHTTPRequestMethod {
		t.Fatal("ReplaceAttr did not receive [http request] method")
	}
	if !seenInterceptorDuration {
		t.Fatal("ReplaceAttr did not receive [http client request] duration")
	}

	lines := splitLines(out.String())
	first := decodeJSONMap(t, lines[0])
	httpMap, ok := first["http"].(map[string]any)
	if !ok {
		t.Fatalf("http group missing or wrong type: %T", first["http"])
	}
	requestMap, ok := httpMap["request"].(map[string]any)
	if !ok {
		t.Fatalf("http[request] missing or wrong type, got: %T", httpMap["request"])
	}
	if requestMap["method"] != "OVERRIDDEN" {
		t.Fatalf("http.request.method = %v, want OVERRIDDEN", requestMap["method"])
	}
}

func TestLoggingInterceptor_HandlerOptions_Level(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelError}))

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}),
	)}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	if strings.TrimSpace(out.String()) != "" {
		t.Fatalf("expected no logs at error level for successful request, got %q", out.String())
	}
}

func TestLoggingInterceptor_OTelAttributeNames(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "3")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok!"))
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransport(nil,
		interceptors.AddRequestLogging(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"Content-Type"},
		}),
	)}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/otel", strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("http.NewRequest(%q) error: %v", server.URL+"/otel", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "otel-test/1.0")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	start := sink.snapshot()[0].attrs
	assertGroupPathString(t, start, "http.request.method", "POST")
	assertGroupPathInt64(t, start, "http.request.body.size", 3)
	assertGroupPathString(t, start, "http.request.header.content-type", "application/json")
	assertGroupPathString(t, start, "url.full", server.URL+"/otel")
	assertGroupPathString(t, start, "url.scheme", "http")
	assertGroupPathString(t, start, "server.address", "127.0.0.1")
	assertGroupPathInt64(t, start, "server.port", int64(mustServerPort(t, server.URL)))
	assertGroupPathString(t, start, "user_agent.original", "otel-test/1.0")

	finish := sink.snapshot()[1].attrs
	assertGroupPathInt64(t, finish, "http.response.status_code", int64(http.StatusAccepted))
	assertGroupPathInt64(t, finish, "http.response.body.size", 3)
	assertGroupPathInt64(t, finish, "http.request.body.size", 3)
	if hasGroupPath(finish, "http.target") {
		t.Fatalf("http.target must not be emitted: %+v", finish)
	}
}

func TestLoggingInterceptor_ServerAddressAndPort(t *testing.T) {
	logger, sink := newCaptureLogger()

	t.Run("implicit http port", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
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

		records := sink.snapshot()
		assertGroupPathString(t, records[len(records)-2].attrs, "server.address", "127.0.0.1")
		assertGroupPathInt64(t, records[len(records)-2].attrs, "server.port", int64(mustServerPort(t, server.URL)))
	})

	t.Run("explicit https default port", func(t *testing.T) {
		transport := interceptor.NewTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}), interceptors.AddRequestLogging(&interceptors.LoggingOptions{Logger: logger}))

		client := &http.Client{Transport: transport}
		req, err := http.NewRequest(http.MethodGet, "https://example.com/resource", nil)
		if err != nil {
			t.Fatalf("http.NewRequest(%q) error: %v", "https://example.com/resource", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("client.Do error: %v", err)
		}
		_ = resp.Body.Close()

		records := sink.snapshot()
		start := records[len(records)-2].attrs
		assertGroupPathString(t, start, "server.address", "example.com")
		assertGroupPathInt64(t, start, "server.port", 443)
	})
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func decodeJSONMap(t *testing.T, line string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("json.Unmarshal(%q) error: %v", line, err)
	}
	return out
}

func splitLines(s string) []string {
	parts := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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
