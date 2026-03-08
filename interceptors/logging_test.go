package interceptors_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fervbmx/interceptor"
	"github.com/fervbmx/interceptor/interceptors"
)

type capturedRecord struct {
	level slog.Level
	msg   string
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, capturedRecord{level: r.Level, msg: r.Message})
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

func newCaptureLogger() (*slog.Logger, *captureHandler) {
	h := &captureHandler{}
	return slog.New(h), h
}

func TestLoggingInterceptor_JSON_FlatKeys(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}),
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

	start := decodeJSONMap(t, records[0].msg)
	if start["http.method"] != http.MethodGet {
		t.Fatalf("http.method = %v, want %q", start["http.method"], http.MethodGet)
	}
	if start["http.url"] != server.URL+"/v1/users?page=2" {
		t.Fatalf("http.url = %v", start["http.url"])
	}
	if start["http.target"] != "/v1/users?page=2" {
		t.Fatalf("http.target = %v", start["http.target"])
	}
	if start["http.request_id"] != "abc-123" {
		t.Fatalf("http.request_id = %v", start["http.request_id"])
	}

	finish := decodeJSONMap(t, records[1].msg)
	if finish["http.status_code"] != float64(http.StatusOK) {
		t.Fatalf("http.status_code = %v, want %d", finish["http.status_code"], http.StatusOK)
	}
	if _, ok := finish["http.duration_ms"]; !ok {
		t.Fatal("http.duration_ms missing")
	}
}

func TestLoggingInterceptor_JSON_NestedKeys(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
			Logger:   logger,
			Format:   interceptors.LogFormatJSON,
			KeyStyle: interceptors.KeyStyleNested,
		}),
	)}

	resp, err := client.Post(server.URL+"/v1/orders", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("client.Post error: %v", err)
	}
	_ = resp.Body.Close()

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	start := decodeJSONMap(t, records[0].msg)
	if _, ok := start["@timestamp"]; !ok {
		t.Fatal("@timestamp missing")
	}
	if start["message"] != "http request started" {
		t.Fatalf("message = %v", start["message"])
	}

	httpMap := start["http"].(map[string]any)
	requestMap := httpMap["request"].(map[string]any)
	if requestMap["method"] != http.MethodPost {
		t.Fatalf("http.request.method = %v", requestMap["method"])
	}
}

func TestLoggingInterceptor_LogfmtFormat(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
			Logger: logger,
			Format: interceptors.LogFormatLogfmt,
		}),
	)}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/q with space", nil)
	if err != nil {
		t.Fatalf("http.NewRequest error: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	line := records[0].msg
	if !strings.Contains(line, "level=INFO") {
		t.Fatalf("line missing level=INFO: %s", line)
	}
	if !strings.Contains(line, "msg=\"http request started\"") {
		t.Fatalf("line missing quoted msg: %s", line)
	}
	if !strings.Contains(line, "http.url=") {
		t.Fatalf("line missing http.url: %s", line)
	}
}

func TestLoggingInterceptor_TextFormat(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
			Logger: logger,
			Format: interceptors.LogFormatText,
		}),
	)}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	line := records[1].msg
	if !strings.Contains(line, "http request completed") {
		t.Fatalf("line missing completion message: %s", line)
	}
	if !strings.Contains(line, "GET") {
		t.Fatalf("line missing method: %s", line)
	}
	if !strings.Contains(line, "204") {
		t.Fatalf("line missing status code: %s", line)
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

			client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
				interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}),
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
		})
	}
}

func TestLoggingInterceptor_TransportError(t *testing.T) {
	logger, sink := newCaptureLogger()

	transport := interceptor.NewTransportInterceptor(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	}), interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}))

	client := &http.Client{Transport: transport}
	_, err := client.Get("http://example.com")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	records := sink.snapshot()
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	finish := decodeJSONMap(t, records[1].msg)
	if finish["error.message"] == nil {
		t.Fatalf("error.message missing: %v", finish)
	}
	if records[1].level != slog.LevelError {
		t.Fatalf("level = %v, want ERROR", records[1].level)
	}
}

func TestLoggingInterceptor_Duration(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}),
	)}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get error: %v", err)
	}
	_ = resp.Body.Close()

	records := sink.snapshot()
	finish := decodeJSONMap(t, records[1].msg)
	duration, ok := finish["http.duration_ms"].(float64)
	if !ok {
		t.Fatalf("http.duration_ms has unexpected type: %T", finish["http.duration_ms"])
	}
	if duration <= 0 {
		t.Fatalf("http.duration_ms = %v, want > 0", duration)
	}
}

func TestLoggingInterceptor_SensitiveHeaderRedaction(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"Authorization", "Cookie"},
		}),
	)}

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	req.Header.Set("Authorization", "Bearer top-secret")
	req.Header.Set("Cookie", "session=secret")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	start := decodeJSONMap(t, sink.snapshot()[0].msg)
	headers := start["http.request.headers"].(map[string]any)
	if headers["Authorization"] != "***" {
		t.Fatalf("Authorization = %v, want ***", headers["Authorization"])
	}
	if headers["Cookie"] != "***" {
		t.Fatalf("Cookie = %v, want ***", headers["Cookie"])
	}
}

func TestLoggingInterceptor_CustomHeaders(t *testing.T) {
	logger, sink := newCaptureLogger()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{
			Logger:       logger,
			HeadersToLog: []string{"X-Correlation-ID"},
		}),
	)}

	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	req.Header.Set("X-Correlation-ID", "corr-1")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do error: %v", err)
	}
	_ = resp.Body.Close()

	start := decodeJSONMap(t, sink.snapshot()[0].msg)
	headers := start["http.request.headers"].(map[string]any)
	if headers["X-Correlation-Id"] != "corr-1" {
		t.Fatalf("X-Correlation-Id = %v, want corr-1", headers["X-Correlation-Id"])
	}
}

func TestLoggingInterceptor_NilOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(nil),
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

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}),
		interceptors.HeaderInterceptor("X-Req-Id", "req-1"),
		interceptors.BasicAuthInterceptor("user", "pass"),
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

	client := &http.Client{Transport: interceptor.NewTransportInterceptor(nil,
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}),
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

	transport := interceptor.NewTransportInterceptor(
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
		interceptors.LoggingInterceptor(&interceptors.LoggingOptions{Logger: logger}),
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
