package interceptors

import (
	"net/http"

	"github.com/fervbmx/interceptor"
)

// BasicAuth returns an interceptor that sets Basic authentication on every
// outgoing request.
//
//	interceptor.NewTransport(nil,
//	    interceptors.BasicAuth("username", "password"),
//	)
func BasicAuth(username, password string) interceptor.Middleware {
	return func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		clonedReq := req.Clone(req.Context())
		clonedReq.SetBasicAuth(username, password)
		return next(clonedReq)
	}
}
