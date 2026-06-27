package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/systynlabs/vaultnuban/internal/logger"
)

const routerCtx = "RouterExplorer"

// Logger is an HTTP middleware that emits a NestJS-style request log line.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		msg := fmt.Sprintf("%s %s %d +%s",
			r.Method, r.URL.Path, rec.status, formatDuration(duration))

		if rec.status >= 500 {
			logger.Error(routerCtx, msg)
		} else if rec.status >= 400 {
			logger.Warn(routerCtx, msg)
		} else {
			logger.Log(routerCtx, msg)
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dμs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}
