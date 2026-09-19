package otel

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// CorrelationIDHeader — header HTTP para correlation ID.
const CorrelationIDHeader = "X-Correlation-ID"

// CorrelationIDContextKey — chave para armazenar correlation ID no context.
type CorrelationIDContextKey string

const CorrelationIDKey CorrelationIDContextKey = "correlation_id"

// HTTPMiddleware — middleware HTTP que adiciona tracing automático com correlation ID.
func HTTPMiddleware(tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extrair ou gerar correlation ID
			correlationID := r.Header.Get(CorrelationIDHeader)
			if correlationID == "" {
				correlationID = uuid.New().String()
			}

			// Injetar correlation ID no response
			w.Header().Set(CorrelationIDHeader, correlationID)

			// Criar span
			ctx, span := tracer.Start(r.Context(),
				r.Method+" "+r.URL.Path,
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.url", r.URL.String()),
					attribute.String("http.target", r.URL.Path),
					attribute.String("correlation_id", correlationID),
				),
			)
			defer span.End()

			// Injetar correlation ID no context
			ctx = context.WithValue(ctx, CorrelationIDKey, correlationID)

			// Criar response wrapper para capturar status code
			rw := &responseWriter{ResponseWriter: w}

			// Handler com novo context
			next.ServeHTTP(rw, r.WithContext(ctx))

			// Registrar status code
			span.SetAttributes(attribute.Int("http.status_code", rw.statusCode))

			// Marcar erro se status >= 400
			if rw.statusCode >= 400 {
				span.SetStatus(codes.Error, "HTTP error")
			}
		})
	}
}

// responseWriter — wrapper para capturar status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}
	return rw.ResponseWriter.Write(b)
}

// GetCorrelationID — extrai correlation ID do context.
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(CorrelationIDKey).(string); ok {
		return id
	}
	return ""
}

// WithCorrelationID — injeta correlation ID no context.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, CorrelationIDKey, id)
}
