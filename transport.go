// Package interceptor provides an HTTP interceptor system. It allows composing
// middleware interceptors that can inspect, modify, retry, or short-circuit
// HTTP requests and responses flowing through a standard http.RoundTripper.
package interceptor

import "net/http"

// HandlerFunc represents the next step in the interceptor chain.
// It takes an HTTP request and returns a response or error.
type HandlerFunc func(req *http.Request) (*http.Response, error)

// Middleware defines an interceptor. It receives the outgoing request and
// a next function representing the next processing step in the interceptor
// chain.
type Middleware func(req *http.Request, next HandlerFunc) (*http.Response, error)

// Transport implements http.RoundTripper by running a chain of interceptors
// in front of a default transport.
type Transport struct {
	defaultTransport http.RoundTripper
	interceptors     []Middleware
}

// NewTransport creates a new Transport with the given default RoundTripper
// and interceptors. If defaultTransport is nil, http.DefaultTransport is used.
// Interceptors are executed in the order provided.
//
// // Using the default transport:
//
//	client := &http.Client{
//			Transport: interceptor.NewTransport(nil, AInterceptor, BInterceptor),
//	}
//
// // Using a custom default transport:
//
//	client := &http.Client{
//			Transport: interceptor.NewTransport(customTransport, AInterceptor, BInterceptor),
//	}
func NewTransport(defaultTransport http.RoundTripper, interceptors ...Middleware) *Transport {
	if defaultTransport == nil {
		defaultTransport = http.DefaultTransport
	}
	return &Transport{
		defaultTransport: defaultTransport,
		interceptors:     interceptors,
	}
}

// Use appends one or more interceptors to the chain. They are appended after
// any interceptors already registered.
//
// t := interceptor.NewTransport(nil, AuthInterceptor).Use(MetricsInterceptor)
func (t *Transport) Use(interceptors ...Middleware) *Transport {
	t.interceptors = append(t.interceptors, interceptors...)
	return t
}

// RoundTrip executes the interceptor chain. The underlying transport is reached
// only if each interceptor calls next.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {

	final := HandlerFunc(func(r *http.Request) (*http.Response, error) {
		return t.defaultTransport.RoundTrip(r)
	})

	handler := final
	for i := len(t.interceptors) - 1; i >= 0; i-- {
		fn := t.interceptors[i]
		next := handler
		handler = func(fn Middleware, n HandlerFunc) HandlerFunc {
			return func(r *http.Request) (*http.Response, error) {
				return fn(r, n)
			}
		}(fn, next)
	}

	return handler(req)
}
