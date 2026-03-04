package interceptors_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fervbmx/interceptor"
	"github.com/fervbmx/interceptor/interceptors"
)

func TestHeaderInterceptor(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "UserAgent",
			key:   "User-Agent",
			value: "interceptor/v1.0.0",
		},
		{
			name:  "APIKey",
			key:   "X-API-KEY",
			value: "MY-SECRET-KEY",
		},
		{
			name:  "Authorization",
			key:   "Authorization",
			value: "Bearer some-token",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var header string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Get(c.key)
			}))
			t.Cleanup(server.Close)

			client := http.Client{
				Transport: interceptor.NewTransportInterceptor(
					http.DefaultTransport,
					interceptors.HeaderInterceptor(c.key, c.value),
				),
				Timeout: 15 * time.Second,
			}

			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("client.Get() returned error: %v", err)
			}

			if resp.StatusCode != 200 {
				t.Fatalf("unexpected status code: %d", resp.StatusCode)
			}

			if header != c.value {
				t.Errorf("Header %q = %q, want %q", c.key, header, c.value)
			}
		})
	}
}
