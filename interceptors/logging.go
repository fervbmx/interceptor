package interceptors

import (
	"log/slog"
	"net/http"
	"net/textproto"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/fervbmx/interceptor"
)

const redacted = "REDACTED"
var defaultSensitiveHeaders = newHeaderSet(
	"Authorization",
	"Cookie",
	"Set-Cookie",
)

type loggingConfig struct {
	logger           *slog.Logger
	headersToLog     map[string]struct{}
	sensitiveHeaders map[string]struct{}
}

type eventData struct {
	level            slog.Level
	message          string
	method           string
	urlFull          string
	urlScheme        string
	serverAddress    string
	serverPort       int
	statusCode       *int
	userAgent        string
	errorType        string
	requestBodySize  *int64
	responseBodySize *int64
	requestHeaders   map[string]string
	duration         *float64
}

// RequestLoggingOptions configures request logging behavior.
type RequestLoggingOptions struct {
	Logger           *slog.Logger
	HeadersToLog     []string
	SensitiveHeaders []string
}

// AddRequestLogging returns an interceptor that logs request lifecycle events
// before and after the next handler runs. It emits a start event, then either a
// completion event or a failure event. If opts is nil, default logging options
// are used.
//
// interceptor.NewTransport(nil,
//	interceptors.AddRequestLogging(
//		Logging: logging
//	),
// )
func AddRequestLogging(opts *RequestLoggingOptions) interceptor.Middleware {
	cfg := buildLoggingConfig(opts)

	return func(req *http.Request, next interceptor.HandlerFunc) (*http.Response, error) {
		clonedReq := req.Clone(req.Context())

		startEvent := buildStartEvent(clonedReq, cfg)
		emitLog(clonedReq, cfg, startEvent)

		start := time.Now()
		resp, err := next(clonedReq)
		duration := time.Since(start)

		endEvent := buildEndEvent(clonedReq, resp, err, duration)
		emitLog(clonedReq, cfg, endEvent)

		return resp, err
	}
}

// buildLoggingConfig merges options with defaults and normalizes header names.
func buildLoggingConfig(opts *RequestLoggingOptions) loggingConfig {
	cfg := loggingConfig{
		logger:           slog.Default(),
		headersToLog:     make(map[string]struct{}),
		sensitiveHeaders: defaultSensitiveHeaders,
	}

	if opts == nil {
		return cfg
	}

	if opts.Logger != nil {
		cfg.logger = opts.Logger
	}

	if len(opts.HeadersToLog) > 0 {
		cfg.headersToLog = newHeaderSet(opts.HeadersToLog...)
	}

	if len(opts.SensitiveHeaders) > 0 {
		cfg.sensitiveHeaders = newHeaderSet(opts.SensitiveHeaders...)
	}

	return cfg
}

func newHeaderSet(headers ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		set[textproto.CanonicalMIMEHeaderKey(h)] = struct{}{}
	}
	return set
}

// buildStartEvent assembles attributes for the request-start log entry.
func buildStartEvent(req *http.Request, cfg loggingConfig) eventData {
	e := eventData{
		level:          slog.LevelInfo,
		message:        "http request started",
		method:         req.Method,
		urlFull:        req.URL.String(),
		urlScheme:      req.URL.Scheme,
		serverAddress:  req.URL.Hostname(),
		serverPort:     getServerPort(req.URL),
		userAgent:      req.Header.Get("User-Agent"),
		requestHeaders: getAllowedHeaders(req.Header, cfg.headersToLog, cfg.sensitiveHeaders),
	}

	if req.ContentLength >= 0 {
		e.requestBodySize = &req.ContentLength
	}

	return e
}

// buildEndEvent assembles attributes for completion and failure log entries.
func buildEndEvent(req *http.Request, resp *http.Response, err error, duration time.Duration) eventData {
	seconds := duration.Seconds()
	e := eventData{
		level:         getLogLevel(resp, err),
		method:        req.Method,
		urlFull:       req.URL.String(),
		urlScheme:     req.URL.Scheme,
		serverAddress: req.URL.Hostname(),
		serverPort:    getServerPort(req.URL),
		duration:      &seconds,
	}

	if req.ContentLength >= 0 {
		e.requestBodySize = &req.ContentLength
	}

	if err != nil {
		e.message = "http request failed"
		e.errorType = getErrorType(err)
		return e
	}

	e.message = "http request completed"
	if resp != nil {
		e.statusCode = &resp.StatusCode
		if resp.ContentLength >= 0 {
			e.responseBodySize = &resp.ContentLength
		}
		if resp.StatusCode >= http.StatusBadRequest {
			e.errorType = strconv.Itoa(resp.StatusCode)
		}
	}

	return e
}

func getLogLevel(resp *http.Response, err error) slog.Level {
	if err != nil {
		return slog.LevelError
	}
	if resp == nil {
		return slog.LevelError
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return slog.LevelError
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return slog.LevelWarn
	}
	return slog.LevelInfo
}

func emitLog(req *http.Request, cfg loggingConfig, event eventData) {
	attrs := buildAttrs(event)
	cfg.logger.LogAttrs(req.Context(), event.level, event.message, attrs...)
}

func buildAttrs(event eventData) []slog.Attr {
	attrs := make([]slog.Attr, 0, 6)

	attrs = append(attrs, buildURLAttrs(event))
	attrs = append(attrs, buildServerAttrs(event))
	attrs = append(attrs, buildHTTPAttrs(event))

	if event.userAgent != "" {
		attrs = append(attrs,
			slog.Group("user_agent",
				slog.String("original", event.userAgent),
			),
		)
	}

	if event.errorType != "" {
		attrs = append(attrs,
			slog.Group("error",
				slog.String("type", event.errorType),
			),
		)
	}

	return attrs
}

func buildURLAttrs(event eventData) slog.Attr {
	attrs := make([]any, 0, 2)
	attrs = append(attrs, slog.String("full", event.urlFull))

	if event.urlScheme != "" {
		attrs = append(attrs, slog.String("scheme", event.urlScheme))
	}

	return slog.Group("url", attrs...)
}

func buildServerAttrs(event eventData) slog.Attr {
	attrs := make([]any, 0, 2)
	attrs = append(attrs, slog.String("address", event.serverAddress))

	if event.serverPort > 0 {
		attrs = append(attrs, slog.Int("port", event.serverPort))
	}

	return slog.Group("server", attrs...)
}

func buildHTTPAttrs(event eventData) slog.Attr {
	attrs := make([]any, 0, 3)

	attrs = append(attrs, buildHTTPRequestAttrs(event))
	attrs = append(attrs, buildHTTPResponseAttrs(event))
	attrs = append(attrs, buildHTTPClientAttrs(event))

	return slog.Group("http", attrs...)
}

func buildHTTPRequestAttrs(event eventData) slog.Attr {
	attrs := make([]any, 0, 3)
	attrs = append(attrs, slog.String("method", event.method))
	attrs = append(attrs, buildHTTPRequestHeaderAttrs(event))

	if event.requestBodySize != nil {
		attrs = append(attrs,
			slog.Group("body",
				slog.Int64("size", *event.requestBodySize),
			),
		)
	}

	return slog.Group("request", attrs...)
}

func buildHTTPRequestHeaderAttrs(event eventData) slog.Attr {
	if len(event.requestHeaders) == 0 {
		return slog.Attr{}
	}

	attrs := make([]any, 0, len(event.requestHeaders))
	for k, v := range event.requestHeaders {
		attrs = append(attrs, slog.String(strings.ToLower(k), v))
	}

	return slog.Group("header", attrs...)
}

func buildHTTPResponseAttrs(event eventData) slog.Attr {
	if event.statusCode == nil && event.responseBodySize == nil {
		return slog.Attr{}
	}

	attrs := make([]any, 0, 2)
	if event.statusCode != nil {
		attrs = append(attrs, slog.Int("status_code", *event.statusCode))
	}

	if event.responseBodySize != nil {
		attrs = append(attrs,
			slog.Group("body",
				slog.Int64("size", *event.responseBodySize),
			),
		)
	}

	return slog.Group("response", attrs...)
}

func buildHTTPClientAttrs(event eventData) slog.Attr {
	if event.duration == nil {
		return slog.Attr{}
	}

	return slog.Group("client",
		slog.Group("request", slog.Float64("duration", *event.duration)),
	)
}

// getErrorType returns port of the url.
func getServerPort(u *url.URL) int {
	if u == nil {
		return 0
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err == nil {
			return value
		}
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

// getErrorType returns the root error type name.
func getErrorType(err error) string {
	if err == nil {
		return ""
	}

	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if name := t.Name(); name != "" {
		return name
	}

	return t.String()
}

// getAllowedHeaders returns allowed headers with sensitive values redacted.
func getAllowedHeaders(headers http.Header, allowed, sensitive map[string]struct{}) map[string]string {
	if len(allowed) == 0 {
		return nil
	}

	logged := make(map[string]string, len(allowed))

	for h := range allowed {
		key := textproto.CanonicalMIMEHeaderKey(h)
		values, ok := headers[key]
		if !ok || len(values) == 0 {
			continue
		}

		if _, isSensitive := sensitive[key]; isSensitive {
			logged[h] = redacted
			continue
		}

		logged[h] = values[0]
	}

	if len(logged) == 0 {
		return nil
	}

	return logged
}
