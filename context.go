package azfunctions

import (
	"context"
	"net/http"

	log "github.com/sirupsen/logrus"
)

type key int

const (
	requestKey key = 1
	loggerKey  key = 2
)

// RequestFromContext returns the HTTP request from a context. The context must
// have been previously extracted from r.Context().
func RequestFromContext(ctx context.Context) *http.Request {
	return ctx.Value(requestKey).(*http.Request)
}

// LoggerFromContext returns the logger from a context. The context must have
// been previously extracted from r.Context().
func LoggerFromContext(ctx context.Context) *log.Entry {
	return ctx.Value(loggerKey).(*log.Entry)
}

// LoggerFromRequest returns the logger from an HTTP request.
func LoggerFromRequest(r *http.Request) *log.Entry {
	return LoggerFromContext(r.Context())
}
