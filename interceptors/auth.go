package interceptors

import (
	"net/http"

	"github.com/fervbmx/interceptor"
)

// AddBasicAuth returns an interceptor that sets Basic authentication on every
// outgoing request.
//
//	interceptor.NewTransport(nil,
//	    interceptors.AddBasicAuth("username", "password"),
//	)
func AddBasicAuth(username, password string) interceptor.Middleware {
	return func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		clonedReq := req.Clone(req.Context())
		clonedReq.SetBasicAuth(username, password)
		return next(clonedReq)
	}
}
