package interceptors

import (
	"net/http"

	"github.com/fervbmx/interceptor"
)

// BasicAuthInterceptor returns an interceptor that sets Basic authentication on every
// outgoing request.
//
//	interceptor.NewTransportInterceptor(nil,
//	    interceptors.BasicAuthInterceptor("username", "password"),
//	)
func BasicAuthInterceptor(username, password string) interceptor.InterceptorFunc {
	return func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		clonedReq := req.Clone(req.Context())
		clonedReq.SetBasicAuth(username, password)
		return next(clonedReq)
	}
}
