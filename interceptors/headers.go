package interceptors

import (
	"net/http"

	"github.com/fervbmx/interceptor"
)

// HeaderInterceptor returns an interceptor that sets a header on every outgoing
// request.
//
//	interceptor.NewTransportInterceptor(nil,
//	    interceptors.HeaderInterceptor("User-Agent", "MyApp/1.0"),
//	)
func HeaderInterceptor(key, value string) interceptor.InterceptorFunc {
	return func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		clonedReq := req.Clone(req.Context())
		clonedReq.Header.Set(key, value)
		return next(clonedReq)
	}
}
