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

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var header string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Get(tc.key)
			}))
			t.Cleanup(server.Close)

			client := http.Client{
				Transport: interceptor.NewTransport(
					http.DefaultTransport,
					interceptors.Header(tc.key, tc.value),
				),
				Timeout: 15 * time.Second,
			}

			resp, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("client.Get() returned error: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("unexpected status code: %d", resp.StatusCode)
			}

			if header != tc.value {
				t.Errorf("Header %q = %q, want %q", tc.key, header, tc.value)
			}
		})
	}
}
