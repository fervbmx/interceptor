package interceptors_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fervbmx/interceptor"
	"github.com/fervbmx/interceptor/interceptors"
)

func TestBasicAuthInterceptor(t *testing.T) {
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

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var username, password string
			var ok bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				username, password, ok = r.BasicAuth()
			}))
			t.Cleanup(server.Close)

			client := http.Client{
				Transport: interceptor.NewTransportInterceptor(
					http.DefaultTransport,
					interceptors.BasicAuthInterceptor(c.username, c.password),
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

			if !ok {
				t.Fatal("BasicAuth() returned ok=false, want true")
			}

			if username != c.username {
				t.Errorf("username = %q, want %q", username, c.username)
			}

			if password != c.password {
				t.Errorf("password = %q, want %q", password, c.password)
			}
		})
	}
}
