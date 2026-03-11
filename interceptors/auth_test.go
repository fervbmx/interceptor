package interceptors_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fervbmx/interceptor"
	"github.com/fervbmx/interceptor/interceptors"
)

func TestBasicAuth(t *testing.T) {
	cases := []struct {
		name     string
		username string
		password string
	}{
		{
			name:     "SimpleCredentials",
			username: "username",
			password: "password",
		},
		{
			name:     "EmptyPassword",
			username: "admin",
			password: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var username, password string
			var ok bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok = r.BasicAuth()
			}))
			t.Cleanup(server.Close)

			client := http.Client{
				Transport: interceptor.NewTransport(
					http.DefaultTransport,
					interceptors.BasicAuth(tc.username, tc.password),
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

			if !ok {
				t.Fatal("BasicAuth() returned ok=false, want true")
			}

			if username != tc.username {
				t.Errorf("username = %q, want %q", username, tc.username)
			}

			if password != tc.password {
				t.Errorf("password = %q, want %q", password, tc.password)
			}
		})
	}
}
