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
		req = req.Clone(req.Context())
		req.Header.Set(key, value)
		return next(req)
	}
}
