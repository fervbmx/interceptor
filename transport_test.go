package interceptor_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fervbmx/interceptor"
)

func TestTransport(t *testing.T) {
	var order []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	aInterceptor := func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		order = append(order, "A")
		return next(req)
	}
	bInterceptor := func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		order = append(order, "B")
		return next(req)
	}

	tp := interceptor.NewTransport(nil, aInterceptor)
	tp.Use(bInterceptor)

	client := &http.Client{
		Transport: tp,
		Timeout:   15 * time.Second,
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("client.Get() returned error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}

	if len(order) != 2 {
		t.Fatalf("len(order) = %d, want 2", len(order))
	}

	if order[0] != "A" {
		t.Errorf("order[0] = %q, want %q", order[0], "A")
	}

	if order[1] != "B" {
		t.Errorf("order[1] = %q, want %q", order[1], "B")
	}
}
