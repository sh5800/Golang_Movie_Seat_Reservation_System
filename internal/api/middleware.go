package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"
)

type contextKey string

const (
	UserIDContextKey        contextKey = "userID"
	CorrelationIDContextKey contextKey = "correlationID"
)

func CorrelationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrID := r.Header.Get("X-Correlation-ID")
		if corrID == "" {
			corrID = uuid.New().String()
		}
		w.Header().Set("X-Correlation-ID", corrID)
		ctx := context.WithValue(r.Context(), CorrelationIDContextKey, corrID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":   "unauthorized",
				"message": "Authorization header with 'Bearer <token>' required",
			})
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		token = strings.TrimSpace(token)

		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error":   "unauthorized",
				"message": "Bearer token cannot be empty",
			})
			return
		}

		ctx := context.WithValue(r.Context(), UserIDContextKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// MetricsAndLoggingMiddleware records request durations and writes structured JSON logs
type responseWriterWithStatus struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriterWithStatus) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func MetricsAndLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriterWithStatus{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		duration := time.Since(start)
		statusStr := strconv.Itoa(rw.statusCode)
		// Record Prometheus metrics
		HTTPRequestsTotal.WithLabelValues(r.Method, r.URL.Path, statusStr).Inc()
		HTTPRequestDurationSeconds.WithLabelValues(r.Method, r.URL.Path).Observe(duration.Seconds())
		// Structured JSON logging
		corrID, _ := r.Context().Value(CorrelationIDContextKey).(string)
		userID, _ := r.Context().Value(UserIDContextKey).(string)
		slog.Info("http_request",
			slog.String("correlation_id", corrID),
			slog.String("user_id", userID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rw.statusCode),
			slog.Duration("latency", duration),
		)
	})
}
