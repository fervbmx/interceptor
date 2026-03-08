package interceptors

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/textproto"
	"sort"
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

// LogFormat controls the log output format.
type LogFormat int

const (
	// LogFormatJSON writes structured JSON logs.
	LogFormatJSON LogFormat = iota
	// LogFormatLogfmt writes logs in key=value format.
	LogFormatLogfmt
	// LogFormatText writes human-readable plain text logs.
	LogFormatText
)

// KeyStyle controls the JSON key naming convention.
type KeyStyle int

const (
	// KeyStyleFlat uses dotted keys like "http.method".
	KeyStyleFlat KeyStyle = iota
	// KeyStyleNested uses nested objects like "http.request.method".
	KeyStyleNested
)

// LoggingOptions configures LoggingInterceptor behavior.
type LoggingOptions struct {
	// Logger receives rendered log lines.
	// If nil, slog.Default() is used.
	Logger *slog.Logger

	// Format selects log rendering format.
	// Default: LogFormatJSON.
	Format LogFormat

	// KeyStyle chooses JSON key style when Format is LogFormatJSON.
	// Default: KeyStyleFlat.
	KeyStyle KeyStyle

	// HeadersToLog is a request header allowlist.
	// Empty means no additional headers are logged.
	HeadersToLog []string

	// SensitiveHeaders lists header names to redact as "***".
	// Defaults to Authorization, Cookie, and Set-Cookie.
	SensitiveHeaders []string
}

type loggingConfig struct {
	logger           *slog.Logger
	format           LogFormat
	keyStyle         KeyStyle
	headersToLog     map[string]struct{}
	sensitiveHeaders map[string]struct{}
}

type eventData struct {
	timestamp             time.Time
	level                 slog.Level
	message               string
	method                string
	url                   string
	target                string
	host                  string
	scheme                string
	statusCode            *int
	durationMS            *float64
	userAgent             string
	requestID             string
	errorMessage          string
	requestContentLength  *int64
	responseContentLength *int64
	requestHeaders        map[string]string
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
		logger:         slog.Default(),
		format:         LogFormatJSON,
		keyStyle:       KeyStyleFlat,
		headersToLog:   make(map[string]struct{}),
		sensitiveHeaders: canonicalHeaderSet(defaultSensitiveHeaders),
	}

	if opts == nil {
		return cfg
	}

	if opts.Logger != nil {
		cfg.logger = opts.Logger
	}

	if opts.Format != 0 {
		cfg.format = opts.Format
	}

	if opts.KeyStyle != 0 {
		cfg.keyStyle = opts.KeyStyle
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
		timestamp:      time.Now().UTC(),
		level:          slog.LevelInfo,
		message:        "http request started",
		method:         req.Method,
		url:            req.URL.String(),
		target:         req.URL.RequestURI(),
		host:           req.URL.Hostname(),
		scheme:         req.URL.Scheme,
		userAgent:      req.Header.Get("User-Agent"),
		requestID:      req.Header.Get("X-Request-ID"),
		requestHeaders: extractAllowedHeaders(req.Header, cfg.headersToLog, cfg.sensitiveHeaders),
	}

	if req.ContentLength >= 0 {
	    e.requestContentLength = &req.ContentLength
	}

	return e
}

func buildEndEvent(req *http.Request, resp *http.Response, err error, duration time.Duration) eventData {
	ms := float64(duration) / float64(time.Millisecond)
	e := eventData{
		timestamp:    time.Now().UTC(),
		level:        getLogLevel(resp, err),
		method:       req.Method,
		url:          req.URL.String(),
		durationMS:   &ms,
	}

	if err != nil {
		e.message = "http request failed"
		e.errorMessage = err.Error()
		return e
	}

	e.message = "http request completed"
	if resp != nil {
		e.statusCode = &resp.StatusCode
		if req.ContentLength >= 0 {
		    e.requestContentLength = &req.ContentLength
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
	line := renderEvent(cfg, event)
	cfg.logger.Log(req.Context(), event.level, line)
}

func renderEvent(cfg loggingConfig, event eventData) string {
	switch cfg.format {
	case LogFormatLogfmt:
		return renderLogfmt(event)
	case LogFormatText:
		return renderText(event)
	default:
		if cfg.keyStyle == KeyStyleNested {
			return renderJSONNested(event)
		}
		return renderJSONFlat(event)
	}
}

func renderJSONFlat(event eventData) string {
	payload := map[string]any{
		"timestamp":   event.timestamp.Format(time.RFC3339Nano),
		"level":       strings.ToUpper(event.level.String()),
		"msg":         event.message,
		"http.method": event.method,
		"http.url":    event.url,
	}

	if event.target != "" {
		payload["http.target"] = event.target
	}
	if event.host != "" {
		payload["http.host"] = event.host
	}
	if event.scheme != "" {
		payload["http.scheme"] = event.scheme
	}
	if event.requestContentLength != nil {
		payload["http.request_content_length"] = *event.requestContentLength
	}
	if event.responseContentLength != nil {
		payload["http.response_content_length"] = *event.responseContentLength
	}
	if event.statusCode != nil {
		payload["http.status_code"] = *event.statusCode
	}
	if event.durationMS != nil {
		payload["http.duration_ms"] = *event.durationMS
	}
	if event.userAgent != "" {
		payload["user_agent.original"] = event.userAgent
	}
	if event.requestID != "" {
		payload["http.request_id"] = event.requestID
	}
	if event.errorMessage != "" {
		payload["error.message"] = event.errorMessage
	}
	if len(event.requestHeaders) > 0 {
		payload["http.request.headers"] = event.requestHeaders
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func renderJSONNested(event eventData) string {
	payload := map[string]any{
		"@timestamp": event.timestamp.Format(time.RFC3339Nano),
		"log": map[string]any{
			"level": strings.ToUpper(event.level.String()),
		},
		"message": event.message,
		"http": map[string]any{
			"request": map[string]any{
				"method": event.method,
			},
		},
		"url": map[string]any{
			"full": event.url,
		},
	}

	httpMap := payload["http"].(map[string]any)
	requestMap := httpMap["request"].(map[string]any)
	urlMap := payload["url"].(map[string]any)

	if event.target != "" {
		urlMap["path"] = event.target
	}
	if event.host != "" {
		urlMap["domain"] = event.host
	}
	if event.scheme != "" {
		urlMap["scheme"] = event.scheme
	}
	if event.requestContentLength != nil {
		requestMap["body"] = map[string]any{"bytes": *event.requestContentLength}
	}
	if event.requestID != "" {
		requestMap["id"] = event.requestID
	}
	if event.userAgent != "" {
		payload["user_agent"] = map[string]any{"original": event.userAgent}
	}
	if event.statusCode != nil {
		httpMap["response"] = map[string]any{"status_code": *event.statusCode}
	}
	if event.responseContentLength != nil {
		responseMap, ok := httpMap["response"].(map[string]any)
		if !ok {
			responseMap = map[string]any{}
			httpMap["response"] = responseMap
		}
		responseMap["body"] = map[string]any{"bytes": *event.responseContentLength}
	}
	if event.durationMS != nil {
		payload["event"] = map[string]any{"duration": *event.durationMS}
	}
	if event.errorMessage != "" {
		payload["error"] = map[string]any{"message": event.errorMessage}
	}
	if len(event.requestHeaders) > 0 {
		requestMap["headers"] = event.requestHeaders
	}

	b, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func renderLogfmt(event eventData) string {
	parts := []string{
		"timestamp=" + encodeLogfmtValue(event.timestamp.Format(time.RFC3339Nano)),
		"level=" + encodeLogfmtValue(strings.ToUpper(event.level.String())),
		"msg=" + encodeLogfmtValue(event.message),
		"http.method=" + encodeLogfmtValue(event.method),
		"http.url=" + encodeLogfmtValue(event.url),
	}

	if event.target != "" {
		parts = append(parts, "http.target="+encodeLogfmtValue(event.target))
	}
	if event.host != "" {
		parts = append(parts, "http.host="+encodeLogfmtValue(event.host))
	}
	if event.scheme != "" {
		parts = append(parts, "http.scheme="+encodeLogfmtValue(event.scheme))
	}
	if event.requestContentLength != nil {
		parts = append(parts, "http.request_content_length="+strconv.FormatInt(*event.requestContentLength, 10))
	}
	if event.responseContentLength != nil {
		parts = append(parts, "http.response_content_length="+strconv.FormatInt(*event.responseContentLength, 10))
	}
	if event.statusCode != nil {
		parts = append(parts, "http.status_code="+strconv.Itoa(*event.statusCode))
	}
	if event.durationMS != nil {
		parts = append(parts, "http.duration_ms="+strconv.FormatFloat(*event.durationMS, 'f', 3, 64))
	}
	if event.userAgent != "" {
		parts = append(parts, "user_agent.original="+encodeLogfmtValue(event.userAgent))
	}
	if event.requestID != "" {
		parts = append(parts, "http.request_id="+encodeLogfmtValue(event.requestID))
	}
	if event.errorMessage != "" {
		parts = append(parts, "error.message="+encodeLogfmtValue(event.errorMessage))
	}

	if len(event.requestHeaders) > 0 {
		headerKeys := make([]string, 0, len(event.requestHeaders))
		for k := range event.requestHeaders {
			headerKeys = append(headerKeys, k)
		}
		sort.Strings(headerKeys)
		for _, key := range headerKeys {
			parts = append(parts, "http.request.header."+sanitizeHeaderFieldKey(key)+"="+encodeLogfmtValue(event.requestHeaders[key]))
		}
	}

	return strings.Join(parts, " ")
}

func renderText(event eventData) string {
	duration := ""
	if event.durationMS != nil {
		duration = fmt.Sprintf(" %.2fms", *event.durationMS)
	}

	status := ""
	if event.statusCode != nil {
		status = fmt.Sprintf(" %d", *event.statusCode)
	}

	line := fmt.Sprintf(
		"%s %s %s %s %s%s%s",
		event.timestamp.Format(time.RFC3339Nano),
		strings.ToUpper(event.level.String()),
		event.message,
		event.method,
		event.url,
		status,
		duration,
	)

	if event.errorMessage != "" {
		line += fmt.Sprintf(" error=%q", event.errorMessage)
	}

	return line
}

func encodeLogfmtValue(value string) string {
	if value == "" {
		return "\"\""
	}

	if strings.ContainsAny(value, " \t\n\r\"=") {
		replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"")
		return "\"" + replacer.Replace(value) + "\""
	}

	return value
}

func sanitizeHeaderFieldKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "-", "_")
	return key
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
