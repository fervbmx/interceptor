package interceptors_test

import (
	"context"
	"errors"
	"fmt"
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
		resolveAttr(attrs, "", a)
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

func resolveAttr(dest map[string]any, prefix string, a slog.Attr) {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}

	if a.Value.Kind() == slog.KindGroup {
		for _, ga := range a.Value.Group() {
			resolveAttr(dest, key, ga)
		}
		return
	}

	dest[key] = a.Value.Any()
}

func newCaptureLogger() (*slog.Logger, *captureHandler) {
	h := &captureHandler{level: slog.LevelDebug}
	return slog.New(h), h
}

type typedErr struct{}

func (typedErr) Error() string { return "typed" }

func TestRequestLogging_SuccessWithHeaders(t *testing.T) {
	logger, sink := newCaptureLogger()

	transport := interceptor.NewTransport(nil,
		interceptors.RequestLogging(&interceptors.RequestLoggingOptions{
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

	client := &http.Client{
		Transport: transport,
	}

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
	if got := start.attrs["http.request.method"]; got != http.MethodPost {
		t.Fatalf("http.request.method = %q, want %q", got, http.MethodPost)
	}
	if got := start.attrs["http.request.header.authorization"]; got != "REDACTED" {
		t.Fatalf("http.request.header.authorization = %q, want %q", got, "REDACTED")
	}
	if got := start.attrs["http.request.header.x-correlation-id"]; got != "corr-1" {
		t.Fatalf("http.request.header.x-correlation-id = %q, want %q", got, "corr-1")
	}
	if got := start.attrs["user_agent.original"]; got != "interceptor-tests/1.0" {
		t.Fatalf("user_agent.original = %q, want %q", got, "interceptor-tests/1.0")
	}
	if got := fmt.Sprint(start.attrs["http.request.body.size"]); got != "3" {
		t.Fatalf("http.request.body.size = %v, want %d", got, 3)
	}

	end := records[1]
	if end.level != slog.LevelInfo {
		t.Fatalf("end level = %v, want INFO", end.level)
	}
	if got := fmt.Sprint(end.attrs["http.response.status_code"]); got != fmt.Sprint(http.StatusOK) {
		t.Fatalf("http.response.status_code = %v, want %d", got, int64(http.StatusOK))
	}
	if got := fmt.Sprint(end.attrs["http.response.body.size"]); got != "2" {
		t.Fatalf("http.response.body.size = %v, want %d", got, 2)
	}
	if _, ok := end.attrs["http.client.request.duration"]; !ok {
		t.Fatal("http.client.request.duration missing")
	}
	if _, ok := end.attrs["error.type"]; ok {
		t.Fatalf("error.type should not exist on success: %+v", end.attrs)
	}
}

func TestRequestLogging_StatusLevels(t *testing.T) {
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
				interceptors.RequestLogging(&interceptors.RequestLoggingOptions{Logger: logger}),
				func(req *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
					return &http.Response{
						StatusCode: tc.status,
						Body:       io.NopCloser(strings.NewReader("")),
					}, nil
				},
			)

			client := &http.Client{
				Transport: transport,
			}

			resp, err := client.Get("http://example.com")
			if err != nil {
				t.Fatalf("client.Get error: %v", err)
			}
			_ = resp.Body.Close()

			end := sink.snapshot()[1]
			if end.level != tc.wantLevel {
				t.Fatalf("end level = %v, want %v", end.level, tc.wantLevel)
			}
			_, hasError := end.attrs["error.type"]
			if tc.wantError == "" {
				if hasError {
					t.Fatalf("error.type should not exist: %+v", end.attrs)
				}
			} else {
				if !hasError {
					t.Fatal("error.type missing")
				}
				if got := end.attrs["error.type"]; got != tc.wantError {
					t.Fatalf("error.type = %q, want %q", got, tc.wantError)
				}
			}
		})
	}
}

func TestRequestLogging_TransportErrors(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		wantType string
	}{
		{name: "typed error", err: typedErr{}, wantType: "typedErr"},
		{name: "generic error", err: errors.New("dial failed"), wantType: "errorString"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger, sink := newCaptureLogger()

			transport := interceptor.NewTransport(nil,
				interceptors.RequestLogging(&interceptors.RequestLoggingOptions{
					Logger: logger,
				}),
				func(req *http.Request, _ interceptor.HandlerFunc) (*http.Response, error) {
					return nil, tc.err
				},
			)

			client := &http.Client{
				Transport: transport,
			}

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
			if got := records[1].attrs["error.type"]; got != tc.wantType {
				t.Fatalf("error.type = %q, want %q", got, tc.wantType)
			}
		})
	}
}

func TestRequestLogging_HTTPSDefaultPort(t *testing.T) {
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
				interceptors.RequestLogging(&interceptors.RequestLoggingOptions{Logger: logger}),
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
			if got := start["server.address"]; got != tc.wantHost {
				t.Fatalf("server.address = %q, want %q", got, tc.wantHost)
			}

			portValue, exists := start["server.port"]
			if tc.portLogged {
				if !exists {
					t.Fatal("server.port missing")
				}
				if got := fmt.Sprint(portValue); got != fmt.Sprint(tc.wantPort) {
					t.Fatalf("server.port = %v, want %d", got, tc.wantPort)
				}
			} else if exists {
				t.Fatalf("server.port should not be logged for %q, got %v", tc.url, portValue)
			}
		})
	}
}
