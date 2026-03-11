package interceptors

import (
	"net/http"

	"github.com/fervbmx/interceptor"
)

// Header returns an interceptor that sets a header on every outgoing
// request.
//
//	interceptor.NewTransport(nil,
//	    interceptors.Header("User-Agent", "MyApp/1.0"),
//	)
func Header(key, value string) interceptor.Middleware {
	return func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		clonedReq := req.Clone(req.Context())
		clonedReq.Header.Set(key, value)
		return next(clonedReq)
	}
}
