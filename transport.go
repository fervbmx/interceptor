// Package interceptor provides an HTTP interceptor system. It allows composing
// middleware interceptors that can inspect, modify, retry, or short-circuit
// HTTP requests and responses flowing through a standard http.RoundTripper.
package interceptor

import "net/http"

// HandlerFunc represents the next step in the interceptor chain.
// It takes an HTTP request and returns a response or error.
type HandlerFunc func(req *http.Request) (*http.Response, error)

// InterceptorFunc defines an interceptor. It receives the outgoing request and
// a next function representing the next processing step in the interceptor
// chain.
type InterceptorFunc func(req *http.Request, next HandlerFunc) (*http.Response, error)

// TransportInterceptor implements http.RoundTripper by running a chain of interceptors
// in front of a default transport.
type TransportInterceptor struct {
	defaultTransport http.RoundTripper
	interceptors     []InterceptorFunc
}

// NewTransportInterceptor creates a new TransportInterceptor with the given default RoundTripper
// and interceptors. If defaultTransport is nil, http.DefaultTransport is used.
// Interceptors are executed in the order provided.
//
//	// Using the default transport:
//	client := &http.Client{
//	    Transport: interceptor.NewTransportInterceptor(nil, AInterceptor, BInterceptor),
//	}
//
//	// Using a custom default transport:
//	client := &http.Client{
//	    Transport: interceptor.NewTransportInterceptor(customTransport, AInterceptor, BInterceptor),
//	}
//
// With this configuration, a request flows as:
//
//	AInterceptor → BInterceptor → customTransport
func NewTransportInterceptor(defaultTransport http.RoundTripper, interceptors ...InterceptorFunc) *TransportInterceptor {
	if defaultTransport == nil {
		defaultTransport = http.DefaultTransport
	}
	return &TransportInterceptor{
		defaultTransport: defaultTransport,
		interceptors:     interceptors,
	}
}

// Use appends one or more interceptors to the chain. They are appended after
// any interceptors already registered.
//
//	t := interceptor.NewTransportInterceptor(nil, AuthInterceptor).Use(MetricsInterceptor)
//	// order: AuthInterceptor → MetricsInterceptor → default transport
func (t *TransportInterceptor) Use(interceptors ...InterceptorFunc) *TransportInterceptor {
	t.interceptors = append(t.interceptors, interceptors...)
	return t
}

// RoundTrip executes the interceptor chain and then the underlying transport.
func (t *TransportInterceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	// Build the final handler that delegates to the default transport.
	final := HandlerFunc(func(r *http.Request) (*http.Response, error) {
		return t.defaultTransport.RoundTrip(r)
	})

	// Wrap interceptors in reverse order so that the first interceptor
	// registered is the first to process the request (outermost).
	handler := final
	for i := len(t.interceptors) - 1; i >= 0; i-- {
		interceptor := t.interceptors[i]
		next := handler
		handler = func(i InterceptorFunc, n HandlerFunc) HandlerFunc {
			return func(r *http.Request) (*http.Response, error) {
				return i(r, n)
			}
		}(interceptor, next)
	}

	return handler(req)
}
