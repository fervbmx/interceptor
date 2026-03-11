package interceptors

import (
	"errors"
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

var defaultSensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Set-Cookie",
}

// LoggingOptions configures LoggingInterceptor behavior.
type LoggingOptions struct {
	// Logger receives structured attributes.
	// If nil, slog.Default() is used.
	Logger *slog.Logger

	// HeadersToLog is a request header allowlist.
	// Empty means no additional headers are logged.
	HeadersToLog []string

	// SensitiveHeaders lists header names to redact as "***".
	// Defaults to Authorization, Cookie, and Set-Cookie.
	SensitiveHeaders []string
}

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
	durationMS       *float64
	requestID        string
}

// LoggingInterceptor returns an interceptor that logs request lifecycle events.
func LoggingInterceptor(opts *LoggingOptions) interceptor.InterceptorFunc {
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

func buildLoggingConfig(opts *LoggingOptions) loggingConfig {
	cfg := loggingConfig{
		logger:           slog.Default(),
		headersToLog:     make(map[string]struct{}),
		sensitiveHeaders: canonicalHeaderSet(defaultSensitiveHeaders),
	}

	if opts == nil {
		return cfg
	}

	if opts.Logger != nil {
		cfg.logger = opts.Logger
	}

	if len(opts.HeadersToLog) > 0 {
		cfg.headersToLog = canonicalHeaderSet(opts.HeadersToLog)
	}

	if len(opts.SensitiveHeaders) > 0 {
		cfg.sensitiveHeaders = canonicalHeaderSet(opts.SensitiveHeaders)
	}

	return cfg
}

func canonicalHeaderSet(headers []string) map[string]struct{} {
	set := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		set[textproto.CanonicalMIMEHeaderKey(h)] = struct{}{}
	}
	return set
}

func buildStartEvent(req *http.Request, cfg loggingConfig) eventData {
	e := eventData{
		level:          slog.LevelInfo,
		message:        "http request started",
		method:         req.Method,
		urlFull:        req.URL.String(),
		urlScheme:      req.URL.Scheme,
		serverAddress:  req.URL.Hostname(),
		serverPort:     extractServerPort(req.URL),
		userAgent:      req.Header.Get("User-Agent"),
		requestID:      req.Header.Get("X-Request-ID"),
		requestHeaders: extractAllowedHeaders(req.Header, cfg.headersToLog, cfg.sensitiveHeaders),
	}

	if req.ContentLength >= 0 {
		e.requestBodySize = &req.ContentLength
	}

	return e
}

func buildEndEvent(req *http.Request, resp *http.Response, err error, duration time.Duration) eventData {
	ms := float64(duration) / float64(time.Millisecond)
	e := eventData{
		level:         getLogLevel(resp, err),
		method:        req.Method,
		urlFull:       req.URL.String(),
		urlScheme:     req.URL.Scheme,
		serverAddress: req.URL.Hostname(),
		serverPort:    extractServerPort(req.URL),
		durationMS:    &ms,
	}

	if req.ContentLength >= 0 {
		e.requestBodySize = &req.ContentLength
	}

	if err != nil {
		e.message = "http request failed"
		e.errorType = classifyErrorType(err)
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

func sanitizeHeaderFieldKey(key string) string {
	key = strings.ToLower(key)
	return key
}

func buildAttrs(event eventData) []slog.Attr {
	attrs := make([]slog.Attr, 0, 6)

	requestAttrs := []any{slog.String("method", event.method)}
	if event.requestBodySize != nil {
		requestAttrs = append(requestAttrs, slog.Group("body", slog.Int64("size", *event.requestBodySize)))
	}
	if len(event.requestHeaders) > 0 {
		headerAttrs := make([]any, 0, len(event.requestHeaders))
		for key, value := range event.requestHeaders {
			headerAttrs = append(headerAttrs, slog.String(sanitizeHeaderFieldKey(key), value))
		}
		requestAttrs = append(requestAttrs, slog.Group("header", headerAttrs...))
	}

	httpAttrs := []any{slog.Group("request", requestAttrs...)}
	if event.statusCode != nil || event.responseBodySize != nil {
		responseAttrs := make([]any, 0, 2)
		if event.statusCode != nil {
			responseAttrs = append(responseAttrs, slog.Int("status_code", *event.statusCode))
		}
		if event.responseBodySize != nil {
			responseAttrs = append(responseAttrs, slog.Group("body", slog.Int64("size", *event.responseBodySize)))
		}
		httpAttrs = append(httpAttrs, slog.Group("response", responseAttrs...))
	}
	attrs = append(attrs, slog.Group("http", httpAttrs...))

	urlAttrs := []any{slog.String("full", event.urlFull)}
	if event.urlScheme != "" {
		urlAttrs = append(urlAttrs, slog.String("scheme", event.urlScheme))
	}
	attrs = append(attrs, slog.Group("url", urlAttrs...))

	serverAttrs := []any{slog.String("address", event.serverAddress)}
	if event.serverPort > 0 {
		serverAttrs = append(serverAttrs, slog.Int("port", event.serverPort))
	}
	attrs = append(attrs, slog.Group("server", serverAttrs...))

	if event.userAgent != "" {
		attrs = append(attrs, slog.Group("user_agent", slog.String("original", event.userAgent)))
	}
	if event.errorType != "" {
		attrs = append(attrs, slog.Group("error", slog.String("type", event.errorType)))
	}

	interceptorAttrs := make([]any, 0, 2)
	if event.durationMS != nil {
		interceptorAttrs = append(interceptorAttrs, slog.Float64("duration_ms", *event.durationMS))
	}
	if event.requestID != "" {
		interceptorAttrs = append(interceptorAttrs, slog.String("request_id", event.requestID))
	}
	if len(interceptorAttrs) > 0 {
		attrs = append(attrs, slog.Group("interceptor", interceptorAttrs...))
	}

	return attrs
}

func extractServerPort(u *url.URL) int {
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

func classifyErrorType(err error) string {
	if err == nil {
		return ""
	}

	root := err
	for {
		unwrapped := errors.Unwrap(root)
		if unwrapped == nil {
			break
		}
		root = unwrapped
	}

	t := reflect.TypeOf(root)
	if t == nil {
		return "error"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if name := t.Name(); name != "" {
		return name
	}

	typeName := t.String()
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		return typeName[idx+1:]
	}
	return typeName
}

func extractAllowedHeaders(headers http.Header, allowlist, sensitive map[string]struct{}) map[string]string {
	if len(allowlist) == 0 {
		return nil
	}

	loggedHeaders := make(map[string]string)
	for header := range allowlist {
		value := headers.Get(header)
		if value == "" {
			continue
		}

		if _, redact := sensitive[textproto.CanonicalMIMEHeaderKey(header)]; redact {
			loggedHeaders[header] = "***"
			continue
		}

		loggedHeaders[header] = value
	}

	if len(loggedHeaders) == 0 {
		return nil
	}

	return loggedHeaders
}
